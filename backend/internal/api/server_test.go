package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"translation-assistant/internal/config"
	"translation-assistant/internal/model"
	"translation-assistant/internal/store"
	"translation-assistant/pkg/pdfengine/object"
)

func makeMinimalPDF(t *testing.T) []byte {
	t.Helper()
	doc := object.NewEmptyDocument()
	fontRef := doc.AllocObj(object.Dict{
		"Type":     object.Name("Font"),
		"Subtype":  object.Name("Type1"),
		"BaseFont": object.Name("Helvetica"),
	})
	comp, _ := object.EncodeFlate([]byte("BT /F1 14 Tf 1 0 0 1 72 720 Tm (Hello World from Go PDF Engine) Tj ET"))
	csRef := doc.AllocObj(&object.Stream{
		Dict:    object.Dict{"Filter": object.Name("FlateDecode"), "Length": object.Int(len(comp))},
		RawData: comp,
	})
	pagesRef := doc.AllocObj(object.Dict{})
	pageRef := doc.AllocObj(object.Dict{
		"Type":      object.Name("Page"),
		"Parent":    pagesRef,
		"MediaBox":  object.Array{object.Int(0), object.Int(0), object.Int(595), object.Int(842)},
		"Resources": object.Dict{"Font": object.Dict{"F1": fontRef}},
		"Contents":  csRef,
	})
	doc.SetObj(pagesRef.ObjNum, object.Dict{"Type": object.Name("Pages"), "Kids": object.Array{pageRef}, "Count": object.Int(1)})
	catRef := doc.AllocObj(object.Dict{"Type": object.Name("Catalog"), "Pages": pagesRef})
	doc.Trailer["Root"] = catRef
	data, err := doc.Write()
	if err != nil {
		t.Fatalf("write minimal pdf: %v", err)
	}
	return data
}

