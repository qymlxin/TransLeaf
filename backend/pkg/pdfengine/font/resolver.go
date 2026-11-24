package font

import (
	"strings"

	"translation-assistant/pkg/pdfengine/object"
)

// ResolvedFont 表示已解析的 PDF 页面字体及其解码/度量信息
type ResolvedFont struct {
	Name           string
	BaseFont       string
	Subtype        string
	FontFamily     string // serif / sans / mono
	IsTwoByte      bool
	IsBold         bool
	IsItalic       bool
	IsMathOrSymbol bool
	ToUnicode      *CMap
	Differences    map[byte]string
	Widths         map[uint32]float64 // 字符码 -> 宽度（单位：千分之一个字号，默认 500~1000）
	DefaultWidth   float64
}

// ResolveFont 解析单个 PDF 字体字典
func ResolveFont(doc *object.Document, resName string, fontObj object.Object) *ResolvedFont {
	rf := &ResolvedFont{
		Name:         resName,
		FontFamily:   "sans",
		Differences:  make(map[byte]string),
		Widths:       make(map[uint32]float64),
		DefaultWidth: 550,
	}

	dict, ok := doc.Resolve(fontObj).(object.Dict)
	if !ok {
		return rf
	}

	if st, ok := doc.Resolve(dict["Subtype"]).(object.Name); ok {
		rf.Subtype = string(st)
	}
	if bf, ok := doc.Resolve(dict["BaseFont"]).(object.Name); ok {
		rf.BaseFont = string(bf)
	}

	upperBase := strings.ToUpper(rf.BaseFont)
	rf.FontFamily = inferFontFamily(upperBase)
	if strings.Contains(upperBase, "BOLD") || strings.Contains(upperBase, "-BD") || strings.Contains(upperBase, "HEAVY") {
		rf.IsBold = true
	}
	if strings.Contains(upperBase, "ITALIC") || strings.Contains(upperBase, "OBLIQUE") || strings.Contains(upperBase, "-IT") {
		rf.IsItalic = true
	}

	// 检测常见数学/符号专用字体（保护公式不被误翻译）
	if isMathFontName(upperBase) {
		rf.IsMathOrSymbol = true
	}

	// 解析 ToUnicode CMap
	if tuObj := doc.Resolve(dict["ToUnicode"]); tuObj != nil {
		if stm, ok := tuObj.(*object.Stream); ok {
			if raw, err := object.DecodeStream(stm, doc.Resolve); err == nil {
				rf.ToUnicode = ParseCMap(raw)
			}
		}
	}

	if rf.Subtype == "Type0" {
		rf.IsTwoByte = true
		rf.DefaultWidth = 1000
		rf.parseDescendantWidths(doc, dict)
	} else {
		rf.parseSimpleWidths(doc, dict)
		rf.parseEncoding(doc, dict)
	}

	return rf
}

func (rf *ResolvedFont) parseSimpleWidths(doc *object.Document, dict object.Dict) {
	firstChar := uint32(0)
	if fc, ok := object.AsInt(doc.Resolve(dict["FirstChar"])); ok && fc >= 0 {
		firstChar = uint32(fc)
	}
	if widths, ok := doc.Resolve(dict["Widths"]).(object.Array); ok {
		for i, wObj := range widths {
			if w, ok := object.AsFloat(doc.Resolve(wObj)); ok {
				rf.Widths[firstChar+uint32(i)] = w
			}
		}
	}
}

func (rf *ResolvedFont) parseDescendantWidths(doc *object.Document, dict object.Dict) {
	descArr, ok := doc.Resolve(dict["DescendantFonts"]).(object.Array)
	if !ok || len(descArr) == 0 {
		return
	}
	cidDict, ok := doc.Resolve(descArr[0]).(object.Dict)
	if !ok {
		return
	}
	if dw, ok := object.AsFloat(doc.Resolve(cidDict["DW"])); ok && dw > 0 {
		rf.DefaultWidth = dw
	}
	wArr, ok := doc.Resolve(cidDict["W"]).(object.Array)
	if !ok {
		return
	}
	// PDF CIDFont /W 格式：c [w1 w2 ...] 或 cFirst cLast w
	i := 0
	for i < len(wArr) {
		cStart, ok := object.AsInt(doc.Resolve(wArr[i]))
		if !ok {
			i++
			continue
		}
		if i+1 >= len(wArr) {
			break
		}
		next := doc.Resolve(wArr[i+1])
		if arr, ok := next.(object.Array); ok {
			for j, item := range arr {
				if w, ok := object.AsFloat(doc.Resolve(item)); ok {
					rf.Widths[uint32(cStart)+uint32(j)] = w
				}
			}
			i += 2
		} else if cEnd, ok := object.AsInt(next); ok && i+2 < len(wArr) {
			if w, ok := object.AsFloat(doc.Resolve(wArr[i+2])); ok {
				for c := cStart; c <= cEnd && c-cStart < 65536; c++ {
					rf.Widths[uint32(c)] = w
				}
			}
			i += 3
		} else {
			i++
		}
	}
}

