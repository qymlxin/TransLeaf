package content

import (
	"fmt"
	"math"
)

// Matrix 表示 PDF 2D 仿射变换矩阵 [a b c d e f]
type Matrix [6]float64

// IdentityMatrix 返回单位矩阵
func IdentityMatrix() Matrix {
	return Matrix{1, 0, 0, 1, 0, 0}
}

// Multiply 执行矩阵乘法 m1 * m2（按照 PDF 规范行向量左乘规则）
func (m1 Matrix) Multiply(m2 Matrix) Matrix {
	return Matrix{
		m1[0]*m2[0] + m1[1]*m2[2],
		m1[0]*m2[1] + m1[1]*m2[3],
		m1[2]*m2[0] + m1[3]*m2[2],
		m1[2]*m2[1] + m1[3]*m2[3],
		m1[4]*m2[0] + m1[5]*m2[2] + m2[4],
		m1[4]*m2[1] + m1[5]*m2[3] + m2[5],
	}
}

// TransformPoint 将点 (x, y) 经矩阵变换到目标坐标系
func (m Matrix) TransformPoint(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// VerticalScale 返回矩阵在 Y 轴上的缩放系数（用于还原真实字号）
func (m Matrix) VerticalScale() float64 {
	return math.Hypot(m[2], m[3])
}

// GraphicsState 维护 PDF 解释器当前图形与文本状态
type GraphicsState struct {
	CTM        Matrix
	Tm         Matrix
	Tlm        Matrix
	InText     bool
	FontName   string
	FontSize   float64
	CharSpace  float64
	WordSpace  float64
	HorizScale float64 // 百分比，默认 100
	Leading    float64
	TextRise   float64
	FillColor  string // 十六进制颜色 "#RRGGBB"
}

// NewGraphicsState 初始化默认图形状态
func NewGraphicsState() GraphicsState {
	return GraphicsState{
		CTM:        IdentityMatrix(),
		Tm:         IdentityMatrix(),
		Tlm:        IdentityMatrix(),
		HorizScale: 100,
		FillColor:  "#000000",
	}
}

// RGBToHex 将 0..1 范围的 RGB 分量转换为十六进制颜色字符串
func RGBToHex(r, g, b float64) string {
	clamp := func(v float64) int {
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 255
		}
		return int(math.Round(v * 255))
	}
	return fmt.Sprintf("#%02X%02X%02X", clamp(r), clamp(g), clamp(b))
}

// CMYKToHex 将 0..1 范围的 CMYK 分量近似转换为十六进制 RGB
func CMYKToHex(c, m, y, k float64) string {
	r := (1 - c) * (1 - k)
	g := (1 - m) * (1 - k)
	b := (1 - y) * (1 - k)
	return RGBToHex(r, g, b)
}
