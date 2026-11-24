package content

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// ParsedBlock 表示合并整理后的逻辑文本块（对应 Document IR 的单块）
type ParsedBlock struct {
	ID           string
	PageIndex    int
	ReadingOrder int
	BlockType    string     // heading / paragraph / list_item / toc_item / header_footer
	BBox         [4]float64 // [x0, y0, x1, y1]
	OriginalText string
	FontSize     float64
	FontFamily   string // serif / sans / mono
	FontWeight   string // normal / bold
	FontStyle    string // normal / italic
	Color        string
	Align        string // left / center / right / justify
	LineHeight   float64
	Translatable bool
}

type mergedLine struct {
	spans      []TextSpan
	bbox       [4]float64
	text       string
	fontSize   float64
	fontFamily string
	isBold     bool
	isItalic   bool
	color      string
	mathRatio  float64
}

type blockBuilder struct {
	block         ParsedBlock
	lines         []mergedLine
	isListBlock   bool
	baselineSteps []float64
	colRightRef   float64
}

var (
	rePureNumber  = regexp.MustCompile(`^[\d\s\.\-\(\)\[\],/]+$`)
	rePureURL     = regexp.MustCompile(`^(https?://|www\.|mailto:)[^\s]+$`)
	reTocDots     = regexp.MustCompile(`(\.\s*){4,}`)
	reListPrefix  = regexp.MustCompile(`^([•▪○●◆◇■□►▸–—\-\*]\s+|\d+(\.\d+)*[\.\)]?\s+[A-Za-z\p{Han}]|\([a-zA-Z0-9]\)\s+)`)
	reBulletStart = regexp.MustCompile(`^[•▪○●◆◇■□►▸–—\-\*]\s+`)
)

// MergeLayout 基于“矢量线条屏障 + 7 道门禁 ParagraphFinder”将碎片 TextSpan 精确重构为高保真逻辑块
func MergeLayout(pageRes *PageExtractResult) []ParsedBlock {
	if len(pageRes.Spans) == 0 {
		return nil
	}

	// 1. 按 Y 坐标聚合成行，并用“严格水平阈值 + 垂直矢量屏障”拆列（保护页眉/页脚/表格多列/并排图注）
	lines := groupSpansIntoLines(pageRes.Spans, pageRes.VBarriers, pageRes.Width)
	if len(lines) == 0 {
		return nil
	}

	// 2. 检测双栏并按阅读顺序排序，同时估算本页内容区左右边界与单/双栏参考宽度
	sortedLines, isTwoCol := sortLinesByReadingOrder(lines, pageRes.Width)
	contentLeft, contentRight := computeContentBounds(sortedLines, pageRes.Width)

	// 3. 应用 7 道门禁 ParagraphFinder 合并自然段（严格保护列表、目录、表格行与短行）
	var builders []*blockBuilder
	for _, ln := range sortedLines {
		colRight := estimateColumnRight(ln, contentLeft, contentRight, pageRes.Width, isTwoCol)
		if len(builders) > 0 && canMergeLine(builders[len(builders)-1], ln, pageRes.HBarriers, colRight) {
			cur := builders[len(builders)-1]
			lastLn := cur.lines[len(cur.lines)-1]
			step := ((ln.bbox[1] + ln.bbox[3]) / 2) - ((lastLn.bbox[1] + lastLn.bbox[3]) / 2)
			if step > 0 {
				cur.baselineSteps = append(cur.baselineSteps, step)
			}
			cur.lines = append(cur.lines, ln)
			cur.block.OriginalText = joinParagraphLines(cur.block.OriginalText, ln.text)
			cur.block.BBox = unionRect(cur.block.BBox, ln.bbox)
		} else {
			weight := "normal"
			if ln.isBold {
				weight = "bold"
			}
			fStyle := "normal"
			if ln.isItalic {
				fStyle = "italic"
			}
			blockType := inferBlockType(ln, pageRes.Height)
			isList := blockType == "list_item"

			builders = append(builders, &blockBuilder{
				block: ParsedBlock{
					PageIndex:    pageRes.PageIndex,
					BlockType:    blockType,
					BBox:         ln.bbox,
					OriginalText: ln.text,
					FontSize:     round2(ln.fontSize),
					FontFamily:   ln.fontFamily,
					FontWeight:   weight,
					FontStyle:    fStyle,
					Color:        ln.color,
					LineHeight:   1.18,
					Translatable: shouldTranslate(ln.text, ln.mathRatio, blockType),
				},
				lines:       []mergedLine{ln},
				isListBlock: isList,
				colRightRef: colRight,
			})
		}
	}

	// 4. 还原真实行高、三态对齐（left/center/right）与最终编号
	filtered := make([]ParsedBlock, 0, len(builders))
	for _, bd := range builders {
		b := bd.block
		b.OriginalText = strings.TrimSpace(b.OriginalText)
		if b.OriginalText == "" {
			continue
		}
		if len(bd.baselineSteps) > 0 && b.FontSize > 0 {
			avgStep := medianFloat(bd.baselineSteps)
			lh := avgStep / b.FontSize
			if lh < 1.05 {
				lh = 1.05
			} else if lh > 1.75 {
				lh = 1.75
			}
			b.LineHeight = round2(lh)
		}
		if b.BlockType == "toc_item" {
			b.Align = "left"
		} else {
			b.Align = inferAlignV2(b.BBox, bd.lines, contentLeft, contentRight, pageRes.Width, isTwoCol)
		}
		b.ReadingOrder = len(filtered)
		b.ID = fmt.Sprintf("p%d_b%d", pageRes.PageIndex, b.ReadingOrder)
		b.Translatable = shouldTranslate(b.OriginalText, 0, b.BlockType) && b.Translatable
		b.BBox = [4]float64{
			round2(math.Max(0, b.BBox[0]-0.5)),
			round2(math.Max(0, b.BBox[1]-0.5)),
			round2(math.Min(pageRes.Width, b.BBox[2]+0.5)),
			round2(math.Min(pageRes.Height, b.BBox[3]+0.5)),
		}
		filtered = append(filtered, b)
	}
	return filtered
}

