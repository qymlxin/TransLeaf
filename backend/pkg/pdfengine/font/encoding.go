package font

// winAnsiHigh 将 WinAnsiEncoding (Windows-1252) 0x80-0x9F 区间映射到标准 Unicode
var winAnsiHigh = map[byte]rune{
	0x80: 0x20AC, 0x82: 0x201A, 0x83: 0x0192, 0x84: 0x201E,
	0x85: 0x2026, 0x86: 0x2020, 0x87: 0x2021, 0x88: 0x02C6,
	0x89: 0x2030, 0x8A: 0x0160, 0x8B: 0x2039, 0x8C: 0x0152,
	0x8E: 0x017D, 0x91: 0x2018, 0x92: 0x2019, 0x93: 0x201C,
	0x94: 0x201D, 0x95: 0x2022, 0x96: 0x2013, 0x97: 0x2014,
	0x98: 0x02DC, 0x99: 0x2122, 0x9A: 0x0161, 0x9B: 0x203A,
	0x9C: 0x0153, 0x9E: 0x017E, 0x9F: 0x0178,
}

// glyphNameToUnicode 覆盖 PDF /Differences 中最常见的 Adobe 字形名称
var glyphNameToUnicode = map[string]string{
	"space": " ", "exclam": "!", "quotedbl": "\"", "numbersign": "#",
	"dollar": "$", "percent": "%", "ampersand": "&", "quotesingle": "'",
	"parenleft": "(", "parenright": ")", "asterisk": "*", "plus": "+",
	"comma": ",", "hyphen": "-", "period": ".", "slash": "/",
	"colon": ":", "semicolon": ";", "less": "<", "equal": "=",
	"greater": ">", "question": "?", "at": "@", "bracketleft": "[",
	"backslash": "\\", "bracketright": "]", "asciicircum": "^", "underscore": "_",
	"grave": "`", "braceleft": "{", "bar": "|", "braceright": "}",
	"asciitilde": "~", "endash": "–", "emdash": "—", "bullet": "•",
	"ellipsis": "…", "quoteleft": "‘", "quoteright": "’",
	"quotedblleft": "“", "quotedblright": "”", "fi": "fi", "fl": "fl",
	"ff": "ff", "ffi": "ffi", "ffl": "ffl", "minus": "-",
	"copyright": "©", "registered": "®", "trademark": "™", "degree": "°",
	"plusminus": "±", "multiply": "×", "divide": "÷", "Euro": "€",
}

// DecodeWinAnsi 将单字节 WinAnsi 码位转换为 Unicode 字符串
func DecodeWinAnsi(b byte) string {
	if b >= 0x20 && b <= 0x7e {
		return string(rune(b))
	}
	if r, ok := winAnsiHigh[b]; ok {
		return string(r)
	}
	if b >= 0xa0 {
		return string(rune(b))
	}
	return ""
}
