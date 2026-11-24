package redactor

import (
	"math"
	"strings"

	"translation-assistant/pkg/pdfengine/content"
	"translation-assistant/pkg/pdfengine/font"
	"translation-assistant/pkg/pdfengine/object"
)

// RedactPage 对指定页面执行指令级无损文字擦除：
// 1. 100% 保留图片 (Do Image)、矢量路径 (m/l/c/re/S/f) 与图形状态；
// 2. 对 TJ 数组内部逐段精准判定，被擦除段转为等效数值偏移，不误删同指令内的非翻译标号；
// 3. 递归按需克隆并擦除 Form XObject 中的可译文字，消除页眉/模板重影；
// 4. 擦除文本时使用纯 [adj] TJ 推进 Tm，绝不污染行矩阵 Tlm。
func RedactPage(doc *object.Document, page object.PageRef, redactRects [][4]float64) error {
	if len(redactRects) == 0 || page.ObjNum <= 0 {
		return nil
	}

	rawStream, err := doc.PageContentStreams(page)
	if err != nil || len(rawStream) == 0 {
		return err
	}

	pageH := page.MediaBox[3] - page.MediaBox[1]
	if pageH <= 0 {
		pageH = 841.89
	}

	fonts := loadFonts(doc, page.Resources)
	gs := content.NewGraphicsState()
	if page.MediaBox[0] != 0 || page.MediaBox[1] != 0 {
		gs.CTM = content.Matrix{1, 0, 0, 1, -page.MediaBox[0], -page.MediaBox[1]}
	}

	ops := content.ParseOperations(rawStream)
	updatedXObjs := make(map[string]object.IndirectRef)
	filtered, modified := filterOperations(doc, ops, page.Resources, fonts, gs, pageH, redactRects, updatedXObjs, 0)
	if !modified && len(updatedXObjs) == 0 {
		return nil
	}

	newPageDict := page.Dict.Clone()

	if modified {
		newRaw := content.SerializeOperations(filtered)
		compressed, err := object.EncodeFlate(newRaw)
		if err != nil {
			return err
		}
		newStreamRef := doc.AllocObj(&object.Stream{
			Dict: object.Dict{
				"Filter": object.Name("FlateDecode"),
				"Length": object.Int(len(compressed)),
			},
			RawData: compressed,
		})
		newPageDict["Contents"] = newStreamRef
	}

	if len(updatedXObjs) > 0 {
		resDict := page.Resources.Clone()
		if resDict == nil {
			resDict = make(object.Dict)
		}
		var xDict object.Dict
		if existingX, ok := doc.Resolve(resDict["XObject"]).(object.Dict); ok {
			xDict = existingX.Clone()
		} else {
			xDict = make(object.Dict)
		}
		for name, ref := range updatedXObjs {
			xDict[object.Name(name)] = ref
		}
		resDict["XObject"] = xDict
		newPageDict["Resources"] = resDict
	}

	doc.SetObj(page.ObjNum, newPageDict)
	return nil
}

func loadFonts(doc *object.Document, resources object.Dict) map[string]*font.ResolvedFont {
	res := make(map[string]*font.ResolvedFont)
	if resources == nil {
		return res
	}
	fDict, ok := doc.Resolve(resources["Font"]).(object.Dict)
	if !ok {
		return res
	}
	for name, fObj := range fDict {
		res[string(name)] = font.ResolveFont(doc, string(name), fObj)
	}
	return res
}

