package server

// proxy_test.go — 离线自测：mock 上游验证核心链路（对齐 Python 版用例，无需华为账号）。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misakano7545/deveco2api-panel/internal/auth"
	"github.com/misakano7545/deveco2api-panel/internal/config"
	"github.com/misakano7545/deveco2api-panel/internal/jsonval"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
	"github.com/misakano7545/deveco2api-panel/internal/panel"
	"github.com/misakano7545/deveco2api-panel/internal/upstream"
)

const rateLimitMsg = "New session request rate exceeded. Please retry later or set up a custom model."

type mockUpstream struct {
	mu sync.Mutex

	oldValid          bool
	modelFailOnce     bool
	s53Think          bool
	rateLimitStream   bool
	rateLimitNoStream string // "", "soft", "hard"

	refreshCalls int
	modelCalls   int
	chatNoStream []map[string]any
	chatHeaders  []map[string]string
	chatStreamN  int
}

func newMock() *mockUpstream { return &mockUpstream{oldValid: true, s53Think: true} }

func (m *mockUpstream) tokenOK(h string) bool {
	token := strings.TrimPrefix(h, "Bearer ")
	if token == "new-token" {
		return true
	}
	if token == "old-token" {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.oldValid
	}
	return false
}

func rateLimitError() map[string]any {
	return map[string]any{"message": rateLimitMsg, "type": "UserSessionLimitExceeded", "code": "403"}
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func (m *mockUpstream) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/codeGenie/modelConfig", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.modelCalls++
		fail := m.modelFailOnce
		m.modelFailOnce = false
		m.mu.Unlock()
		if fail || !m.tokenOK(r.Header.Get("Authorization")) {
			writeTestJSON(w, 200, map[string]any{"errorCode": 5002, "errorMsg": "authorization is null."})
			return
		}
		writeTestJSON(w, 200, map[string]any{
			"success": true,
			"body": map[string]any{"inner_models": []any{
				map[string]any{"group_name": "GLM", "group_name_cn": "智谱", "model_configs": []any{
					map[string]any{"model_id": "GLM-5.1"}, map[string]any{"model_id": "GLM-5.3"},
				}},
			}},
		})
	})

	mux.HandleFunc("/authrouter/auth/api/jwToken/check", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("refresh") == "true" {
			m.mu.Lock()
			m.refreshCalls++
			m.mu.Unlock()
		}
		writeTestJSON(w, 200, map[string]any{"status": true, "userInfo": map[string]any{
			"accessToken": "new-token", "refreshToken": "r2", "userId": "U1",
			"name": "tester", "realName": true, "nationalCode": "CN",
		}})
	})

	mux.HandleFunc("/sse/codeGenie/maas/v2/no-stream/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		hdrs := map[string]string{}
		for k, v := range r.Header {
			hdrs[strings.ToLower(k)] = v[0]
		}
		m.mu.Lock()
		m.chatNoStream = append(m.chatNoStream, req)
		m.chatHeaders = append(m.chatHeaders, hdrs)
		soft, hard := m.rateLimitNoStream == "soft", m.rateLimitNoStream == "hard"
		m.mu.Unlock()

		if !m.tokenOK(r.Header.Get("Authorization")) {
			writeTestJSON(w, 401, map[string]any{"error": "token expired"})
			return
		}
		if soft {
			writeTestJSON(w, 200, map[string]any{"error": rateLimitError()})
			return
		}
		if hard {
			writeTestJSON(w, 429, map[string]any{"error": rateLimitError()})
			return
		}
		content := "pong"
		if jsonval.Str(req["model"]) == "GLM-5.3" {
			content = "Let me think about it carefully.</think>42"
		}
		writeTestJSON(w, 200, map[string]any{
			"id": "c1", "object": "chat.completion",
			"choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage": map[string]any{"total_tokens": 2},
		})
	})

	mux.HandleFunc("/sse/codeGenie/maas/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		m.mu.Lock()
		m.chatStreamN++
		think, rl := m.s53Think, m.rateLimitStream
		m.mu.Unlock()

		if !m.tokenOK(r.Header.Get("Authorization")) {
			writeTestJSON(w, 401, map[string]any{"error": "token expired"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		if rl {
			io.WriteString(w, "data: \n\n")
			io.WriteString(w, "data: "+mustJSON(map[string]any{"error": rateLimitError()})+"\n\n")
			return
		}
		io.WriteString(w, "data: \n\n") // 上游偶发空帧：必须被跳过
		parts := []string{"po", "ng"}
		if jsonval.Str(req["model"]) == "GLM-5.3" && think {
			parts = []string{"Let me think", " about it", " carefully.</thi", "nk>", "42"}
		}
		for i, p := range parts {
			delta := map[string]any{"content": p}
			if i == 0 {
				delta["role"] = "assistant"
			}
			choice := map[string]any{"index": 0, "delta": delta}
			if i == len(parts)-1 {
				choice["finish_reason"] = "stop"
			}
			ch := map[string]any{"id": "c1", "object": "chat.completion.chunk", "choices": []any{choice}}
			io.WriteString(w, "data: "+mustJSON(ch)+"\n\n")
		}
		io.WriteString(w, "data: [DONE]\n\n")
	})

	return mux
}

// ---------------------------------------------------------------- helpers

// newTestServer mock 上游 + 真实网关（默认 6h 保活间隔）。
func newTestServer(t *testing.T, m *mockUpstream) (*httptest.Server, *Handler, *config.Config) {
	t.Helper()
	return newTestServerWithKeepalive(t, m, 6.0, false)
}

// newTestServerWithKeepalive 同 newTestServer，保活间隔与会话复用可指定
// （保活用例要秒级；限流对照用例要切换 session_reuse）。
// 装配方式与 cmd/server/wiring.go 一致：依赖在此注入，组件本身不认识配置文件。
func newTestServerWithKeepalive(t *testing.T, m *mockUpstream, keepaliveHours float64, sessionReuse bool) (*httptest.Server, *Handler, *config.Config) {
	t.Helper()
	up := httptest.NewServer(m.handler())
	t.Cleanup(up.Close)

	cfg := config.Default()
	cfg.Server.APIKey = "test-key"
	cfg.DevEco.BaseURL = up.URL
	cfg.DevEco.KeepaliveHours = keepaliveHours
	cfg.DevEco.Auth = config.AuthConfig{JWTToken: "jwt-1", AccessToken: "old-token"}
	cfg.SetPath(filepath.Join(t.TempDir(), "config.json"))
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	store := auth.New(auth.Config{
		BaseURL:      cfg.DevEco.BaseURL,
		AppID:        cfg.DevEco.AppID,
		AuthURL:      cfg.DevEco.AuthURL,
		CallbackPort: cfg.DevEco.CallbackPort,
	}, cfg.Tokens(), cfg.SaveTokens)

	upc := upstream.New(upstream.Config{
		BaseURL:        cfg.DevEco.BaseURL,
		Client:         cfg.DevEco.Client,
		Project:        cfg.DevEco.Project,
		UserAgent:      cfg.DevEco.UserAgent,
		Model:          cfg.DevEco.Model,
		ThinkingModels: cfg.DevEco.ThinkingModels,
		VisionModels:   cfg.DevEco.VisionModels,
		Token:          func() string { return store.Tokens().AccessToken },
	})

	ring := panel.NewRing(500)
	logfmt.SetSink(ring.Add) // 面板日志接口读这个环
	console := panel.New(panel.Config{
		Version:        "test",
		APIKey:         cfg.Server.APIKey,
		Listen:         "127.0.0.1:0",
		ConfigPath:     cfg.Path(),
		UpstreamURL:    cfg.DevEco.BaseURL,
		ModelDefault:   cfg.DevEco.Model,
		ThinkingModels: cfg.DevEco.ThinkingModels,
		KeepaliveHours: cfg.DevEco.KeepaliveHours,
		Auth:           store,
		Upstream:       upc,
		Logs:           ring,
		// 与 cmd/server/wiring.go 同口径：导入 = 换内存 token + 落盘，回显旧身份
		ImportAuth: func(t auth.Tokens) (auth.Tokens, error) {
			prev := store.Tokens()
			if err := store.Save(t); err != nil {
				return auth.Tokens{}, err
			}
			return prev, nil
		},
	})

	h := New(Config{
		Version:        "test",
		APIKey:         cfg.Server.APIKey,
		Listen:         "127.0.0.1:0",
		Upstream:       upc,
		Auth:           store,
		Panel:          console,
		KeepaliveHours: cfg.DevEco.KeepaliveHours,
		SessionReuse:   sessionReuse,
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, h, cfg
}

func doJSON(t *testing.T, method, url, key string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

type sseAgg struct {
	content   string
	reasoning string
	done      bool
}

func aggregateSSE(raw string) sseAgg {
	var agg sseAgg
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		p := strings.TrimSpace(line[6:])
		if p == "[DONE]" {
			agg.done = true
			continue
		}
		var ch map[string]any
		if json.Unmarshal([]byte(p), &ch) != nil {
			continue
		}
		choices, _ := ch["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		cm, _ := choices[0].(map[string]any)
		d, _ := cm["delta"].(map[string]any)
		agg.content += jsonval.Str(d["content"])
		agg.reasoning += jsonval.Str(d["reasoning_content"])
	}
	return agg
}

// ---------------------------------------------------------------- tests

func TestModelsAuthAndMapping(t *testing.T) {
	ts, _, _ := newTestServer(t, newMock())

	if code, _ := doJSON(t, "GET", ts.URL+"/v1/models", "", nil); code != 401 {
		t.Fatalf("无 key 应 401，实际 %d", code)
	}
	if code, _ := doJSON(t, "GET", ts.URL+"/v1/models", "wrong", nil); code != 401 {
		t.Fatalf("错误 key 应 401，实际 %d", code)
	}
	code, raw := doJSON(t, "GET", ts.URL+"/v1/models", "test-key", nil)
	if code != 200 {
		t.Fatalf("应 200，实际 %d %s", code, raw)
	}
	var data struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Data) != 2 || jsonval.Str(data.Data[0]["id"]) != "GLM-5.1" || jsonval.Str(data.Data[1]["id"]) != "GLM-5.3" {
		t.Fatalf("模型列表不对: %s", raw)
	}
	if jsonval.Str(data.Data[0]["owned_by"]) != "GLM" {
		t.Fatalf("owned_by 不对: %s", raw)
	}
}

func TestModelsRefreshOnBusinessError(t *testing.T) {
	m := newMock()
	m.modelFailOnce = true
	ts, _, _ := newTestServer(t, m)

	code, raw := doJSON(t, "GET", ts.URL+"/v1/models", "test-key", nil)
	if code != 200 {
		t.Fatalf("200+errorCode 应刷新后重试成功，实际 %d %s", code, raw)
	}
	m.mu.Lock()
	calls, refreshes := m.modelCalls, m.refreshCalls
	m.mu.Unlock()
	if calls < 2 {
		t.Fatalf("应失败+重试两次 modelConfig，实际 %d", calls)
	}
	if refreshes < 1 {
		t.Fatalf("应触发一次刷新")
	}
}

func TestChatNonstream401RefreshRetry(t *testing.T) {
	m := newMock()
	m.oldValid = false
	ts, _, _ := newTestServer(t, m)

	payload := map[string]any{"model": "GLM-5.1", "stream": false, "messages": []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
			map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "get_time", "arguments": "{}"}}}},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "12:00"},
	}, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "get_time"}}}}

	code, raw := doJSON(t, "POST", ts.URL+"/v1/chat/completions", "test-key", payload)
	if code != 200 {
		t.Fatalf("401→刷新→重试应成功，实际 %d %s", code, raw)
	}
	var d map[string]any
	_ = json.Unmarshal(raw, &d)
	msg, _ := d["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if jsonval.Str(msg["content"]) != "pong" {
		t.Fatalf("内容不对: %s", raw)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.chatNoStream) != 2 {
		t.Fatalf("应 401 后重试共 2 次，实际 %d", len(m.chatNoStream))
	}
	second := m.chatNoStream[1]
	msgs, _ := second["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("消息数不对: %v", msgs)
	}
	asst, _ := msgs[1].(map[string]any)
	if tcs, _ := asst["tool_calls"].([]any); len(tcs) != 1 {
		t.Fatalf("tool_calls 未透传: %v", asst)
	}
	if _, ok := second["tools"]; !ok {
		t.Fatalf("tools 未透传")
	}
	h2 := m.chatHeaders[1]
	if h2["lang"] != "en" {
		t.Fatalf("lang 头缺失: %v", h2)
	}
	if h2["x-deveco-client"] != "cli" {
		t.Fatalf("x-deveco-client 缺失: %v", h2)
	}
	if h2["session-id"] == "" || h2["session-id"] != h2["x-deveco-session"] {
		t.Fatalf("session 头不一致: %v", h2)
	}
	if h2["chat-id"] == "" {
		t.Fatalf("chat-id 缺失: %v", h2)
	}
	if !strings.HasPrefix(h2["user-agent"], "deveco/") {
		t.Fatalf("user-agent 不对: %s", h2["user-agent"])
	}
	if m.refreshCalls < 1 {
		t.Fatalf("应发生刷新")
	}
}