func groupSpansIntoLines(spans []TextSpan, vBarriers []VectorLine, pageW float64) []mergedLine {
	cp := make([]TextSpan, len(spans))
	copy(cp, spans)
	sort.Slice(cp, func(i, j int) bool {
		yi := (cp[i].BBox[1] + cp[i].BBox[3]) / 2
		yj := (cp[j].BBox[1] + cp[j].BBox[3]) / 2
		if math.Abs(yi-yj) > 2.8 {
			return yi < yj
		}
		return cp[i].BBox[0] < cp[j].BBox[0]
	})

	// 按 Y 中心相近分桶（对小字号上标/下标如 ® ™ 放宽容差，归回同行）
	var yBuckets [][]TextSpan
	for _, sp := range cp {
		cy := (sp.BBox[1] + sp.BBox[3]) / 2
		isTinyScript := len([]rune(strings.TrimSpace(sp.Text))) <= 2
		placed := false
		for i := len(yBuckets) - 1; i >= 0; i-- {
			ref := yBuckets[i][0]
			refCy := (ref.BBox[1] + ref.BBox[3]) / 2
			tol := math.Max(2.8, math.Max(sp.FontSize, ref.FontSize)*0.42)
			if isTinyScript || len([]rune(strings.TrimSpace(ref.Text))) <= 2 {
				tol = math.Max(tol, math.Max(sp.FontSize, ref.FontSize)*0.68)
			}
			if math.Abs(cy-refCy) <= tol {
				yBuckets[i] = append(yBuckets[i], sp)
				placed = true
				break
			}
			if cy-refCy > 25 {
				break
			}
		}
		if !placed {
			yBuckets = append(yBuckets, []TextSpan{sp})
		}
	}

	var lines []mergedLine
	for _, bucket := range yBuckets {
		sort.Slice(bucket, func(i, j int) bool { return bucket[i].BBox[0] < bucket[j].BBox[0] })

		var subGroups [][]TextSpan
		for _, sp := range bucket {
			if len(subGroups) == 0 {
				subGroups = append(subGroups, []TextSpan{sp})
				continue
			}
			cur := subGroups[len(subGroups)-1]
			last := cur[len(cur)-1]
			gap := sp.BBox[0] - last.BBox[2]

			// 特例 1：项目符号与紧随其后的首词间距可能略宽（如 1.8*fontSize），允许合并进同一行
			isBulletGap := isBulletSymbolOnly(last.Text) && gap <= last.FontSize*2.6
			// 特例 2：纯章节编号（如 2.3.21）后跟随大于 0.85 倍字号的空白，代表制表缩进，拆分为独立块保留精确缩进
			isSectionTabGap := rePureNumber.MatchString(strings.TrimSpace(last.Text)) && gap > last.FontSize*0.85
			// 收紧常规水平拆列阈值：保护页眉左右、页脚三段、表格多列与并排图注
			colGapThreshold := math.Max(last.FontSize*1.45, 11.5)
			if colGapThreshold > pageW*0.04 {
				colGapThreshold = pageW * 0.04
			}

			crossedVLine := hasVerticalBarrierBetween(last.BBox, sp.BBox, vBarriers)
			if crossedVLine || isSectionTabGap || (!isBulletGap && gap > colGapThreshold) {
				subGroups = append(subGroups, []TextSpan{sp})
			} else {
				subGroups[len(subGroups)-1] = append(cur, sp)
			}
		}

		for _, grp := range subGroups {
			lines = append(lines, buildMergedLine(grp))
		}
	}
	return lines
}

