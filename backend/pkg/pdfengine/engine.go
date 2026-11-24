package pdfengine

import (
	"bytes"
	"fmt"

	"transleaf/pkg/pdfengine/content"
	"transleaf/pkg/pdfengine/layout"
	"transleaf/pkg/pdfengine/object"
	"transleaf/pkg/pdfengine/redactor"
	"transleaf/pkg/pdfengine/subsetter"
)

// ParsedPage 表示单页解析结果（尺寸与合并后的文本块）
type ParsedPage struct {
	PageIndex int
	Width     float64
	Height    float64
	Blocks    []content.ParsedBlock
}

// ParseOutput 表示整份 PDF 的结构化解析输出
type ParseOutput struct {
	PageCount   int
	IsEncrypted bool
	IsScanned   bool
	Pages       []ParsedPage
}

// ParsePDF 解析 PDF 字节流，提取页面尺寸、逻辑文本块、双栏阅读顺序并检测加密/扫描状态
func ParsePDF(data []byte) (*ParseOutput, error) {
	doc, err := object.Parse(data)
	if err != nil {
		return nil, err
	}
	if doc.IsEncrypted {
		return &ParseOutput{IsEncrypted: true}, nil
	}

	pages, err := doc.Pages()
	if err != nil {
		return nil, err
	}

	out := &ParseOutput{
		PageCount: len(pages),
		Pages:     make([]ParsedPage, 0, len(pages)),
	}

	totalChars := 0
	totalImages := 0
	for _, p := range pages {
		extracted, err := content.ExtractPage(doc, p)
		if err != nil {
			// 单页异常降级为空页，避免中断整份文档解析
			out.Pages = append(out.Pages, ParsedPage{
				PageIndex: p.Index,
				Width:     p.MediaBox[2] - p.MediaBox[0],
				Height:    p.MediaBox[3] - p.MediaBox[1],
			})
			continue
		}
		totalImages += extracted.ImageCount
		blocks := content.MergeLayout(extracted)
		for _, b := range blocks {
			totalChars += len([]rune(b.OriginalText))
		}
		out.Pages = append(out.Pages, ParsedPage{
			PageIndex: extracted.PageIndex,
			Width:     extracted.Width,
			Height:    extracted.Height,
			Blocks:    blocks,
		})
	}

	if len(pages) > 0 && totalChars/len(pages) < 10 && totalImages > 0 {
		out.IsScanned = true
	}
	return out, nil
}

// GenerateCleanBg 使用指令级无损擦除器移除各页指定矩形内的文字，输出纯净底图 clean_bg.pdf
func GenerateCleanBg(origData []byte, redactByPage map[int][][4]float64) ([]byte, error) {
	doc, err := object.Parse(origData)
	if err != nil {
		return nil, err
	}
	if doc.IsEncrypted {
		return nil, fmt.Errorf("cannot redact encrypted PDF")
	}

	pages, err := doc.Pages()
	if err != nil {
		return nil, err
	}

	for _, p := range pages {
		rects := redactByPage[p.Index]
		if len(rects) == 0 {
			continue
		}
		if err := redactor.RedactPage(doc, p, rects); err != nil {
			return nil, fmt.Errorf("redact page %d failed: %w", p.Index, err)
		}
	}

	return doc.Write()
}

// ExportTranslated 基于纯净底图 clean_bg.pdf 嵌入中文字体并重绘所有译文块，导出高保真纯译文 PDF
func ExportTranslated(
	cleanBgData []byte,
	fontPath string,
	blocksByPage map[int][]layout.RenderBlock,
) ([]byte, error) {
	doc, err := object.Parse(cleanBgData)
	if err != nil {
		return nil, err
	}

	lf, err := subsetter.LoadTrueType(fontPath)
	if err != nil {
		return nil, err
	}

	// 收集整份文档所有译文用到的字符（用于精准构建 /W 与 ToUnicode CMap）
	usedRunes := make(map[rune]bool)
	usedRunes[' '] = true
	for _, blks := range blocksByPage {
		for _, b := range blks {
			for _, r := range b.Text {
				usedRunes[r] = true
			}
		}
	}

	fontRef, err := subsetter.EmbedCIDFont(doc, lf, usedRunes)
	if err != nil {
		return nil, err
	}

	const fontResName = "FTransCJK"
	pages, err := doc.Pages()
	if err != nil {
		return nil, err
	}

	for _, p := range pages {
		blks := blocksByPage[p.Index]
		if len(blks) == 0 || p.ObjNum <= 0 {
			continue
		}
		pageH := p.MediaBox[3] - p.MediaBox[1]
		if pageH <= 0 {
			pageH = 841.89
		}

		// 拼接该页所有译文绘制流
		var overlayBuf bytes.Buffer
		// 先用 q/Q 保护原页面图形状态，若原 MediaBox 非 (0,0) 起步则平移对齐
		overlayBuf.WriteString("q\n")
		if p.MediaBox[0] != 0 || p.MediaBox[1] != 0 {
			fmt.Fprintf(&overlayBuf, "1 0 0 1 %.2f %.2f cm\n", p.MediaBox[0], p.MediaBox[1])
		}
		for _, blk := range blks {
			overlayBuf.Write(layout.EmitBlockStream(lf, fontResName, blk, pageH))
		}
		overlayBuf.WriteString("Q\n")

		compressed, err := object.EncodeFlate(overlayBuf.Bytes())
		if err != nil {
			return nil, err
		}
		overlayStreamRef := doc.AllocObj(&object.Stream{
			Dict: object.Dict{
				"Filter": object.Name("FlateDecode"),
				"Length": object.Int(len(compressed)),
			},
			RawData: compressed,
		})

		// 将译文流追加到页面 /Contents（保留原有底图流不被修改）
		newPageDict := p.Dict.Clone()
		switch origC := p.Dict["Contents"].(type) {
		case object.Array:
			arr := make(object.Array, len(origC), len(origC)+1)
			copy(arr, origC)
			arr = append(arr, overlayStreamRef)
			newPageDict["Contents"] = arr
		case nil:
			newPageDict["Contents"] = overlayStreamRef
		default:
			newPageDict["Contents"] = object.Array{origC, overlayStreamRef}
		}

		// 注册 /FTransCJK 字体到页面 /Resources /Font
		resDict := p.Resources.Clone()
		if resDict == nil {
			resDict = make(object.Dict)
		}
		var fontDict object.Dict
		if existingFont, ok := doc.Resolve(resDict["Font"]).(object.Dict); ok {
			fontDict = existingFont.Clone()
		} else {
			fontDict = make(object.Dict)
		}
		fontDict[object.Name(fontResName)] = fontRef
		resDict["Font"] = fontDict
		newPageDict["Resources"] = resDict

		doc.SetObj(p.ObjNum, newPageDict)
	}

	return doc.Write()
}

