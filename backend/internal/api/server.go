package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"translation-assistant/internal/adapter"
	"translation-assistant/internal/config"
	"translation-assistant/internal/llm"
	"translation-assistant/internal/model"
	"translation-assistant/internal/service"
	"translation-assistant/internal/store"
)

// Server 聚合全部后端依赖并暴露 Chi HTTP 路由
type Server struct {
	cfg        *config.Config
	store      *store.Store
	adapter    adapter.DocumentAdapter
	llmClient  *llm.Client
	broker     *service.EventBroker
	translator *service.TranslatorService
}

func NewServer(cfg *config.Config, st *store.Store) *Server {
	llmClient := llm.NewClient()
	broker := service.NewEventBroker()
	return &Server{
		cfg:        cfg,
		store:      st,
		adapter:    adapter.NewPDFAdapter(),
		llmClient:  llmClient,
		broker:     broker,
		translator: service.NewTranslatorService(st, llmClient, broker),
	}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.Logger)
	r.Use(corsMiddleware)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)

		// 文档管理与编辑
		r.Post("/documents/upload", s.handleUploadDocument)
		r.Get("/documents", s.handleListDocuments)
		r.Get("/documents/{id}", s.handleGetDocumentIR)
		r.Delete("/documents/{id}", s.handleDeleteDocument)
		r.Get("/documents/{id}/file", s.handleGetOriginalFile)
		r.Get("/documents/{id}/clean-bg", s.handleGetCleanBgFile)
		r.Get("/documents/{id}/thumbnail", s.handleGetThumbnail)
		r.Put("/documents/{id}/thumbnail", s.handlePutThumbnail)
		r.Post("/documents/{id}/reparse", s.handleReparseDocument)

		// 块级编辑与 AI 局部润色
		r.Patch("/documents/{id}/blocks/{blockId}", s.handleUpdateBlock)
		r.Post("/documents/{id}/blocks/{blockId}/retranslate", s.handleRetranslateBlock)

		// 并发流式翻译与 SSE
		r.Post("/documents/{id}/translate", s.handleStartTranslate)
		r.Post("/documents/{id}/translate/pause", s.handlePauseTranslate)
		r.Get("/documents/{id}/translate/events", s.handleTranslateEvents)

		// 高保真 PDF 导出
		r.Post("/documents/{id}/export", s.handleExportDocument)

		// AI 模型服务商配置
		r.Get("/providers", s.handleListProviders)
		r.Post("/providers", s.handleCreateProvider)
		r.Put("/providers/{id}", s.handleUpdateProvider)
		r.Delete("/providers/{id}", s.handleDeleteProvider)
		r.Post("/providers/test", s.handleTestProvider)
	})

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"font_ready": s.cfg.FontPath != "",
	})
}

func (s *Server) handleUploadDocument(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadSize+1024*1024)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "文件过大或请求格式无效（最大支持 100MB）")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "请选择要上传的 PDF 文件")
		return
	}
	defer file.Close()

	origName := filepath.Base(header.Filename)
	if !strings.HasSuffix(strings.ToLower(origName), ".pdf") {
		writeError(w, http.StatusBadRequest, "当前首期仅支持上传 .pdf 格式文档")
		return
	}

	raw, err := io.ReadAll(io.LimitReader(file, s.cfg.MaxUploadSize+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取上传文件失败")
		return
	}
	if int64(len(raw)) > s.cfg.MaxUploadSize {
		writeError(w, http.StatusBadRequest, "文件大小超过 100MB 限制")
		return
	}
	if !bytes.Contains(raw[:minInt(len(raw), 1024)], []byte("%PDF-")) {
		writeError(w, http.StatusBadRequest, "文件内容不是有效的 PDF 文档（缺少 %PDF- 文件头）")
		return
	}

	docID := "doc_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	origPath := filepath.Join(s.cfg.UploadsDir, docID+".pdf")
	cleanBgPath := filepath.Join(s.cfg.CleanBgDir, docID+"_clean.pdf")
	if err := os.WriteFile(origPath, raw, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "保存 PDF 文件失败")
		return
	}

	rec := &model.DocumentRecord{
		DocumentSummary: model.DocumentSummary{
			ID:         docID,
			Filename:   origName,
			DocType:    "pdf",
			SourceLang: "auto",
			TargetLang: "zh-CN",
			Status:     "parsing",
		},
		FilePath:    origPath,
		CleanBgPath: cleanBgPath,
	}
	if err := s.store.CreateDocument(rec); err != nil {
		_ = os.Remove(origPath)
		writeError(w, http.StatusInternalServerError, "创建文档记录失败")
		return
	}

	// 拷贝值传给异步解析 Goroutine，避免与 HTTP 响应序列化产生数据竞争
	summary := rec.DocumentSummary
	recCopy := *rec
	go s.parseDocumentAsync(&recCopy)

	writeJSON(w, http.StatusCreated, summary)
}