func buildMergedLine(spans []TextSpan) mergedLine {
	bbox := spans[0].BBox
	var sb strings.Builder
	var sizeSum, mathChars, totalChars float64
	boldCount, italicCount := 0, 0
	familyScore := map[string]float64{}
	colorScore := map[string]float64{}

	for i, sp := range spans {
		bbox = unionRect(bbox, sp.BBox)
		if i > 0 {
			gap := sp.BBox[0] - spans[i-1].BBox[2]
			if gap > sp.FontSize*0.16 && !strings.HasSuffix(sb.String(), " ") && !strings.HasPrefix(sp.Text, " ") {
				sb.WriteByte(' ')
			}
		}
		sb.WriteString(sp.Text)
		n := float64(len([]rune(sp.Text)))
		sizeSum += sp.FontSize * n
		totalChars += n
		// 按非空白字符数统计主导色，避免行首空白或符号覆盖整行彩色
		trimmedLen := float64(len([]rune(strings.TrimSpace(sp.Text))))
		if trimmedLen > 0 && sp.Color != "" {
			colorScore[sp.Color] += trimmedLen
		}
		if sp.FontFamily != "" {
			familyScore[sp.FontFamily] += n
		} else {
			familyScore["sans"] += n
		}
		if sp.IsMathOrSymbol {
			mathChars += n
		}
		if sp.IsBold {
			boldCount++
		}
		if sp.IsItalic {
			italicCount++
		}
	}

	bestFamily := "sans"
	bestScore := -1.0
	for fam, sc := range familyScore {
		if sc > bestScore {
			bestScore = sc
			bestFamily = fam
		}
	}

	bestColor := spans[0].Color
	if bestColor == "" {
		bestColor = "#000000"
	}
	bestColorScore := -1.0
	for c, sc := range colorScore {
		if sc > bestColorScore || (sc == bestColorScore && c < bestColor) {
			bestColorScore = sc
			bestColor = c
		}
	}

	avgSize := spans[0].FontSize
	if totalChars > 0 {
		avgSize = sizeSum / totalChars
	}
	mathRatio := 0.0
	if totalChars > 0 {
		mathRatio = mathChars / totalChars
	}

	return mergedLine{
		spans:      spans,
		bbox:       bbox,
		text:       strings.TrimSpace(sb.String()),
		fontSize:   avgSize,
		fontFamily: bestFamily,
		isBold:     boldCount*2 >= len(spans),
		isItalic:   italicCount*2 >= len(spans),
		color:      bestColor,
		mathRatio:  mathRatio,
	}
}

