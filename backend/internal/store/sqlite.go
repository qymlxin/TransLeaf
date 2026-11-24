package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"transleaf/internal/model"
)

// Store 封装 SQLite 数据持久化操作
type Store struct {
	db *sql.DB
}

// New打开并自动迁移 SQLite 数据库（纯 Go 零 CGo）
func New(dbPath string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite failed: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite 单连接写避免锁争用

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库连接
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS documents (
		id TEXT PRIMARY KEY,
		filename TEXT NOT NULL,
		file_path TEXT NOT NULL,
		clean_bg_path TEXT NOT NULL DEFAULT '',
		thumbnail_path TEXT NOT NULL DEFAULT '',
		doc_type TEXT NOT NULL DEFAULT 'pdf',
		source_lang TEXT NOT NULL DEFAULT 'auto',
		target_lang TEXT NOT NULL DEFAULT 'zh-CN',
		page_count INTEGER NOT NULL DEFAULT 0,
		is_scanned INTEGER NOT NULL DEFAULT 0,
		is_encrypted INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'uploaded',
		error_message TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS pages (
		id TEXT PRIMARY KEY,
		doc_id TEXT NOT NULL,
		page_index INTEGER NOT NULL,
		width REAL NOT NULL,
		height REAL NOT NULL,
		FOREIGN KEY (doc_id) REFERENCES documents(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_pages_doc ON pages(doc_id, page_index);

	CREATE TABLE IF NOT EXISTS blocks (
		id TEXT NOT NULL,
		doc_id TEXT NOT NULL,
		page_index INTEGER NOT NULL,
		reading_order INTEGER NOT NULL,
		block_type TEXT NOT NULL DEFAULT 'paragraph',
		x0 REAL NOT NULL,
		y0 REAL NOT NULL,
		x1 REAL NOT NULL,
		y1 REAL NOT NULL,
		original_text TEXT NOT NULL,
		translated_text TEXT NOT NULL DEFAULT '',
		style_json TEXT NOT NULL,
		translatable INTEGER NOT NULL DEFAULT 1,
		status TEXT NOT NULL DEFAULT 'pending',
		is_edited INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (doc_id, id),
		FOREIGN KEY (doc_id) REFERENCES documents(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_blocks_doc_page ON blocks(doc_id, page_index, reading_order);

	CREATE TABLE IF NOT EXISTS provider_configs (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		base_url TEXT NOT NULL,
		api_key TEXT NOT NULL,
		model_name TEXT NOT NULL,
		max_concurrency INTEGER NOT NULL DEFAULT 3,
		temperature REAL NOT NULL DEFAULT 0.3,
		is_default INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS translation_jobs (
		id TEXT PRIMARY KEY,
		doc_id TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		style_preset TEXT NOT NULL DEFAULT 'general',
		total_blocks INTEGER NOT NULL DEFAULT 0,
		completed_blocks INTEGER NOT NULL DEFAULT 0,
		failed_blocks INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'running',
		started_at DATETIME NOT NULL,
		finished_at DATETIME,
		FOREIGN KEY (doc_id) REFERENCES documents(id) ON DELETE CASCADE
	);
	`
	_, err := s.db.Exec(schema)
	return err
}

// CreateDocument 创建新文档记录
func (s *Store) CreateDocument(rec *model.DocumentRecord) error {
	now := time.Now().UTC()
	rec.CreatedAt = now
	rec.UpdatedAt = now
	_, err := s.db.Exec(`
		INSERT INTO documents (
			id, filename, file_path, clean_bg_path, thumbnail_path, doc_type,
			source_lang, target_lang, page_count, is_scanned, is_encrypted,
			status, error_message, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Filename, rec.FilePath, rec.CleanBgPath, rec.ThumbnailPath, rec.DocType,
		rec.SourceLang, rec.TargetLang, rec.PageCount, boolToInt(rec.IsScanned), boolToInt(rec.IsEncrypted),
		rec.Status, rec.ErrorMessage, rec.CreatedAt, rec.UpdatedAt,
	)
	return err
}

// UpdateDocumentMeta 更新文档状态、路径或解析元信息
func (s *Store) UpdateDocumentMeta(rec *model.DocumentRecord) error {
	rec.UpdatedAt = time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE documents SET
			clean_bg_path = ?, thumbnail_path = ?, source_lang = ?, target_lang = ?,
			page_count = ?, is_scanned = ?, is_encrypted = ?, status = ?, error_message = ?, updated_at = ?
		WHERE id = ?`,
		rec.CleanBgPath, rec.ThumbnailPath, rec.SourceLang, rec.TargetLang,
		rec.PageCount, boolToInt(rec.IsScanned), boolToInt(rec.IsEncrypted),
		rec.Status, rec.ErrorMessage, rec.UpdatedAt, rec.ID,
	)
	return err
}

// UpdateDocumentStatus 仅更新文档状态与错误信息
func (s *Store) UpdateDocumentStatus(docID, status, errMsg string) error {
	_, err := s.db.Exec(`UPDATE documents SET status = ?, error_message = ?, updated_at = ? WHERE id = ?`,
		status, errMsg, time.Now().UTC(), docID)
	return err
}

// UpdateDocumentThumbnailPath 更新文档缩略图路径
func (s *Store) UpdateDocumentThumbnailPath(docID, path string) error {
	_, err := s.db.Exec(`UPDATE documents SET thumbnail_path = ?, updated_at = ? WHERE id = ?`,
		path, time.Now().UTC(), docID)
	return err
}

// SaveParsedPagesAndBlocks 事务性写入解析出的全部页面与文本块
func (s *Store) SaveParsedPagesAndBlocks(docID string, pages []model.PageIR) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM blocks WHERE doc_id = ?`, docID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM pages WHERE doc_id = ?`, docID); err != nil {
		return err
	}

	now := time.Now().UTC()
	pageStmt, err := tx.Prepare(`INSERT INTO pages (id, doc_id, page_index, width, height) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer pageStmt.Close()

	blockStmt, err := tx.Prepare(`
		INSERT INTO blocks (
			id, doc_id, page_index, reading_order, block_type,
			x0, y0, x1, y1, original_text, translated_text, style_json,
			translatable, status, is_edited, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer blockStmt.Close()

	for _, p := range pages {
		pageID := fmt.Sprintf("%s_p%d", docID, p.PageIndex)
		if _, err := pageStmt.Exec(pageID, docID, p.PageIndex, p.Width, p.Height); err != nil {
			return err
		}
		for _, b := range p.Blocks {
			styleBytes, _ := json.Marshal(b.Style)
			if _, err := blockStmt.Exec(
				b.ID, docID, b.PageIndex, b.ReadingOrder, b.BlockType,
				b.BBox[0], b.BBox[1], b.BBox[2], b.BBox[3],
				b.OriginalText, b.TranslatedText, string(styleBytes),
				boolToInt(b.Translatable), b.Status, boolToInt(b.IsEdited), now,
			); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// ListDocuments 返回所有文档摘要（按更新时间倒序，附带翻译进度统计）
func (s *Store) ListDocuments() ([]model.DocumentSummary, error) {
	rows, err := s.db.Query(`
		SELECT
			d.id, d.filename, d.doc_type, d.source_lang, d.target_lang,
			d.page_count, d.is_scanned, d.is_encrypted, d.status, d.error_message,
			d.thumbnail_path, d.created_at, d.updated_at,
			COALESCE(SUM(CASE WHEN b.translatable = 1 THEN 1 ELSE 0 END), 0) AS total_blocks,
			COALESCE(SUM(CASE WHEN b.translatable = 1 AND b.status IN ('translated','edited','reviewed') THEN 1 ELSE 0 END), 0) AS completed_blocks
		FROM documents d
		LEFT JOIN blocks b ON d.id = b.doc_id
		GROUP BY d.id
		ORDER BY d.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.DocumentSummary
	for rows.Next() {
		var item model.DocumentSummary
		var scanned, encrypted int
		var thumbPath string
		if err := rows.Scan(
			&item.ID, &item.Filename, &item.DocType, &item.SourceLang, &item.TargetLang,
			&item.PageCount, &scanned, &encrypted, &item.Status, &item.ErrorMessage,
			&thumbPath, &item.CreatedAt, &item.UpdatedAt,
			&item.TotalBlocks, &item.CompletedBlocks,
		); err != nil {
			return nil, err
		}
		item.IsScanned = scanned != 0
		item.IsEncrypted = encrypted != 0
		item.HasThumbnail = thumbPath != ""
		list = append(list, item)
	}
	if list == nil {
		list = []model.DocumentSummary{}
	}
	return list, nil
}

// GetDocumentRecord 获取单份文档完整记录（含磁盘路径）
func (s *Store) GetDocumentRecord(docID string) (*model.DocumentRecord, error) {
	var rec model.DocumentRecord
	var scanned, encrypted int
	err := s.db.QueryRow(`
		SELECT
			id, filename, file_path, clean_bg_path, thumbnail_path, doc_type,
			source_lang, target_lang, page_count, is_scanned, is_encrypted,
			status, error_message, created_at, updated_at
		FROM documents WHERE id = ?`, docID).Scan(
		&rec.ID, &rec.Filename, &rec.FilePath, &rec.CleanBgPath, &rec.ThumbnailPath, &rec.DocType,
		&rec.SourceLang, &rec.TargetLang, &rec.PageCount, &scanned, &encrypted,
		&rec.Status, &rec.ErrorMessage, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	rec.IsScanned = scanned != 0
	rec.IsEncrypted = encrypted != 0
	rec.HasThumbnail = rec.ThumbnailPath != ""
	return &rec, nil
}

// GetDocumentIR 读取完整 DocumentIR（含所有页面及文本块）
func (s *Store) GetDocumentIR(docID string) (*model.DocumentIR, error) {
	rec, err := s.GetDocumentRecord(docID)
	if err != nil {
		return nil, err
	}

	pageRows, err := s.db.Query(`SELECT page_index, width, height FROM pages WHERE doc_id = ? ORDER BY page_index ASC`, docID)
	if err != nil {
		return nil, err
	}
	defer pageRows.Close()

	pageMap := make(map[int]*model.PageIR)
	var pages []model.PageIR
	for pageRows.Next() {
		var p model.PageIR
		if err := pageRows.Scan(&p.PageIndex, &p.Width, &p.Height); err != nil {
			return nil, err
		}
		p.Blocks = []model.TranslationBlock{}
		pages = append(pages, p)
	}
	for i := range pages {
		pageMap[pages[i].PageIndex] = &pages[i]
	}

	blockRows, err := s.db.Query(`
		SELECT
			id, page_index, reading_order, block_type,
			x0, y0, x1, y1, original_text, translated_text, style_json,
			translatable, status, is_edited, updated_at
		FROM blocks
		WHERE doc_id = ?
		ORDER BY page_index ASC, reading_order ASC`, docID)
	if err != nil {
		return nil, err
	}
	defer blockRows.Close()

	total, completed := 0, 0
	for blockRows.Next() {
		var b model.TranslationBlock
		var styleJSON string
		var translatable, isEdited int
		if err := blockRows.Scan(
			&b.ID, &b.PageIndex, &b.ReadingOrder, &b.BlockType,
			&b.BBox[0], &b.BBox[1], &b.BBox[2], &b.BBox[3],
			&b.OriginalText, &b.TranslatedText, &styleJSON,
			&translatable, &b.Status, &isEdited, &b.UpdatedAt,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(styleJSON), &b.Style)
		b.Translatable = translatable != 0
		b.IsEdited = isEdited != 0
		if b.Translatable {
			total++
			if b.Status == "translated" || b.Status == "edited" || b.Status == "reviewed" {
				completed++
			}
		}
		if p, ok := pageMap[b.PageIndex]; ok {
			p.Blocks = append(p.Blocks, b)
		}
	}

	rec.TotalBlocks = total
	rec.CompletedBlocks = completed
	if pages == nil {
		pages = []model.PageIR{}
	}
	return &model.DocumentIR{
		DocumentSummary: rec.DocumentSummary,
		Pages:           pages,
	}, nil
}

// GetBlock 获取单个文本块
func (s *Store) GetBlock(docID, blockID string) (*model.TranslationBlock, error) {
	var b model.TranslationBlock
	var styleJSON string
	var translatable, isEdited int
	err := s.db.QueryRow(`
		SELECT
			id, page_index, reading_order, block_type,
			x0, y0, x1, y1, original_text, translated_text, style_json,
			translatable, status, is_edited, updated_at
		FROM blocks WHERE doc_id = ? AND id = ?`, docID, blockID).Scan(
		&b.ID, &b.PageIndex, &b.ReadingOrder, &b.BlockType,
		&b.BBox[0], &b.BBox[1], &b.BBox[2], &b.BBox[3],
		&b.OriginalText, &b.TranslatedText, &styleJSON,
		&translatable, &b.Status, &isEdited, &b.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(styleJSON), &b.Style)
	b.DocID = docID
	b.Translatable = translatable != 0
	b.IsEdited = isEdited != 0
	return &b, nil
}

// UpdateBlock 保存单个文本块的修改
func (s *Store) UpdateBlock(docID string, b *model.TranslationBlock) error {
	b.UpdatedAt = time.Now().UTC()
	styleBytes, _ := json.Marshal(b.Style)
	_, err := s.db.Exec(`
		UPDATE blocks SET
			x0 = ?, y0 = ?, x1 = ?, y1 = ?,
			translated_text = ?, style_json = ?, translatable = ?,
			status = ?, is_edited = ?, updated_at = ?
		WHERE doc_id = ? AND id = ?`,
		b.BBox[0], b.BBox[1], b.BBox[2], b.BBox[3],
		b.TranslatedText, string(styleBytes), boolToInt(b.Translatable),
		b.Status, boolToInt(b.IsEdited), b.UpdatedAt,
		docID, b.ID,
	)
	if err == nil {
		_, _ = s.db.Exec(`UPDATE documents SET updated_at = ? WHERE id = ?`, b.UpdatedAt, docID)
	}
	return err
}

// UpdateBlockTranslation 更新单块 AI 翻译结果
func (s *Store) UpdateBlockTranslation(docID, blockID, translatedText, status string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE blocks SET translated_text = ?, status = ?, updated_at = ?
		WHERE doc_id = ? AND id = ?`,
		translatedText, status, now, docID, blockID,
	)
	return err
}

// DeleteDocument 删除文档及其级联记录
func (s *Store) DeleteDocument(docID string) error {
	_, err := s.db.Exec(`DELETE FROM documents WHERE id = ?`, docID)
	return err
}

// --- ProviderConfig CRUD ---

func (s *Store) ListProviders() ([]model.ProviderConfig, error) {
	rows, err := s.db.Query(`
		SELECT id, name, base_url, api_key, model_name, max_concurrency, temperature, is_default, created_at
		FROM provider_configs ORDER BY is_default DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.ProviderConfig
	for rows.Next() {
		var p model.ProviderConfig
		var isDef int
		if err := rows.Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKey, &p.ModelName, &p.MaxConcurrency, &p.Temperature, &isDef, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.IsDefault = isDef != 0
		list = append(list, p)
	}
	if list == nil {
		list = []model.ProviderConfig{}
	}
	return list, nil
}

func (s *Store) GetProvider(id string) (*model.ProviderConfig, error) {
	var p model.ProviderConfig
	var isDef int
	err := s.db.QueryRow(`
		SELECT id, name, base_url, api_key, model_name, max_concurrency, temperature, is_default, created_at
		FROM provider_configs WHERE id = ?`, id).Scan(
		&p.ID, &p.Name, &p.BaseURL, &p.APIKey, &p.ModelName, &p.MaxConcurrency, &p.Temperature, &isDef, &p.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	p.IsDefault = isDef != 0
	return &p, nil
}

func (s *Store) GetDefaultProvider() (*model.ProviderConfig, error) {
	var p model.ProviderConfig
	var isDef int
	err := s.db.QueryRow(`
		SELECT id, name, base_url, api_key, model_name, max_concurrency, temperature, is_default, created_at
		FROM provider_configs ORDER BY is_default DESC, created_at DESC LIMIT 1`).Scan(
		&p.ID, &p.Name, &p.BaseURL, &p.APIKey, &p.ModelName, &p.MaxConcurrency, &p.Temperature, &isDef, &p.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	p.IsDefault = isDef != 0
	return &p, nil
}

func (s *Store) SaveProvider(p *model.ProviderConfig) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if p.IsDefault {
		if _, err := tx.Exec(`UPDATE provider_configs SET is_default = 0`); err != nil {
			return err
		}
	}

	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	_, err = tx.Exec(`
		INSERT INTO provider_configs (id, name, base_url, api_key, model_name, max_concurrency, temperature, is_default, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			base_url = excluded.base_url,
			api_key = CASE WHEN excluded.api_key = '' OR excluded.api_key LIKE '%****%' THEN provider_configs.api_key ELSE excluded.api_key END,
			model_name = excluded.model_name,
			max_concurrency = excluded.max_concurrency,
			temperature = excluded.temperature,
			is_default = excluded.is_default`,
		p.ID, p.Name, p.BaseURL, p.APIKey, p.ModelName, p.MaxConcurrency, p.Temperature, boolToInt(p.IsDefault), p.CreatedAt,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteProvider(id string) error {
	_, err := s.db.Exec(`DELETE FROM provider_configs WHERE id = ?`, id)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
