package server

// trace_test.go — 请求记录埋点：面板「日志」页的请求记录表读的就是这些事件。

import (
	"net/http/httptest"
	"testing"

	"github.com/misakano7545/deveco2api-panel/internal/reqlog"
)

func onlyEvent(t *testing.T, h *Handler) reqlog.Event {
	t.Helper()
	recent := h.cfg.RequestLog.Snapshot().Recent
	if len(recent) != 1 {
		t.Fatalf("应记录 1 条请求，实际 %d 条", len(recent))
	}
	return recent[0]
}

func TestChatRecordsRequestEvent(t *testing.T) {
	m := newMock()
	ts, h, _ := newTestServer(t, m)

	status, _ := doJSON(t, "POST", ts.URL+"/v1/chat/completions", "test-key",
		map[string]any{"model": "GLM-5.1", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if status != 200 {
		t.Fatalf("status = %d", status)
	}

	e := onlyEvent(t, h)
	if e.Path != "/v1/chat/completions" || e.Model != "GLM-5.1" {
		t.Fatalf("路径/模型不对: %+v", e)
	}
	if e.Status != 200 || !e.OK || e.Outcome != reqlog.OutcomeSuccess {
		t.Fatalf("结果字段不对: %+v", e)
	}
	if e.TotalTokens != 2 || e.RequestID == "" || e.Time.IsZero() {
		t.Fatalf("token/请求 ID/时间不对: %+v", e)
	}
	// 来源：测试里是直连，IP 回落 TCP 对端；UA 由 http 默认客户端给。
	if e.ClientIP == "" || e.UserAgent == "" {
		t.Fatalf("来源未记录: %+v", e)
	}
}

func TestChatRecordsFailureEvent(t *testing.T) {
	m := newMock()
	m.rateLimitNoStream = "hard" // 上游 429 → 网关原样映射 429
	ts, h, _ := newTestServer(t, m)

	status, _ := doJSON(t, "POST", ts.URL+"/v1/chat/completions", "test-key",
		map[string]any{"model": "GLM-5.1", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if status != 429 {
		t.Fatalf("status = %d", status)
	}
	e := onlyEvent(t, h)
	if e.Status != 429 || e.OK || e.Outcome != reqlog.OutcomeHTTPError {
		t.Fatalf("失败事件字段不对: %+v", e)
	}
}

func TestClientIPPreference(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.RemoteAddr = "10.0.0.9:5555"
	if got := clientIP(r); got != "10.0.0.9" {
		t.Fatalf("无代理头应回落 TCP 对端，得到 %q", got)
	}
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.9")
	if got := clientIP(r); got != "1.2.3.4" {
		t.Fatalf("XFF 应取首段，得到 %q", got)
	}
	// 橙云后 XFF 只剩边缘节点，真实来源在 CF-Connecting-IP
	r.Header.Set("CF-Connecting-IP", "5.6.7.8")
	if got := clientIP(r); got != "5.6.7.8" {
		t.Fatalf("应优先 CF-Connecting-IP，得到 %q", got)
	}
}

func TestUsageFromSSE(t *testing.T) {
	tr := &requestTrace{}
	raw := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5,\"total_tokens\":16}}\n\n" +
		"data: [DONE]\n\n"
	tr.setUsageFromSSE(raw)
	if tr.prompt != 11 || tr.completion != 5 || tr.total != 16 {
		t.Fatalf("SSE usage 解析不对: %+v", tr)
	}
}