// canMergeLine 实现 7 道门禁 ParagraphFinder 判定
func canMergeLine(cur *blockBuilder, next mergedLine, hBarriers []VectorLine, colRight float64) bool {
	prev := &cur.block
	lastLn := cur.lines[len(cur.lines)-1]

	// 门禁 1：水平矢量屏障（表格横线 / 分隔线）—— 严禁跨线合并
	if hasHorizontalBarrierBetween(lastLn.bbox, next.bbox, hBarriers) {
		return false
	}

	// 门禁 2：样式一致（字号、字重、颜色）
	if math.Abs(prev.FontSize-next.fontSize) > 1.0 {
		return false
	}
	nextWeight := "normal"
	if next.isBold {
		nextWeight = "bold"
	}
	if prev.FontWeight != nextWeight || prev.Color != next.color {
		return false
	}

	// 门禁 3：目录行保护（含引导点线 . . .）与页眉页脚保护
	if prev.BlockType == "toc_item" || prev.BlockType == "header_footer" || isTocLine(next.text) {
		return false
	}

	// 门禁 4：列表项 / 编号条款边界 —— 遇到新列表项强制断开
	if isListStart(next.text) {
		return false
	}

	// 门禁 5：水平重叠与左边界/悬挂缩进校验
	overlapX := math.Min(lastLn.bbox[2], next.bbox[2]) - math.Max(lastLn.bbox[0], next.bbox[0])
	if overlapX <= 0 {
		return false
	}
	leftDiff := next.bbox[0] - lastLn.bbox[0]
	if cur.isListBlock {
		if len(cur.lines) == 1 {
			// 列表首行 -> 第二行续行：允许与符号齐平或向右缩进 0 ~ 28pt（悬挂缩进）
			if leftDiff < -3.0 || leftDiff > 28.0 {
				return false
			}
		} else {
			// 列表续行 -> 下一续行：必须与上一续行左对齐
			if math.Abs(leftDiff) > 4.0 {
				return false
			}
		}
	} else {
		if len(cur.lines) == 1 {
			// 首行缩进段：第二行可能比第一行略靠左（最多 3 * fontSize）
			if leftDiff > 4.0 || leftDiff < -prev.FontSize*3.0 {
				return false
			}
		} else {
			if math.Abs(leftDiff) > 4.0 {
				return false
			}
		}
	}

	// 门禁 6：短行主动换行保护（Short-Line Guard）
	// 若上一行明显未排满当前栏宽（且距离栏右边界还有 > 28% 栏宽），视为原文主动换行（短标题/图注/独立条）
	colWidth := colRight - lastLn.bbox[0]
	lastW := lastLn.bbox[2] - lastLn.bbox[0]
	if colWidth > 80 && lastW < colWidth*0.72 {
		return false
	}

	// 门禁 7：动态行距门禁（防止跨段间距合并）
	vGap := next.bbox[1] - lastLn.bbox[3]
	if vGap < -prev.FontSize*0.25 || vGap > prev.FontSize*0.72 {
		return false
	}
	if len(cur.baselineSteps) > 0 {
		step := ((next.bbox[1] + next.bbox[3]) / 2) - ((lastLn.bbox[1] + lastLn.bbox[3]) / 2)
		med := medianFloat(cur.baselineSteps)
		if step > med*1.32 {
			return false
		}
	}

	return true
}

func hasVerticalBarrierBetween(a, b [4]float64, vBarriers []VectorLine) bool {
	if len(vBarriers) == 0 {
		return false
	}
	xMin := a[2] - 1.0
	xMax := b[0] + 1.0
	yMid := (math.Max(a[1], b[1]) + math.Min(a[3], b[3])) / 2.0
	for _, v := range vBarriers {
		if v.X0 >= xMin && v.X0 <= xMax && v.Y0 <= yMid+2.0 && v.Y1 >= yMid-2.0 {
			return true
		}
	}
	return false
}

func hasHorizontalBarrierBetween(a, b [4]float64, hBarriers []VectorLine) bool {
	if len(hBarriers) == 0 {
		return false
	}
	yMin := a[3] - 1.5
	yMax := b[1] + 1.5
	if yMax < yMin {
		yMin, yMax = yMax, yMin
	}
	xOverlapLo := math.Max(a[0], b[0])
	xOverlapHi := math.Min(a[2], b[2])
	if xOverlapHi <= xOverlapLo {
		return false
	}
	for _, h := range hBarriers {
		if h.Y0 >= yMin && h.Y0 <= yMax {
			segOverlap := math.Min(h.X1, xOverlapHi) - math.Max(h.X0, xOverlapLo)
			if segOverlap > (xOverlapHi-xOverlapLo)*0.3 {
				return true
			}
		}
	}
	return false
}

