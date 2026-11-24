package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"translation-assistant/internal/llm"
	"translation-assistant/internal/model"
	"translation-assistant/internal/store"
)

// EventBroker 管理按 docID 分组的 SSE 订阅与实时事件广播
type EventBroker struct {
	mu   sync.RWMutex
	subs map[string]map[chan model.SSEEvent]struct{}
}

func NewEventBroker() *EventBroker {
	return &EventBroker{
		subs: make(map[string]map[chan model.SSEEvent]struct{}),
	}
}

func (b *EventBroker) Subscribe(docID string) chan model.SSEEvent {
	ch := make(chan model.SSEEvent, 64)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[docID] == nil {
		b.subs[docID] = make(map[chan model.SSEEvent]struct{})
	}
	b.subs[docID][ch] = struct{}{}
	return ch
}

func (b *EventBroker) Unsubscribe(docID string, ch chan model.SSEEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if m, ok := b.subs[docID]; ok {
		delete(m, ch)
		if len(m) == 0 {
			delete(b.subs, docID)
		}
	}
	close(ch)
}

func (b *EventBroker) Publish(docID string, ev model.SSEEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs[docID] {
		select {
		case ch <- ev:
		default:
			// 防止慢客户端阻塞后端翻译调度
		}
	}
}

// TranslateOptions 描述启动翻译任务的配置参数
type TranslateOptions struct {
	ProviderID  string `json:"provider_id"`
	SourceLang  string `json:"source_lang"`
	TargetLang  string `json:"target_lang"`
	StylePreset string `json:"style_preset"`
	Retranslate bool   `json:"retranslate"` // true 时重译所有块，false 时断点续译
}

