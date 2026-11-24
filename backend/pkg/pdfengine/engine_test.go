package pdfengine

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"transleaf/pkg/pdfengine/content"
	"transleaf/pkg/pdfengine/layout"
	"transleaf/pkg/pdfengine/object"
)

// buildSampleTwoColumnPDF 构造一份包含标题、双栏正文及矢量表格线的测试 PDF
func buildSampleTwoColumnPDF(t *testing.T) []byte {
	t.Helper()
	doc := object.NewEmptyDocument()

	fontRef := doc.AllocObj(object.Dict{
		"Type":     object.Name("Font"),
		"Subtype":  object.Name("Type1"),
		"BaseFont": object.Name("Helvetica"),
	})

	// 包含矢量矩形框 (re/S)、表格线 (m/l/S)、跨栏标题与左右双栏正文
	rawStream := `
q
0.2 0.2 0.2 RG 1 w
40 40 515 760 re S
40 700 m 555 700 l S
Q
BT
/F1 18 Tf
1 0 0 1 180 760 Tm
(Attention Is All You Need) Tj
/F1 11 Tf
1 0 0 1 50 660 Tm
(Left column line one starts here.) Tj
1 0 0 1 50 644 Tm
(Left column line two continues.) Tj
1 0 0 1 50 628 Tm
(Left column line three ends.) Tj
1 0 0 1 320 660 Tm
(Right column line one starts here.) Tj
1 0 0 1 320 644 Tm
(Right column line two continues.) Tj
1 0 0 1 320 628 Tm
(Right column line three ends.) Tj
ET
`
	comp, err := object.EncodeFlate([]byte(rawStream))
	if err != nil {
		t.Fatalf("encode sample stream: %v", err)
	}
	contentRef := doc.AllocObj(&object.Stream{
		Dict: object.Dict{
			"Filter": object.Name("FlateDecode"),
			"Length": object.Int(len(comp)),
		},
		RawData: comp,
	})

	pagesRef := doc.AllocObj(object.Dict{})
	pageRef := doc.AllocObj(object.Dict{
		"Type":     object.Name("Page"),
		"Parent":   pagesRef,
		"MediaBox": object.Array{object.Int(0), object.Int(0), object.Int(595), object.Int(842)},
		"Resources": object.Dict{
			"Font": object.Dict{
				"F1": fontRef,
			},
		},
		"Contents": contentRef,
	})
	doc.SetObj(pagesRef.ObjNum, object.Dict{
		"Type":  object.Name("Pages"),
		"Kids":  object.Array{pageRef},
		"Count": object.Int(1),
	})
	catalogRef := doc.AllocObj(object.Dict{
		"Type":  object.Name("Catalog"),
		"Pages": pagesRef,
	})
	doc.Trailer["Root"] = catalogRef

	data, err := doc.Write()
	if err != nil {
		t.Fatalf("write sample pdf: %v", err)
	}
	return data
}

