// trace.go 请求记录埋点（面板「日志」页的请求记录表数据来源）。
//
// 只记元数据：请求 ID、路径、模型、状态码、耗时、token 计数，以及按配置开关
// 决定是否记录的调用来源（IP / User-Agent）。**不记提示词、响应正文与凭证**。
//
// 埋点用 ResponseWriter 包一层拿状态码，而不是在每个 return 分支手工赋值——
// forward 里有七八处出口，逐个改必然漏。
package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/misakano7545/deveco2api-panel/internal/reqlog"
)

// requestTrace 一次 /v1/chat/completions 的累积状态。
type requestTrace struct {
	id     string
	start  time.Time
	path   string
	model  string
	ip, ua string

	prompt, completion, total int64
}

func newTrace(r *http.Request, model string, recordClient bool) *requestTrace {
	t := &requestTrace{
		id:    reqlog.NewRequestID(),
		start: time.Now(),
		path:  r.URL.Path,
		model: model,
	}
	if recordClient {
		t.ip, t.ua = clientIP(r), r.UserAgent()
	}
	return t
}

// event 把累积状态定格成一条记录（status 由 statusRecorder 提供）。
func (t *requestTrace) event(status int) reqlog.Event {
	e := reqlog.Event{
		Time: t.start, RequestID: t.id, Path: t.path, Model: t.model,
		Status: status, DurationMs: time.Since(t.start).Milliseconds(),
		PromptTokens: t.prompt, CompletionTokens: t.completion, TotalTokens: t.total,
		ClientIP: t.ip, UserAgent: t.ua,
	}
	e.OK = status >= 200 && status < 300
	if e.OK {
		e.Outcome = reqlog.OutcomeSuccess
	} else {
		e.Outcome = reqlog.OutcomeHTTPError
	}
	return e
}

// setUsage 记录上游回传的 usage（非流式响应体里的 usage 对象）。
func (t *requestTrace) setUsage(u any) {
	m, _ := u.(map[string]any)
	if m == nil {
		return
	}
	t.prompt = intField(m["prompt_tokens"])
	t.completion = intField(m["completion_tokens"])
	t.total = intField(m["total_tokens"])
	if t.total == 0 {
		t.total = t.prompt + t.completion
	}
}

// setUsageFromSSE 从 SSE 文本里取最后一个带 usage 的 data 帧（stream_options
// include_usage 时上游会在末帧给用量）。
func (t *requestTrace) setUsageFromSSE(raw string) {
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		p := strings.TrimSpace(line[6:])
		if p == "" || p == "[DONE]" || !strings.Contains(p, "\"usage\"") {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(p), &chunk) != nil {
			continue
		}
		t.setUsage(chunk["usage"])
	}
}

func intField(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
	case string:
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return i
		}
	}
	return 0
}

// clientIP 调用来源：代理头 X-Forwarded-For 首段，无代理头时回落 TCP 对端。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// statusRecorder 捕获下游实际写出的状态码（默认 200：直接 Write 未显式 WriteHeader）。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) code() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}