// sortLinesByReadingOrder 通过中心空白槽检测双栏，支持跨栏标题与左右双栏阅读顺序
func sortLinesByReadingOrder(lines []mergedLine, pageW float64) ([]mergedLine, bool) {
	mid := pageW / 2.0
	leftCount, rightCount := 0, 0
	for _, ln := range lines {
		w := ln.bbox[2] - ln.bbox[0]
		if w > pageW*0.55 {
			continue
		}
		if ln.bbox[2] <= mid+pageW*0.03 {
			leftCount++
		} else if ln.bbox[0] >= mid-pageW*0.03 {
			rightCount++
		}
	}

	if leftCount < 3 || rightCount < 3 {
		out := make([]mergedLine, len(lines))
		copy(out, lines)
		sort.Slice(out, func(i, j int) bool {
			if math.Abs(out[i].bbox[1]-out[j].bbox[1]) > 3 {
				return out[i].bbox[1] < out[j].bbox[1]
			}
			return out[i].bbox[0] < out[j].bbox[0]
		})
		return out, false
	}

	var spanning, leftCol, rightCol []mergedLine
	for _, ln := range lines {
		w := ln.bbox[2] - ln.bbox[0]
		if w > pageW*0.55 || (ln.bbox[0] < mid-pageW*0.05 && ln.bbox[2] > mid+pageW*0.05) {
			spanning = append(spanning, ln)
		} else if (ln.bbox[0]+ln.bbox[2])/2 < mid {
			leftCol = append(leftCol, ln)
		} else {
			rightCol = append(rightCol, ln)
		}
	}

	sortByY := func(s []mergedLine) {
		sort.Slice(s, func(i, j int) bool {
			if math.Abs(s[i].bbox[1]-s[j].bbox[1]) > 2.5 {
				return s[i].bbox[1] < s[j].bbox[1]
			}
			return s[i].bbox[0] < s[j].bbox[0]
		})
	}
	sortByY(spanning)
	sortByY(leftCol)
	sortByY(rightCol)

	var result []mergedLine
	prevY := -1e9
	for _, sp := range spanning {
		curY := sp.bbox[1]
		for _, l := range leftCol {
			if l.bbox[1] >= prevY && l.bbox[1] < curY {
				result = append(result, l)
			}
		}
		for _, r := range rightCol {
			if r.bbox[1] >= prevY && r.bbox[1] < curY {
				result = append(result, r)
			}
		}
		result = append(result, sp)
		prevY = curY
	}
	for _, l := range leftCol {
		if l.bbox[1] >= prevY {
			result = append(result, l)
		}
	}
	for _, r := range rightCol {
		if r.bbox[1] >= prevY {
			result = append(result, r)
		}
	}
	return result, true
}

func computeContentBounds(lines []mergedLine, pageW float64) (float64, float64) {
	if len(lines) == 0 {
		return 50, pageW - 50
	}
	minX := pageW
	maxX := 0.0
	for _, ln := range lines {
		if ln.bbox[0] < minX {
			minX = ln.bbox[0]
		}
		if ln.bbox[2] > maxX {
			maxX = ln.bbox[2]
		}
	}
	if maxX <= minX {
		return 50, pageW - 50
	}
	return minX, maxX
}

func estimateColumnRight(ln mergedLine, contentLeft, contentRight, pageW float64, isTwoCol bool) float64 {
	if !isTwoCol {
		return contentRight
	}
	mid := pageW / 2.0
	if ln.bbox[2] <= mid+pageW*0.03 {
		return mid - 8.0
	}
	if ln.bbox[0] >= mid-pageW*0.03 {
		return contentRight
	}
	_ = contentLeft
	return contentRight
}

