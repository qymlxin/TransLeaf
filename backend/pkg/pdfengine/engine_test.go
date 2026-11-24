package pdfengine

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"translation-assistant/pkg/pdfengine/content"
	"translation-assistant/pkg/pdfengine/layout"
	"translation-assistant/pkg/pdfengine/object"
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
	if len(matches) == 0 {
		t.Skip("local STM32 sample not found, skipping real-sample layout check")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Skip("local STM32 sample unreadable, skipping")
	}

	parsed, err := ParsePDF(data)
	if err != nil {
		t.Fatalf("ParsePDF STM32 failed: %v", err)
	}
	if parsed.PageCount != 116 {
		t.Fatalf("expected 116 pages, got %d", parsed.PageCount)
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