// ExportBilingualInterleaved 生成“奇原偶译”交替页双语对照 PDF
func ExportBilingualInterleaved(origData, transData []byte) ([]byte, error) {
	origDoc, err := object.Parse(origData)
	if err != nil {
		return nil, err
	}
	transDoc, err := object.Parse(transData)
	if err != nil {
		return nil, err
	}

	origPages, err := origDoc.Pages()
	if err != nil {
		return nil, err
	}
	transPages, err := transDoc.Pages()
	if err != nil {
		return nil, err
	}

	outDoc := object.NewEmptyDocument()
	pagesRootRef := outDoc.AllocObj(object.Dict{}) // 占位，最后回填
	var kids object.Array

	maxLen := len(origPages)
	if len(transPages) > maxLen {
		maxLen = len(transPages)
	}

	origMap := make(map[int]int)
	transMap := make(map[int]int)

	for i := 0; i < maxLen; i++ {
		if i < len(origPages) {
			ref := clonePageToDoc(origDoc, origPages[i], outDoc, pagesRootRef, origMap)
			kids = append(kids, ref)
		}
		if i < len(transPages) {
			ref := clonePageToDoc(transDoc, transPages[i], outDoc, pagesRootRef, transMap)
			kids = append(kids, ref)
		}
	}

	outDoc.SetObj(pagesRootRef.ObjNum, object.Dict{
		"Type":  object.Name("Pages"),
		"Kids":  kids,
		"Count": object.Int(len(kids)),
	})
	catalogRef := outDoc.AllocObj(object.Dict{
		"Type":  object.Name("Catalog"),
		"Pages": pagesRootRef,
	})
	outDoc.Trailer["Root"] = catalogRef
	return outDoc.Write()
}