func inferAlignV2(bbox [4]float64, lines []mergedLine, contentLeft, contentRight, pageW float64, isTwoCol bool) string {
	w := bbox[2] - bbox[0]
	contentW := contentRight - contentLeft
	if contentW <= 0 {
		return "left"
	}

	// 多行段落：依据各行左右边界齐平度精准判断（支持首行缩进的 justify 与普通 left/right）
	if len(lines) >= 2 {
		bodyX0 := lines[1].bbox[0]
		firstIndentOk := math.Abs(lines[0].bbox[0]-bodyX0) <= 4.5 ||
			(lines[0].bbox[0] > bodyX0 && lines[0].bbox[0]-bodyX0 <= 26.0)

		leftAligned := firstIndentOk
		bodyRightRef := lines[0].bbox[2]
		bodyRightAligned := true
		allRightAligned := true

		for idx, ln := range lines[1:] {
			if math.Abs(ln.bbox[0]-bodyX0) > 4.5 {
				leftAligned = false
			}
			if math.Abs(ln.bbox[2]-bodyRightRef) > 6.5 {
				allRightAligned = false
				if idx < len(lines)-2 {
					bodyRightAligned = false
				}
			}
		}

		if leftAligned {
			// 多行正文（>= 3 行前 N-1 行右齐平，或 2 行左右均齐平）且宽度占比较高，判定为两端对齐 justify
			if bodyRightAligned && w > contentW*0.45 && (len(lines) >= 3 || allRightAligned) {
				return "justify"
			}
			return "left"
		}
		if allRightAligned && math.Abs(contentRight-bbox[2]) <= 6.0 && bbox[0] > pageW*0.50 {
			return "right"
		}
		return "left"
	}

	// 单行块：先判断居中
	leftMargin := bbox[0]
	rightMargin := pageW - bbox[2]
	if math.Abs(leftMargin-rightMargin) < pageW*0.03 && leftMargin > contentLeft+20 && w < contentW*0.80 {
		return "center"
	}

	// 单行块右对齐：必须贴近内容区右边界、起点位于页面右半区且宽度不超过内容区 48%
	if math.Abs(contentRight-bbox[2]) <= 6.0 && bbox[0] > pageW*0.50 && w < contentW*0.48 {
		return "right"
	}
	return "left"
}

func isTocLine(text string) bool {
	return reTocDots.MatchString(text)
}

func isListStart(text string) bool {
	t := strings.TrimSpace(text)
	return reBulletStart.MatchString(t) || reListPrefix.MatchString(t)
}

func joinParagraphLines(a, b string) string {
	a = strings.TrimRight(a, " ")
	b = strings.TrimLeft(b, " ")
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if strings.HasSuffix(a, "-") && len(b) > 0 && b[0] >= 'a' && b[0] <= 'z' {
		return a[:len(a)-1] + b
	}
	lastRune := []rune(a)[len([]rune(a))-1]
	firstRune := []rune(b)[0]
	if lastRune >= 0x2E80 && firstRune >= 0x2E80 {
		return a + b
	}
	return a + " " + b
}

func inferBlockType(ln mergedLine, pageH float64) string {
	if isTocLine(ln.text) {
		return "toc_item"
	}
	if ln.bbox[1] < pageH*0.075 || ln.bbox[3] > pageH*0.92 {
		if len([]rune(ln.text)) < 50 {
			return "header_footer"
		}
	}
	if isListStart(ln.text) {
		return "list_item"
	}
	if ln.fontSize >= 13.5 || (ln.isBold && len([]rune(ln.text)) < 80) {
		return "heading"
	}
	return "paragraph"
}

func shouldTranslate(text string, mathRatio float64, blockType string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if mathRatio > 0.6 {
		return false
	}
	if rePureNumber.MatchString(trimmed) {
		return false
	}
	if rePureURL.MatchString(trimmed) {
		return false
	}
	if blockType == "header_footer" && len([]rune(trimmed)) <= 4 {
		return false
	}
	return true
}

func unionRect(a, b [4]float64) [4]float64 {
	return [4]float64{
		math.Min(a[0], b[0]),
		math.Min(a[1], b[1]),
		math.Max(a[2], b[2]),
		math.Max(a[3], b[3]),
	}
}

func medianFloat(vals []float64) float64 {
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 0 {
		return (cp[mid-1] + cp[mid]) / 2.0
	}
	return cp[mid]
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