func (rf *ResolvedFont) parseEncoding(doc *object.Document, dict object.Dict) {
	encObj := doc.Resolve(dict["Encoding"])
	encDict, ok := encObj.(object.Dict)
	if !ok {
		return
	}
	diffArr, ok := doc.Resolve(encDict["Differences"]).(object.Array)
	if !ok {
		return
	}
	code := 0
	for _, item := range diffArr {
		switch v := doc.Resolve(item).(type) {
		case object.Int:
			code = int(v)
		case object.Name:
			if code >= 0 && code <= 255 {
				if u, exists := glyphNameToUnicode[string(v)]; exists {
					rf.Differences[byte(code)] = u
				} else if len(v) == 1 {
					rf.Differences[byte(code)] = string(v)
				}
			}
			code++
		}
	}
}

// DecodeString 将 PDF 绘图指令中的字节串解码为 (UTF-8 字符串列表, 对应原始字形码列表)
func (rf *ResolvedFont) DecodeString(raw []byte) ([]string, []uint32) {
	if rf.ToUnicode != nil {
		return rf.ToUnicode.DecodeBytes(raw, rf.IsTwoByte)
	}
	if rf.IsTwoByte {
		// 无 ToUnicode 的 Type0 字体尝试按 UTF-16BE 读取
		var texts []string
		var codes []uint32
		for i := 0; i+1 < len(raw); i += 2 {
			code := uint32(raw[i])<<8 | uint32(raw[i+1])
			codes = append(codes, code)
			texts = append(texts, string(rune(code)))
		}
		return texts, codes
	}

	texts := make([]string, 0, len(raw))
	codes := make([]uint32, 0, len(raw))
	for _, b := range raw {
		codes = append(codes, uint32(b))
		if u, ok := rf.Differences[b]; ok {
			texts = append(texts, u)
		} else {
			texts = append(texts, DecodeWinAnsi(b))
		}
	}
	return texts, codes
}

// GlyphWidth 返回某字形码的归一化宽度（1/1000 em）
func (rf *ResolvedFont) GlyphWidth(code uint32, text string) float64 {
	if w, ok := rf.Widths[code]; ok && w > 0 {
		return w
	}
	if text == " " {
		return 280
	}
	// 中文全角字符默认 1000
	for _, r := range text {
		if r >= 0x2E80 {
			return 1000
		}
	}
	return rf.DefaultWidth
}

func isMathFontName(upper string) bool {
	// 去除子集前缀（如 ABCDEF+CMMI10 -> CMMI10）
	if idx := strings.IndexByte(upper, '+'); idx >= 0 {
		upper = upper[idx+1:]
	}
	prefixes := []string{"CMMI", "CMSY", "CMEX", "MSAM", "MSBM", "SYMBOL", "ZAPFDINGBATS", "MATH", "EUFM"}
	for _, p := range prefixes {
		if strings.HasPrefix(upper, p) || strings.Contains(upper, p) {
			return true
		}
	}
	return false
}

func inferFontFamily(upper string) string {
	if idx := strings.IndexByte(upper, '+'); idx >= 0 {
		upper = upper[idx+1:]
	}
	monoKeys := []string{"COURIER", "CMTT", "MONO", "CONSOLAS", "MENLO", "INCONSOLATA", "NIMBUSMON", "LUCIDACONS"}
	for _, k := range monoKeys {
		if strings.Contains(upper, k) {
			return "mono"
		}
	}
	serifKeys := []string{
		"TIMES", "NIMBUSROM", "CMR", "CMBX", "CMTI", "CMCSC", "CMSL", "LMR",
		"SERIF", "GEORGIA", "PALATINO", "GARAMOND", "MINION", "CAMBRIA", "CHARTER",
		"SONG", "KAITI", "FANGSONG", "MING",
	}
	for _, k := range serifKeys {
		if strings.Contains(upper, k) {
			return "serif"
		}
	}
	return "sans"
}