func findTestCJKFont() string {
	candidates := []string{
		"../../assets/fonts/NotoSansSC-Regular.ttf",
		"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
		"/System/Library/Fonts/Supplemental/NISC18030.ttf",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func TestPDFEngineFullPipeline(t *testing.T) {
	origData := buildSampleTwoColumnPDF(t)

	// 1. 测试解析与双栏阅读顺序
	parsed, err := ParsePDF(origData)
	if err != nil {
		t.Fatalf("ParsePDF failed: %v", err)
	}
	if parsed.PageCount != 1 {
		t.Fatalf("expected 1 page, got %d", parsed.PageCount)
	}
	blocks := parsed.Pages[0].Blocks
	if len(blocks) < 3 {
		t.Fatalf("expected >= 3 blocks (title + left col + right col), got %d: %+v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0].OriginalText, "Attention Is All You Need") {
		t.Fatalf("expected block 0 to be title, got %q", blocks[0].OriginalText)
	}
	if !strings.Contains(blocks[1].OriginalText, "Left column") {
		t.Fatalf("expected block 1 to be left column, got %q", blocks[1].OriginalText)
	}
	if !strings.Contains(blocks[2].OriginalText, "Right column") {
		t.Fatalf("expected block 2 to be right column, got %q", blocks[2].OriginalText)
	}

	// 2. 测试指令级无损擦除（去字留矢量线）
	redactMap := map[int][][4]float64{0: {}}
	for _, b := range blocks {
		if b.Translatable {
			redactMap[0] = append(redactMap[0], b.BBox)
		}
	}
	cleanBgData, err := GenerateCleanBg(origData, redactMap)
	if err != nil {
		t.Fatalf("GenerateCleanBg failed: %v", err)
	}

	// 确认擦除后原可译文字已彻底消失
	reParsedClean, err := ParsePDF(cleanBgData)
	if err != nil {
		t.Fatalf("ParsePDF clean_bg failed: %v", err)
	}
	if len(reParsedClean.Pages[0].Blocks) != 0 {
		t.Fatalf("expected 0 blocks in clean_bg, got %d", len(reParsedClean.Pages[0].Blocks))
	}

	// 确认原有矢量边框和表格线指令 (re/S, m/l/S) 100% 完整保留
	cleanDoc, _ := object.Parse(cleanBgData)
	cleanPages, _ := cleanDoc.Pages()
	cleanStream, _ := cleanDoc.PageContentStreams(cleanPages[0])
	cleanOps := content.ParseOperations(cleanStream)
	hasRect, hasLine := false, false
	for _, op := range cleanOps {
		if op.Operator == "re" {
			hasRect = true
		}
		if op.Operator == "l" {
			hasLine = true
		}
	}
	if !hasRect || !hasLine {
		t.Fatalf("expected vector rect & line to be preserved in clean_bg, hasRect=%v hasLine=%v", hasRect, hasLine)
	}

	// 3. 测试中文字体嵌入与高保真回填导出
	fontPath := findTestCJKFont()
	if fontPath == "" {
		t.Skip("no CJK TrueType font available on host, skipping CJK embed test")
	}

	renderBlocks := map[int][]layout.RenderBlock{
		0: {
			{
				BBox:       blocks[0].BBox,
				Text:       "注意力机制即是一切",
				FontSize:   blocks[0].FontSize,
				LineHeight: 1.25,
				Align:      "center",
				Color:      "#000000",
			},
			{
				BBox:       blocks[1].BBox,
				Text:       "左栏第一行从这里开始。左栏第二行继续延伸，用于测试自动折行与中英文混排 Test123。",
				FontSize:   blocks[1].FontSize,
				LineHeight: 1.25,
				Align:      "left",
				Color:      "#111827",
			},
			{
				BBox:       blocks[2].BBox,
				Text:       "右栏译文内容展示，验证双栏坐标精准还原。",
				FontSize:   blocks[2].FontSize,
				LineHeight: 1.25,
				Align:      "left",
				Color:      "#111827",
			},
		},
	}

	transData, err := ExportTranslated(cleanBgData, fontPath, renderBlocks)
	if err != nil {
		t.Fatalf("ExportTranslated failed: %v", err)
	}
	if !bytes.HasPrefix(transData, []byte("%PDF-")) {
		t.Fatalf("exported translated data is not a valid PDF")
	}

	// 反向解析导出的中文 PDF，验证 ToUnicode CMap 可以精准还原中文文本！
	parsedTrans, err := ParsePDF(transData)
	if err != nil {
		t.Fatalf("ParsePDF translated failed: %v", err)
	}
	var allText strings.Builder
	for _, b := range parsedTrans.Pages[0].Blocks {
		allText.WriteString(b.OriginalText)
		allText.WriteByte(' ')
	}
	gotText := allText.String()
	if !strings.Contains(gotText, "注意力机制即是一切") || !strings.Contains(gotText, "右栏译文") {
		t.Fatalf("expected exported PDF to contain Chinese text via ToUnicode CMap, got: %q", gotText)
	}

	// 4. 测试双语对照导出（交替页与左右并排）
	interleaved, err := ExportBilingualInterleaved(origData, transData)
	if err != nil {
		t.Fatalf("ExportBilingualInterleaved failed: %v", err)
	}
	pInter, err := ParsePDF(interleaved)
	if err != nil || pInter.PageCount != 2 {
		t.Fatalf("expected 2 pages in interleaved bilingual PDF, got %v (err=%v)", pInter, err)
	}

	sideBySide, err := ExportBilingualSideBySide(origData, transData)
	if err != nil {
		t.Fatalf("ExportBilingualSideBySide failed: %v", err)
	}
	pSide, err := ParsePDF(sideBySide)
	if err != nil || pSide.PageCount != 1 {
		t.Fatalf("expected 1 page in side-by-side bilingual PDF, got %v (err=%v)", pSide, err)
	}
	if pSide.Pages[0].Width < 1100 {
		t.Fatalf("expected double width (~1190pt) in side-by-side PDF, got %.2f", pSide.Pages[0].Width)
	}
}

func TestLayoutEngineV2StructuredSample(t *testing.T) {
	matches, _ := filepath.Glob("../../data/uploads/*.pdf")
	var data []byte
	var parsed *ParseOutput
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		p, err := ParsePDF(b)
		if err == nil && p.PageCount == 116 && len(p.Pages) > 0 && len(p.Pages[0].Blocks) > 0 &&
			strings.Contains(p.Pages[0].Blocks[0].OriginalText, "STM32") {
			data = b
			parsed = p
			break
		}
	}
	if parsed == nil {
		t.Skip("local STM32 116-page sample not found in uploads, skipping")
	}

	p0 := parsed.Pages[0].Blocks
	p1 := parsed.Pages[1].Blocks
	t.Logf("STM32 v2 解析结果: 第1页共 %d 块（v1 为 21 块），第2页目录共 %d 块（v1 为 8 块）", len(p0), len(p1))

	// 检查第1页不再出现 400pt 以上被压扁的超大列表块
	for _, b := range p0 {
		h := b.BBox[3] - b.BBox[1]
		if h > 150 {
			t.Errorf("第1页仍存在过度合并的超大块 %s (h=%.1f): %q", b.ID, h, b.OriginalText)
		}
	}
	// 检查第2页目录条目已被独立拆开（至少 20 个独立块）
	if len(p1) < 20 {
		t.Errorf("第2页目录条目未被正确拆分为独立块，仅得到 %d 块", len(p1))
	}

	// 打印第1页前 12 块与第2页前 8 块供人工核对
	for i := 0; i < len(p0) && i < 12; i++ {
		b := p0[i]
		t.Logf("P0[%d] type=%-13s align=%-6s lh=%.2f bbox=[%.1f,%.1f,%.1f,%.1f] text=%q",
			i, b.BlockType, b.Align, b.LineHeight, b.BBox[0], b.BBox[1], b.BBox[2], b.BBox[3], b.OriginalText)
	}
	for i := 0; i < len(p1) && i < 8; i++ {
		b := p1[i]
		t.Logf("P1[%d] type=%-13s align=%-6s lh=%.2f bbox=[%.1f,%.1f,%.1f,%.1f] text=%q",
			i, b.BlockType, b.Align, b.LineHeight, b.BBox[0], b.BBox[1], b.BBox[2], b.BBox[3], b.OriginalText)
	}

	// 验证第3页（page_index=2）生成 clean_bg.pdf 后：
	// 1. 所有 translatable=false 的章节标号（如 5, 5.1, 5.1.1~5.1.7）100% 保留，不被误删；
	// 2. 所有 translatable=true 的页眉与目录标题（如 STM32F103x8, Parameter conditions）100% 擦净，无重影！
	p2 := parsed.Pages[2].Blocks
	redactMap := map[int][][4]float64{2: {}}
	for _, b := range p2 {
		if b.Translatable {
			redactMap[2] = append(redactMap[2], b.BBox)
		}
	}
	cleanData, err := GenerateCleanBg(data, redactMap)
	if err != nil {
		t.Fatalf("GenerateCleanBg STM32 page 3 failed: %v", err)
	}
	cleanParsed, err := ParsePDF(cleanData)
	if err != nil {
		t.Fatalf("ParsePDF clean_bg failed: %v", err)
	}
	var cleanText strings.Builder
	for _, b := range cleanParsed.Pages[2].Blocks {
		cleanText.WriteString(b.OriginalText)
		cleanText.WriteString(" | ")
	}
	gotClean := cleanText.String()
	t.Logf("第3页 clean_bg 残留文本（应仅含标号）: %s", gotClean)

	for _, mustKeep := range []string{"5.1", "5.1.1", "5.1.2", "5.1.7", "5.2", "5.3"} {
		if !strings.Contains(gotClean, mustKeep) {
			t.Errorf("标号 %q 在 clean_bg.pdf 中被误删！", mustKeep)
		}
	}
	for _, mustErase := range []string{"STM32F103x8", "Parameter conditions", "Minimum and maximum values", "Typical values"} {
		if strings.Contains(gotClean, mustErase) {
			t.Errorf("可译文本 %q 在 clean_bg.pdf 中漏擦（会导致重影）！", mustErase)
		}
	}

	// 验证第20页（page_index=19，含 2.3.21 GPIOs 与 2.3.22 ADC）：
	// 1. 缩进多行正文必须为 align=left，绝不能误判为 right；
	// 2. 章节号 2.3.21 与标题 GPIOs 拆分为独立块，保留原缩进坐标。
	if len(parsed.Pages) > 19 {
		rawDoc, _ := object.Parse(data)
		rawPages, _ := rawDoc.Pages()
		rawStream19, _ := rawDoc.PageContentStreams(rawPages[19])
		for _, op := range content.ParseOperations(rawStream19) {
			if op.Operator == "TJ" || op.Operator == "Tj" {
				var b bytes.Buffer
				for _, arg := range op.Operands {
					arg.Serialize(&b)
					b.WriteByte(' ')
				}
				if strings.Contains(b.String(), "2.3.21") {
					t.Logf("P19 raw op (%s): %s", op.Operator, b.String())
				}
			}
		}
		for _, b := range parsed.Pages[19].Blocks {
			if strings.Contains(b.OriginalText, "Two 12-bit analog-to-digital") && b.Align != "left" {
				t.Errorf("第20页缩进正文 %q 被误判为 align=%s，应为 left", b.OriginalText, b.Align)
			}
			if strings.Contains(b.OriginalText, "GPIOs (general-purpose") {
				t.Logf("第20页标题块: x0=%.1f align=%s text=%q", b.BBox[0], b.Align, b.OriginalText)
				if b.BBox[0] < 100 {
					t.Errorf("标题 GPIOs 缩进丢失，期望 x0>=100，实际 %.1f", b.BBox[0])
				}
			}
		}
	}
}

func TestStyleColorBoldAndTocExport(t *testing.T) {
	// 1. 验证 sc / scn 设色指令提取与普通字体加粗、目录点线重绘导出
	doc := object.NewEmptyDocument()
	fontRef := doc.AllocObj(object.Dict{
		"Type":     object.Name("Font"),
		"Subtype":  object.Name("Type1"),
		"BaseFont": object.Name("Helvetica-Bold"),
	})
	rawStream := `
BT
/F1 12 Tf
/CS1 cs 0.1 0.4 0.8 scn
1 0 0 1 60 760 Tm
(Blue Bold Heading) Tj
0.25 sc
1 0 0 1 60 730 Tm
(Gray Content Line) Tj
ET
`
	comp, err := object.EncodeFlate([]byte(rawStream))
	if err != nil {
		t.Fatalf("encode stream: %v", err)
	}
	csRef := doc.AllocObj(&object.Stream{
		Dict:    object.Dict{"Filter": object.Name("FlateDecode"), "Length": object.Int(len(comp))},
		RawData: comp,
	})
	pagesRef := doc.AllocObj(object.Dict{})
	pageRef := doc.AllocObj(object.Dict{
		"Type":      object.Name("Page"),
		"Parent":    pagesRef,
		"MediaBox":  object.Array{object.Int(0), object.Int(0), object.Int(595), object.Int(842)},
		"Resources": object.Dict{"Font": object.Dict{"F1": fontRef}},
		"Contents":  csRef,
	})
	doc.SetObj(pagesRef.ObjNum, object.Dict{"Type": object.Name("Pages"), "Kids": object.Array{pageRef}, "Count": object.Int(1)})
	catRef := doc.AllocObj(object.Dict{"Type": object.Name("Catalog"), "Pages": pagesRef})
	doc.Trailer["Root"] = catRef

	pdfBytes, err := doc.Write()
	if err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	parsed, err := ParsePDF(pdfBytes)
	if err != nil {
		t.Fatalf("parse pdf: %v", err)
	}
	if len(parsed.Pages[0].Blocks) < 2 {
		t.Fatalf("expected >= 2 blocks, got %d", len(parsed.Pages[0].Blocks))
	}
	b0 := parsed.Pages[0].Blocks[0]
	b1 := parsed.Pages[0].Blocks[1]
	if b0.Color != "#1A66CC" {
		t.Errorf("expected scn RGB color #1A66CC, got %s", b0.Color)
	}
	if b1.Color != "#404040" {
		t.Errorf("expected sc gray color #404040, got %s", b1.Color)
	}

	// 2. 验证导出流中的粗体描边渲染模式 (2 Tr) 与目录导引点线三段式右对齐
	fontPath := findTestCJKFont()
	if fontPath == "" {
		t.Skip("no CJK TrueType font available on host")
	}
	cleanBg, err := GenerateCleanBg(pdfBytes, map[int][][4]float64{
		0: {b0.BBox, b1.BBox},
	})
	if err != nil {
		t.Fatalf("clean_bg failed: %v", err)
	}
	exported, err := ExportTranslated(cleanBg, fontPath, map[int][]layout.RenderBlock{
		0: {
			{
				BBox:       b0.BBox,
				Text:       "蓝色粗体标题",
				FontSize:   12,
				FontWeight: "bold",
				BlockType:  "heading",
				LineHeight: 1.2,
				Align:      "left",
				Color:      b0.Color,
			},
			{
				BBox:       [4]float64{60, 100, 520, 114},
				Text:       "引脚分布与引脚说明 . . . . . 21",
				FontSize:   12,
				FontWeight: "bold",
				BlockType:  "toc_item",
				LineHeight: 1.2,
				Align:      "left",
				Color:      "#000000",
			},
		},
	})
	if err != nil {
		t.Fatalf("ExportTranslated failed: %v", err)
	}

	expDoc, err := object.Parse(exported)
	if err != nil {
		t.Fatalf("parse exported failed: %v", err)
	}
	expPages, _ := expDoc.Pages()
	expStream, _ := expDoc.PageContentStreams(expPages[0])
	if !bytes.Contains(expStream, []byte("2 Tr")) {
		t.Errorf("expected bold render mode '2 Tr' in exported content stream")
	}

	expParsed, err := ParsePDF(exported)
	if err != nil {
		t.Fatalf("ParsePDF exported failed: %v", err)
	}
	var allText strings.Builder
	for _, blk := range expParsed.Pages[0].Blocks {
		allText.WriteString(blk.OriginalText)
		allText.WriteByte(' ')
	}
	got := allText.String()
	if !strings.Contains(got, "蓝色粗体标题") || !strings.Contains(got, "引脚分布与引脚说明") || !strings.Contains(got, "21") {
		t.Errorf("exported text missing expected bold/toc content, got %q", got)
	}
}

func TestAcademicPaperSerifAndJustify(t *testing.T) {
	// 验证真实学术论文（若本地存在）是否能准确提取 serif 字体家族与两端对齐 justify，并补充合成测试
	matches, _ := filepath.Glob("../../data/uploads/*.pdf")
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		parsed, err := ParsePDF(data)
		if err != nil || len(parsed.Pages) == 0 {
			continue
		}
		for _, b := range parsed.Pages[0].Blocks {
			if strings.Contains(b.OriginalText, "While large language models") {
				t.Logf("ReAct Abstract 解析结果: font_family=%s align=%s lh=%.2f", b.FontFamily, b.Align, b.LineHeight)
				if b.FontFamily != "serif" {
					t.Errorf("expected serif font_family for academic paper Abstract, got %s", b.FontFamily)
				}
				if b.Align != "justify" {
					t.Errorf("expected justify align for academic paper Abstract, got %s", b.Align)
				}
			}
		}
	}

	// 合成一份 Times-Roman 多行两端对齐 PDF 验证通用行为
	doc := object.NewEmptyDocument()
	fontRef := doc.AllocObj(object.Dict{
		"Type":     object.Name("Font"),
		"Subtype":  object.Name("Type1"),
		"BaseFont": object.Name("Times-Roman"),
	})
	rawStream := `
BT
/F1 10 Tf
1 0 0 1 72 740 Tm
(This is the first full line of a justified academic paragraph spanning to margin.) Tj
1 0 0 1 72 728 Tm
(This is the second full line of a justified academic paragraph reaching same end.) Tj
1 0 0 1 72 716 Tm
(Short last line ends here.) Tj
ET
`
	comp, _ := object.EncodeFlate([]byte(rawStream))
	csRef := doc.AllocObj(&object.Stream{
		Dict:    object.Dict{"Filter": object.Name("FlateDecode"), "Length": object.Int(len(comp))},
		RawData: comp,
	})
	pagesRef := doc.AllocObj(object.Dict{})
	pageRef := doc.AllocObj(object.Dict{
		"Type":      object.Name("Page"),
		"Parent":    pagesRef,
		"MediaBox":  object.Array{object.Int(0), object.Int(0), object.Int(595), object.Int(842)},
		"Resources": object.Dict{"Font": object.Dict{"F1": fontRef}},
		"Contents":  csRef,
	})
	doc.SetObj(pagesRef.ObjNum, object.Dict{"Type": object.Name("Pages"), "Kids": object.Array{pageRef}, "Count": object.Int(1)})
	catRef := doc.AllocObj(object.Dict{"Type": object.Name("Catalog"), "Pages": pagesRef})
	doc.Trailer["Root"] = catRef

	pdfBytes, err := doc.Write()
	if err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	parsed, err := ParsePDF(pdfBytes)
	if err != nil {
		t.Fatalf("parse pdf: %v", err)
	}
	if len(parsed.Pages[0].Blocks) != 1 {
		t.Fatalf("expected 1 merged paragraph block, got %d", len(parsed.Pages[0].Blocks))
	}
	blk := parsed.Pages[0].Blocks[0]
	if blk.FontFamily != "serif" {
		t.Errorf("expected serif font_family for Times-Roman, got %s", blk.FontFamily)
	}
	if blk.Align != "justify" {
		t.Errorf("expected justify align for full multi-line paragraph, got %s", blk.Align)
	}
}

