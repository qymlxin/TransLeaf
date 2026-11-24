package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"translation-assistant/internal/model"
)

// BatchItem 表示待翻译批次中的单个文本块
type BatchItem struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Client 为纯 Go 标准库实现的 OpenAI 兼容协议 LLM 客户端
type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 90 * time.Second},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// TestConnection 发送最小化探针请求验证模型配置连通性（10 秒超时）
func (c *Client) TestConnection(ctx context.Context, p *model.ProviderConfig) (int64, error) {
	tCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	_, err := c.chat(tCtx, p, []chatMessage{
		{Role: "user", Content: "Reply OK."},
	}, 0)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return elapsed, err
	}
	return elapsed, nil
}

// TranslateBatch 执行结构化 JSON 批处理翻译（含 3 次指数退避重试）
func (c *Client) TranslateBatch(
	ctx context.Context,
	p *model.ProviderConfig,
	sourceLang, targetLang, stylePreset, prevContext string,
	items []BatchItem,
) (map[string]string, error) {
	if len(items) == 0 {
		return map[string]string{}, nil
	}

	sysPrompt := buildBatchSystemPrompt(sourceLang, targetLang, stylePreset, prevContext)
	payloadBytes, _ := json.Marshal(map[string]any{"items": items})

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * 1500 * time.Millisecond
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		raw, err := c.chat(ctx, p, []chatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: string(payloadBytes)},
		}, p.Temperature)
		if err != nil {
			lastErr = err
			continue
		}

		parsed, err := parseBatchJSON(raw)
		if err != nil {
			lastErr = err
			continue
		}
		return parsed, nil
	}
	return nil, lastErr
}

// RetranslateBlock 对单个文本块执行 AI 局部润色、重译或精简
func (c *Client) RetranslateBlock(
	ctx context.Context,
	p *model.ProviderConfig,
	originalText, currentTranslation, targetLang, instruction string,
) (string, error) {
	if instruction == "" {
		instruction = "请提供更自然、准确且专业的高质量译文。"
	}
	sys := fmt.Sprintf(
		"你是一名精通排版与专业文档翻译的资深译者。请将给定原文翻译或润色为 %s。严格只输出最终译文本身，不要添加任何解释、引号或 Markdown 包装。",
		langDisplayName(targetLang),
	)
	user := fmt.Sprintf("【原文】\n%s\n\n【当前译文】\n%s\n\n【用户指令】\n%s", originalText, currentTranslation, instruction)

	resp, err := c.chat(ctx, p, []chatMessage{
		{Role: "system", Content: sys},
		{Role: "user", Content: user},
	}, p.Temperature)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp), nil
}

func (c *Client) chat(ctx context.Context, p *model.ProviderConfig, msgs []chatMessage, temp float64) (string, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}

	reqBody, err := json.Marshal(chatRequest{
		Model:       p.ModelName,
		Messages:    msgs,
		Temperature: temp,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("LLM API HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 300))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode LLM response failed: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", fmt.Errorf("LLM API error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("LLM returned empty choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

func buildBatchSystemPrompt(sourceLang, targetLang, stylePreset, prevContext string) string {
	styleDesc := map[string]string{
		"general":  "自然流畅、准确忠实于原文的通用专业风格",
		"academic": "严谨规范的学术论文风格，保留专业术语英文缩写（如 Transformer, LLM）",
		"technical": "清晰准确的技术文档风格，保留变量名、代码标识符、API 名称与命令行原样",
		"business": "正式得体的商业报告与法务文书风格",
	}[stylePreset]
	if styleDesc == "" {
		styleDesc = "自然流畅、准确忠实于原文的专业风格"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "你是一名专业的文档保真翻译引擎。请将输入的 JSON 数组中每个文本块翻译为【%s】。\n", langDisplayName(targetLang))
	if sourceLang != "" && sourceLang != "auto" {
		fmt.Fprintf(&sb, "源语言：%s。\n", sourceLang)
	}
	fmt.Fprintf(&sb, "翻译风格：%s。\n", styleDesc)
	sb.WriteString("严格要求：\n")
	sb.WriteString("1. 必须严格输出合法 JSON 对象，格式为：{\"translations\":[{\"id\":\"...\",\"text\":\"...\"}]}，不要输出任何额外说明或 Markdown 标记。\n")
	sb.WriteString("2. 保持每个 id 与输入完全一一对应，不可遗漏或合并。\n")
	sb.WriteString("3. 若原文以项目符号（如 •、–、-）或章节编号（如 2.3.1）开头，译文必须原样保留该符号与编号前缀。\n")
	sb.WriteString("4. 若原文为目录项（含 . . . 点线与末尾页码），请保持“章节号 + 译文 + 点线 + 原页码”的结构。\n")
	sb.WriteString("5. 译文长度尽量精炼紧凑，便于适配原有 PDF 排版宽度；保留引用编号（如 [1]）、型号代码（如 STM32F103C8）与公式符号。\n")
	if strings.TrimSpace(prevContext) != "" {
		fmt.Fprintf(&sb, "\n前文语境参考（无需重复翻译，仅用于保持术语一致）：\n%s\n", truncateStr(prevContext, 400))
	}
	return sb.String()
}

func parseBatchJSON(raw string) (map[string]string, error) {
	cleaned := strings.TrimSpace(raw)
	if strings.HasPrefix(cleaned, "```") {
		if idx := strings.IndexByte(cleaned, '\n'); idx >= 0 {
			cleaned = cleaned[idx+1:]
		}
		cleaned = strings.TrimSuffix(strings.TrimSpace(cleaned), "```")
		cleaned = strings.TrimSpace(cleaned)
	}
	// 提取首个 '{' 到末尾 '}' 之间的 JSON
	start := strings.IndexByte(cleaned, '{')
	end := strings.LastIndexByte(cleaned, '}')
	if start >= 0 && end > start {
		cleaned = cleaned[start : end+1]
	}

	var wrapper struct {
		Translations []BatchItem `json:"translations"`
	}
	if err := json.Unmarshal([]byte(cleaned), &wrapper); err == nil && len(wrapper.Translations) > 0 {
		out := make(map[string]string, len(wrapper.Translations))
		for _, item := range wrapper.Translations {
			out[item.ID] = strings.TrimSpace(item.Text)
		}
		return out, nil
	}

	// 兼容直接返回 map[id]text 的模型输出
	var direct map[string]string
	if err := json.Unmarshal([]byte(cleaned), &direct); err == nil && len(direct) > 0 {
		return direct, nil
	}
	return nil, fmt.Errorf("failed to parse batch translation JSON: %s", truncateStr(raw, 200))
}

func langDisplayName(code string) string {
	switch strings.ToLower(code) {
	case "zh", "zh-cn", "zh_cn":
		return "简体中文"
	case "zh-tw", "zh_tw":
		return "繁体中文"
	case "en", "en-us":
		return "英文"
	case "ja":
		return "日文"
	case "ko":
		return "韩文"
	default:
		if code == "" {
			return "简体中文"
		}
		return code
	}
}

func truncateStr(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}
