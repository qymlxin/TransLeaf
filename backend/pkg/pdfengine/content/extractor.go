package content

import (
	"math"
	"strings"

	"translation-assistant/pkg/pdfengine/font"
	"translation-assistant/pkg/pdfengine/object"
)

// VectorLine 表示从 PDF 内容流提取出的水平或垂直矢量分割线（左上角原点坐标系）
type VectorLine struct {
	X0, Y0, X1, Y1 float64
}

// TextSpan 表示从 PDF 内容流中提取出的单个连续文本片段（坐标已统一为左上角原点）
type TextSpan struct {
	Text           string
	BBox           [4]float64 // [x0, y0, x1, y1]，原点在页面左上角
	FontSize       float64
	FontFamily     string // serif / sans / mono
	IsBold         bool
	IsItalic       bool
	Color          string
	IsMathOrSymbol bool
}

// PageExtractResult 表示单页解析结果（文本片段、矢量线条屏障与图像统计）
type PageExtractResult struct {
	PageIndex  int
	Width      float64
	Height     float64
	Spans      []TextSpan
	HBarriers  []VectorLine // 水平矢量线（表格横线、页眉/页脚分隔线）
	VBarriers  []VectorLine // 垂直矢量线（表格竖线、分栏线）
	ImageCount int
}

// ExtractPage 执行页面 Content Stream 状态机并提取全部 TextSpan 与矢量屏障线
func ExtractPage(doc *object.Document, page object.PageRef) (*PageExtractResult, error) {
	pageW := page.MediaBox[2] - page.MediaBox[0]
	pageH := page.MediaBox[3] - page.MediaBox[1]
	if pageW <= 0 {
		pageW = 595.28
	}
	if pageH <= 0 {
		pageH = 841.89
	}

	res := &PageExtractResult{
		PageIndex: page.Index,
		Width:     pageW,
		Height:    pageH,
	}

	rawStream, err := doc.PageContentStreams(page)
	if err != nil || len(rawStream) == 0 {
		return res, err
	}

	fonts := loadResourceFonts(doc, page.Resources)
	gs := NewGraphicsState()
	if page.MediaBox[0] != 0 || page.MediaBox[1] != 0 {
		gs.CTM = Matrix{1, 0, 0, 1, -page.MediaBox[0], -page.MediaBox[1]}
	}

	execStream(doc, rawStream, page.Resources, fonts, gs, pageH, res, 0)
	return res, nil
}

func loadResourceFonts(doc *object.Document, resources object.Dict) map[string]*font.ResolvedFont {
	result := make(map[string]*font.ResolvedFont)
	if resources == nil {
		return result
	}
	fontDict, ok := doc.Resolve(resources["Font"]).(object.Dict)
	if !ok {
		return result
	}
	for name, fObj := range fontDict {
		result[string(name)] = font.ResolveFont(doc, string(name), fObj)
	}
	return result
}

type pathSegment struct {
	x0, y0, x1, y1 float64
}

