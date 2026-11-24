package object

import (
	"bytes"
	"fmt"
	"strconv"
)

// XRefEntry 表示交叉引用表中的单个对象定位条目
type XRefEntry struct {
	Type   int // 0=空闲, 1=未压缩(Offset为字节偏移), 2=压缩在ObjStm中(ObjStmNum+Index)
	Offset int64
	GenNum int
	// 当 Type == 2 时使用
	ObjStmNum int
	Index     int
}

// PageRef 表示文档中的一页及其继承属性
type PageRef struct {
	Index     int
	ObjNum    int
	Dict      Dict
	MediaBox  [4]float64 // [llx, lly, urx, ury]
	Resources Dict
	Rotate    int
}

// Document 表示一个已解析、可修改的 PDF 文档对象模型
type Document struct {
	raw           []byte
	XRef          map[int]XRefEntry
	Trailer       Dict
	Objects       map[int]Object
	objStmCache   map[int]map[int]Object
	MaxObjNum     int
	IsEncrypted   bool
}

// NewEmptyDocument 创建一个空白可构建 PDF 文档（用于新建双语 PDF 或单元测试）
func NewEmptyDocument() *Document {
	return &Document{
		XRef:        make(map[int]XRefEntry),
		Trailer:     make(Dict),
		Objects:     make(map[int]Object),
		objStmCache: make(map[int]map[int]Object),
	}
}

// Parse 解析原始 PDF 字节流为 Document
func Parse(data []byte) (*Document, error) {
	headerIdx := bytes.Index(data[:minInt(len(data), 1024)], []byte("%PDF-"))
	if headerIdx < 0 {
		return nil, fmt.Errorf("invalid PDF: missing %%PDF- header")
	}
	if headerIdx > 0 {
		data = data[headerIdx:]
	}

	doc := &Document{
		raw:         data,
		XRef:        make(map[int]XRefEntry),
		Trailer:     make(Dict),
		Objects:     make(map[int]Object),
		objStmCache: make(map[int]map[int]Object),
	}

	// 从文件末尾逆向查找 startxref
	tailStart := len(data) - 2048
	if tailStart < 0 {
		tailStart = 0
	}
	sxrefRel := bytes.LastIndex(data[tailStart:], []byte("startxref"))
	if sxrefRel < 0 {
		return nil, fmt.Errorf("invalid PDF: missing startxref")
	}
	lex := NewLexer(data, tailStart+sxrefRel+len("startxref"))
	offStr := lex.ReadKeyword()
	xrefOffset, err := strconv.ParseInt(offStr, 10, 64)
	if err != nil || xrefOffset < 0 || int(xrefOffset) >= len(data) {
		return nil, fmt.Errorf("invalid startxref offset: %q", offStr)
	}

	visited := make(map[int64]bool)
	if err := doc.parseXRefAt(xrefOffset, visited); err != nil {
		return nil, err
	}

	// 检查加密字典
	if enc := doc.Trailer["Encrypt"]; enc != nil {
		if _, isNull := enc.(Null); !isNull {
			doc.IsEncrypted = true
		}
	}

	// 计算最大对象号
	for num := range doc.XRef {
		if num > doc.MaxObjNum {
			doc.MaxObjNum = num
		}
	}
	if sizeObj, ok := doc.Trailer["Size"]; ok {
		if sz, ok := AsInt(sizeObj); ok && int(sz)-1 > doc.MaxObjNum {
			doc.MaxObjNum = int(sz) - 1
		}
	}

	return doc, nil
}

func (d *Document) parseXRefAt(offset int64, visited map[int64]bool) error {
	if visited[offset] || offset < 0 || int(offset) >= len(d.raw) {
		return nil
	}
	visited[offset] = true

	lex := NewLexer(d.raw, int(offset))
	lex.SkipWhitespace()

	// 判断是传统 xref 表还是 PDF 1.5+ 的 XRef Stream 对象
	if bytes.HasPrefix(d.raw[lex.Pos():], []byte("xref")) {
		return d.parseClassicXRef(lex, visited)
	}
	return d.parseXRefStream(lex, visited)
}