func (s *Server) parseDocumentAsync(rec *model.DocumentRecord) {
	parsed, err := s.adapter.Parse(rec.FilePath)
	if err != nil {
		_ = s.store.UpdateDocumentStatus(rec.ID, "failed", "PDF 解析失败："+err.Error())
		return
	}
	if parsed.IsEncrypted {
		rec.IsEncrypted = true
		rec.Status = "failed"
		rec.ErrorMessage = "检测到密码加密 PDF，请先解除密码保护后再上传"
		_ = s.store.UpdateDocumentMeta(rec)
		return
	}

	rec.PageCount = parsed.PageCount
	rec.IsScanned = parsed.IsScanned

	if err := s.store.SaveParsedPagesAndBlocks(rec.ID, parsed.Pages); err != nil {
		_ = s.store.UpdateDocumentStatus(rec.ID, "failed", "保存解析数据失败："+err.Error())
		return
	}

	// 生成指令级无损擦除后的纯净底图 clean_bg.pdf
	if err := s.adapter.BuildCleanBackground(rec.FilePath, rec.CleanBgPath, parsed.Pages); err != nil {
		// 若底图擦除失败降级回原文件，避免阻塞预览
		_ = copyFile(rec.FilePath, rec.CleanBgPath)
	}

	rec.Status = "parsed"
	if parsed.IsScanned {
		rec.ErrorMessage = "检测到纯图片扫描版 PDF（无可选中文本层），当前 MVP 版本暂未启用 OCR"
	}
	_ = s.store.UpdateDocumentMeta(rec)
}

func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListDocuments()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetDocumentIR(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	ir, err := s.store.GetDocumentIR(docID)
	if err != nil {
		writeError(w, http.StatusNotFound, "文档不存在")
		return
	}
	writeJSON(w, http.StatusOK, ir)
}

