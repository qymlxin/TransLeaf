package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"transleaf/internal/llm"
	"transleaf/internal/model"
	"transleaf/internal/store"
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
	PageRange   string `json:"page_range"`  // 1-based 页码范围，如 "1"、"1-5"、"1,3,5-8"，空或 "all" 表示全部页
	Retranslate bool   `json:"retranslate"` // true 时重译所有块，false 时断点续译
}

// ParsePageRange 将 1-based 页码范围表达式解析为 0-based 页面索引集合（空或 "all" 返回 nil 表示全部页）
func ParsePageRange(spec string, pageCount int) (map[int]bool, error) {
	s := strings.TrimSpace(spec)
	if s == "" || strings.EqualFold(s, "all") {
		return nil, nil
	}
	if pageCount <= 0 {
		return nil, fmt.Errorf("文档无可翻译页面")
	}
	s = strings.NewReplacer("，", ",", "～", "-", "—", "-", "–", "-").Replace(s)
	parts := strings.Split(s, ",")
	selected := make(map[int]bool)

	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if strings.Contains(p, "-") {
			bounds := strings.Split(p, "-")
			if len(bounds) != 2 {
				return nil, fmt.Errorf("无效页码区间 %q，请使用类似 1-5 的格式", part)
			}
			start, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err1 != nil || err2 != nil || start < 1 || end < start || end > pageCount {
				return nil, fmt.Errorf("页码区间 %q 超出范围（文档共 %d 页）", part, pageCount)
			}
			for pg := start; pg <= end; pg++ {
				selected[pg-1] = true
			}
		} else {
			pg, err := strconv.Atoi(p)
			if err != nil || pg < 1 || pg > pageCount {
				return nil, fmt.Errorf("页码 %q 无效或超出范围（文档共 %d 页）", part, pageCount)
			}
			selected[pg-1] = true
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("请输入有效的页码范围（例如 1、1-5 或 1,3,5-8）")
	}
	return selected, nil
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

	selectedPages, err := ParsePageRange(opts.PageRange, len(ir.Pages))
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

	go ts.runDocumentJob(ctx, docID, ir, provider, opts, selectedPages)
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
	selectedPages map[int]bool,
) {
	defer func() {
		ts.mu.Lock()
		delete(ts.running, docID)
		ts.mu.Unlock()
	}()

	type batchJob struct {
		pageIndex   int
		blocks      []model.TranslationBlock
		prevContext string
	}
	type preCachedItem struct {
		block      model.TranslationBlock
		translated string
	}

	// 文档内重复原文缓存与随从映射：
	// 相同文本（如每页重复页眉、页脚、表头、参数名）仅请求一次 LLM，其余直接复用，大幅省时省 Token
	dedupCache := make(map[string]string)
	if !opts.Retranslate {
		for _, page := range ir.Pages {
			for _, b := range page.Blocks {
				if !b.Translatable || strings.TrimSpace(b.TranslatedText) == "" {
					continue
				}
				if b.Status == "translated" || b.Status == "edited" || b.Status == "reviewed" {
					norm := strings.TrimSpace(b.OriginalText)
					if norm != "" {
						dedupCache[norm] = b.TranslatedText
					}
				}
			}
		}
	}

	var batches []batchJob
	var preCached []preCachedItem
	seenLeader := make(map[string]bool)
	dupFollowers := make(map[string][]model.TranslationBlock)
	totalBlocks := 0
	var completedCount int64
	var failedCount int64
	var prevSummary string

	for _, page := range ir.Pages {
		inRange := selectedPages == nil || selectedPages[page.PageIndex]
		var pending []model.TranslationBlock
		for _, b := range page.Blocks {
			if !b.Translatable {
				continue
			}
			totalBlocks++
			if !inRange {
				if b.Status == "translated" || b.Status == "edited" || b.Status == "reviewed" {
					completedCount++
					prevSummary = b.OriginalText
				}
				continue
			}
			if !opts.Retranslate && (b.Status == "translated" || b.Status == "edited" || b.Status == "reviewed") {
				completedCount++
				prevSummary = b.OriginalText
				continue
			}

			norm := strings.TrimSpace(b.OriginalText)
			if norm != "" {
				if cached, ok := dedupCache[norm]; ok {
					preCached = append(preCached, preCachedItem{block: b, translated: cached})
					continue
				}
				if seenLeader[norm] {
					dupFollowers[norm] = append(dupFollowers[norm], b)
					continue
				}
				seenLeader[norm] = true
			}
			pending = append(pending, b)
		}

		// 提升短块单批容量至 25 块 / 2400 字符，降低目录/表格页 API 往返次数
		var cur []model.TranslationBlock
		curChars := 0
		for _, b := range pending {
			bLen := len([]rune(b.OriginalText))
			if len(cur) > 0 && (len(cur) >= 25 || curChars+bLen > 2400) {
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
			"completed_blocks": atomic.LoadInt64(&completedCount),
		},
	})

	// 先将直接命中已有缓存的重复块落盘并推送完成事件
	for _, item := range preCached {
		if ctx.Err() != nil {
			break
		}
		_ = ts.store.UpdateBlockTranslation(docID, item.block.ID, item.translated, "translated")
		done := atomic.AddInt64(&completedCount, 1)
		ts.broker.Publish(docID, model.SSEEvent{
			Event: "block_translated",
			Data: map[string]any{
				"block_id":         item.block.ID,
				"page_index":       item.block.PageIndex,
				"translated_text":  item.translated,
				"status":           "translated",
				"completed_blocks": done,
				"total_blocks":     totalBlocks,
			},
		})
	}

	var dedupMu sync.RWMutex
	publishFailed := func(b model.TranslationBlock, pageIdx int, errMsg string) {
		_ = ts.store.UpdateBlockTranslation(docID, b.ID, b.TranslatedText, "failed")
		atomic.AddInt64(&failedCount, 1)
		ts.broker.Publish(docID, model.SSEEvent{
			Event: "block_failed",
			Data: map[string]any{
				"block_id":   b.ID,
				"page_index": pageIdx,
				"error":      errMsg,
			},
		})
	}
	publishSuccess := func(b model.TranslationBlock, pageIdx int, translated string) {
		_ = ts.store.UpdateBlockTranslation(docID, b.ID, translated, "translated")
		done := atomic.AddInt64(&completedCount, 1)
		ts.broker.Publish(docID, model.SSEEvent{
			Event: "block_translated",
			Data: map[string]any{
				"block_id":         b.ID,
				"page_index":       pageIdx,
				"translated_text":  translated,
				"status":           "translated",
				"completed_blocks": done,
				"total_blocks":     totalBlocks,
			},
		})
	}

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
					publishFailed(b, job.pageIndex, err.Error())
					norm := strings.TrimSpace(b.OriginalText)
					for _, fb := range dupFollowers[norm] {
						publishFailed(fb, fb.PageIndex, err.Error())
					}
				}
				return
			}

			for _, b := range job.blocks {
				norm := strings.TrimSpace(b.OriginalText)
				translated, ok := resMap[b.ID]
				if !ok || strings.TrimSpace(translated) == "" {
					// 二次查缓存，未命中则降级单块补翻
					dedupMu.RLock()
					cached, hit := dedupCache[norm]
					dedupMu.RUnlock()
					if hit {
						translated = cached
					} else {
						single, sErr := ts.llm.RetranslateBlock(ctx, provider, b.OriginalText, "", opts.TargetLang, "准确流畅翻译此段内容")
						if sErr != nil {
							publishFailed(b, job.pageIndex, sErr.Error())
							for _, fb := range dupFollowers[norm] {
								publishFailed(fb, fb.PageIndex, sErr.Error())
							}
							continue
						}
						translated = single
					}
				}

				if norm != "" {
					dedupMu.Lock()
					dedupCache[norm] = translated
					dedupMu.Unlock()
				}

				publishSuccess(b, job.pageIndex, translated)
				// 同步将文档内所有重复原文块复用该译文
				for _, fb := range dupFollowers[norm] {
					if ctx.Err() != nil {
						break
					}
					publishSuccess(fb, fb.PageIndex, translated)
				}
			}
		}(bj)
	}

	wg.Wait()

	finalStatus := "completed"
	if ctx.Err() != nil {
		finalStatus = "paused"
	} else if atomic.LoadInt64(&completedCount) < int64(totalBlocks) {
		// 指定页局部翻译完毕后（全篇尚有未译块）回落为 parsed，方便继续翻其他页
		finalStatus = "parsed"
	}
	_ = ts.store.UpdateDocumentStatus(docID, finalStatus, "")

	ts.broker.Publish(docID, model.SSEEvent{
		Event: "job_completed",
		Data: map[string]any{
			"doc_id":           docID,
			"status":           finalStatus,
			"page_range":       opts.PageRange,
			"completed_blocks": atomic.LoadInt64(&completedCount),
			"failed_blocks":    atomic.LoadInt64(&failedCount),
			"total_blocks":     totalBlocks,
		},
	})
}