func execStream(
	doc *object.Document,
	streamData []byte,
	resources object.Dict,
	fonts map[string]*font.ResolvedFont,
	initState GraphicsState,
	pageH float64,
	out *PageExtractResult,
	depth int,
) {
	if depth > 8 {
		return
	}
	ops := ParseOperations(streamData)
	gs := initState
	var stack []GraphicsState

	// 当前正在构建的矢量路径
	var curX, curY float64
	var startX, startY float64
	var pendingSegs []pathSegment

	flushPath := func(paint bool) {
		if paint {
			for _, seg := range pendingSegs {
				px0, py0 := gs.CTM.TransformPoint(seg.x0, seg.y0)
				px1, py1 := gs.CTM.TransformPoint(seg.x1, seg.y1)
				// 转换为左上角原点坐标
				iy0 := pageH - py0
				iy1 := pageH - py1
				dx := math.Abs(px1 - px0)
				dy := math.Abs(iy1 - iy0)
				if dy <= 2.0 && dx >= 10.0 {
					yAvg := (iy0 + iy1) / 2.0
					out.HBarriers = append(out.HBarriers, VectorLine{
						X0: math.Min(px0, px1), Y0: yAvg,
						X1: math.Max(px0, px1), Y1: yAvg,
					})
				} else if dx <= 2.0 && dy >= 10.0 {
					xAvg := (px0 + px1) / 2.0
					out.VBarriers = append(out.VBarriers, VectorLine{
						X0: xAvg, Y0: math.Min(iy0, iy1),
						X1: xAvg, Y1: math.Max(iy0, iy1),
					})
				}
			}
		}
		pendingSegs = pendingSegs[:0]
	}

	for _, op := range ops {
		switch op.Operator {
		case "q":
			stack = append(stack, gs)
		case "Q":
			if len(stack) > 0 {
				gs = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
		case "cm":
			if len(op.Operands) >= 6 {
				m := operandsToMatrix(op.Operands)
				gs.CTM = m.Multiply(gs.CTM)
			}
		// 路径构造指令（提取表格边框与分割线屏障）
		case "m":
			if len(op.Operands) >= 2 {
				curX, _ = object.AsFloat(op.Operands[0])
				curY, _ = object.AsFloat(op.Operands[1])
				startX, startY = curX, curY
			}
		case "l":
			if len(op.Operands) >= 2 {
				nx, _ := object.AsFloat(op.Operands[0])
				ny, _ := object.AsFloat(op.Operands[1])
				pendingSegs = append(pendingSegs, pathSegment{curX, curY, nx, ny})
				curX, curY = nx, ny
			}
		case "h":
			if curX != startX || curY != startY {
				pendingSegs = append(pendingSegs, pathSegment{curX, curY, startX, startY})
				curX, curY = startX, startY
			}
		case "re":
			if len(op.Operands) >= 4 {
				rx, _ := object.AsFloat(op.Operands[0])
				ry, _ := object.AsFloat(op.Operands[1])
				rw, _ := object.AsFloat(op.Operands[2])
				rh, _ := object.AsFloat(op.Operands[3])
				// 细长矩形常用于绘制表格线或下划线，较大矩形四边作为单元格边框
				if math.Abs(rh) <= 3.0 && math.Abs(rw) >= 10.0 {
					pendingSegs = append(pendingSegs, pathSegment{rx, ry + rh/2, rx + rw, ry + rh/2})
				} else if math.Abs(rw) <= 3.0 && math.Abs(rh) >= 10.0 {
					pendingSegs = append(pendingSegs, pathSegment{rx + rw/2, ry, rx + rw/2, ry + rh})
				} else if math.Abs(rw) >= 10.0 && math.Abs(rh) >= 8.0 {
					pendingSegs = append(pendingSegs,
						pathSegment{rx, ry, rx + rw, ry},
						pathSegment{rx, ry + rh, rx + rw, ry + rh},
						pathSegment{rx, ry, rx, ry + rh},
						pathSegment{rx + rw, ry, rx + rw, ry + rh},
					)
				}
			}
		case "S", "s", "f", "F", "f*", "B", "B*", "b", "b*":
			flushPath(true)
		case "n":
			// 裁剪路径不绘制，清空待定路径避免误当表格线
			flushPath(false)

		// 文本与图形状态指令
		case "BT":
			gs.InText = true
			gs.Tm = IdentityMatrix()
			gs.Tlm = IdentityMatrix()
		case "ET":
			gs.InText = false
		case "Tf":
			if len(op.Operands) >= 2 {
				if n, ok := op.Operands[0].(object.Name); ok {
					gs.FontName = string(n)
				}
				if sz, ok := object.AsFloat(op.Operands[1]); ok {
					gs.FontSize = sz
				}
			}
		case "Tc":
			if len(op.Operands) >= 1 {
				if v, ok := object.AsFloat(op.Operands[0]); ok {
					gs.CharSpace = v
				}
			}
		case "Tw":
			if len(op.Operands) >= 1 {
				if v, ok := object.AsFloat(op.Operands[0]); ok {
					gs.WordSpace = v
				}
			}
		case "Tz":
			if len(op.Operands) >= 1 {
				if v, ok := object.AsFloat(op.Operands[0]); ok && v > 0 {
					gs.HorizScale = v
				}
			}
		case "TL":
			if len(op.Operands) >= 1 {
				if v, ok := object.AsFloat(op.Operands[0]); ok {
					gs.Leading = v
				}
			}
		case "Ts":
			if len(op.Operands) >= 1 {
				if v, ok := object.AsFloat(op.Operands[0]); ok {
					gs.TextRise = v
				}
			}
		case "Tm":
			if len(op.Operands) >= 6 {
				m := operandsToMatrix(op.Operands)
				gs.Tm = m
				gs.Tlm = m
			}
		case "Td":
			if len(op.Operands) >= 2 {
				tx, _ := object.AsFloat(op.Operands[0])
				ty, _ := object.AsFloat(op.Operands[1])
				m := Matrix{1, 0, 0, 1, tx, ty}
				gs.Tlm = m.Multiply(gs.Tlm)
				gs.Tm = gs.Tlm
			}
		case "TD":
			if len(op.Operands) >= 2 {
				tx, _ := object.AsFloat(op.Operands[0])
				ty, _ := object.AsFloat(op.Operands[1])
				gs.Leading = -ty
				m := Matrix{1, 0, 0, 1, tx, ty}
				gs.Tlm = m.Multiply(gs.Tlm)
				gs.Tm = gs.Tlm
			}
		case "T*":
			m := Matrix{1, 0, 0, 1, 0, -gs.Leading}
			gs.Tlm = m.Multiply(gs.Tlm)
			gs.Tm = gs.Tlm
		case "rg":
			if len(op.Operands) >= 3 {
				r, _ := object.AsFloat(op.Operands[0])
				g, _ := object.AsFloat(op.Operands[1])
				b, _ := object.AsFloat(op.Operands[2])
				gs.FillColor = RGBToHex(r, g, b)
			}
		case "g":
			if len(op.Operands) >= 1 {
				gv, _ := object.AsFloat(op.Operands[0])
				gs.FillColor = RGBToHex(gv, gv, gv)
			}
		case "k":
			if len(op.Operands) >= 4 {
				c, _ := object.AsFloat(op.Operands[0])
				m, _ := object.AsFloat(op.Operands[1])
				y, _ := object.AsFloat(op.Operands[2])
				k, _ := object.AsFloat(op.Operands[3])
				gs.FillColor = CMYKToHex(c, m, y, k)
			}
		case "sc", "scn":
			var nums []float64
			for _, operand := range op.Operands {
				if v, ok := object.AsFloat(operand); ok {
					nums = append(nums, v)
				} else {
					break
				}
			}
			switch len(nums) {
			case 1:
				gs.FillColor = RGBToHex(nums[0], nums[0], nums[0])
			case 3:
				gs.FillColor = RGBToHex(nums[0], nums[1], nums[2])
			case 4:
				gs.FillColor = CMYKToHex(nums[0], nums[1], nums[2], nums[3])
			}
		case "Tj":
			if len(op.Operands) >= 1 {
				if s, ok := op.Operands[0].(object.String); ok {
					emitTextString(&gs, fonts, s.Value, pageH, out)
				}
			}
		case "'":
			m := Matrix{1, 0, 0, 1, 0, -gs.Leading}
			gs.Tlm = m.Multiply(gs.Tlm)
			gs.Tm = gs.Tlm
			if len(op.Operands) >= 1 {
				if s, ok := op.Operands[0].(object.String); ok {
					emitTextString(&gs, fonts, s.Value, pageH, out)
				}
			}
		case "\"":
			if len(op.Operands) >= 3 {
				aw, _ := object.AsFloat(op.Operands[0])
				ac, _ := object.AsFloat(op.Operands[1])
				gs.WordSpace = aw
				gs.CharSpace = ac
				m := Matrix{1, 0, 0, 1, 0, -gs.Leading}
				gs.Tlm = m.Multiply(gs.Tlm)
				gs.Tm = gs.Tlm
				if s, ok := op.Operands[2].(object.String); ok {
					emitTextString(&gs, fonts, s.Value, pageH, out)
				}
			}
		case "TJ":
			if len(op.Operands) >= 1 {
				if arr, ok := op.Operands[0].(object.Array); ok {
					emitTJArray(&gs, fonts, arr, pageH, out)
				}
			}
		case "Do":
			if len(op.Operands) >= 1 {
				if xName, ok := op.Operands[0].(object.Name); ok {
					handleXObject(doc, resources, string(xName), gs, pageH, out, depth+1)
				}
			}
		}
	}
}

func emitTextString(
	gs *GraphicsState,
	fonts map[string]*font.ResolvedFont,
	raw []byte,
	pageH float64,
	out *PageExtractResult,
) {
	if len(raw) == 0 {
		return
	}
	rf := fonts[gs.FontName]
	var texts []string
	var codes []uint32
	if rf != nil {
		texts, codes = rf.DecodeString(raw)
	} else {
		for _, b := range raw {
			texts = append(texts, font.DecodeWinAnsi(b))
			codes = append(codes, uint32(b))
		}
	}

	trm := gs.Tm.Multiply(gs.CTM)
	startX, startY := trm.TransformPoint(0, gs.TextRise)
	effSize := gs.FontSize * trm.VerticalScale()
	if effSize <= 0 {
		effSize = 10
	}

	var sb strings.Builder
	hScale := gs.HorizScale / 100.0
	tabSpaceThreshold := gs.FontSize * 1.1

	flushSeg := func(segStartX, segEndX float64) {
		txt := normalizeExtractedText(sb.String())
		sb.Reset()
		if strings.TrimSpace(txt) == "" {
			return
		}
		sx, ex := segStartX, segEndX
		if ex < sx {
			sx, ex = ex, sx
		}
		if ex-sx < 1 {
			ex = sx + effSize*0.5
		}
		topY := pageH - (startY + effSize*0.85)
		botY := pageH - (startY - effSize*0.22)
		span := TextSpan{
			Text:       txt,
			BBox:       [4]float64{sx, topY, ex, botY},
			FontSize:   effSize,
			FontFamily: "sans",
			Color:      gs.FillColor,
		}
		if rf != nil {
			if rf.FontFamily != "" {
				span.FontFamily = rf.FontFamily
			}
			span.IsBold = rf.IsBold
			span.IsItalic = rf.IsItalic
			span.IsMathOrSymbol = rf.IsMathOrSymbol && !isBulletSymbolOnly(txt)
		}
		out.Spans = append(out.Spans, span)
	}

	var pendingSpaceDx float64
	segStartX := startX
	hasContent := false

	for i, t := range texts {
		w := 550.0
		if rf != nil {
			w = rf.GlyphWidth(codes[i], t)
		}
		tx := ((w / 1000.0 * gs.FontSize) + gs.CharSpace) * hScale
		if t == " " {
			tx += gs.WordSpace * hScale
		}

		if strings.TrimSpace(t) == "" {
			pendingSpaceDx += tx
			gs.Tm = Matrix{1, 0, 0, 1, tx, 0}.Multiply(gs.Tm)
			if hasContent && pendingSpaceDx > tabSpaceThreshold {
				// 回退空白前的位置作为上一段右边界
				endX, _ := Matrix{1, 0, 0, 1, -pendingSpaceDx, 0}.Multiply(gs.Tm).Multiply(gs.CTM).TransformPoint(0, gs.TextRise)
				flushSeg(segStartX, endX)
				hasContent = false
				pendingSpaceDx = 0
			}
			continue
		}

		if !hasContent {
			curTrm := gs.Tm.Multiply(gs.CTM)
			segStartX, startY = curTrm.TransformPoint(0, gs.TextRise)
			hasContent = true
			pendingSpaceDx = 0
		} else if pendingSpaceDx > 0 {
			sb.WriteByte(' ')
			pendingSpaceDx = 0
		}
		sb.WriteString(t)
		gs.Tm = Matrix{1, 0, 0, 1, tx, 0}.Multiply(gs.Tm)
	}

	if hasContent {
		endX, _ := Matrix{1, 0, 0, 1, -pendingSpaceDx, 0}.Multiply(gs.Tm).Multiply(gs.CTM).TransformPoint(0, gs.TextRise)
		flushSeg(segStartX, endX)
	}
}

func emitTJArray(
	gs *GraphicsState,
	fonts map[string]*font.ResolvedFont,
	arr object.Array,
	pageH float64,
	out *PageExtractResult,
) {
	rf := fonts[gs.FontName]
	trm := gs.Tm.Multiply(gs.CTM)
	startX, startY := trm.TransformPoint(0, gs.TextRise)
	effSize := gs.FontSize * trm.VerticalScale()
	if effSize <= 0 {
		effSize = 10
	}

	var sb strings.Builder
	hScale := gs.HorizScale / 100.0
	tabSpaceThreshold := gs.FontSize * 1.1

	flushSeg := func(segEndX float64) {
		txt := normalizeExtractedText(sb.String())
		sb.Reset()
		if strings.TrimSpace(txt) == "" {
			return
		}
		sx, ex := startX, segEndX
		if ex < sx {
			sx, ex = ex, sx
		}
		if ex-sx < 1 {
			ex = sx + effSize*0.5
		}
		topY := pageH - (startY + effSize*0.85)
		botY := pageH - (startY - effSize*0.22)
		span := TextSpan{
			Text:       txt,
			BBox:       [4]float64{sx, topY, ex, botY},
			FontSize:   effSize,
			FontFamily: "sans",
			Color:      gs.FillColor,
		}
		if rf != nil {
			if rf.FontFamily != "" {
				span.FontFamily = rf.FontFamily
			}
			span.IsBold = rf.IsBold
			span.IsItalic = rf.IsItalic
			span.IsMathOrSymbol = rf.IsMathOrSymbol && !isBulletSymbolOnly(txt)
		}
		out.Spans = append(out.Spans, span)
	}

	var pendingSpaceDx float64
	hasContent := false

	for _, item := range arr {
		switch v := item.(type) {
		case object.String:
			var texts []string
			var codes []uint32
			if rf != nil {
				texts, codes = rf.DecodeString(v.Value)
			} else {
				for _, b := range v.Value {
					texts = append(texts, font.DecodeWinAnsi(b))
					codes = append(codes, uint32(b))
				}
			}
			for i, t := range texts {
				w := 550.0
				if rf != nil {
					w = rf.GlyphWidth(codes[i], t)
				}
				tx := ((w / 1000.0 * gs.FontSize) + gs.CharSpace) * hScale
				if t == " " {
					tx += gs.WordSpace * hScale
				}
				if strings.TrimSpace(t) == "" {
					pendingSpaceDx += tx
					gs.Tm = Matrix{1, 0, 0, 1, tx, 0}.Multiply(gs.Tm)
					continue
				}
				if hasContent && pendingSpaceDx > tabSpaceThreshold {
					endX, _ := Matrix{1, 0, 0, 1, -pendingSpaceDx, 0}.Multiply(gs.Tm).Multiply(gs.CTM).TransformPoint(0, gs.TextRise)
					flushSeg(endX)
					hasContent = false
					pendingSpaceDx = 0
				}
				if !hasContent {
					curTrm := gs.Tm.Multiply(gs.CTM)
					startX, startY = curTrm.TransformPoint(0, gs.TextRise)
					hasContent = true
					pendingSpaceDx = 0
				} else if pendingSpaceDx > 0 {
					sb.WriteByte(' ')
					pendingSpaceDx = 0
				}
				sb.WriteString(t)
				gs.Tm = Matrix{1, 0, 0, 1, tx, 0}.Multiply(gs.Tm)
			}
		case object.Int, object.Real:
			adj, _ := object.AsFloat(v)
			tx := (-adj / 1000.0) * gs.FontSize * hScale
			gs.Tm = Matrix{1, 0, 0, 1, tx, 0}.Multiply(gs.Tm)
			if adj <= -1100 && hasContent {
				endX, _ := Matrix{1, 0, 0, 1, -tx - pendingSpaceDx, 0}.Multiply(gs.Tm).Multiply(gs.CTM).TransformPoint(0, gs.TextRise)
				flushSeg(endX)
				hasContent = false
				pendingSpaceDx = 0
			} else if pendingSpaceDx > 0 {
				// 叠加正/负 TJ 调整（如 FrameMaker 先加 Tw 空格再用 +1670 拉回，合并净空白后再判断是否切分）
				pendingSpaceDx += tx
				if pendingSpaceDx < 0 {
					pendingSpaceDx = 0
				}
			} else if adj <= -180 && hasContent {
				pendingSpaceDx += tx
			}
		}
	}

	if hasContent {
		endX, _ := Matrix{1, 0, 0, 1, -pendingSpaceDx, 0}.Multiply(gs.Tm).Multiply(gs.CTM).TransformPoint(0, gs.TextRise)
		flushSeg(endX)
	}
}

func handleXObject(
	doc *object.Document,
	resources object.Dict,
	xName string,
	gs GraphicsState,
	pageH float64,
	out *PageExtractResult,
	depth int,
) {
	if resources == nil {
		return
	}
	xDict, ok := doc.Resolve(resources["XObject"]).(object.Dict)
	if !ok {
		return
	}
	xObj := doc.Resolve(xDict[object.Name(xName)])
	stm, ok := xObj.(*object.Stream)
	if !ok {
		return
	}
	subType, _ := doc.Resolve(stm.Dict["Subtype"]).(object.Name)
	if subType == "Image" {
		out.ImageCount++
		return
	}
	if subType != "Form" {
		return
	}

	formState := gs
	if mArr, ok := doc.Resolve(stm.Dict["Matrix"]).(object.Array); ok && len(mArr) >= 6 {
		fm := operandsToMatrix(mArr)
		formState.CTM = fm.Multiply(formState.CTM)
	}

	formRes := resources
	if rDict, ok := doc.Resolve(stm.Dict["Resources"]).(object.Dict); ok {
		formRes = rDict
	}
	formFonts := loadResourceFonts(doc, formRes)
	decoded, err := object.DecodeStream(stm, doc.Resolve)
	if err != nil {
		return
	}
	execStream(doc, decoded, formRes, formFonts, formState, pageH, out, depth)
}

// normalizeExtractedText 将 Symbol/Wingdings 常见私用区列表符规范为标准 Unicode，清理异常控制符
func normalizeExtractedText(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch r {
		case 0xF0B7, 0xF0A7, 0xF076, 0xF0D8, 0xF06C, 0xF0A8:
			sb.WriteRune('•')
		case 0xF020, 0xF0A0, 0x00A0:
			sb.WriteByte(' ')
		default:
			// 过滤其余私用区异常符号（如 \uF020 附近的不可见字符）
			if r >= 0xE000 && r <= 0xF8FF {
				continue
			}
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func isBulletSymbolOnly(s string) bool {
	t := strings.TrimSpace(s)
	return t == "•" || t == "–" || t == "-" || t == "▪" || t == "○"
}

func operandsToMatrix(ops []object.Object) Matrix {
	var m Matrix
	for i := 0; i < 6 && i < len(ops); i++ {
		m[i], _ = object.AsFloat(ops[i])
	}
	return m
}
