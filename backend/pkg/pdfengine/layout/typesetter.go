package layout

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"translation-assistant/pkg/pdfengine/subsetter"
)

// RenderBlock 表示待回填到 PDF 页面的单个译文块
type RenderBlock struct {
	BBox       [4]float64 // [x0, y0, x1, y1]，左上角原点
	Text       string
	FontSize   float64
	FontWeight string // normal / bold
	BlockType  string // heading / paragraph / toc_item / ...
	LineHeight float64
	Align      string // left / center / right / justify
	Color      string // "#RRGGBB"
}

// noLineStartPunct 标点避头尾：不允许出现在行首的常见中文标点
const noLineStartPunct = "，。！？；：、）】》」』”’.,!?;:)]}"

var reTocExport = regexp.MustCompile(`^(.+?)\s*(?:\.\s*){3,}(\d+)\s*$`)

// EmitBlockStream 对单个译文块执行中英混排折行与自适应字号缩放，返回标准 PDF Content Stream 片段
func EmitBlockStream(
	lf *subsetter.LoadedFont,
	fontResName string,
	blk RenderBlock,
	pageH float64,
) []byte {
	text := strings.TrimSpace(blk.Text)
	if text == "" {
		return nil
	}

	boxW := blk.BBox[2] - blk.BBox[0]
	boxH := blk.BBox[3] - blk.BBox[1]
	if boxW < 8 {
		boxW = 8
	}
	if boxH < 8 {
		boxH = 8
	}

	targetSize := blk.FontSize
	if targetSize <= 0 {
		targetSize = 10.5
	}
	lineHeight := blk.LineHeight
	if lineHeight <= 0.8 {
		lineHeight = 1.25
	}

	// 单行原位块（页眉/页脚/表格单行单元格/短标题）优先水平缩字号保单行，不轻易折成两行撑破高度
	minSize := math.Max(6.0, targetSize*0.60)
	chosenSize := targetSize
	var lines []string

	isSingleLineBox := boxH <= targetSize*1.65 && !strings.Contains(text, "\n")
	var tocTitle, tocPage string
	isTocExport := false
	if isSingleLineBox && (blk.BlockType == "toc_item" || reTocExport.MatchString(text)) {
		if m := reTocExport.FindStringSubmatch(text); len(m) == 3 {
			tocTitle = strings.TrimSpace(m[1])
			tocPage = strings.TrimSpace(m[2])
			minNeededW := measureLineWidth(lf, tocTitle+"  "+tocPage, chosenSize)
			if minNeededW > boxW+2.0 {
				scaled := chosenSize * (boxW / minNeededW)
				if scaled >= minSize {
					chosenSize = scaled
					isTocExport = true
					lines = []string{text}
				}
			} else {
				isTocExport = true
				lines = []string{text}
			}
		}
	}

	if !isTocExport {
		if isSingleLineBox {
			singleW := measureLineWidth(lf, text, chosenSize)
			if singleW <= boxW+2.0 {
				lines = []string{text}
			} else {
				scaled := chosenSize * (boxW / singleW)
				if scaled >= minSize {
					chosenSize = scaled
					lines = []string{text}
				} else {
					lines = wrapMixedText(lf, text, chosenSize, boxW)
				}
			}
		} else {
			lines = wrapMixedText(lf, text, chosenSize, boxW)
		}

		if len(lines) > 1 && float64(len(lines))*chosenSize*lineHeight > boxH+2.0 && targetSize > minSize {
			lo, hi := minSize, targetSize
			bestSize := minSize
			bestLines := wrapMixedText(lf, text, minSize, boxW)
			for iter := 0; iter < 10; iter++ {
				mid := (lo + hi) / 2.0
				cand := wrapMixedText(lf, text, mid, boxW)
				totalH := float64(len(cand)) * mid * lineHeight
				if totalH <= boxH+2.0 {
					bestSize = mid
					bestLines = cand
					lo = mid
				} else {
					hi = mid
				}
			}
			chosenSize = bestSize
			lines = bestLines
		}
	}

	r, g, b := parseHexColor(blk.Color)

	var buf bytes.Buffer
	buf.WriteString("q\n")
	fmt.Fprintf(&buf, "%.4f %.4f %.4f rg\n", r, g, b)
	if blk.FontWeight == "bold" {
		// 使用标准 PDF 填充+微描边渲染模式 (2 Tr) 实现无额外字体体积的高质量粗体
		strokeW := chosenSize * 0.028
		fmt.Fprintf(&buf, "%.4f %.4f %.4f RG\n%.3f w\n2 Tr\n", r, g, b, strokeW)
	}
	buf.WriteString("BT\n")
	fmt.Fprintf(&buf, "/%s %.2f Tf\n", fontResName, chosenSize)

	if isTocExport {
		irBaselineY := blk.BBox[1] + chosenSize*0.85
		pdfY := pageH - irBaselineY
		emitTextAt(&buf, lf, tocTitle, blk.BBox[0], pdfY)

		pageNumW := measureLineWidth(lf, tocPage, chosenSize)
		pageNumX := blk.BBox[2] - pageNumW
		emitTextAt(&buf, lf, tocPage, pageNumX, pdfY)

		titleW := measureLineWidth(lf, tocTitle, chosenSize)
		dotStartX := blk.BBox[0] + titleW + chosenSize*0.4
		dotEndX := pageNumX - chosenSize*0.4
		dotUnitW := measureLineWidth(lf, " .", chosenSize)
		if availW := dotEndX - dotStartX; availW > dotUnitW && dotUnitW > 0 {
			count := int(availW / dotUnitW)
			if count > 0 {
				dots := strings.Repeat(" .", count)
				dotsW := measureLineWidth(lf, dots, chosenSize)
				emitTextAt(&buf, lf, dots, dotEndX-dotsW, pdfY)
			}
		}
	} else {
		lineStep := chosenSize * lineHeight
		for idx, ln := range lines {
			lnWidth := measureLineWidth(lf, ln, chosenSize)
			x := blk.BBox[0]
			tcExtra := 0.0
			switch blk.Align {
			case "center":
				if lnWidth < boxW {
					x = blk.BBox[0] + (boxW-lnWidth)/2.0
				}
			case "right":
				if lnWidth < boxW {
					x = blk.BBox[2] - lnWidth
				}
			case "justify":
				// 非最后一行且已占满大部分栏宽时，通过微调字符间距 Tc 实现笔直两端对齐
				if idx < len(lines)-1 && lnWidth < boxW && lnWidth > boxW*0.70 {
					runeCnt := len([]rune(ln))
					if runeCnt > 1 {
						candTc := (boxW - lnWidth) / float64(runeCnt-1)
						if candTc > 0 && candTc <= chosenSize*0.35 {
							tcExtra = candTc
						}
					}
				}
			}

			// Document IR 左上角坐标 -> PDF 左下角基线坐标
			irBaselineY := blk.BBox[1] + chosenSize*0.85 + float64(idx)*lineStep
			pdfY := pageH - irBaselineY
			if tcExtra > 0 {
				fmt.Fprintf(&buf, "%.3f Tc\n", tcExtra)
			}
			emitTextAt(&buf, lf, ln, x, pdfY)
			if tcExtra > 0 {
				buf.WriteString("0 Tc\n")
			}
		}
	}

	buf.WriteString("ET\nQ\n")
	return buf.Bytes()
}