func TestThinkStripNonstream(t *testing.T) {
	ts, _, _ := newTestServer(t, newMock())
	code, raw := doJSON(t, "POST", ts.URL+"/v1/chat/completions", "test-key",
		map[string]any{"model": "GLM-5.3", "messages": []any{map[string]any{"role": "user", "content": "1+1?"}}})
	if code != 200 {
		t.Fatalf("应 200: %d %s", code, raw)
	}
	var d map[string]any
	_ = json.Unmarshal(raw, &d)
	msg, _ := d["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if jsonval.Str(msg["content"]) != "42" {
		t.Fatalf("正文应为 42: %v", msg)
	}
	if jsonval.Str(msg["reasoning_content"]) != "Let me think about it carefully." {
		t.Fatalf("思维链不对: %v", msg)
	}
}

func TestThinkStripStream(t *testing.T) {
	ts, _, _ := newTestServer(t, newMock())
	code, raw := doJSON(t, "POST", ts.URL+"/v1/chat/completions", "test-key",
		map[string]any{"model": "GLM-5.3", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "1+1?"}}})
	if code != 200 {
		t.Fatalf("应 200: %d %s", code, raw)
	}
	agg := aggregateSSE(string(raw))
	if !agg.done {
		t.Fatalf("缺 [DONE]: %s", raw)
	}
	if agg.content != "42" {
		t.Fatalf("正文应为 42（跨 chunk 的 </think> 也不能泄漏），实际 %q", agg.content)
	}
	if agg.reasoning != "Let me think about it carefully." {
		t.Fatalf("思维链不对: %q", agg.reasoning)
	}
	if strings.Contains(string(raw), "data: \n\n") {
		t.Fatalf("空 data 帧未被跳过")
	}
}

