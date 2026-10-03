// errors.go 上游错误转译：上游有 3 种错误形态，统一摘出 error 对象并按语义映射状态码。
//
//	形态 1：HTTP 4xx/5xx + error 对象（或 errorMsg/message/detail 平铺）
//	形态 2：HTTP 200 + SSE error 帧（限流常走这条）
//	形态 3：HTTP 200 + error 对象（token 失效、业务错误）
//
// 客户端只该看到干净的 JSON + 正确状态码（限流 → 429），而不是一坨上游原文。
package upstream

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/misakano7545/deveco2api-panel/internal/jsonval"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
)

// ExtractHTTPError 从非 200 响应体里摘出错对象；摘不到就生成一条带原文摘要的。
func ExtractHTTPError(status int, body []byte) map[string]any {
	var obj map[string]any
	if json.Unmarshal(body, &obj) == nil {
		if e, ok := obj["error"].(map[string]any); ok {
			return e
		}
		for _, k := range []string{"errorMsg", "message", "detail"} {
			if msg := jsonval.Str(obj[k]); msg != "" {
				return map[string]any{"message": msg, "code": strconv.Itoa(status)}
			}
		}
	}
	return map[string]any{
		"message": "Upstream HTTP " + strconv.Itoa(status) + ": " + logfmt.Truncate(string(body), 300),
		"code":    strconv.Itoa(status),
	}
}

// ExtractSSEError 从 200 的 SSE 文本里找 error 帧（没有则 nil）。
func ExtractSSEError(text string) map[string]any {
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		p := strings.TrimSpace(line[6:])
		if p == "" {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(p), &obj) != nil {
			continue
		}
		if e, ok := obj["error"].(map[string]any); ok {
			return e
		}
	}
	return nil
}

// ErrorStatus 上游错误 → 客户端可见 HTTP 状态码：限流 429，带 4xx/5xx code 的沿用，
// 其余（含上游未给码的）按 502 处理。
func ErrorStatus(e map[string]any) int {
	if jsonval.Str(e["type"]) == "UserSessionLimitExceeded" {
		return http.StatusTooManyRequests
	}
	if code, err := strconv.Atoi(jsonval.Str(e["code"])); err == nil && code >= 400 && code <= 599 {
		return code
	}
	return http.StatusBadGateway
}