func emitTextAt(buf *bytes.Buffer, lf *subsetter.LoadedFont, text string, x, pdfY float64) {
	gidBytes := lf.EncodeTextToGIDBytes(text)
	if len(gidBytes) == 0 {
		return
	}
	fmt.Fprintf(buf, "1 0 0 1 %.2f %.2f Tm\n", x, pdfY)
	fmt.Fprintf(buf, "<%s> Tj\n", strings.ToUpper(hex.EncodeToString(gidBytes)))
}

// wrapMixedText 实现中英文混排折行（英文按词折行，中文按字折行并做行首标点避让）
func wrapMixedText(lf *subsetter.LoadedFont, text string, fontSize, maxW float64) []string {
	paragraphs := strings.Split(text, "\n")
	var result []string

	for _, para := range paragraphs {
		tokens := tokenizeMixed(para)
		if len(tokens) == 0 {
			result = append(result, "")
			continue
		}

		var curLine strings.Builder
		var curW float64

		for _, tok := range tokens {
			tokW := measureLineWidth(lf, tok, fontSize)
			if curLine.Len() == 0 {
				trimmed := strings.TrimLeft(tok, " ")
				curLine.WriteString(trimmed)
				curW = measureLineWidth(lf, trimmed, fontSize)
				continue
			}

			if curW+tokW <= maxW || (len([]rune(tok)) == 1 && strings.ContainsRune(noLineStartPunct, []rune(tok)[0])) {
				curLine.WriteString(tok)
				curW += tokW
			} else {
				result = append(result, strings.TrimRight(curLine.String(), " "))
				curLine.Reset()
				trimmed := strings.TrimLeft(tok, " ")
				curLine.WriteString(trimmed)
				curW = measureLineWidth(lf, trimmed, fontSize)
			}
		}
		if curLine.Len() > 0 {
			result = append(result, strings.TrimRight(curLine.String(), " "))
		}
	}
	return result
}

// tokenizeMixed 将中英文混排文本切分为“英文单词块 + 单个中文/标点字符”
func tokenizeMixed(s string) []string {
	var tokens []string
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		r := runes[i]
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '\'') {
			start := i
			for i < len(runes) && runes[i] < 128 &&
				(unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '-' || runes[i] == '_' || runes[i] == '\'') {
				i++
			}
			tokens = append(tokens, string(runes[start:i]))
		} else {
			tokens = append(tokens, string(r))
			i++
		}
	}
	return tokens
}

func measureLineWidth(lf *subsetter.LoadedFont, s string, fontSize float64) float64 {
	var w float64
	for _, r := range s {
		w += lf.RuneWidthPt(r, fontSize)
	}
	return w
}

func parseHexColor(hexStr string) (float64, float64, float64) {
	s := strings.TrimPrefix(strings.TrimSpace(hexStr), "#")
	if len(s) != 6 {
		return 0, 0, 0
	}
	rv, err1 := strconv.ParseUint(s[0:2], 16, 8)
	gv, err2 := strconv.ParseUint(s[2:4], 16, 8)
	bv, err3 := strconv.ParseUint(s[4:6], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0
	}
	return float64(rv) / 255.0, float64(gv) / 255.0, float64(bv) / 255.0
}