func (d *Document) parseClassicXRef(lex *Lexer, visited map[int64]bool) error {
	_ = lex.ReadKeyword() // 消费 "xref"
	for {
		saved := lex.Pos()
		tok := lex.ReadKeyword()
		if tok == "" || tok == "trailer" {
			break
		}
		startNum, err1 := strconv.Atoi(tok)
		countTok := lex.ReadKeyword()
		count, err2 := strconv.Atoi(countTok)
		if err1 != nil || err2 != nil {
			lex.SetPos(saved)
			break
		}
		for i := 0; i < count; i++ {
			offTok := lex.ReadKeyword()
			genTok := lex.ReadKeyword()
			flagTok := lex.ReadKeyword()
			offVal, _ := strconv.ParseInt(offTok, 10, 64)
			genVal, _ := strconv.Atoi(genTok)
			objNum := startNum + i
			// 仅在尚未被更新的增量 xref 覆盖时写入
			if _, exists := d.XRef[objNum]; !exists {
				if flagTok == "n" {
					d.XRef[objNum] = XRefEntry{Type: 1, Offset: offVal, GenNum: genVal}
				} else {
					d.XRef[objNum] = XRefEntry{Type: 0, Offset: offVal, GenNum: genVal}
				}
			}
		}
	}

	trObj, err := lex.ReadObject()
	if err != nil {
		return fmt.Errorf("parse trailer failed: %w", err)
	}
	trDict, ok := trObj.(Dict)
	if !ok {
		return fmt.Errorf("trailer is not a dict")
	}
	for k, v := range trDict {
		if _, exists := d.Trailer[k]; !exists {
			d.Trailer[k] = v
		}
	}

	// 支持混合交叉引用流（/XRefStm）
	if xstmObj, ok := trDict["XRefStm"]; ok {
		if xstmOff, ok := AsInt(xstmObj); ok {
			_ = d.parseXRefAt(xstmOff, visited)
		}
	}
	// 递归解析 /Prev 增量链
	if prevObj, ok := trDict["Prev"]; ok {
		if prevOff, ok := AsInt(prevObj); ok {
			if err := d.parseXRefAt(prevOff, visited); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Document) parseXRefStream(lex *Lexer, visited map[int64]bool) error {
	// 读取 "N G obj"
	_ = lex.ReadKeyword()
	_ = lex.ReadKeyword()
	kw := lex.ReadKeyword()
	if kw != "obj" {
		return fmt.Errorf("expected XRef stream 'obj', got %q", kw)
	}
	obj, err := lex.ReadObject()
	if err != nil {
		return fmt.Errorf("read XRef stream object failed: %w", err)
	}
	stm, ok := obj.(*Stream)
	if !ok {
		return fmt.Errorf("XRef object is not a stream")
	}

	for k, v := range stm.Dict {
		if k == "Type" || k == "W" || k == "Index" || k == "Filter" || k == "DecodeParms" || k == "Length" {
			continue
		}
		if _, exists := d.Trailer[k]; !exists {
			d.Trailer[k] = v
		}
	}

	decoded, err := DecodeStream(stm, func(o Object) Object { return o })
	if err != nil {
		return fmt.Errorf("decode XRef stream failed: %w", err)
	}

	wArr, ok := stm.Dict["W"].(Array)
	if !ok || len(wArr) != 3 {
		return fmt.Errorf("invalid XRef stream /W array")
	}
	w0, _ := AsInt(wArr[0])
	w1, _ := AsInt(wArr[1])
	w2, _ := AsInt(wArr[2])
	entrySize := int(w0 + w1 + w2)
	if entrySize <= 0 {
		return fmt.Errorf("invalid XRef entry size")
	}

	sizeVal, _ := AsInt(stm.Dict["Size"])
	var sections [][2]int
	if idxArr, ok := stm.Dict["Index"].(Array); ok && len(idxArr) >= 2 {
		for i := 0; i+1 < len(idxArr); i += 2 {
			s0, _ := AsInt(idxArr[i])
			s1, _ := AsInt(idxArr[i+1])
			sections = append(sections, [2]int{int(s0), int(s1)})
		}
	} else {
		sections = [][2]int{{0, int(sizeVal)}}
	}

	pos := 0
	for _, sec := range sections {
		startObj, count := sec[0], sec[1]
		for i := 0; i < count && pos+entrySize <= len(decoded); i++ {
			f0 := int64(1)
			if w0 > 0 {
				f0 = readBigEndianUint(decoded[pos : pos+int(w0)])
			}
			f1 := readBigEndianUint(decoded[pos+int(w0) : pos+int(w0+w1)])
			f2 := readBigEndianUint(decoded[pos+int(w0+w1) : pos+entrySize])
			pos += entrySize

			objNum := startObj + i
			if _, exists := d.XRef[objNum]; exists {
				continue
			}
			switch f0 {
			case 0:
				d.XRef[objNum] = XRefEntry{Type: 0}
			case 1:
				d.XRef[objNum] = XRefEntry{Type: 1, Offset: f1, GenNum: int(f2)}
			case 2:
				d.XRef[objNum] = XRefEntry{Type: 2, ObjStmNum: int(f1), Index: int(f2)}
			}
		}
	}

	if prevObj, ok := stm.Dict["Prev"]; ok {
		if prevOff, ok := AsInt(prevObj); ok {
			if err := d.parseXRefAt(prevOff, visited); err != nil {
				return err
			}
		}
	}
	return nil
}

func readBigEndianUint(b []byte) int64 {
	var v int64
	for _, c := range b {
		v = (v << 8) | int64(c)
	}
	return v
}

// GetObject 根据对象号懒加载并缓存间接对象
func (d *Document) GetObject(objNum int) (Object, error) {
	if obj, ok := d.Objects[objNum]; ok {
		return obj, nil
	}
	entry, ok := d.XRef[objNum]
	if !ok || entry.Type == 0 {
		return Null{}, nil
	}

	if entry.Type == 1 {
		if entry.Offset < 0 || int(entry.Offset) >= len(d.raw) {
			return nil, fmt.Errorf("object %d offset %d out of range", objNum, entry.Offset)
		}
		lex := NewLexer(d.raw, int(entry.Offset))
		_ = lex.ReadKeyword() // objNum
		_ = lex.ReadKeyword() // genNum
		kw := lex.ReadKeyword()
		if kw != "obj" {
			return nil, fmt.Errorf("object %d at %d missing 'obj', got %q", objNum, entry.Offset, kw)
		}
		obj, err := lex.ReadObject()
		if err != nil {
			return nil, fmt.Errorf("read object %d failed: %w", objNum, err)
		}
		d.Objects[objNum] = obj
		return obj, nil
	}

	// Type == 2：从压缩对象流 (/ObjStm) 中解压提取
	if entry.Type == 2 {
		stmObjs, err := d.loadObjStm(entry.ObjStmNum)
		if err != nil {
			return nil, err
		}
		if obj, exists := stmObjs[objNum]; exists {
			d.Objects[objNum] = obj
			return obj, nil
		}
		return Null{}, nil
	}

	return Null{}, nil
}

func (d *Document) loadObjStm(stmNum int) (map[int]Object, error) {
	if cached, ok := d.objStmCache[stmNum]; ok {
		return cached, nil
	}
	stmObj, err := d.GetObject(stmNum)
	if err != nil {
		return nil, err
	}
	stm, ok := stmObj.(*Stream)
	if !ok {
		return nil, fmt.Errorf("ObjStm %d is not a stream", stmNum)
	}
	nVal, _ := AsInt(d.Resolve(stm.Dict["N"]))
	firstVal, _ := AsInt(d.Resolve(stm.Dict["First"]))

	decoded, err := DecodeStream(stm, d.Resolve)
	if err != nil {
		return nil, fmt.Errorf("decode ObjStm %d failed: %w", stmNum, err)
	}

	headerLex := NewLexer(decoded, 0)
	type pair struct {
		num int
		off int
	}
	pairs := make([]pair, 0, nVal)
	for i := 0; i < int(nVal); i++ {
		nTok := headerLex.ReadKeyword()
		oTok := headerLex.ReadKeyword()
		num, err1 := strconv.Atoi(nTok)
		off, err2 := strconv.Atoi(oTok)
		if err1 != nil || err2 != nil {
			break
		}
		pairs = append(pairs, pair{num: num, off: off})
	}

	res := make(map[int]Object, len(pairs))
	for _, p := range pairs {
		absOff := int(firstVal) + p.off
		if absOff < 0 || absOff >= len(decoded) {
			continue
		}
		objLex := NewLexer(decoded, absOff)
		if obj, err := objLex.ReadObject(); err == nil {
			res[p.num] = obj
		}
	}
	d.objStmCache[stmNum] = res
	return res, nil
}

// Resolve 自动解引用 IndirectRef（带最大深度保护防止循环引用）
func (d *Document) Resolve(obj Object) Object {
	curr := obj
	for depth := 0; depth < 16; depth++ {
		ref, ok := curr.(IndirectRef)
		if !ok {
			return curr
		}
		next, err := d.GetObject(ref.ObjNum)
		if err != nil {
			return Null{}
		}
		curr = next
	}
	return curr
}

// Pages 按页序遍历文档所有页面，处理 /MediaBox、/Resources 与 /Rotate 继承
func (d *Document) Pages() ([]PageRef, error) {
	rootObj := d.Resolve(d.Trailer["Root"])
	rootDict, ok := rootObj.(Dict)
	if !ok {
		return nil, fmt.Errorf("PDF missing /Root catalog")
	}
	pagesRef := rootDict["Pages"]
	if pagesRef == nil {
		return nil, fmt.Errorf("PDF catalog missing /Pages")
	}

	var result []PageRef
	defaultBox := [4]float64{0, 0, 595.28, 841.89}
	var walk func(nodeRef Object, inheritedBox [4]float64, inheritedRes Dict, inheritedRot int) error
	walk = func(nodeRef Object, inheritedBox [4]float64, inheritedRes Dict, inheritedRot int) error {
		objNum := 0
		if ir, ok := nodeRef.(IndirectRef); ok {
			objNum = ir.ObjNum
		}
		nodeDict, ok := d.Resolve(nodeRef).(Dict)
		if !ok {
			return nil
		}

		box := inheritedBox
		if mb := d.Resolve(nodeDict["MediaBox"]); mb != nil {
			if b, ok := parseRect(mb, d.Resolve); ok {
				box = b
			}
		}
		if cb := d.Resolve(nodeDict["CropBox"]); cb != nil {
			if b, ok := parseRect(cb, d.Resolve); ok {
				box = b
			}
		}

		res := inheritedRes
		if rDict, ok := d.Resolve(nodeDict["Resources"]).(Dict); ok {
			res = rDict
		}

		rot := inheritedRot
		if rObj := d.Resolve(nodeDict["Rotate"]); rObj != nil {
			if rv, ok := AsInt(rObj); ok {
				rot = int(rv)
			}
		}

		nodeType, _ := d.Resolve(nodeDict["Type"]).(Name)
		if nodeType == "Pages" {
			kids, _ := d.Resolve(nodeDict["Kids"]).(Array)
			for _, kid := range kids {
				if err := walk(kid, box, res, rot); err != nil {
					return err
				}
			}
			return nil
		}

		if nodeType == "Page" {
			result = append(result, PageRef{
				Index:     len(result),
				ObjNum:    objNum,
				Dict:      nodeDict,
				MediaBox:  box,
				Resources: res,
				Rotate:    rot,
			})
		}
		return nil
	}

	if err := walk(pagesRef, defaultBox, make(Dict), 0); err != nil {
		return nil, err
	}
	return result, nil
}

// PageContentStreams 解码并拼接指定页面的全部 /Contents 流
func (d *Document) PageContentStreams(page PageRef) ([]byte, error) {
	contentsObj := d.Resolve(page.Dict["Contents"])
	if contentsObj == nil {
		return nil, nil
	}
	switch c := contentsObj.(type) {
	case *Stream:
		return DecodeStream(c, d.Resolve)
	case Array:
		var merged bytes.Buffer
		for _, item := range c {
			if stm, ok := d.Resolve(item).(*Stream); ok {
				dec, err := DecodeStream(stm, d.Resolve)
				if err != nil {
					return nil, err
				}
				merged.Write(dec)
				merged.WriteByte('\n')
			}
		}
		return merged.Bytes(), nil
	default:
		return nil, nil
	}
}

func parseRect(obj Object, resolve func(Object) Object) ([4]float64, bool) {
	var rect [4]float64
	arr, ok := obj.(Array)
	if !ok || len(arr) != 4 {
		return rect, false
	}
	for i := 0; i < 4; i++ {
		v, ok := AsFloat(resolve(arr[i]))
		if !ok {
			return rect, false
		}
		rect[i] = v
	}
	return rect, true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
