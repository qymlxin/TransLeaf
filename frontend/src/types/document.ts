export type BlockStatus =
  | 'skipped'
  | 'pending'
  | 'translating'
  | 'translated'
  | 'edited'
  | 'reviewed'
  | 'failed'

export type DocumentStatus =
  | 'uploaded'
  | 'parsing'
  | 'parsed'
  | 'translating'
  | 'paused'
  | 'completed'
  | 'failed'

export type ViewMode = 'dual' | 'translated' | 'list'
export type ExportMode = 'translated' | 'bilingual_interleaved' | 'bilingual_side_by_side'

export interface BlockStyle {
  font_size: number
  translated_font_size: number
  font_weight: 'normal' | 'bold'
  font_style: 'normal' | 'italic'
  color: string
  bg_color: string
  align: 'left' | 'center' | 'right' | 'justify'
  line_height: number
}

export interface TranslationBlock {
  id: string
  page_index: number
  reading_order: number
  block_type: string
  bbox: [number, number, number, number] // [x0, y0, x1, y1] in PDF points
  original_text: string
  translated_text: string
  style: BlockStyle
  translatable: boolean
  status: BlockStatus
  is_edited: boolean
}

export interface PageIR {
  page_index: number
  width: number
  height: number
  blocks: TranslationBlock[]
}

export interface DocumentSummary {
  id: string
  filename: string
  doc_type: string
  source_lang: string
  target_lang: string
  page_count: number
  is_scanned: boolean
  is_encrypted: boolean
  status: DocumentStatus
  error_message?: string
  total_blocks: number
  completed_blocks: number
  has_thumbnail: boolean
  created_at: string
  updated_at: string
}

export interface DocumentIR extends DocumentSummary {
  pages: PageIR[]
}

export interface ProviderConfig {
  id: string
  name: string
  base_url: string
  api_key: string
  model_name: string
  max_concurrency: number
  temperature: number
  is_default: boolean
  created_at?: string
}
