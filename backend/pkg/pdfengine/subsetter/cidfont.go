package subsetter

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"unicode/utf16"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"transleaf/pkg/pdfengine/object"
)

// LoadedFont 封装解析后的 TrueType 字体及其 PDF 度量信息
type LoadedFont struct {
	Font       *sfnt.Font
	RawBytes   []byte
	PostName   string
	UnitsPerEm int
	Ascent     int // 归一化到 1000 em
	Descent    int // 归一化到 1000 em（通常为负值）
	BBox       [4]int
}

// LoadTrueType 从磁盘加载 TrueType (.ttf) 字体
func LoadTrueType(path string) (*LoadedFont, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read font %s failed: %w", path, err)
	}
	f, err := sfnt.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse sfnt font %s failed: %w", path, err)
	}

	var buf sfnt.Buffer
	upem := int(f.UnitsPerEm())
	if upem <= 0 {
		upem = 1000
	}

	ppem := fixed.Int26_6(upem << 6)
	metrics, err := f.Metrics(&buf, ppem, 0)
	ascent, descent := 880, -120
	if err == nil {
		ascent = int(metrics.Ascent.Round()) * 1000 / upem
		descent = -int(metrics.Descent.Round()) * 1000 / upem
	}

	bounds, err := f.Bounds(&buf, ppem, 0)
	bbox := [4]int{-200, descent, 1200, ascent}
	if err == nil {
		bbox = [4]int{
			int(bounds.Min.X.Round()) * 1000 / upem,
			-int(bounds.Max.Y.Round()) * 1000 / upem,
			int(bounds.Max.X.Round()) * 1000 / upem,
			-int(bounds.Min.Y.Round()) * 1000 / upem,
		}
	}

	postName := "NotoSansSC-Regular"
	if n, err := f.Name(&buf, sfnt.NameIDPostScript); err == nil && n != "" {
		postName = sanitizeFontName(n)
	}

	return &LoadedFont{
		Font:       f,
		RawBytes:   raw,
		PostName:   postName,
		UnitsPerEm: upem,
		Ascent:     ascent,
		Descent:    descent,
		BBox:       bbox,
	}, nil
}

// GlyphIndex 返回指定 Unicode 字符在字体中的 Glyph ID
func (lf *LoadedFont) GlyphIndex(r rune) (sfnt.GlyphIndex, bool) {
	var buf sfnt.Buffer
	gid, err := lf.Font.GlyphIndex(&buf, r)
	if err != nil || gid == 0 {
		return 0, false
	}
	return gid, true
}

// GlyphAdvance1000 返回指定 Glyph ID 的前进宽度（PDF 标准 1/1000 em 单位）
func (lf *LoadedFont) GlyphAdvance1000(gid sfnt.GlyphIndex) int {
	var buf sfnt.Buffer
	ppem := fixed.Int26_6(lf.UnitsPerEm << 6)
	adv, err := lf.Font.GlyphAdvance(&buf, gid, ppem, 0)
	if err != nil {
		return 1000
	}
	return int(adv.Round()) * 1000 / lf.UnitsPerEm
}

// RuneWidthPt 返回指定字符在给定字号下的真实显示宽度（单位 pt）
func (lf *LoadedFont) RuneWidthPt(r rune, fontSize float64) float64 {
	gid, ok := lf.GlyphIndex(r)
	if !ok {
		if r >= 0x2E80 {
			return fontSize
		}
		return fontSize * 0.5
	}
	return float64(lf.GlyphAdvance1000(gid)) / 1000.0 * fontSize
}

// EncodeTextToGIDBytes 将 UTF-8 文本编码为 2 字节大端 Identity-H GID 字节序列
func (lf *LoadedFont) EncodeTextToGIDBytes(text string) []byte {
	out := make([]byte, 0, len(text)*2)
	for _, r := range text {
		gid, ok := lf.GlyphIndex(r)
		if !ok {
			if r == ' ' {
				gid, _ = lf.GlyphIndex(' ')
			} else {
				gid = 0
			}
		}
		out = append(out, byte(gid>>8), byte(gid&0xff))
	}
	return out
}

