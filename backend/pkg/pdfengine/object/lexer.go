package object

import (
	"bytes"
	"fmt"
	"strconv"
)

// Lexer 实现 PDF 字节流词法扫描与对象解析
type Lexer struct {
	data []byte
	pos  int
}

// NewLexer 创建新的 PDF 词法扫描器
func NewLexer(data []byte, offset int) *Lexer {
	if offset < 0 {
		offset = 0
	}
	if offset > len(data) {
		offset = len(data)
	}
	return &Lexer{data: data, pos: offset}
}

// Pos 返回当前扫描位置
func (l *Lexer) Pos() int { return l.pos }

// SetPos 设置扫描位置
func (l *Lexer) SetPos(p int) {
	if p < 0 {
		p = 0
	}
	if p > len(l.data) {
		p = len(l.data)
	}
	l.pos = p
}

// EOF 判断是否到达字节流末尾
func (l *Lexer) EOF() bool { return l.pos >= len(l.data) }

func isWhitespace(c byte) bool {
	return c == 0x00 || c == 0x09 || c == 0x0a || c == 0x0c || c == 0x0d || c == 0x20
}

func isDelimiter(c byte) bool {
	return c == '(' || c == ')' || c == '<' || c == '>' ||
		c == '[' || c == ']' || c == '{' || c == '}' || c == '/' || c == '%'
}

// SkipWhitespace 跳过空白符与以 '%' 开头的注释行
func (l *Lexer) SkipWhitespace() {
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		if isWhitespace(c) {
			l.pos++
			continue
		}
		if c == '%' {
			for l.pos < len(l.data) && l.data[l.pos] != '\n' && l.data[l.pos] != '\r' {
				l.pos++
			}
			continue
		}
		break
	}
}

// ReadKeyword 读取下一个普通关键字或数字词素
func (l *Lexer) ReadKeyword() string {
	l.SkipWhitespace()
	start := l.pos
	for l.pos < len(l.data) && !isWhitespace(l.data[l.pos]) && !isDelimiter(l.data[l.pos]) {
		l.pos++
	}
	return string(l.data[start:l.pos])
}

// ReadObject 解析当前位置的完整 PDF 对象
func (l *Lexer) ReadObject() (Object, error) {
	l.SkipWhitespace()
	if l.pos >= len(l.data) {
		return nil, fmt.Errorf("unexpected EOF while reading PDF object")
	}

	c := l.data[l.pos]
	switch c {
	case '/':
		return l.readName(), nil
	case '(':
		return l.readLiteralString(), nil
	case '<':
		if l.pos+1 < len(l.data) && l.data[l.pos+1] == '<' {
			return l.readDictOrStream()
		}
		return l.readHexString(), nil
	case '[':
		return l.readArray()
	default:
		// 可能是数字、间接引用 (N G R) 或布尔/null 关键字
		saved := l.pos
		tok := l.ReadKeyword()
		switch tok {
		case "true":
			return Bool(true), nil
		case "false":
			return Bool(false), nil
		case "null":
			return Null{}, nil
		case "":
			return nil, fmt.Errorf("unexpected delimiter %q at offset %d", c, saved)
		}

		// 尝试解析整数及后续的间接引用 "objNum genNum R"
		if iv, err := strconv.ParseInt(tok, 10, 64); err == nil {
			afterFirst := l.pos
			tok2 := l.ReadKeyword()
			if gen, err2 := strconv.ParseInt(tok2, 10, 64); err2 == nil && gen >= 0 {
				tok3 := l.ReadKeyword()
				if tok3 == "R" && iv > 0 {
					return IndirectRef{ObjNum: int(iv), GenNum: int(gen)}, nil
				}
			}
			l.pos = afterFirst
			return Int(iv), nil
		}

		// 尝试解析浮点数
		if fv, err := strconv.ParseFloat(tok, 64); err == nil {
			return Real(fv), nil
		}

		// 作为未知名称/关键字返回（常用于 Content Stream 场景或容错）
		return Name(tok), nil
	}
}

func (l *Lexer) readName() Name {
	l.pos++ // 跳过 '/'
	var buf bytes.Buffer
	for l.pos < len(l.data) && !isWhitespace(l.data[l.pos]) && !isDelimiter(l.data[l.pos]) {
		c := l.data[l.pos]
		if c == '#' && l.pos+2 < len(l.data) {
			h1 := hexVal(l.data[l.pos+1])
			h2 := hexVal(l.data[l.pos+2])
			if h1 >= 0 && h2 >= 0 {
				buf.WriteByte(byte(h1<<4 | h2))
				l.pos += 3
				continue
			}
		}
		buf.WriteByte(c)
		l.pos++
	}
	return Name(buf.String())
}