func TestBackendEndToEndAPI(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("DATA_DIR", tmpDir)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	st, err := store.New(cfg.DBPath)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	defer st.Close()

	// 1. 启动 Mock OpenAI 兼容服务端
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{
						"content": `{"translations":[{"id":"p0_b0","text":"来自 Go PDF 引擎的问候"}]}`,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLM.Close()

	srv := NewServer(cfg, st)
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// 2. 创建 Provider 并验证 API Key 脱敏
	provBody := `{"name":"MockLLM","base_url":"` + mockLLM.URL + `","api_key":"sk-1234567890abcdef","model_name":"mock-gpt","max_concurrency":2,"is_default":true}`
	resp, err := http.Post(ts.URL+"/api/providers", "application/json", strings.NewReader(provBody))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create provider failed: status=%v err=%v", resp.StatusCode, err)
	}
	var createdProv model.ProviderConfig
	_ = json.NewDecoder(resp.Body).Decode(&createdProv)
	resp.Body.Close()
	if strings.Contains(createdProv.APIKey, "567890") {
		t.Fatalf("expected api_key to be masked, got %q", createdProv.APIKey)
	}

	// 3. 上传 PDF 文档并等待异步解析完成
	var formBuf bytes.Buffer
	mw := multipart.NewWriter(&formBuf)
	fw, _ := mw.CreateFormFile("file", "demo.pdf")
	_, _ = fw.Write(makeMinimalPDF(t))
	mw.Close()

	upResp, err := http.Post(ts.URL+"/api/documents/upload", mw.FormDataContentType(), &formBuf)
	if err != nil || upResp.StatusCode != http.StatusCreated {
		t.Fatalf("upload failed: status=%v err=%v", upResp.StatusCode, err)
	}
	var docSummary model.DocumentSummary
	_ = json.NewDecoder(upResp.Body).Decode(&docSummary)
	upResp.Body.Close()

	// 轮询等待解析完成
	var docIR model.DocumentIR
	for i := 0; i < 30; i++ {
		time.Sleep(50 * time.Millisecond)
		r, err := http.Get(ts.URL + "/api/documents/" + docSummary.ID)
		if err == nil {
			_ = json.NewDecoder(r.Body).Decode(&docIR)
			r.Body.Close()
			if docIR.Status == "parsed" {
				break
			}
		}
	}
	if docIR.Status != "parsed" || len(docIR.Pages) != 1 || len(docIR.Pages[0].Blocks) != 1 {
		t.Fatalf("expected parsed 1 page 1 block, got status=%s pages=%d", docIR.Status, len(docIR.Pages))
	}

	// 4. 启动 Mock LLM 流式翻译并等待完成
	trResp, err := http.Post(ts.URL+"/api/documents/"+docSummary.ID+"/translate", "application/json", strings.NewReader(`{"target_lang":"zh-CN"}`))
	if err != nil || trResp.StatusCode != http.StatusAccepted {
		t.Fatalf("start translate failed: err=%v", err)
	}
	trResp.Body.Close()

	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		r, _ := http.Get(ts.URL + "/api/documents/" + docSummary.ID)
		_ = json.NewDecoder(r.Body).Decode(&docIR)
		r.Body.Close()
		if docIR.Status == "completed" {
			break
		}
	}
	if docIR.Status != "completed" || docIR.Pages[0].Blocks[0].TranslatedText != "来自 Go PDF 引擎的问候" {
		t.Fatalf("unexpected translated result: status=%s text=%q", docIR.Status, docIR.Pages[0].Blocks[0].TranslatedText)
	}

	// 5. 测试块级编辑持久化
	patchReq, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/documents/"+docSummary.ID+"/blocks/p0_b0", strings.NewReader(`{"translated_text":"手工校对后的译文"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil || patchResp.StatusCode != http.StatusOK {
		t.Fatalf("patch block failed: err=%v", err)
	}
	patchResp.Body.Close()

	// 6. 验证 clean-bg 接口与导出接口
	bgResp, err := http.Get(ts.URL + "/api/documents/" + docSummary.ID + "/clean-bg")
	if err != nil || bgResp.StatusCode != http.StatusOK {
		t.Fatalf("get clean-bg failed: err=%v", err)
	}
	bgResp.Body.Close()

	if cfg.FontPath != "" {
		if _, statErr := os.Stat(filepath.Clean(cfg.FontPath)); statErr == nil {
			expResp, err := http.Post(ts.URL+"/api/documents/"+docSummary.ID+"/export", "application/json", strings.NewReader(`{"mode":"translated"}`))
			if err != nil || expResp.StatusCode != http.StatusOK {
				t.Fatalf("export failed: status=%v err=%v", expResp.StatusCode, err)
			}
			expResp.Body.Close()
		}
	}
}

func makeTwoPageSharedHeaderPDF(t *testing.T) []byte {
	t.Helper()
	doc := object.NewEmptyDocument()
	fontRef := doc.AllocObj(object.Dict{
		"Type":     object.Name("Font"),
		"Subtype":  object.Name("Type1"),
		"BaseFont": object.Name("Helvetica"),
	})
	pagesRef := doc.AllocObj(object.Dict{})

	mkPage := func(body string) object.IndirectRef {
		stream := "BT /F1 12 Tf 1 0 0 1 72 780 Tm (Shared Header Title) Tj 1 0 0 1 72 700 Tm (" + body + ") Tj ET"
		comp, _ := object.EncodeFlate([]byte(stream))
		csRef := doc.AllocObj(&object.Stream{
			Dict:    object.Dict{"Filter": object.Name("FlateDecode"), "Length": object.Int(len(comp))},
			RawData: comp,
		})
		return doc.AllocObj(object.Dict{
			"Type":      object.Name("Page"),
			"Parent":    pagesRef,
			"MediaBox":  object.Array{object.Int(0), object.Int(0), object.Int(595), object.Int(842)},
			"Resources": object.Dict{"Font": object.Dict{"F1": fontRef}},
			"Contents":  csRef,
		})
	}

	p0 := mkPage("First page unique paragraph")
	p1 := mkPage("Second page unique paragraph")
	doc.SetObj(pagesRef.ObjNum, object.Dict{"Type": object.Name("Pages"), "Kids": object.Array{p0, p1}, "Count": object.Int(2)})
	catRef := doc.AllocObj(object.Dict{"Type": object.Name("Catalog"), "Pages": pagesRef})
	doc.Trailer["Root"] = catRef
	data, err := doc.Write()
	if err != nil {
		t.Fatalf("write two-page pdf: %v", err)
	}
	return data
}

