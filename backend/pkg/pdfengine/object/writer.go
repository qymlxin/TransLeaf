package object

import (
	"bytes"
	"fmt"
	"sort"
)

// AllocObj 分配一个新的间接对象号并注册到文档中
func (d *Document) AllocObj(obj Object) IndirectRef {
	d.MaxObjNum++
	num := d.MaxObjNum
	d.Objects[num] = obj
	d.XRef[num] = XRefEntry{Type: 1, GenNum: 0}
	return IndirectRef{ObjNum: num, GenNum: 0}
}

// SetObj 覆写或注册指定对象号的内容
func (d *Document) SetObj(num int, obj Object) {
	if num > d.MaxObjNum {
		d.MaxObjNum = num
	}
	d.Objects[num] = obj
	d.XRef[num] = XRefEntry{Type: 1, GenNum: 0}
}

// Write 将当前文档所有有效对象重构并序列化为标准 PDF 1.5 字节流
func (d *Document) Write() ([]byte, error) {
	// 1. 收集所有需要写出的对象号（跳过 ObjStm 与 XRef 自身流，因为全量展开为标准对象）
	objNumsMap := make(map[int]bool)
	for num, entry := range d.XRef {
		if num > 0 && entry.Type != 0 {
			objNumsMap[num] = true
		}
	}
	for num := range d.Objects {
		if num > 0 {
			objNumsMap[num] = true
		}
	}

	nums := make([]int, 0, len(objNumsMap))
	for num := range objNumsMap {
		nums = append(nums, num)
	}
	sort.Ints(nums)

	// 预先加载所有对象，跳过 XRef / ObjStm 内部元对象
	resolved := make(map[int]Object, len(nums))
	maxNum := 0
	for _, num := range nums {
		obj, err := d.GetObject(num)
		if err != nil || obj == nil {
			continue
		}
		if _, isNull := obj.(Null); isNull {
			continue
		}
		if stm, ok := obj.(*Stream); ok {
			t, _ := stm.Dict["Type"].(Name)
			if t == "ObjStm" || t == "XRef" {
				continue
			}
		}
		resolved[num] = obj
		if num > maxNum {
			maxNum = num
		}
	}

	var buf bytes.Buffer
	// 标准 PDF 1.5 文件头 + 二进制注释行
	buf.WriteString("%PDF-1.5\n%\xE2\xE3\xCF\xD3\n")

	offsets := make(map[int]int, len(resolved))
	for num := 1; num <= maxNum; num++ {
		obj, ok := resolved[num]
		if !ok {
			continue
		}
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n", num)
		obj.Serialize(&buf)
		buf.WriteString("\nendobj\n")
	}

	// 写入标准传统 XRef 表（全阅读器 100% 兼容）
	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", maxNum+1)
	buf.WriteString("0000000000 65535 f \n")
	for num := 1; num <= maxNum; num++ {
		if off, ok := offsets[num]; ok {
			fmt.Fprintf(&buf, "%010d 00000 n \n", off)
		} else {
			buf.WriteString("0000000000 65535 f \n")
		}
	}

	// 写入 Trailer
	trailer := make(Dict)
	if root, ok := d.Trailer["Root"]; ok {
		trailer["Root"] = root
	}
	if info, ok := d.Trailer["Info"]; ok {
		trailer["Info"] = info
	}
	trailer["Size"] = Int(maxNum + 1)

	buf.WriteString("trailer\n")
	trailer.Serialize(&buf)
	fmt.Fprintf(&buf, "\nstartxref\n%d\n%%%%EOF\n", xrefOffset)

	return buf.Bytes(), nil
}