func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	s.translator.Pause(docID)

	rec, err := s.store.GetDocumentRecord(docID)
	if err != nil {
		writeError(w, http.StatusNotFound, "文档不存在")
		return
	}
	if err := s.store.DeleteDocument(docID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, p := range []string{rec.FilePath, rec.CleanBgPath, rec.ThumbnailPath} {
		if p != "" {
			_ = os.Remove(p)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleGetOriginalFile(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	rec, err := s.store.GetDocumentRecord(docID)
	if err != nil {
		writeError(w, http.StatusNotFound, "文档不存在")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	http.ServeFile(w, r, rec.FilePath)
}

func (s *Server) handleGetCleanBgFile(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	rec, err := s.store.GetDocumentRecord(docID)
	if err != nil {
		writeError(w, http.StatusNotFound, "文档不存在")
		return
	}
	target := rec.CleanBgPath
	if _, statErr := os.Stat(target); statErr != nil {
		target = rec.FilePath
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, target)
}

func (s *Server) handleGetThumbnail(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	rec, err := s.store.GetDocumentRecord(docID)
	if err != nil || rec.ThumbnailPath == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, rec.ThumbnailPath)
}

func (s *Server) handlePutThumbnail(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	if _, err := s.store.GetDocumentRecord(docID); err != nil {
		writeError(w, http.StatusNotFound, "文档不存在")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil || len(data) == 0 {
		writeError(w, http.StatusBadRequest, "无效缩略图数据")
		return
	}
	thumbPath := filepath.Join(s.cfg.ThumbnailsDir, docID+".png")
	if err := os.WriteFile(thumbPath, data, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "写入缩略图失败")
		return
	}
	_ = s.store.UpdateDocumentThumbnailPath(docID, thumbPath)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleReparseDocument(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	s.translator.Pause(docID)

	rec, err := s.store.GetDocumentRecord(docID)
	if err != nil {
		writeError(w, http.StatusNotFound, "文档不存在")
		return
	}
	if rec.CleanBgPath == "" {
		rec.CleanBgPath = filepath.Join(s.cfg.CleanBgDir, docID+"_clean.pdf")
	}
	rec.Status = "parsing"
	rec.ErrorMessage = ""
	if err := s.store.UpdateDocumentMeta(rec); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	recCopy := *rec
	go s.parseDocumentAsync(&recCopy)

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "parsing"})
}

func (s *Server) handleUpdateBlock(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	blockID := chi.URLParam(r, "blockId")

	blk, err := s.store.GetBlock(docID, blockID)
	if err != nil {
		writeError(w, http.StatusNotFound, "文本块不存在")
		return
	}

	var req struct {
		TranslatedText *string           `json:"translated_text"`
		BBox           *[4]float64       `json:"bbox"`
		Style          *model.BlockStyle `json:"style"`
		Translatable   *bool             `json:"translatable"`
		Status         *string           `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "无效请求体")
		return
	}

	translatableChanged := false
	if req.TranslatedText != nil {
		blk.TranslatedText = *req.TranslatedText
		blk.IsEdited = true
		if req.Status == nil {
			blk.Status = "edited"
		}
	}
	if req.BBox != nil {
		blk.BBox = *req.BBox
		blk.IsEdited = true
	}
	if req.Style != nil {
		blk.Style = *req.Style
		blk.IsEdited = true
	}
	if req.Translatable != nil && *req.Translatable != blk.Translatable {
		blk.Translatable = *req.Translatable
		translatableChanged = true
		if !blk.Translatable {
			blk.Status = "skipped"
		} else if blk.TranslatedText == "" {
			blk.Status = "pending"
		}
	}
	if req.Status != nil {
		blk.Status = *req.Status
	}

	if err := s.store.UpdateBlock(docID, blk); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 若用户切换了该块的“是否参与翻译”状态，同步重建 clean_bg.pdf
	if translatableChanged {
		if rec, err := s.store.GetDocumentRecord(docID); err == nil {
			if ir, err := s.store.GetDocumentIR(docID); err == nil {
				_ = s.adapter.BuildCleanBackground(rec.FilePath, rec.CleanBgPath, ir.Pages)
			}
		}
	}

	writeJSON(w, http.StatusOK, blk)
}

func (s *Server) handleRetranslateBlock(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	blockID := chi.URLParam(r, "blockId")

	blk, err := s.store.GetBlock(docID, blockID)
	if err != nil {
		writeError(w, http.StatusNotFound, "文本块不存在")
		return
	}
	rec, err := s.store.GetDocumentRecord(docID)
	if err != nil {
		writeError(w, http.StatusNotFound, "文档不存在")
		return
	}

	var req struct {
		ProviderID  string `json:"provider_id"`
		Instruction string `json:"instruction"`
		TargetLang  string `json:"target_lang"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	var provider *model.ProviderConfig
	if req.ProviderID != "" {
		provider, err = s.store.GetProvider(req.ProviderID)
	} else {
		provider, err = s.store.GetDefaultProvider()
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "请先在模型设置中配置可用的 AI 服务商")
		return
	}

	targetLang := req.TargetLang
	if targetLang == "" {
		targetLang = rec.TargetLang
	}

	suggestion, err := s.llmClient.RetranslateBlock(
		r.Context(), provider, blk.OriginalText, blk.TranslatedText, targetLang, req.Instruction,
	)
	if err != nil {
		writeError(w, http.StatusBadGateway, "AI 局部润色失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"translated_text": suggestion})
}

func (s *Server) handleStartTranslate(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	var opts service.TranslateOptions
	_ = json.NewDecoder(r.Body).Decode(&opts)

	if err := s.translator.Start(docID, opts); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "translating"})
}

func (s *Server) handlePauseTranslate(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	paused := s.translator.Pause(docID)
	writeJSON(w, http.StatusOK, map[string]bool{"paused": paused})
}

func (s *Server) handleTranslateEvents(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := s.broker.Subscribe(docID)
	defer s.broker.Unsubscribe(docID, ch)

	// 发送初始握手注释
	_, _ = fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			payload, _ := json.Marshal(ev.Data)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, payload)
			flusher.Flush()
		}
	}
}

