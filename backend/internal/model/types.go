package model

import "time"

// BlockStyle 描述文本块的视觉排版属性（与前端 TypeScript 契约一致）
type BlockStyle struct {
	FontSize           float64 `json:"font_size"`
	TranslatedFontSize float64 `json:"translated_font_size"`
	FontFamily         string  `json:"font_family,omitempty"` // serif / sans / mono
	FontWeight         string  `json:"font_weight"`           // normal / bold
	FontStyle          string  `json:"font_style"`            // normal / italic
	Color              string  `json:"color"`                 // "#RRGGBB"
	BgColor            string  `json:"bg_color"`              // 默认 "transparent"（使用 clean_bg.pdf 无损底图）
	Align              string  `json:"align"`                 // left / center / right / justify
	LineHeight         float64 `json:"line_height"`
}

// TranslationBlock 表示文档中的单个可编辑/可翻译文本块
type TranslationBlock struct {
	ID             string     `json:"id"`
	DocID          string     `json:"doc_id,omitempty"`
	PageIndex      int        `json:"page_index"`
	ReadingOrder   int        `json:"reading_order"`
	BlockType      string     `json:"block_type"` // heading / paragraph / list_item / caption / header_footer
	BBox           [4]float64 `json:"bbox"`       // [x0, y0, x1, y1]
	OriginalText   string     `json:"original_text"`
	TranslatedText string     `json:"translated_text"`
	Style          BlockStyle `json:"style"`
	Translatable   bool       `json:"translatable"`
	Status         string     `json:"status"` // skipped / pending / translating / translated / edited / reviewed / failed
	IsEdited       bool       `json:"is_edited"`
	UpdatedAt      time.Time  `json:"updated_at,omitempty"`
}

// PageIR 表示单页的文档中间表示
type PageIR struct {
	PageIndex int                `json:"page_index"`
	Width     float64            `json:"width"`
	Height    float64            `json:"height"`
	Blocks    []TranslationBlock `json:"blocks"`
}

// DocumentSummary 表示文档库列表中的文档摘要信息
type DocumentSummary struct {
	ID              string    `json:"id"`
	Filename        string    `json:"filename"`
	DocType         string    `json:"doc_type"`
	SourceLang      string    `json:"source_lang"`
	TargetLang      string    `json:"target_lang"`
	PageCount       int       `json:"page_count"`
	IsScanned       bool      `json:"is_scanned"`
	IsEncrypted     bool      `json:"is_encrypted"`
	Status          string    `json:"status"`
	ErrorMessage    string    `json:"error_message,omitempty"`
	TotalBlocks     int       `json:"total_blocks"`
	CompletedBlocks int       `json:"completed_blocks"`
	HasThumbnail    bool      `json:"has_thumbnail"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// DocumentIR 表示完整的统一文档中间表示
type DocumentIR struct {
	DocumentSummary
	Pages []PageIR `json:"pages"`
}

// DocumentRecord 为内部存储层使用的完整文档记录（含磁盘文件路径）
type DocumentRecord struct {
	DocumentSummary
	FilePath      string
	CleanBgPath   string
	ThumbnailPath string
}

// ProviderConfig 表示 AI 模型服务商配置
type ProviderConfig struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	BaseURL        string    `json:"base_url"`
	APIKey         string    `json:"api_key"`
	ModelName      string    `json:"model_name"`
	MaxConcurrency int       `json:"max_concurrency"`
	Temperature    float64   `json:"temperature"`
	IsDefault      bool      `json:"is_default"`
	CreatedAt      time.Time `json:"created_at"`
}

// Masked 返回对 APIKey 脱敏后的副本（遵循 AC-3.3 安全规范）
func (p ProviderConfig) Masked() ProviderConfig {
	cp := p
	k := cp.APIKey
	if len(k) > 8 {
		cp.APIKey = k[:4] + "****" + k[len(k)-4:]
	} else if len(k) > 0 {
		cp.APIKey = "****"
	}
	return cp
}

// SSEEvent 表示后端推送给前端的实时翻译事件
type SSEEvent struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}
