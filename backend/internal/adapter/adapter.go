package adapter

import (
	"fmt"
	"os"

	"translation-assistant/internal/model"
	"translation-assistant/pkg/pdfengine"
	"translation-assistant/pkg/pdfengine/layout"
)

// ParseResult 封装文档解析结果，统一适配 PDF 及未来 DOCX/PPTX 等格式
type ParseResult struct {
	PageCount   int
	IsScanned   bool
	IsEncrypted bool
	Pages       []model.PageIR
}

// DocumentAdapter 定义插件化文档解析与导出接口（为 Phase 2/3 多格式扩展预留）
type DocumentAdapter interface {
	DocType() string
	Parse(filePath string) (*ParseResult, error)
	BuildCleanBackground(origPath, cleanBgPath string, pages []model.PageIR) error
	Export(ir *model.DocumentIR, origPath, cleanBgPath, fontPath, mode string) ([]byte, error)
}

// PDFAdapter 基于自研纯 Go pdfengine 实现 PDF 文档解析与高保真导出
type PDFAdapter struct{}

func NewPDFAdapter() *PDFAdapter {
	return &PDFAdapter{}
}

func (a *PDFAdapter) DocType() string { return "pdf" }

func (a *PDFAdapter) Parse(filePath string) (*ParseResult, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	parsed, err := pdfengine.ParsePDF(data)
	if err != nil {
		return nil, err
	}
	if parsed.IsEncrypted {
		return &ParseResult{IsEncrypted: true}, nil
	}

	pages := make([]model.PageIR, 0, len(parsed.Pages))
	for _, p := range parsed.Pages {
		blks := make([]model.TranslationBlock, 0, len(p.Blocks))
		for _, b := range p.Blocks {
			status := "pending"
			if !b.Translatable {
				status = "skipped"
			}
			blks = append(blks, model.TranslationBlock{
				ID:           b.ID,
				PageIndex:    b.PageIndex,
				ReadingOrder: b.ReadingOrder,
				BlockType:    b.BlockType,
				BBox:         b.BBox,
				OriginalText: b.OriginalText,
				Style: model.BlockStyle{
					FontSize:           b.FontSize,
					TranslatedFontSize: b.FontSize,
					FontWeight:         b.FontWeight,
					FontStyle:          b.FontStyle,
					Color:              b.Color,
					BgColor:            "transparent",
					Align:              b.Align,
					LineHeight:         b.LineHeight,
				},
				Translatable: b.Translatable,
				Status:       status,
			})
		}
		pages = append(pages, model.PageIR{
			PageIndex: p.PageIndex,
			Width:     p.Width,
			Height:    p.Height,
			Blocks:    blks,
		})
	}

	return &ParseResult{
		PageCount: parsed.PageCount,
		IsScanned: parsed.IsScanned,
		Pages:     pages,
	}, nil
}

func (a *PDFAdapter) BuildCleanBackground(origPath, cleanBgPath string, pages []model.PageIR) error {
	origData, err := os.ReadFile(origPath)
	if err != nil {
		return err
	}
	redactMap := make(map[int][][4]float64, len(pages))
	for _, p := range pages {
		for _, b := range p.Blocks {
			if b.Translatable {
				redactMap[p.PageIndex] = append(redactMap[p.PageIndex], b.BBox)
			}
		}
	}
	cleanData, err := pdfengine.GenerateCleanBg(origData, redactMap)
	if err != nil {
		return err
	}
	return os.WriteFile(cleanBgPath, cleanData, 0o644)
}

func (a *PDFAdapter) Export(ir *model.DocumentIR, origPath, cleanBgPath, fontPath, mode string) ([]byte, error) {
	if fontPath == "" {
		return nil, fmt.Errorf("未找到可用的中文字体文件，请将 NotoSansSC-Regular.ttf 放置于 backend/assets/fonts/ 目录")
	}

	cleanBgData, err := os.ReadFile(cleanBgPath)
	if err != nil {
		// 若 clean_bg 缺失则即时重新生成
		if buildErr := a.BuildCleanBackground(origPath, cleanBgPath, ir.Pages); buildErr != nil {
			return nil, buildErr
		}
		cleanBgData, err = os.ReadFile(cleanBgPath)
		if err != nil {
			return nil, err
		}
	}

	blocksByPage := make(map[int][]layout.RenderBlock, len(ir.Pages))
	for _, p := range ir.Pages {
		for _, b := range p.Blocks {
			if !b.Translatable {
				continue
			}
			txt := b.TranslatedText
			if txt == "" {
				txt = b.OriginalText
			}
			fs := b.Style.TranslatedFontSize
			if fs <= 0 {
				fs = b.Style.FontSize
			}
			blocksByPage[p.PageIndex] = append(blocksByPage[p.PageIndex], layout.RenderBlock{
				BBox:       b.BBox,
				Text:       txt,
				FontSize:   fs,
				LineHeight: b.Style.LineHeight,
				Align:      b.Style.Align,
				Color:      b.Style.Color,
			})
		}
	}

	transData, err := pdfengine.ExportTranslated(cleanBgData, fontPath, blocksByPage)
	if err != nil {
		return nil, err
	}

	switch mode {
	case "", "translated":
		return transData, nil
	case "bilingual_interleaved":
		origData, err := os.ReadFile(origPath)
		if err != nil {
			return nil, err
		}
		return pdfengine.ExportBilingualInterleaved(origData, transData)
	case "bilingual_side_by_side":
		origData, err := os.ReadFile(origPath)
		if err != nil {
			return nil, err
		}
		return pdfengine.ExportBilingualSideBySide(origData, transData)
	default:
		return nil, fmt.Errorf("unsupported export mode: %s", mode)
	}
}
