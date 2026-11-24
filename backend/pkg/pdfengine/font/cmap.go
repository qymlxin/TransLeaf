package font

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"

	"transleaf/pkg/pdfengine/object"
)

// CMap 表示解析后的 PDF ToUnicode CMap 映射表
type CMap struct {
	CodeSpaceBytes []int // 支持的字节宽度（1 或 2 字节，按常见顺序）
	CharMap        map[uint32]string
	MaxByteLen     int
}

// ParseCMap 解析 ToUnicode CMap 流字节为 CMap 结构
func ParseCMap(data []byte) *CMap {
	cm := &CMap{
		CharMap:    make(map[uint32]string),
		MaxByteLen: 1,
	}
	lex := object.NewLexer(data, 0)
	var stack []object.Object

	for !lex.EOF() {
		lex.SkipWhitespace()
		if lex.EOF() {
			break
		}
		tok, err := lex.ReadObject()
		if err != nil {
			break
		}
		if name, ok := tok.(object.Name); ok {
			switch name {
			case "begincodespacerange":
				count := popInt(stack)
				stack = nil
				for i := 0; i < count && !lex.EOF(); i++ {
					s1, err1 := lex.ReadObject()
					s2, err2 := lex.ReadObject()
					if err1 != nil || err2 != nil {
						break
					}
					if str1, ok := s1.(object.String); ok {
						bl := len(str1.Value)
						if bl > cm.MaxByteLen {
							cm.MaxByteLen = bl
						}
						cm.addCodeSpaceLen(bl)
					}
					_ = s2
				}
				continue
			case "beginbfchar":
				count := popInt(stack)
				stack = nil
				for i := 0; i < count && !lex.EOF(); i++ {
					srcObj, err1 := lex.ReadObject()
					dstObj, err2 := lex.ReadObject()
					if err1 != nil || err2 != nil {
						break
					}
					src, ok1 := srcObj.(object.String)
					dst, ok2 := dstObj.(object.String)
					if ok1 && ok2 {
						code := bytesToUint32(src.Value)
						cm.CharMap[code] = decodeUTF16BE(dst.Value)
						if len(src.Value) > cm.MaxByteLen {
							cm.MaxByteLen = len(src.Value)
						}
					}
				}
				continue
			case "beginbfrange":
				count := popInt(stack)
				stack = nil
				for i := 0; i < count && !lex.EOF(); i++ {
					startObj, err1 := lex.ReadObject()
					endObj, err2 := lex.ReadObject()
					dstObj, err3 := lex.ReadObject()
					if err1 != nil || err2 != nil || err3 != nil {
						break
					}
					sStart, ok1 := startObj.(object.String)
					sEnd, ok2 := endObj.(object.String)
					if !ok1 || !ok2 {
						continue
					}
					if len(sStart.Value) > cm.MaxByteLen {
						cm.MaxByteLen = len(sStart.Value)
					}
					lo := bytesToUint32(sStart.Value)
					hi := bytesToUint32(sEnd.Value)
					if hi < lo || hi-lo > 65536 {
						continue
					}
					switch d := dstObj.(type) {
					case object.String:
						baseVal := make([]byte, len(d.Value))
						copy(baseVal, d.Value)
						for code := lo; code <= hi; code++ {
							cm.CharMap[code] = decodeUTF16BE(baseVal)
							incrementBigEndianBytes(baseVal)
						}
					case object.Array:
						for idx, item := range d {
							code := lo + uint32(idx)
							if code > hi {
								break
							}
							if ds, ok := item.(object.String); ok {
								cm.CharMap[code] = decodeUTF16BE(ds.Value)
							}
						}
					}
				}
				continue
			case "endcodespacerange", "endbfchar", "endbfrange":
				stack = nil
				continue
			}
		}
		stack = append(stack, tok)
		if len(stack) > 16 {
			stack = stack[len(stack)-8:]
		}
	}

	return cm
}

func (cm *CMap) addCodeSpaceLen(n int) {
	if n <= 0 || n > 4 {
		return
	}
	for _, existing := range cm.CodeSpaceBytes {
		if existing == n {
			return
		}
	}
	cm.CodeSpaceBytes = append(cm.CodeSpaceBytes, n)
}

// DecodeBytes 将 PDF 字符串字节流按 CMap 解码为 (字符列表, 对应原始字形码列表)
func (cm *CMap) DecodeBytes(raw []byte, isTwoByte bool) ([]string, []uint32) {
	var texts []string
	var codes []uint32

	step := 1
	if isTwoByte || cm.MaxByteLen == 2 {
		step = 2
	}

	i := 0
	for i < len(raw) {
		if step == 2 && i+2 <= len(raw) {
			code := uint32(raw[i])<<8 | uint32(raw[i+1])
			if s, ok := cm.CharMap[code]; ok {
				texts = append(texts, s)
				codes = append(codes, code)
				i += 2
				continue
			}
			// 尝试单字节兜底
			if !isTwoByte {
				c1 := uint32(raw[i])
				if s, ok := cm.CharMap[c1]; ok {
					texts = append(texts, s)
					codes = append(codes, c1)
					i++
					continue
				}
			}
			// 未命中映射时保留占位
			codes = append(codes, code)
			if code >= 0x20 && code < 0x7f {
				texts = append(texts, string(rune(code)))
			} else {
				texts = append(texts, "")
			}
			i += 2
			continue
		}

		c1 := uint32(raw[i])
		codes = append(codes, c1)
		if s, ok := cm.CharMap[c1]; ok {
			texts = append(texts, s)
		} else if c1 >= 0x20 && c1 < 0x7f {
			texts = append(texts, string(rune(c1)))
		} else {
			texts = append(texts, "")
		}
		i++
	}
	return texts, codes
}

func popInt(stack []object.Object) int {
	if len(stack) == 0 {
		return 0
	}
	if v, ok := object.AsInt(stack[len(stack)-1]); ok && v > 0 {
		return int(v)
	}
	return 0
}

func bytesToUint32(b []byte) uint32 {
	var v uint32
	for _, c := range b {
		v = (v << 8) | uint32(c)
	}
	return v
}

func incrementBigEndianBytes(b []byte) {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			break
		}
	}
}

func decodeUTF16BE(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) == 1 {
		return string(rune(b[0]))
	}
	if len(b)%2 != 0 {
		b = append([]byte{0}, b...)
	}
	u16 := make([]uint16, len(b)/2)
	for i := 0; i < len(u16); i++ {
		u16[i] = binary.BigEndian.Uint16(b[i*2 : i*2+2])
	}
	runes := utf16.Decode(u16)
	var sb strings.Builder
	for _, r := range runes {
		if r != 0 {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
