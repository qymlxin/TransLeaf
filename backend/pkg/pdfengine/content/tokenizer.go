package content

import (
	"bytes"

	"translation-assistant/pkg/pdfengine/object"
)

// Operation 表示 PDF Content Stream 中的单条绘图指令（操作数列表 + 操作符）
type Operation struct {
	Operator string
	Operands []object.Object
}

// ParseOperations 将解码后的 Content Stream 字节流解析为指令序列
func ParseOperations(data []byte) []Operation {
	lex := object.NewLexer(data, 0)
	var ops []Operation
	var operands []object.Object

	for !lex.EOF() {
		lex.SkipWhitespace()
		if lex.EOF() {
			break
		}

		// 检查内联图像 BI ... ID ... EI，避免二进制数据干扰分词
		if lex.Pos()+2 <= len(data) && data[lex.Pos()] == 'B' && data[lex.Pos()+1] == 'I' &&
			(lex.Pos()+2 == len(data) || data[lex.Pos()+2] == ' ' || data[lex.Pos()+2] == '\n' || data[lex.Pos()+2] == '\r') {
			skipInlineImage(lex, data)
			operands = nil
			continue
		}

		c := data[lex.Pos()]
		if c == '/' || c == '(' || c == '<' || c == '[' {
			obj, err := lex.ReadObject()
			if err != nil {
				break
			}
			operands = append(operands, obj)
			continue
		}

		saved := lex.Pos()
		tok := lex.ReadKeyword()
		if tok == "" {
			lex.SetPos(saved + 1)
			continue
		}

		// 判断是否为布尔、null 或数字操作数
		if tok == "true" {
			operands = append(operands, object.Bool(true))
			continue
		}
		if tok == "false" {
			operands = append(operands, object.Bool(false))
			continue
		}
		if tok == "null" {
			operands = append(operands, object.Null{})
			continue
		}

		lex.SetPos(saved)
		obj, err := lex.ReadObject()
		if err != nil {
			lex.SetPos(saved + len(tok))
			continue
		}
		switch obj.(type) {
		case object.Int, object.Real:
			operands = append(operands, obj)
		default:
			// 非数字关键字即为操作符
			ops = append(ops, Operation{
				Operator: tok,
				Operands: operands,
			})
			operands = nil
		}
	}
	return ops
}

// SerializeOperations 将指令序列重新序列化为合法的 PDF Content Stream 字节流
func SerializeOperations(ops []Operation) []byte {
	var buf bytes.Buffer
	for _, op := range ops {
		for _, arg := range op.Operands {
			if arg != nil {
				arg.Serialize(&buf)
				buf.WriteByte(' ')
			}
		}
		buf.WriteString(op.Operator)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func skipInlineImage(lex *object.Lexer, data []byte) {
	pos := lex.Pos() + 2
	// 寻找内联图像结束符 EI
	for pos+2 < len(data) {
		if (data[pos] == ' ' || data[pos] == '\n' || data[pos] == '\r') &&
			data[pos+1] == 'E' && data[pos+2] == 'I' &&
			(pos+3 == len(data) || data[pos+3] == ' ' || data[pos+3] == '\n' || data[pos+3] == '\r') {
			lex.SetPos(pos + 3)
			return
		}
		pos++
	}
	lex.SetPos(len(data))
}