// EmbedCIDFont 向 PDF 文档嵌入标准 Type0 + CIDFontType2 复合中文字体及精确 ToUnicode CMap
func EmbedCIDFont(doc *object.Document, lf *LoadedFont, usedRunes map[rune]bool) (object.IndirectRef, error) {
	// 1. 压缩并写入 FontFile2 流
	compressedFont, err := object.EncodeFlate(lf.RawBytes)
	if err != nil {
		return object.IndirectRef{}, fmt.Errorf("compress font stream failed: %w", err)
	}
	fontFileRef := doc.AllocObj(&object.Stream{
		Dict: object.Dict{
			"Filter":  object.Name("FlateDecode"),
			"Length":  object.Int(len(compressedFont)),
			"Length1": object.Int(len(lf.RawBytes)),
		},
		RawData: compressedFont,
	})

	// 2. 构建 FontDescriptor
	descriptorRef := doc.AllocObj(object.Dict{
		"Type":        object.Name("FontDescriptor"),
		"FontName":    object.Name(lf.PostName),
		"Flags":       object.Int(32), // Nonsymbolic
		"FontBBox":    object.Array{object.Int(lf.BBox[0]), object.Int(lf.BBox[1]), object.Int(lf.BBox[2]), object.Int(lf.BBox[3])},
		"ItalicAngle": object.Int(0),
		"Ascent":      object.Int(lf.Ascent),
		"Descent":     object.Int(lf.Descent),
		"CapHeight":   object.Int(lf.Ascent),
		"StemV":       object.Int(80),
		"FontFile2":   fontFileRef,
	})

	// 3. 收集所有已用字符的 GID -> Rune 与 GID -> Width 映射
	gidToRune := make(map[uint16]rune, len(usedRunes))
	gidWidths := make(map[uint16]int, len(usedRunes))
	for r := range usedRunes {
		if gid, ok := lf.GlyphIndex(r); ok {
			g := uint16(gid)
			if _, exists := gidToRune[g]; !exists {
				gidToRune[g] = r
			}
			gidWidths[g] = lf.GlyphAdvance1000(gid)
		}
	}

	gids := make([]int, 0, len(gidWidths))
	for g := range gidWidths {
		gids = append(gids, int(g))
	}
	sort.Ints(gids)

	// 构建 /W 宽度数组（按连续 GID 分组以缩减体积）
	var wArr object.Array
	for i := 0; i < len(gids); {
		start := gids[i]
		var group object.Array
		j := i
		for j < len(gids) && gids[j] == start+(j-i) {
			group = append(group, object.Int(gidWidths[uint16(gids[j])]))
			j++
		}
		wArr = append(wArr, object.Int(start), group)
		i = j
	}

	// 4. 构建 CIDFontType2 字典
	cidFontRef := doc.AllocObj(object.Dict{
		"Type":     object.Name("Font"),
		"Subtype":  object.Name("CIDFontType2"),
		"BaseFont": object.Name(lf.PostName),
		"CIDSystemInfo": object.Dict{
			"Registry":   object.String{Value: []byte("Adobe")},
			"Ordering":   object.String{Value: []byte("Identity")},
			"Supplement": object.Int(0),
		},
		"FontDescriptor": descriptorRef,
		"DW":             object.Int(1000),
		"W":              wArr,
		"CIDToGIDMap":    object.Name("Identity"),
	})

	// 5. 生成精确的 ToUnicode CMap 流（支持中文复制与搜索）
	cmapBytes := buildToUnicodeCMap(gids, gidToRune)
	compressedCMap, err := object.EncodeFlate(cmapBytes)
	if err != nil {
		return object.IndirectRef{}, err
	}
	toUnicodeRef := doc.AllocObj(&object.Stream{
		Dict: object.Dict{
			"Filter": object.Name("FlateDecode"),
			"Length": object.Int(len(compressedCMap)),
		},
		RawData: compressedCMap,
	})

	// 6. 构建顶层 Type0 字体字典
	type0Ref := doc.AllocObj(object.Dict{
		"Type":            object.Name("Font"),
		"Subtype":         object.Name("Type0"),
		"BaseFont":        object.Name(lf.PostName),
		"Encoding":        object.Name("Identity-H"),
		"DescendantFonts": object.Array{cidFontRef},
		"ToUnicode":       toUnicodeRef,
	})

	return type0Ref, nil
}

func buildToUnicodeCMap(gids []int, gidToRune map[uint16]rune) []byte {
	var buf bytes.Buffer
	buf.WriteString("/CIDInit /ProcSet findresource begin\n")
	buf.WriteString("12 dict begin\n")
	buf.WriteString("begincmap\n")
	buf.WriteString("/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n")
	buf.WriteString("/CMapName /Adobe-Identity-UCS def\n")
	buf.WriteString("/CMapType 2 def\n")
	buf.WriteString("1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n")

	// PDF 规范要求单个 beginbfchar 块最多 100 条
	const chunkSize = 100
	for i := 0; i < len(gids); i += chunkSize {
		end := i + chunkSize
		if end > len(gids) {
			end = len(gids)
		}
		chunk := gids[i:end]
		fmt.Fprintf(&buf, "%d beginbfchar\n", len(chunk))
		for _, g := range chunk {
			r := gidToRune[uint16(g)]
			u16 := utf16.Encode([]rune{r})
			var hexDst bytes.Buffer
			for _, v := range u16 {
				var b [2]byte
				binary.BigEndian.PutUint16(b[:], v)
				fmt.Fprintf(&hexDst, "%02X%02X", b[0], b[1])
			}
			fmt.Fprintf(&buf, "<%04X> <%s>\n", g, hexDst.String())
		}
		buf.WriteString("endbfchar\n")
	}

	buf.WriteString("endcmap\n")
	buf.WriteString("CMapName currentdict /CMap defineresource pop\n")
	buf.WriteString("end\nend\n")
	return buf.Bytes()
}

func sanitizeFontName(name string) string {
	var buf bytes.Buffer
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			buf.WriteByte(c)
		}
	}
	if buf.Len() == 0 {
		return "CustomCJKFont"
	}
	return buf.String()
}