func TestDeduplicateTranslationReuse(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("DATA_DIR", tmpDir)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	st, err := store.New(cfg.DBPath)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	defer st.Close()

	sharedCount := 0
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		userMsg := ""
		if len(req.Messages) > 1 {
			userMsg = req.Messages[1].Content
		}
		var payload struct {
			Items []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"items"`
		}
		_ = json.Unmarshal([]byte(userMsg), &payload)

		type trItem struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		var out []trItem
		for _, it := range payload.Items {
			if it.Text == "Shared Header Title" {
				sharedCount++
				out = append(out, trItem{ID: it.ID, Text: "共享页眉标题"})
			} else {
				out = append(out, trItem{ID: it.ID, Text: "译文:" + it.Text})
			}
		}
		contentBytes, _ := json.Marshal(map[string]any{"translations": out})
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": string(contentBytes)}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLM.Close()

	srv := NewServer(cfg, st)
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	provBody := `{"name":"MockDedup","base_url":"` + mockLLM.URL + `","api_key":"sk-test","model_name":"mock","max_concurrency":2,"is_default":true}`
	resp, err := http.Post(ts.URL+"/api/providers", "application/json", strings.NewReader(provBody))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create provider failed: %v", err)
	}
	resp.Body.Close()

	var formBuf bytes.Buffer
	mw := multipart.NewWriter(&formBuf)
	fw, _ := mw.CreateFormFile("file", "dedup.pdf")
	_, _ = fw.Write(makeTwoPageSharedHeaderPDF(t))
	mw.Close()

	upResp, err := http.Post(ts.URL+"/api/documents/upload", mw.FormDataContentType(), &formBuf)
	if err != nil || upResp.StatusCode != http.StatusCreated {
		t.Fatalf("upload failed: %v", err)
	}
	var docSummary model.DocumentSummary
	_ = json.NewDecoder(upResp.Body).Decode(&docSummary)
	upResp.Body.Close()

	var docIR model.DocumentIR
	for i := 0; i < 30; i++ {
		time.Sleep(50 * time.Millisecond)
		r, err := http.Get(ts.URL + "/api/documents/" + docSummary.ID)
		if err == nil {
			_ = json.NewDecoder(r.Body).Decode(&docIR)
			r.Body.Close()
			if docIR.Status == "parsed" {
				break
			}
		}
	}
	if docIR.Status != "parsed" || len(docIR.Pages) != 2 {
		t.Fatalf("expected 2 parsed pages, got status=%s pages=%d", docIR.Status, len(docIR.Pages))
	}

	trResp, err := http.Post(ts.URL+"/api/documents/"+docSummary.ID+"/translate", "application/json", strings.NewReader(`{"target_lang":"zh-CN"}`))
	if err != nil || trResp.StatusCode != http.StatusAccepted {
		t.Fatalf("start translate failed: %v", err)
	}
	trResp.Body.Close()

	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		r, _ := http.Get(ts.URL + "/api/documents/" + docSummary.ID)
		_ = json.NewDecoder(r.Body).Decode(&docIR)
		r.Body.Close()
		if docIR.Status == "completed" {
			break
		}
	}
	if docIR.Status != "completed" {
		t.Fatalf("expected completed status, got %s", docIR.Status)
	}
	if sharedCount != 1 {
		t.Fatalf("expected 'Shared Header Title' to be sent to LLM only 1 time, got %d", sharedCount)
	}
	for pIdx, p := range docIR.Pages {
		if len(p.Blocks) < 2 || p.Blocks[0].TranslatedText != "共享页眉标题" {
			t.Fatalf("page %d missing reused shared header translation: %+v", pIdx, p.Blocks)
		}
	}
}

func TestTranslatePageRange(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("DATA_DIR", tmpDir)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	st, err := store.New(cfg.DBPath)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	defer st.Close()

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var payload struct {
			Items []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"items"`
		}
		if len(req.Messages) > 1 {
			_ = json.Unmarshal([]byte(req.Messages[1].Content), &payload)
		}
		type trItem struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		var out []trItem
		for _, it := range payload.Items {
			out = append(out, trItem{ID: it.ID, Text: "已译:" + it.Text})
		}
		contentBytes, _ := json.Marshal(map[string]any{"translations": out})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": string(contentBytes)}}},
		})
	}))
	defer mockLLM.Close()

	srv := NewServer(cfg, st)
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	provBody := `{"name":"MockRange","base_url":"` + mockLLM.URL + `","api_key":"sk-test","model_name":"mock","max_concurrency":2,"is_default":true}`
	pResp, _ := http.Post(ts.URL+"/api/providers", "application/json", strings.NewReader(provBody))
	pResp.Body.Close()

	var formBuf bytes.Buffer
	mw := multipart.NewWriter(&formBuf)
	fw, _ := mw.CreateFormFile("file", "range.pdf")
	_, _ = fw.Write(makeTwoPageSharedHeaderPDF(t))
	mw.Close()

	upResp, _ := http.Post(ts.URL+"/api/documents/upload", mw.FormDataContentType(), &formBuf)
	var docSummary model.DocumentSummary
	_ = json.NewDecoder(upResp.Body).Decode(&docSummary)
	upResp.Body.Close()

	var docIR model.DocumentIR
	for i := 0; i < 30; i++ {
		time.Sleep(50 * time.Millisecond)
		r, err := http.Get(ts.URL + "/api/documents/" + docSummary.ID)
		if err == nil {
			_ = json.NewDecoder(r.Body).Decode(&docIR)
			r.Body.Close()
			if docIR.Status == "parsed" {
				break
			}
		}
	}

	// 1. 非法页码范围应返回 400
	badResp, err := http.Post(ts.URL+"/api/documents/"+docSummary.ID+"/translate", "application/json", strings.NewReader(`{"page_range":"99"}`))
	if err != nil || badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for out-of-range page_range, got %v err=%v", badResp.StatusCode, err)
	}
	badResp.Body.Close()

	// 2. 仅翻译第 2 页 ("2")
	okResp, err := http.Post(ts.URL+"/api/documents/"+docSummary.ID+"/translate", "application/json", strings.NewReader(`{"page_range":"2"}`))
	if err != nil || okResp.StatusCode != http.StatusAccepted {
		t.Fatalf("start translate page 2 failed: %v", err)
	}
	okResp.Body.Close()

	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		r, _ := http.Get(ts.URL + "/api/documents/" + docSummary.ID)
		_ = json.NewDecoder(r.Body).Decode(&docIR)
		r.Body.Close()
		if docIR.Status == "parsed" && len(docIR.Pages[1].Blocks) > 0 && docIR.Pages[1].Blocks[0].Status == "translated" {
			break
		}
	}

	if docIR.Status != "parsed" {
		t.Fatalf("expected partial translation to return status=parsed, got %s", docIR.Status)
	}
	for _, b := range docIR.Pages[0].Blocks {
		if b.Status != "pending" || b.TranslatedText != "" {
			t.Fatalf("expected page 1 to remain untranslated, got %+v", b)
		}
	}
	for _, b := range docIR.Pages[1].Blocks {
		if b.Status != "translated" || !strings.HasPrefix(b.TranslatedText, "已译:") {
			t.Fatalf("expected page 2 to be translated, got %+v", b)
		}
	}
}