func (l *Lexer) readLiteralString() String {
	l.pos++ // 跳过 '('
	var buf bytes.Buffer
	depth := 1
	for l.pos < len(l.data) && depth > 0 {
		c := l.data[l.pos]
		l.pos++
		if c == '\\' {
			if l.pos >= len(l.data) {
				break
			}
			next := l.data[l.pos]
			l.pos++
			switch next {
			case 'n':
				buf.WriteByte('\n')
			case 'r':
				buf.WriteByte('\r')
			case 't':
				buf.WriteByte('\t')
			case 'b':
				buf.WriteByte('\b')
			case 'f':
				buf.WriteByte('\f')
			case '(', ')', '\\':
				buf.WriteByte(next)
			case '\r':
				if l.pos < len(l.data) && l.data[l.pos] == '\n' {
					l.pos++
				}
			case '\n':
				// 行续接符，忽略
			default:
				if next >= '0' && next <= '7' {
					oct := int(next - '0')
					for i := 0; i < 2 && l.pos < len(l.data); i++ {
						d := l.data[l.pos]
						if d >= '0' && d <= '7' {
							oct = oct*8 + int(d-'0')
							l.pos++
						} else {
							break
						}
					}
					buf.WriteByte(byte(oct & 0xff))
				} else {
					buf.WriteByte(next)
				}
			}
			continue
		}
		if c == '(' {
			depth++
			buf.WriteByte(c)
		} else if c == ')' {
			depth--
			if depth > 0 {
				buf.WriteByte(c)
			}
		} else if c == '\r' {
			// PDF 规范中字面量字符串中的 CR/CRLF 统一规整为 LF
			if l.pos < len(l.data) && l.data[l.pos] == '\n' {
				l.pos++
			}
			buf.WriteByte('\n')
		} else {
			buf.WriteByte(c)
		}
	}
	return String{Value: buf.Bytes(), IsHex: false}
}

func (l *Lexer) readHexString() String {
	l.pos++ // 跳过 '<'
	var buf bytes.Buffer
	high := -1
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		l.pos++
		if c == '>' {
			break
		}
		if isWhitespace(c) {
			continue
		}
		v := hexVal(c)
		if v < 0 {
			continue
		}
		if high < 0 {
			high = v
		} else {
			buf.WriteByte(byte(high<<4 | v))
			high = -1
		}
	}
	if high >= 0 {
		buf.WriteByte(byte(high << 4))
	}
	return String{Value: buf.Bytes(), IsHex: true}
}

func (l *Lexer) readArray() (Array, error) {
	l.pos++ // 跳过 '['
	var arr Array
	for {
		l.SkipWhitespace()
		if l.pos >= len(l.data) {
			return arr, fmt.Errorf("unterminated PDF array")
		}
		if l.data[l.pos] == ']' {
			l.pos++
			return arr, nil
		}
		item, err := l.ReadObject()
		if err != nil {
			return arr, err
		}
		arr = append(arr, item)
	}
}

func (l *Lexer) readDictOrStream() (Object, error) {
	l.pos += 2 // 跳过 "<<"
	dict := make(Dict)
	for {
		l.SkipWhitespace()
		if l.pos+1 < len(l.data) && l.data[l.pos] == '>' && l.data[l.pos+1] == '>' {
			l.pos += 2
			break
		}
		if l.pos >= len(l.data) {
			return nil, fmt.Errorf("unterminated PDF dictionary")
		}
		keyObj, err := l.ReadObject()
		if err != nil {
			return nil, err
		}
		keyName, ok := keyObj.(Name)
		if !ok {
			return nil, fmt.Errorf("PDF dictionary key must be Name, got %T", keyObj)
		}
		valObj, err := l.ReadObject()
		if err != nil {
			return nil, err
		}
		dict[keyName] = valObj
	}

	// 检查紧随字典后是否为 stream
	saved := l.pos
	kw := l.ReadKeyword()
	if kw != "stream" {
		l.pos = saved
		return dict, nil
	}

	// 跳过 stream 后的单次 CRLF 或 LF
	if l.pos < len(l.data) && l.data[l.pos] == '\r' {
		l.pos++
	}
	if l.pos < len(l.data) && l.data[l.pos] == '\n' {
		l.pos++
	}

	streamStart := l.pos
	var rawData []byte

	// 优先使用显式整数 /Length，同时校验其后是否紧跟 endstream，否则回退扫描 endstream
	if lenObj, ok := dict["Length"]; ok {
		if length, ok := AsInt(lenObj); ok && length >= 0 && streamStart+int(length) <= len(l.data) {
			candidateEnd := streamStart + int(length)
			checkPos := candidateEnd
			for checkPos < len(l.data) && isWhitespace(l.data[checkPos]) {
				checkPos++
			}
			if bytes.HasPrefix(l.data[checkPos:], []byte("endstream")) {
				rawData = make([]byte, length)
				copy(rawData, l.data[streamStart:candidateEnd])
				l.pos = checkPos + len("endstream")
				return &Stream{Dict: dict, RawData: rawData}, nil
			}
		}
	}

	// 回退：扫描 endstream 关键字
	endIdx := bytes.Index(l.data[streamStart:], []byte("endstream"))
	if endIdx < 0 {
		return nil, fmt.Errorf("unterminated PDF stream at offset %d", streamStart)
	}
	streamEnd := streamStart + endIdx
	// 去除 endstream 之前紧贴的单个换行符
	if streamEnd > streamStart && l.data[streamEnd-1] == '\n' {
		streamEnd--
		if streamEnd > streamStart && l.data[streamEnd-1] == '\r' {
			streamEnd--
		}
	}
	rawData = make([]byte, streamEnd-streamStart)
	copy(rawData, l.data[streamStart:streamEnd])
	l.pos = streamStart + endIdx + len("endstream")
	return &Stream{Dict: dict, RawData: rawData}, nil
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}
