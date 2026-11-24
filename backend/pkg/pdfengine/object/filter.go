package object

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"fmt"
	"io"
)

// DecodeStream 解码 PDF 流数据，支持单个滤镜或滤镜链
func DecodeStream(s *Stream, resolve func(Object) Object) ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	filterObj := resolve(s.Dict["Filter"])
	if filterObj == nil {
		out := make([]byte, len(s.RawData))
		copy(out, s.RawData)
		return out, nil
	}

	var filters []Name
	switch f := filterObj.(type) {
	case Name:
		filters = []Name{f}
	case Array:
		for _, item := range f {
			if n, ok := resolve(item).(Name); ok {
				filters = append(filters, n)
			}
		}
	}

	var parmsList []Dict
	parmsObj := resolve(s.Dict["DecodeParms"])
	switch p := parmsObj.(type) {
	case Dict:
		parmsList = []Dict{p}
	case Array:
		for _, item := range p {
			if d, ok := resolve(item).(Dict); ok {
				parmsList = append(parmsList, d)
			} else {
				parmsList = append(parmsList, nil)
			}
		}
	}

	data := s.RawData
	var err error
	for i, flt := range filters {
		var parms Dict
		if i < len(parmsList) {
			parms = parmsList[i]
		}
		switch flt {
		case "FlateDecode", "Fl":
			data, err = decodeFlate(data, parms, resolve)
		case "ASCIIHexDecode", "AHx":
			data, err = decodeASCIIHex(data)
		case "ASCII85Decode", "A85":
			data, err = decodeASCII85(data)
		default:
			return nil, fmt.Errorf("unsupported PDF stream filter: %s", flt)
		}
		if err != nil {
			return nil, fmt.Errorf("decode filter %s failed: %w", flt, err)
		}
	}
	return data, nil
}

// EncodeFlate 使用标准 zlib (FlateDecode) 压缩流数据
func EncodeFlate(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeFlate(data []byte, parms Dict, resolve func(Object) Object) ([]byte, error) {
	// 去除前导空白，提高对非标准 PDF 的容错率
	trimmed := bytes.TrimLeft(data, "\r\n\t ")
	r, err := zlib.NewReader(bytes.NewReader(trimmed))
	if err != nil {
		return nil, err
	}
	defer r.Close()

	raw, err := io.ReadAll(r)
	if err != nil && len(raw) == 0 {
		return nil, err
	}
	if parms == nil {
		return raw, nil
	}

	predictor := 1
	if pObj := resolve(parms["Predictor"]); pObj != nil {
		if pv, ok := AsInt(pObj); ok {
			predictor = int(pv)
		}
	}
	if predictor <= 1 {
		return raw, nil
	}

	columns := 1
	if cObj := resolve(parms["Columns"]); cObj != nil {
		if cv, ok := AsInt(cObj); ok && cv > 0 {
			columns = int(cv)
		}
	}
	colors := 1
	if coObj := resolve(parms["Colors"]); coObj != nil {
		if cov, ok := AsInt(coObj); ok && cov > 0 {
			colors = int(cov)
		}
	}
	bpc := 8
	if bObj := resolve(parms["BitsPerComponent"]); bObj != nil {
		if bv, ok := AsInt(bObj); ok && bv > 0 {
			bpc = int(bv)
		}
	}

	if predictor >= 10 && predictor <= 15 {
		return undoPNGPredictor(raw, columns, colors, bpc)
	}
	if predictor == 2 {
		return undoTIFFPredictor(raw, columns, colors, bpc)
	}
	return raw, nil
}

// undoPNGPredictor 还原 PDF FlateDecode 中的 PNG 行预测编码（XRef Stream 常用）
func undoPNGPredictor(data []byte, columns, colors, bpc int) ([]byte, error) {
	bpp := (colors*bpc + 7) / 8
	rowBytes := (columns*colors*bpc + 7) / 8
	stride := rowBytes + 1
	if rowBytes <= 0 || len(data) < stride {
		return data, nil
	}
	rows := len(data) / stride
	out := make([]byte, rows*rowBytes)
	prevRow := make([]byte, rowBytes)

	for r := 0; r < rows; r++ {
		filterType := data[r*stride]
		src := data[r*stride+1 : r*stride+1+rowBytes]
		dst := out[r*rowBytes : (r+1)*rowBytes]

		switch filterType {
		case 0: // None
			copy(dst, src)
		case 1: // Sub
			for i := 0; i < rowBytes; i++ {
				var left byte
				if i >= bpp {
					left = dst[i-bpp]
				}
				dst[i] = src[i] + left
			}
		case 2: // Up
			for i := 0; i < rowBytes; i++ {
				dst[i] = src[i] + prevRow[i]
			}
		case 3: // Average
			for i := 0; i < rowBytes; i++ {
				var left int
				if i >= bpp {
					left = int(dst[i-bpp])
				}
				up := int(prevRow[i])
				dst[i] = byte(int(src[i]) + (left+up)/2)
			}
		case 4: // Paeth
			for i := 0; i < rowBytes; i++ {
				var a, c int
				if i >= bpp {
					a = int(dst[i-bpp])
					c = int(prevRow[i-bpp])
				}
				b := int(prevRow[i])
				dst[i] = byte(int(src[i]) + paethPredictor(a, b, c))
			}
		default:
			copy(dst, src)
		}
		copy(prevRow, dst)
	}
	return out, nil
}

func paethPredictor(a, b, c int) int {
	p := a + b - c
	pa := absInt(p - a)
	pb := absInt(p - b)
	pc := absInt(p - c)
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func undoTIFFPredictor(data []byte, columns, colors, bpc int) ([]byte, error) {
	if bpc != 8 {
		return data, nil
	}
	rowBytes := columns * colors
	out := make([]byte, len(data))
	copy(out, data)
	for off := 0; off+rowBytes <= len(out); off += rowBytes {
		for i := colors; i < rowBytes; i++ {
			out[off+i] += out[off+i-colors]
		}
	}
	return out, nil
}

func decodeASCIIHex(data []byte) ([]byte, error) {
	var out bytes.Buffer
	high := -1
	for _, c := range data {
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
			out.WriteByte(byte(high<<4 | v))
			high = -1
		}
	}
	if high >= 0 {
		out.WriteByte(byte(high << 4))
	}
	return out.Bytes(), nil
}

func decodeASCII85(data []byte) ([]byte, error) {
	cleaned := bytes.TrimSpace(data)
	if idx := bytes.Index(cleaned, []byte("~>")); idx >= 0 {
		cleaned = cleaned[:idx]
	}
	dec := ascii85.NewDecoder(bytes.NewReader(cleaned))
	return io.ReadAll(dec)
}