func (s *Server) handleExportDocument(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "id")
	var req struct {
		Mode string `json:"mode"` // translated | bilingual_interleaved | bilingual_side_by_side
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Mode == "" {
		req.Mode = "translated"
	}

	rec, err := s.store.GetDocumentRecord(docID)
	if err != nil {
		writeError(w, http.StatusNotFound, "文档不存在")
		return
	}
	ir, err := s.store.GetDocumentIR(docID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	pdfBytes, err := s.adapter.Export(ir, rec.FilePath, rec.CleanBgPath, s.cfg.FontPath, req.Mode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "导出 PDF 失败："+err.Error())
		return
	}

	base := strings.TrimSuffix(rec.Filename, filepath.Ext(rec.Filename))
	suffix := "_translated.pdf"
	if strings.HasPrefix(req.Mode, "bilingual") {
		suffix = "_bilingual.pdf"
	}
	dlName := base + suffix

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename*=UTF-8''%s`, url.PathEscape(dlName)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
}

// --- Provider CRUD & 测试连接 ---

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListProviders()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	masked := make([]model.ProviderConfig, len(list))
	for i, p := range list {
		masked[i] = p.Masked()
	}
	writeJSON(w, http.StatusOK, masked)
}

func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	var p model.ProviderConfig
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "无效配置格式")
		return
	}
	if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.ModelName) == "" {
		writeError(w, http.StatusBadRequest, "配置名称和模型名称不能为空")
		return
	}
	p.ID = "prov_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:10]
	if p.MaxConcurrency <= 0 {
		p.MaxConcurrency = 3
	}
	if err := s.store.SaveProvider(&p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p.Masked())
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var p model.ProviderConfig
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "无效配置格式")
		return
	}
	p.ID = id
	if p.MaxConcurrency <= 0 {
		p.MaxConcurrency = 3
	}
	if err := s.store.SaveProvider(&p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	saved, err := s.store.GetProvider(id)
	if err != nil {
		writeJSON(w, http.StatusOK, p.Masked())
		return
	}
	writeJSON(w, http.StatusOK, saved.Masked())
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteProvider(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	var p model.ProviderConfig
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "无效测试参数")
		return
	}
	// 若前端回传的是脱敏 Key 且带了 ID，则从数据库补回真实 Key 进行测试
	if p.ID != "" && (p.APIKey == "" || strings.Contains(p.APIKey, "****")) {
		if existing, err := s.store.GetProvider(p.ID); err == nil {
			p.APIKey = existing.APIKey
		}
	}
	latencyMs, err := s.llmClient.TestConnection(r.Context(), &p)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"latency_ms": latencyMs,
			"error":      err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"latency_ms": latencyMs,
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