// TranslatorService 负责并发批处理翻译调度、暂停/续译与 SSE 推送
type TranslatorService struct {
	store  *store.Store
	llm    *llm.Client
	broker *EventBroker

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func NewTranslatorService(st *store.Store, llmClient *llm.Client, broker *EventBroker) *TranslatorService {
	return &TranslatorService{
		store:   st,
		llm:     llmClient,
		broker:  broker,
		running: make(map[string]context.CancelFunc),
	}
}

// Start 启动或断点续译指定文档
func (ts *TranslatorService) Start(docID string, opts TranslateOptions) error {
	ts.mu.Lock()
	if _, exists := ts.running[docID]; exists {
		ts.mu.Unlock()
		return fmt.Errorf("该文档正在翻译中，请勿重复启动")
	}

	var provider *model.ProviderConfig
	var err error
	if opts.ProviderID != "" {
		provider, err = ts.store.GetProvider(opts.ProviderID)
	} else {
		provider, err = ts.store.GetDefaultProvider()
	}
	if err != nil {
		ts.mu.Unlock()
		return fmt.Errorf("请先在“模型设置”中配置至少一个可用的 AI 模型服务商")
	}

	ir, err := ts.store.GetDocumentIR(docID)
	if err != nil {
		ts.mu.Unlock()
		return err
	}

	if opts.TargetLang == "" {
		opts.TargetLang = ir.TargetLang
		if opts.TargetLang == "" {
			opts.TargetLang = "zh-CN"
		}
	}
	if opts.SourceLang == "" {
		opts.SourceLang = ir.SourceLang
		if opts.SourceLang == "" {
			opts.SourceLang = "auto"
		}
	}
	if opts.StylePreset == "" {
		opts.StylePreset = "general"
	}

	ctx, cancel := context.WithCancel(context.Background())
	ts.running[docID] = cancel
	ts.mu.Unlock()

	_ = ts.store.UpdateDocumentStatus(docID, "translating", "")

	go ts.runDocumentJob(ctx, docID, ir, provider, opts)
	return nil
}

// Pause 优雅暂停正在运行的翻译任务
func (ts *TranslatorService) Pause(docID string) bool {
	ts.mu.Lock()
	cancel, exists := ts.running[docID]
	ts.mu.Unlock()
	if !exists {
		return false
	}
	cancel()
	return true
}

func (ts *TranslatorService) runDocumentJob(
	ctx context.Context,
	docID string,
	ir *model.DocumentIR,
	provider *model.ProviderConfig,
	opts TranslateOptions,
) {
	defer func() {
		ts.mu.Lock()
		delete(ts.running, docID)
		ts.mu.Unlock()
	}()

	// 按页构建动态批次，并统计总可译块与已完成块
	type batchJob struct {
		pageIndex   int
		blocks      []model.TranslationBlock
		prevContext string
	}

	var batches []batchJob
	totalBlocks := 0
	var completedCount int64
	var failedCount int64
	var prevSummary string

	for _, page := range ir.Pages {
		var pending []model.TranslationBlock
		for _, b := range page.Blocks {
			if !b.Translatable {
				continue
			}
			totalBlocks++
			if !opts.Retranslate && (b.Status == "translated" || b.Status == "edited" || b.Status == "reviewed") {
				completedCount++
				prevSummary = b.OriginalText
				continue
			}
			pending = append(pending, b)
		}

		// 按 1800 字符或最多 12 块动态切分批次
		var cur []model.TranslationBlock
		curChars := 0
		for _, b := range pending {
			bLen := len([]rune(b.OriginalText))
			if len(cur) > 0 && (len(cur) >= 12 || curChars+bLen > 1800) {
				batches = append(batches, batchJob{
					pageIndex:   page.PageIndex,
					blocks:      cur,
					prevContext: prevSummary,
				})
				prevSummary = cur[len(cur)-1].OriginalText
				cur = nil
				curChars = 0
			}
			cur = append(cur, b)
			curChars += bLen
		}
		if len(cur) > 0 {
			batches = append(batches, batchJob{
				pageIndex:   page.PageIndex,
				blocks:      cur,
				prevContext: prevSummary,
			})
			prevSummary = cur[len(cur)-1].OriginalText
		}
	}

	ts.broker.Publish(docID, model.SSEEvent{
		Event: "job_started",
		Data: map[string]any{
			"doc_id":           docID,
			"total_blocks":     totalBlocks,
			"completed_blocks": completedCount,
		},
	})

	concurrency := provider.MaxConcurrency
	if concurrency <= 0 {
		concurrency = 3
	}
	if concurrency > 10 {
		concurrency = 10
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, bj := range batches {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(job batchJob) {
			defer wg.Done()
			defer func() { <-sem }()

			if ctx.Err() != nil {
				return
			}

			items := make([]llm.BatchItem, len(job.blocks))
			for i, b := range job.blocks {
				items[i] = llm.BatchItem{ID: b.ID, Text: b.OriginalText}
			}

			resMap, err := ts.llm.TranslateBatch(
				ctx, provider, opts.SourceLang, opts.TargetLang, opts.StylePreset, job.prevContext, items,
			)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				for _, b := range job.blocks {
					_ = ts.store.UpdateBlockTranslation(docID, b.ID, b.TranslatedText, "failed")
					atomic.AddInt64(&failedCount, 1)
					ts.broker.Publish(docID, model.SSEEvent{
						Event: "block_failed",
						Data: map[string]any{
							"block_id":   b.ID,
							"page_index": job.pageIndex,
							"error":      err.Error(),
						},
					})
				}
				return
			}

			for _, b := range job.blocks {
				translated, ok := resMap[b.ID]
				if !ok || strings.TrimSpace(translated) == "" {
					// 缺失块自动单块降级补翻
					single, sErr := ts.llm.RetranslateBlock(ctx, provider, b.OriginalText, "", opts.TargetLang, "准确流畅翻译此段内容")
					if sErr != nil {
						_ = ts.store.UpdateBlockTranslation(docID, b.ID, b.TranslatedText, "failed")
						atomic.AddInt64(&failedCount, 1)
						ts.broker.Publish(docID, model.SSEEvent{
							Event: "block_failed",
							Data: map[string]any{
								"block_id":   b.ID,
								"page_index": job.pageIndex,
								"error":      sErr.Error(),
							},
						})
						continue
					}
					translated = single
				}

				_ = ts.store.UpdateBlockTranslation(docID, b.ID, translated, "translated")
				done := atomic.AddInt64(&completedCount, 1)
				ts.broker.Publish(docID, model.SSEEvent{
					Event: "block_translated",
					Data: map[string]any{
						"block_id":         b.ID,
						"page_index":       job.pageIndex,
						"translated_text":  translated,
						"status":           "translated",
						"completed_blocks": done,
						"total_blocks":     totalBlocks,
					},
				})
			}
		}(bj)
	}

	wg.Wait()

	finalStatus := "completed"
	if ctx.Err() != nil {
		finalStatus = "paused"
	}
	_ = ts.store.UpdateDocumentStatus(docID, finalStatus, "")

	ts.broker.Publish(docID, model.SSEEvent{
		Event: "job_completed",
		Data: map[string]any{
			"doc_id":           docID,
			"status":           finalStatus,
			"completed_blocks": atomic.LoadInt64(&completedCount),
			"failed_blocks":    atomic.LoadInt64(&failedCount),
			"total_blocks":     totalBlocks,
		},
	})
}