func TestStreamNoBoundaryFallback(t *testing.T) {
	m := newMock()
	m.s53Think = false
	ts, _, _ := newTestServer(t, m)
	_, raw := doJSON(t, "POST", ts.URL+"/v1/chat/completions", "test-key",
		map[string]any{"model": "GLM-5.3", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	agg := aggregateSSE(string(raw))
	if agg.content != "pong" {
		t.Fatalf("无边界应整段按正文兜底，实际 %q", agg.content)
	}
	if agg.reasoning != "" {
		t.Fatalf("不应有 reasoning_content: %q", agg.reasoning)
	}
}

func TestStreamNonThinkingModelUntouched(t *testing.T) {
	ts, _, _ := newTestServer(t, newMock())
	_, raw := doJSON(t, "POST", ts.URL+"/v1/chat/completions", "test-key",
		map[string]any{"model": "GLM-5.1", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	agg := aggregateSSE(string(raw))
	if agg.content != "pong" || agg.reasoning != "" {
		t.Fatalf("非思考模型不应受影响: %+v", agg)
	}
	if strings.Contains(string(raw), "reasoning_content") {
		t.Fatalf("非思考模型不应出现 reasoning_content")
	}
}

func TestRateLimitTranslations(t *testing.T) {
	// 流式：200 + 空帧 + error 帧 → 429
	m := newMock()
	m.rateLimitStream = true
	ts, _, _ := newTestServer(t, m)
	code, raw := doJSON(t, "POST", ts.URL+"/v1/chat/completions", "test-key",
		map[string]any{"model": "GLM-5.1", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if code != 429 {
		t.Fatalf("流式限流应 429，实际 %d %s", code, raw)
	}
	var d map[string]any
	_ = json.Unmarshal(raw, &d)
	e, _ := d["error"].(map[string]any)
	if jsonval.Str(e["type"]) != "UserSessionLimitExceeded" {
		t.Fatalf("error 未透传: %s", raw)
	}

	// 非流式 hard：HTTP 429 + error 对象 → 429
	m2 := newMock()
	m2.rateLimitNoStream = "hard"
	ts2, _, _ := newTestServer(t, m2)
	if code, raw := doJSON(t, "POST", ts2.URL+"/v1/chat/completions", "test-key",
		map[string]any{"model": "GLM-5.1", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}); code != 429 {
		t.Fatalf("非流式 HTTP 级错误应转 429，实际 %d %s", code, raw)
	}

	// 非流式 soft：200 + error 对象 → 429
	m3 := newMock()
	m3.rateLimitNoStream = "soft"
	ts3, _, _ := newTestServer(t, m3)
	if code, raw := doJSON(t, "POST", ts3.URL+"/v1/chat/completions", "test-key",
		map[string]any{"model": "GLM-5.1", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}); code != 429 {
		t.Fatalf("非流式 200+error 应转 429，实际 %d %s", code, raw)
	}
}

// TestVisionPassthrough 图片入站：vision_models 命中的模型 content 数组透传，
// 其余模型仍降级成 "[image: url]" 文本（实测 GLM-5.1/5.3 带图一律 403 ModelServiceError）。
func TestVisionPassthrough(t *testing.T) {
	m := newMock()
	ts, _, _ := newTestServer(t, m)

	imgMsg := []any{
		map[string]any{"type": "text", "text": "这张图什么颜色"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
	}
	sentContent := func(model string) any {
		code, raw := doJSON(t, "POST", ts.URL+"/v1/chat/completions", "test-key",
			map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": imgMsg}}})
		if code != 200 {
			t.Fatalf("请求应 200，实际 %d %s", code, raw)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		req := m.chatNoStream[len(m.chatNoStream)-1]
		msgs, _ := req["messages"].([]any)
		if len(msgs) == 0 {
			t.Fatalf("上游请求缺 messages: %v", req)
		}
		return msgs[0].(map[string]any)["content"]
	}

	if c := sentContent("Qwen3_VL_235B_A22B_Instruct"); !isContentArray(c) {
		t.Fatalf("视觉模型应透传 content 数组，实际 %T %v", c, c)
	}
	c := sentContent("GLM-5.1")
	s, ok := c.(string)
	if !ok || !strings.Contains(s, "[image: data:image/png;base64,") {
		t.Fatalf("非视觉模型应降级为文本，实际 %T %v", c, c)
	}
}

// isContentArray 判断上游收到的 content 是否为结构化数组（multimodal 透传形态）。
func isContentArray(c any) bool {
	arr, ok := c.([]any)
	if !ok || len(arr) != 2 {
		return false
	}
	part, _ := arr[1].(map[string]any)
	return part != nil && part["type"] == "image_url"
}

func TestKeepaliveRefreshes(t *testing.T) {
	m := newMock()
	_, h, _ := newTestServerWithKeepalive(t, m, 0.0003, false) // ≈1.08s
	h.StartKeepalive()
	defer h.StopKeepalive()

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		n := m.refreshCalls
		m.mu.Unlock()
		if n >= 2 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	m.mu.Lock()
	n := m.refreshCalls
	m.mu.Unlock()
	t.Fatalf("保活应至少触发 2 次刷新，实际 %d", n)
}

// TestSessionReuseStickySession 会话复用开关（实测：上游拦的是「新建会话」约 5 次/分，
// 同一会话内连发 64 次全 200 —— 所以未带 session_id 时默认复用同一会话是可用性的关键）。
func TestSessionReuseStickySession(t *testing.T) {
	twoSessions := func(reuse bool) (string, string) {
		m := newMock()
		ts, _, _ := newTestServerWithKeepalive(t, m, 6.0, reuse)
		for i := 0; i < 2; i++ {
			code, raw := doJSON(t, "POST", ts.URL+"/v1/chat/completions", "test-key",
				map[string]any{"model": "GLM-5.1", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
			if code != 200 {
				t.Fatalf("请求应 200，实际 %d %s", code, raw)
			}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.chatHeaders) < 2 {
			t.Fatalf("mock 只记录到 %d 次请求", len(m.chatHeaders))
		}
		return m.chatHeaders[0]["session-id"], m.chatHeaders[1]["session-id"]
	}

	if a, b := twoSessions(false); a == b {
		t.Fatalf("关闭复用时两次会话应不同，实际都是 %s", a)
	}
	if c, d := twoSessions(true); c != d {
		t.Fatalf("开启复用时两次会话应相同，实际 %s != %s", c, d)
	}

	// 客户端显式带 session_id 时优先用它（复用开关不干扰会话隔离）
	m := newMock()
	ts, _, _ := newTestServerWithKeepalive(t, m, 6.0, true)
	for _, sid := range []string{"cli-a", "cli-b"} {
		code, raw := doJSON(t, "POST", ts.URL+"/v1/chat/completions", "test-key",
			map[string]any{"model": "GLM-5.1", "session_id": sid,
				"messages": []any{map[string]any{"role": "user", "content": "hi"}}})
		if code != 200 {
			t.Fatalf("请求应 200，实际 %d %s", code, raw)
		}
	}
	m.mu.Lock()
	hdr := append([]map[string]string{}, m.chatHeaders...)
	m.mu.Unlock()
	if hdr[0]["session-id"] != "cli-a" || hdr[1]["session-id"] != "cli-b" {
		t.Fatalf("显式 session_id 未被沿用: %v %v", hdr[0]["session-id"], hdr[1]["session-id"])
	}
}
