package object

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// Object 表示任意 PDF 底层对象
type Object interface {
	Serialize(buf *bytes.Buffer)
}

// Null 表示 PDF 的 null 对象
type Null struct{}

func (Null) Serialize(buf *bytes.Buffer) { buf.WriteString("null") }

// Bool 表示 PDF 布尔值
type Bool bool

func (b Bool) Serialize(buf *bytes.Buffer) {
	if b {
		buf.WriteString("true")
	} else {
		buf.WriteString("false")
	}
}

// Int 表示 PDF 整数
type Int int64

func (i Int) Serialize(buf *bytes.Buffer) {
	buf.WriteString(strconv.FormatInt(int64(i), 10))
}

// Real 表示 PDF 浮点数
type Real float64

func (r Real) Serialize(buf *bytes.Buffer) {
	v := float64(r)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		buf.WriteString("0")
		return
	}
	// 去除多余尾随零，保持 PDF 体积紧凑
	s := strconv.FormatFloat(v, 'f', 4, 64)
	if idx := bytes.IndexByte([]byte(s), '.'); idx >= 0 {
		end := len(s)
		for end > idx+1 && s[end-1] == '0' {
			end--
		}
		if end == idx+1 {
			end = idx
		}
		s = s[:end]
	}
	if s == "-0" {
		s = "0"
	}
	buf.WriteString(s)
}

// Name 表示 PDF 名称对象（不包含前导斜杠）
type Name string

func (n Name) Serialize(buf *bytes.Buffer) {
	buf.WriteByte('/')
	for i := 0; i < len(n); i++ {
		c := n[i]
		if c < 0x21 || c > 0x7e || c == '#' || c == '/' || c == '%' ||
			c == '(' || c == ')' || c == '<' || c == '>' || c == '[' || c == ']' || c == '{' || c == '}' {
			fmt.Fprintf(buf, "#%02X", c)
		} else {
			buf.WriteByte(c)
		}
	}
}

// String 表示 PDF 字节串对象（支持普通括号形式与十六进制形式）
type String struct {
	Value []byte
	IsHex bool
}

func (s String) Serialize(buf *bytes.Buffer) {
	if s.IsHex {
		buf.WriteByte('<')
		buf.WriteString(hex.EncodeToString(s.Value))
		buf.WriteByte('>')
		return
	}
	buf.WriteByte('(')
	for _, b := range s.Value {
		switch b {
		case '\\':
			buf.WriteString(`\\`)
		case '(':
			buf.WriteString(`\(`)
		case ')':
			buf.WriteString(`\)`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if b < 0x20 || b > 0x7e {
				fmt.Fprintf(buf, `\%03o`, b)
			} else {
				buf.WriteByte(b)
			}
		}
	}
	buf.WriteByte(')')
}

// Array 表示 PDF 数组对象
type Array []Object

func (a Array) Serialize(buf *bytes.Buffer) {
	buf.WriteByte('[')
	for i, item := range a {
		if i > 0 {
			buf.WriteByte(' ')
		}
		if item == nil {
			buf.WriteString("null")
		} else {
			item.Serialize(buf)
		}
	}
	buf.WriteByte(']')
}

// Dict 表示 PDF 字典对象
type Dict map[Name]Object

func (d Dict) Serialize(buf *bytes.Buffer) {
	buf.WriteString("<<")
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	for _, ks := range keys {
		k := Name(ks)
		v := d[k]
		if v == nil {
			continue
		}
		buf.WriteByte(' ')
		k.Serialize(buf)
		buf.WriteByte(' ')
		v.Serialize(buf)
	}
	buf.WriteString(" >>")
}

// Clone 浅拷贝字典
func (d Dict) Clone() Dict {
	out := make(Dict, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}

// Stream 表示 PDF 流对象（字典 + 原始字节载荷）
type Stream struct {
	Dict    Dict
	RawData []byte
}

func (s *Stream) Serialize(buf *bytes.Buffer) {
	d := s.Dict.Clone()
	d["Length"] = Int(len(s.RawData))
	d.Serialize(buf)
	buf.WriteString("\nstream\n")
	buf.Write(s.RawData)
	buf.WriteString("\nendstream")
}

// IndirectRef 表示 PDF 间接引用（如 "12 0 R"）
type IndirectRef struct {
	ObjNum int
	GenNum int
}

func (r IndirectRef) Serialize(buf *bytes.Buffer) {
	fmt.Fprintf(buf, "%d %d R", r.ObjNum, r.GenNum)
}

// AsFloat 将 Int 或 Real 安全转为 float64
func AsFloat(obj Object) (float64, bool) {
	switch v := obj.(type) {
	case Int:
		return float64(v), true
	case Real:
		return float64(v), true
	default:
		return 0, false
	}
}

// AsInt 将 Int 或整数值的 Real 安全转为 int64
func AsInt(obj Object) (int64, bool) {
	switch v := obj.(type) {
	case Int:
		return int64(v), true
	case Real:
		return int64(v), true
	default:
		return 0, false
	}
}