func filterOperations(
	doc *object.Document,
	ops []content.Operation,
	resources object.Dict,
	fonts map[string]*font.ResolvedFont,
	initState content.GraphicsState,
	pageH float64,
	redactRects [][4]float64,
	updatedXObjs map[string]object.IndirectRef,
	depth int,
) ([]content.Operation, bool) {
	gs := initState
	var stack []content.GraphicsState
	out := make([]content.Operation, 0, len(ops))
	modified := false

	for _, op := range ops {
		switch op.Operator {
		case "q":
			stack = append(stack, gs)
			out = append(out, op)
		case "Q":
			if len(stack) > 0 {
				gs = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
			out = append(out, op)
		case "cm":
			if len(op.Operands) >= 6 {
				m := toMatrix(op.Operands)
				gs.CTM = m.Multiply(gs.CTM)
			}
			out = append(out, op)
		case "BT":
			gs.InText = true
			gs.Tm = content.IdentityMatrix()
			gs.Tlm = content.IdentityMatrix()
			out = append(out, op)
		case "ET":
			gs.InText = false
			out = append(out, op)
		case "Tf":
			if len(op.Operands) >= 2 {
				if n, ok := op.Operands[0].(object.Name); ok {
					gs.FontName = string(n)
				}
				if sz, ok := object.AsFloat(op.Operands[1]); ok {
					gs.FontSize = sz
				}
			}
			out = append(out, op)
		case "Tc":
			if len(op.Operands) >= 1 {
				if v, ok := object.AsFloat(op.Operands[0]); ok {
					gs.CharSpace = v
				}
			}
			out = append(out, op)
		case "Tw":
			if len(op.Operands) >= 1 {
				if v, ok := object.AsFloat(op.Operands[0]); ok {
					gs.WordSpace = v
				}
			}
			out = append(out, op)
		case "Tz":
			if len(op.Operands) >= 1 {
				if v, ok := object.AsFloat(op.Operands[0]); ok && v > 0 {
					gs.HorizScale = v
				}
			}
			out = append(out, op)
		case "TL":
			if len(op.Operands) >= 1 {
				if v, ok := object.AsFloat(op.Operands[0]); ok {
					gs.Leading = v
				}
			}
			out = append(out, op)
		case "Ts":
			if len(op.Operands) >= 1 {
				if v, ok := object.AsFloat(op.Operands[0]); ok {
					gs.TextRise = v
				}
			}
			out = append(out, op)
		case "Tm":
			if len(op.Operands) >= 6 {
				m := toMatrix(op.Operands)
				gs.Tm = m
				gs.Tlm = m
			}
			out = append(out, op)
		case "Td":
			if len(op.Operands) >= 2 {
				tx, _ := object.AsFloat(op.Operands[0])
				ty, _ := object.AsFloat(op.Operands[1])
				m := content.Matrix{1, 0, 0, 1, tx, ty}
				gs.Tlm = m.Multiply(gs.Tlm)
				gs.Tm = gs.Tlm
			}
			out = append(out, op)
		case "TD":
			if len(op.Operands) >= 2 {
				tx, _ := object.AsFloat(op.Operands[0])
				ty, _ := object.AsFloat(op.Operands[1])
				gs.Leading = -ty
				m := content.Matrix{1, 0, 0, 1, tx, ty}
				gs.Tlm = m.Multiply(gs.Tlm)
				gs.Tm = gs.Tlm
			}
			out = append(out, op)
		case "T*":
			m := content.Matrix{1, 0, 0, 1, 0, -gs.Leading}
			gs.Tlm = m.Multiply(gs.Tlm)
			gs.Tm = gs.Tlm
			out = append(out, op)

		case "Tj":
			if len(op.Operands) >= 1 {
				if s, ok := op.Operands[0].(object.String); ok {
					newOp, anyRemoved, keepEmpty := filterTjString(&gs, fonts, s, pageH, redactRects)
					if anyRemoved {
						modified = true
						if !keepEmpty {
							out = append(out, newOp)
						}
						continue
					}
				}
			}
			out = append(out, op)

		case "'":
			m := content.Matrix{1, 0, 0, 1, 0, -gs.Leading}
			gs.Tlm = m.Multiply(gs.Tlm)
			gs.Tm = gs.Tlm
			if len(op.Operands) >= 1 {
				if s, ok := op.Operands[0].(object.String); ok {
					rect, dx, hasText := measureString(&gs, fonts, s.Value, pageH)
					gs.Tm = content.Matrix{1, 0, 0, 1, dx, 0}.Multiply(gs.Tm)
					if hasText && intersectsAny(rect, redactRects) {
						modified = true
						out = append(out, content.Operation{Operator: "T*"})
						if adjOp, ok := makeAdvanceTJOp(&gs, dx); ok {
							out = append(out, adjOp)
						}
						continue
					}
				}
			}
			out = append(out, op)

		case "\"":
			if len(op.Operands) >= 3 {
				aw, _ := object.AsFloat(op.Operands[0])
				ac, _ := object.AsFloat(op.Operands[1])
				gs.WordSpace = aw
				gs.CharSpace = ac
				m := content.Matrix{1, 0, 0, 1, 0, -gs.Leading}
				gs.Tlm = m.Multiply(gs.Tlm)
				gs.Tm = gs.Tlm
				if s, ok := op.Operands[2].(object.String); ok {
					rect, dx, hasText := measureString(&gs, fonts, s.Value, pageH)
					gs.Tm = content.Matrix{1, 0, 0, 1, dx, 0}.Multiply(gs.Tm)
					if hasText && intersectsAny(rect, redactRects) {
						modified = true
						out = append(out, content.Operation{Operator: "T*"})
						if adjOp, ok := makeAdvanceTJOp(&gs, dx); ok {
							out = append(out, adjOp)
						}
						continue
					}
				}
			}
			out = append(out, op)

		case "TJ":
			if len(op.Operands) >= 1 {
				if arr, ok := op.Operands[0].(object.Array); ok {
					newArr, anyRemoved := filterTJArray(&gs, fonts, arr, pageH, redactRects)
					if anyRemoved {
						modified = true
						if len(newArr) > 0 {
							out = append(out, content.Operation{
								Operator: "TJ",
								Operands: []object.Object{newArr},
							})
						}
						continue
					}
				}
			}
			out = append(out, op)

		case "Do":
			if depth < 6 && len(op.Operands) >= 1 {
				if xName, ok := op.Operands[0].(object.Name); ok {
					redactFormXObject(doc, resources, string(xName), gs, pageH, redactRects, updatedXObjs, depth+1)
				}
			}
			out = append(out, op)

		default:
			out = append(out, op)
		}
	}

	return out, modified
}

// filterTjString 对单个 Tj 字符串按大空白跨度（如 Tw 制表缩进）拆分逻辑子段并精准擦除
func filterTjString(
	gs *content.GraphicsState,
	fonts map[string]*font.ResolvedFont,
	s object.String,
	pageH float64,
	redactRects [][4]float64,
) (content.Operation, bool, bool) {
	rf := fonts[gs.FontName]
	var texts []string
	var codes []uint32
	step := 1
	if rf != nil {
		texts, codes = rf.DecodeString(s.Value)
		if rf.IsTwoByte {
			step = 2
		}
	} else {
		for _, b := range s.Value {
			texts = append(texts, font.DecodeWinAnsi(b))
			codes = append(codes, uint32(b))
		}
	}

	hScale := gs.HorizScale / 100.0
	if hScale <= 0 {
		hScale = 1.0
	}
	denom := gs.FontSize * hScale
	if denom <= 0 {
		denom = 10.0
	}
	tabSpaceThreshold := gs.FontSize * 1.1

	// 若字节长度与解码字符数无法一一对应，退回整体擦除
	if len(texts)*step != len(s.Value) {
		rect, dx, hasText := measureString(gs, fonts, s.Value, pageH)
		gs.Tm = content.Matrix{1, 0, 0, 1, dx, 0}.Multiply(gs.Tm)
		if hasText && intersectsAny(rect, redactRects) {
			if adjOp, ok := makeAdvanceTJOp(gs, dx); ok {
				return adjOp, true, false
			}
			return content.Operation{}, true, true
		}
		return content.Operation{}, false, false
	}

	type subRun struct {
		raw     []byte
		rect    [4]float64
		dx      float64
		hasText bool
	}

	var runs []subRun
	var curRaw []byte
	var curDx float64
	var pendingSpaceDx float64
	var segStartX, segStartY float64
	hasContent := false

	effSize := gs.FontSize * gs.Tm.Multiply(gs.CTM).VerticalScale()
	if effSize <= 0 {
		effSize = 10
	}

	flushRun := func(isText bool) {
		if len(curRaw) == 0 {
			return
		}
		r := subRun{raw: append([]byte(nil), curRaw...), dx: curDx, hasText: isText}
		if isText {
			endX, _ := gs.Tm.Multiply(gs.CTM).TransformPoint(0, gs.TextRise)
			sx, ex := segStartX, endX
			if ex < sx {
				sx, ex = ex, sx
			}
			if ex-sx < 1 {
				ex = sx + effSize*0.5
			}
			r.rect = [4]float64{sx, pageH - (segStartY + effSize*0.85), ex, pageH - (segStartY - effSize*0.22)}
		}
		runs = append(runs, r)
		curRaw = curRaw[:0]
		curDx = 0
	}

	for i, t := range texts {
		charBytes := s.Value[i*step : (i+1)*step]
		w := 550.0
		if rf != nil {
			w = rf.GlyphWidth(codes[i], t)
		}
		tx := ((w / 1000.0 * gs.FontSize) + gs.CharSpace) * hScale
		if t == " " {
			tx += gs.WordSpace * hScale
		}

		if strings.TrimSpace(t) == "" {
			if hasContent && pendingSpaceDx+tx > tabSpaceThreshold {
				// 先把前面的可见内容段结束，再把当前空格归入纯位移段
				flushRun(true)
				hasContent = false
				pendingSpaceDx = 0
				curRaw = append(curRaw, charBytes...)
				curDx += tx
				gs.Tm = content.Matrix{1, 0, 0, 1, tx, 0}.Multiply(gs.Tm)
				flushRun(false)
				continue
			}
			pendingSpaceDx += tx
			curRaw = append(curRaw, charBytes...)
			curDx += tx
			gs.Tm = content.Matrix{1, 0, 0, 1, tx, 0}.Multiply(gs.Tm)
			continue
		}

		if !hasContent {
			if len(curRaw) > 0 {
				flushRun(false)
			}
			segStartX, segStartY = gs.Tm.Multiply(gs.CTM).TransformPoint(0, gs.TextRise)
			hasContent = true
			pendingSpaceDx = 0
		} else {
			pendingSpaceDx = 0
		}
		curRaw = append(curRaw, charBytes...)
		curDx += tx
		gs.Tm = content.Matrix{1, 0, 0, 1, tx, 0}.Multiply(gs.Tm)
	}
	flushRun(hasContent)

	var tjArr object.Array
	anyRemoved := false
	for _, r := range runs {
		if r.hasText && intersectsAny(r.rect, redactRects) {
			anyRemoved = true
			adj := -(r.dx / denom) * 1000.0
			if math.Abs(adj) > 0.01 {
				tjArr = append(tjArr, object.Real(adj))
			}
		} else {
			tjArr = append(tjArr, object.String{Value: r.raw, IsHex: s.IsHex})
		}
	}
	if !anyRemoved {
		return content.Operation{}, false, false
	}
	if len(tjArr) == 0 {
		return content.Operation{}, true, true
	}
	return content.Operation{
		Operator: "TJ",
		Operands: []object.Object{tjArr},
	}, true, false
}

// filterTJArray 对 TJ 数组内部按每个逻辑子段独立判定擦除：
// 未命中 redactRects 的子段（如章节标号 5.1.1）原样保留，命中段替换为等效水平数值偏移，精准保持原位。
func filterTJArray(
	gs *content.GraphicsState,
	fonts map[string]*font.ResolvedFont,
	arr object.Array,
	pageH float64,
	redactRects [][4]float64,
) (object.Array, bool) {
	hScale := gs.HorizScale / 100.0
	if hScale <= 0 {
		hScale = 1.0
	}
	denom := gs.FontSize * hScale
	if denom <= 0 {
		denom = 10.0
	}

	// 将 TJ 数组按超大偏移（adj <= -1100，与 extractor.go 对齐）切分成逻辑子段
	type tjSeg struct {
		items   object.Array
		rect    [4]float64
		dx      float64
		hasText bool
	}

	var segs []tjSeg
	cur := tjSeg{}
	var curStartX, curStartY float64
	hasStart := false

	flushCur := func() {
		if len(cur.items) == 0 {
			return
		}
		if cur.hasText {
			endTrm := gs.Tm.Multiply(gs.CTM)
			endX, _ := endTrm.TransformPoint(0, gs.TextRise)
			effSize := gs.FontSize * endTrm.VerticalScale()
			if effSize <= 0 {
				effSize = 10
			}
			sx, ex := curStartX, endX
			if ex < sx {
				sx, ex = ex, sx
			}
			if ex-sx < 1 {
				ex = sx + effSize*0.5
			}
			cur.rect = [4]float64{sx, pageH - (curStartY + effSize*0.85), ex, pageH - (curStartY - effSize*0.22)}
		}
		segs = append(segs, cur)
		cur = tjSeg{}
		hasStart = false
	}

	// 先展开 TJ 数组内含超大 Tw 空格的字符串项（使 redactor 与 extractor 的分段 100% 同步）
	expandedArr := expandLargeSpacesInTJ(gs, fonts, arr)

	for _, item := range expandedArr {
		switch v := item.(type) {
		case object.String:
			if !hasStart {
				trm := gs.Tm.Multiply(gs.CTM)
				curStartX, curStartY = trm.TransformPoint(0, gs.TextRise)
				hasStart = true
			}
			_, dx, hasText := measureString(gs, fonts, v.Value, pageH)
			gs.Tm = content.Matrix{1, 0, 0, 1, dx, 0}.Multiply(gs.Tm)
			cur.items = append(cur.items, v)
			cur.dx += dx
			if hasText {
				cur.hasText = true
			}
		case object.Int, object.Real:
			adj, _ := object.AsFloat(v)
			if adj <= -1100 && cur.hasText {
				flushCur()
				// 将该大间隔作为纯位移项独立存放
				tx := (-adj / 1000.0) * denom
				gs.Tm = content.Matrix{1, 0, 0, 1, tx, 0}.Multiply(gs.Tm)
				segs = append(segs, tjSeg{items: object.Array{v}, dx: tx, hasText: false})
			} else {
				tx := (-adj / 1000.0) * denom
				gs.Tm = content.Matrix{1, 0, 0, 1, tx, 0}.Multiply(gs.Tm)
				cur.items = append(cur.items, v)
				cur.dx += tx
			}
		}
	}
	flushCur()

	var result object.Array
	anyRemoved := false
	for _, seg := range segs {
		if seg.hasText && intersectsAny(seg.rect, redactRects) {
			anyRemoved = true
			// 用等效负数值偏移替代被擦除的文本段，保持后续未擦除子段的精确 X 坐标
			adj := -(seg.dx / denom) * 1000.0
			if math.Abs(adj) > 0.01 {
				result = append(result, object.Real(adj))
			}
		} else {
			result = append(result, seg.items...)
		}
	}

	if !anyRemoved {
		return arr, false
	}

	// 合并相邻纯数值偏移，保持流整洁
	compact := make(object.Array, 0, len(result))
	for _, item := range result {
		if val, ok := object.AsFloat(item); ok && len(compact) > 0 {
			if prev, ok2 := object.AsFloat(compact[len(compact)-1]); ok2 {
				compact[len(compact)-1] = object.Real(prev + val)
				continue
			}
		}
		compact = append(compact, item)
	}
	return compact, true
}

// expandLargeSpacesInTJ 将 TJ 数组中被 Tw 拉宽超过 1.1*fontSize 的单个空格拆成“文本 + 等效数值偏移 + 文本”
func expandLargeSpacesInTJ(gs *content.GraphicsState, fonts map[string]*font.ResolvedFont, arr object.Array) object.Array {
	hScale := gs.HorizScale / 100.0
	if hScale <= 0 {
		hScale = 1.0
	}
	denom := gs.FontSize * hScale
	if denom <= 0 || gs.WordSpace*hScale <= gs.FontSize*0.75 {
		return arr
	}
	rf := fonts[gs.FontName]
	step := 1
	if rf != nil && rf.IsTwoByte {
		step = 2
	}

	out := make(object.Array, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(object.String)
		if !ok || len(s.Value) == 0 {
			out = append(out, item)
			continue
		}
		var texts []string
		var codes []uint32
		if rf != nil {
			texts, codes = rf.DecodeString(s.Value)
		} else {
			for _, b := range s.Value {
				texts = append(texts, font.DecodeWinAnsi(b))
				codes = append(codes, uint32(b))
			}
		}
		if len(texts)*step != len(s.Value) {
			out = append(out, item)
			continue
		}

		var chunk []byte
		for i, t := range texts {
			charBytes := s.Value[i*step : (i+1)*step]
			if t == " " {
				w := 280.0
				if rf != nil {
					w = rf.GlyphWidth(codes[i], t)
				}
				tx := ((w / 1000.0 * gs.FontSize) + gs.CharSpace + gs.WordSpace) * hScale
				if tx > gs.FontSize*1.1 {
					if len(chunk) > 0 {
						out = append(out, object.String{Value: append([]byte(nil), chunk...), IsHex: s.IsHex})
						chunk = chunk[:0]
					}
					adj := -(tx / denom) * 1000.0
					out = append(out, object.Real(adj))
					continue
				}
			}
			chunk = append(chunk, charBytes...)
		}
		if len(chunk) > 0 {
			out = append(out, object.String{Value: chunk, IsHex: s.IsHex})
		}
	}
	return out
}

func redactFormXObject(
	doc *object.Document,
	resources object.Dict,
	xName string,
	gs content.GraphicsState,
	pageH float64,
	redactRects [][4]float64,
	updatedXObjs map[string]object.IndirectRef,
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
	if subType != "Form" {
		return
	}

	formState := gs
	if mArr, ok := doc.Resolve(stm.Dict["Matrix"]).(object.Array); ok && len(mArr) >= 6 {
		fm := toMatrix(mArr)
		formState.CTM = fm.Multiply(formState.CTM)
	}
	formRes := resources
	if rDict, ok := doc.Resolve(stm.Dict["Resources"]).(object.Dict); ok {
		formRes = rDict
	}
	formFonts := loadFonts(doc, formRes)
	decoded, err := object.DecodeStream(stm, doc.Resolve)
	if err != nil {
		return
	}

	ops := content.ParseOperations(decoded)
	nestedUpdated := make(map[string]object.IndirectRef)
	filtered, modified := filterOperations(doc, ops, formRes, formFonts, formState, pageH, redactRects, nestedUpdated, depth)
	if !modified && len(nestedUpdated) == 0 {
		return
	}

	newFormDict := stm.Dict.Clone()
	var rawData []byte
	if modified {
		newRaw := content.SerializeOperations(filtered)
		comp, err := object.EncodeFlate(newRaw)
		if err != nil {
			return
		}
		newFormDict["Filter"] = object.Name("FlateDecode")
		newFormDict["Length"] = object.Int(len(comp))
		rawData = comp
	} else {
		rawData = make([]byte, len(stm.RawData))
		copy(rawData, stm.RawData)
	}

	if len(nestedUpdated) > 0 {
		nr := formRes.Clone()
		var nx object.Dict
		if ex, ok := doc.Resolve(nr["XObject"]).(object.Dict); ok {
			nx = ex.Clone()
		} else {
			nx = make(object.Dict)
		}
		for k, v := range nestedUpdated {
			nx[object.Name(k)] = v
		}
		nr["XObject"] = nx
		newFormDict["Resources"] = nr
	}

	newFormRef := doc.AllocObj(&object.Stream{
		Dict:    newFormDict,
		RawData: rawData,
	})
	updatedXObjs[xName] = newFormRef
}

func makeAdvanceTJOp(gs *content.GraphicsState, dx float64) (content.Operation, bool) {
	hScale := gs.HorizScale / 100.0
	if hScale <= 0 {
		hScale = 1.0
	}
	denom := gs.FontSize * hScale
	if denom <= 0 || math.Abs(dx) < 0.01 {
		return content.Operation{}, false
	}
	adj := -(dx / denom) * 1000.0
	return content.Operation{
		Operator: "TJ",
		Operands: []object.Object{object.Array{object.Real(adj)}},
	}, true
}

func measureString(
	gs *content.GraphicsState,
	fonts map[string]*font.ResolvedFont,
	raw []byte,
	pageH float64,
) ([4]float64, float64, bool) {
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

	hScale := gs.HorizScale / 100.0
	var dx float64
	hasText := false
	for i, t := range texts {
		if strings.TrimSpace(t) != "" {
			hasText = true
		}
		w := 550.0
		if rf != nil {
			w = rf.GlyphWidth(codes[i], t)
		}
		tx := ((w / 1000.0 * gs.FontSize) + gs.CharSpace) * hScale
		if t == " " {
			tx += gs.WordSpace * hScale
		}
		dx += tx
	}

	endTm := content.Matrix{1, 0, 0, 1, dx, 0}.Multiply(gs.Tm)
	endX, _ := endTm.Multiply(gs.CTM).TransformPoint(0, gs.TextRise)
	if endX < startX {
		startX, endX = endX, startX
	}
	topY := pageH - (startY + effSize*0.85)
	botY := pageH - (startY - effSize*0.22)
	return [4]float64{startX, topY, endX, botY}, dx, hasText
}

func intersectsAny(r [4]float64, rects [][4]float64) bool {
	cx := (r[0] + r[2]) / 2
	cy := (r[1] + r[3]) / 2
	for _, b := range rects {
		if cx >= b[0]-1.5 && cx <= b[2]+1.5 && cy >= b[1]-1.5 && cy <= b[3]+1.5 {
			return true
		}
		ox := math.Min(r[2], b[2]) - math.Max(r[0], b[0])
		oy := math.Min(r[3], b[3]) - math.Max(r[1], b[1])
		if ox > 0 && oy > 0 {
			rArea := math.Max(1, (r[2]-r[0])*(r[3]-r[1]))
			if (ox*oy)/rArea >= 0.35 {
				return true
			}
		}
	}
	return false
}

func toMatrix(ops []object.Object) content.Matrix {
	var m content.Matrix
	for i := 0; i < 6 && i < len(ops); i++ {
		m[i], _ = object.AsFloat(ops[i])
	}
	return m
}