// ExportBilingualSideBySide 生成“左原右译”双宽单页并排双语对照 PDF（基于 Form XObject 无损拼装）
func ExportBilingualSideBySide(origData, transData []byte) ([]byte, error) {
	origDoc, err := object.Parse(origData)
	if err != nil {
		return nil, err
	}
	transDoc, err := object.Parse(transData)
	if err != nil {
		return nil, err
	}

	origPages, err := origDoc.Pages()
	if err != nil {
		return nil, err
	}
	transPages, err := transDoc.Pages()
	if err != nil {
		return nil, err
	}

	outDoc := object.NewEmptyDocument()
	pagesRootRef := outDoc.AllocObj(object.Dict{})
	var kids object.Array

	origMap := make(map[int]int)
	transMap := make(map[int]int)

	n := len(origPages)
	if len(transPages) < n {
		n = len(transPages)
	}

	for i := 0; i < n; i++ {
		op := origPages[i]
		tp := transPages[i]
		w := op.MediaBox[2] - op.MediaBox[0]
		h := op.MediaBox[3] - op.MediaBox[1]
		if w <= 0 {
			w = 595.28
		}
		if h <= 0 {
			h = 841.89
		}

		origFormRef, err := pageToFormXObject(origDoc, op, outDoc, origMap)
		if err != nil {
			return nil, err
		}
		transFormRef, err := pageToFormXObject(transDoc, tp, outDoc, transMap)
		if err != nil {
			return nil, err
		}

		var cs bytes.Buffer
		// 左侧原页
		fmt.Fprintf(&cs, "q 1 0 0 1 %.2f %.2f cm /POrig Do Q\n", -op.MediaBox[0], -op.MediaBox[1])
		// 右侧译文页
		fmt.Fprintf(&cs, "q 1 0 0 1 %.2f %.2f cm /PTrans Do Q\n", w-tp.MediaBox[0], -tp.MediaBox[1])
		// 中缝浅灰分割线
		fmt.Fprintf(&cs, "q 0.85 0.85 0.85 RG 0.5 w %.2f 0 m %.2f %.2f l S Q\n", w, w, h)

		comp, err := object.EncodeFlate(cs.Bytes())
		if err != nil {
			return nil, err
		}
		csRef := outDoc.AllocObj(&object.Stream{
			Dict: object.Dict{
				"Filter": object.Name("FlateDecode"),
				"Length": object.Int(len(comp)),
			},
			RawData: comp,
		})

		pageRef := outDoc.AllocObj(object.Dict{
			"Type":     object.Name("Page"),
			"Parent":   pagesRootRef,
			"MediaBox": object.Array{object.Int(0), object.Int(0), object.Real(w * 2), object.Real(h)},
			"Resources": object.Dict{
				"XObject": object.Dict{
					"POrig":  origFormRef,
					"PTrans": transFormRef,
				},
			},
			"Contents": csRef,
		})
		kids = append(kids, pageRef)
	}

	outDoc.SetObj(pagesRootRef.ObjNum, object.Dict{
		"Type":  object.Name("Pages"),
		"Kids":  kids,
		"Count": object.Int(len(kids)),
	})
	catalogRef := outDoc.AllocObj(object.Dict{
		"Type":  object.Name("Catalog"),
		"Pages": pagesRootRef,
	})
	outDoc.Trailer["Root"] = catalogRef
	return outDoc.Write()
}

func pageToFormXObject(src *object.Document, page object.PageRef, dst *object.Document, objMap map[int]int) (object.IndirectRef, error) {
	rawStream, err := src.PageContentStreams(page)
	if err != nil {
		return object.IndirectRef{}, err
	}
	comp, err := object.EncodeFlate(rawStream)
	if err != nil {
		return object.IndirectRef{}, err
	}
	clonedRes := cloneObject(src, page.Resources, dst, objMap)
	return dst.AllocObj(&object.Stream{
		Dict: object.Dict{
			"Type":      object.Name("XObject"),
			"Subtype":   object.Name("Form"),
			"BBox":      object.Array{object.Real(page.MediaBox[0]), object.Real(page.MediaBox[1]), object.Real(page.MediaBox[2]), object.Real(page.MediaBox[3])},
			"Resources": clonedRes,
			"Filter":    object.Name("FlateDecode"),
			"Length":    object.Int(len(comp)),
		},
		RawData: comp,
	}), nil
}

func clonePageToDoc(src *object.Document, page object.PageRef, dst *object.Document, parentRef object.IndirectRef, objMap map[int]int) object.IndirectRef {
	d := make(object.Dict)
	d["Type"] = object.Name("Page")
	d["Parent"] = parentRef
	d["MediaBox"] = object.Array{
		object.Real(page.MediaBox[0]),
		object.Real(page.MediaBox[1]),
		object.Real(page.MediaBox[2]),
		object.Real(page.MediaBox[3]),
	}
	if page.Resources != nil {
		d["Resources"] = cloneObject(src, page.Resources, dst, objMap)
	}
	if c, ok := page.Dict["Contents"]; ok {
		d["Contents"] = cloneObject(src, c, dst, objMap)
	}
	if page.Rotate != 0 {
		d["Rotate"] = object.Int(page.Rotate)
	}
	return dst.AllocObj(d)
}

func cloneObject(src *object.Document, obj object.Object, dst *object.Document, objMap map[int]int) object.Object {
	switch v := obj.(type) {
	case object.IndirectRef:
		if mapped, ok := objMap[v.ObjNum]; ok {
			return object.IndirectRef{ObjNum: mapped, GenNum: 0}
		}
		target, err := src.GetObject(v.ObjNum)
		if err != nil || target == nil {
			return object.Null{}
		}
		// 预先占位防循环引用
		placeholder := dst.AllocObj(object.Null{})
		objMap[v.ObjNum] = placeholder.ObjNum
		cloned := cloneObject(src, target, dst, objMap)
		dst.SetObj(placeholder.ObjNum, cloned)
		return placeholder
	case object.Array:
		out := make(object.Array, len(v))
		for i, item := range v {
			out[i] = cloneObject(src, item, dst, objMap)
		}
		return out
	case object.Dict:
		out := make(object.Dict, len(v))
		for k, val := range v {
			if k == "Parent" {
				continue
			}
			out[k] = cloneObject(src, val, dst, objMap)
		}
		return out
	case *object.Stream:
		newDict := make(object.Dict, len(v.Dict))
		for k, val := range v.Dict {
			newDict[k] = cloneObject(src, val, dst, objMap)
		}
		rawCopy := make([]byte, len(v.RawData))
		copy(rawCopy, v.RawData)
		return &object.Stream{Dict: newDict, RawData: rawCopy}
	default:
		return v
	}
}
