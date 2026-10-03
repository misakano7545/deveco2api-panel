package main

// proxy.go — OpenAI 兼容 API 代理（对 proxy.py 的逐行移植）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

func marshalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // 与 Python json.dumps 一致：不转义 < > &
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := marshalJSON(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"detail": detail})
}

func pyFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// ---------------------------------------------------------------- server

type server struct {
	cfg     *Config
	cfgPath string

	chatMu    sync.Mutex
	chatIDs   map[string]string
	refreshMu sync.Mutex

	// ponytail: 上游响应整包读完后一次性转发（与 Python 版行为一致；真流式是后续升级点）
	upstream *http.Client
}

func newServer(cfg *Config, cfgPath string) *server {
	return &server{
		cfg:      cfg,
		cfgPath:  cfgPath,
		chatIDs:  map[string]string{},
		upstream: &http.Client{Timeout: 300 * time.Second},
	}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	return mux
}

func (s *server) authOK(w http.ResponseWriter, r *http.Request) bool {
	expected := s.cfg.Server.APIKey
	if expected == "" {
		return true
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token != expected {
		writeError(w, http.StatusUnauthorized, "Invalid API key")
		return false
	}
	return true
}

func (s *server) chatIDFor(session string) string {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if id, ok := s.chatIDs[session]; ok {
		return id
	}
	id := chatID()
	s.chatIDs[session] = id
	return id
}

func (s *server) buildHeaders(sessionIDValue, userMsgID string) map[string]string {
	return map[string]string{
		"Authorization":    "Bearer " + s.cfg.DevEco.Auth.AccessToken,
		"Content-Type":     "application/json",
		"Chat-Id":          s.chatIDFor(sessionIDValue),
		"Session-Id":       sessionIDValue,
		"x-deveco-client":  s.cfg.DevEco.Client,
		"x-deveco-project": s.cfg.DevEco.Project,
		"x-deveco-request": userMsgID,
		"x-deveco-session": sessionIDValue,
		"User-Agent":       s.cfg.DevEco.UserAgent,
		"lang":             "en",
		"Accept":           "*/*",
	}
}

// refreshToken — 使用 jwt_token 刷新 access_token，成功则更新内存与配置文件。
func (s *server) refreshToken() bool {
	jwt := s.cfg.DevEco.Auth.JWTToken
	if jwt == "" {
		return false
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	data, err := refreshAccessToken(s.cfg.DevEco.BaseURL, jwt)
	if err != nil {
		logError("刷新 access_token 失败: %v", err)
		return false
	}
	applyUserInfo(s.cfg, data)
	if err := s.cfg.save(); err != nil {
		logWarn("保存配置失败: %v", err)
	}
	logInfo("access_token 刷新成功")
	return true
}

func (s *server) startKeepalive() {
	hours := s.cfg.DevEco.KeepaliveHours
	if hours <= 0 {
		logInfo("token 保活未启用（deveco.keepalive_hours = 0）")
		return
	}
	logInfo("token 保活已启用：每 %s 小时自动刷新一次", pyFloat(hours))
	go func() {
		fails := 0
		for {
			time.Sleep(time.Duration(hours * 3600 * float64(time.Second)))
			if s.refreshToken() {
				fails = 0
				logInfo("token 保活刷新成功")
			} else {
				fails++
				if fails >= 3 {
					logError("token 保活连续 %d 次失败，如持续失败请重新登录: main --login", fails)
				} else {
					logWarn("token 保活刷新失败（连续 %d 次）", fails)
				}
			}
		}
	}()
}

// ---------------------------------------------------------------- /v1/models

func (s *server) fetchModelConfig() (map[string]any, int, error) {
	u := strings.TrimRight(s.cfg.DevEco.BaseURL, "/") + "/codeGenie/modelConfig?localVersion=0&pluginVersion=CLI.0.2.0"
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.DevEco.Auth.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.cfg.DevEco.UserAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := s.upstream.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, 200, err
	}
	return data, 200, nil
}

func (s *server) handleModels(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(w, r) {
		return
	}
	data, status, err := s.fetchModelConfig()
	if err != nil {
		if status == 401 || status == 403 {
			if s.refreshToken() {
				data, status, err = s.fetchModelConfig()
			}
		}
		if err != nil {
			logError("获取模型列表失败: %v (HTTP %d)", err, status)
			writeError(w, http.StatusBadGateway, fmt.Sprintf("Upstream error: %v", err))
			return
		}
	}
	// token 失效时上游可能返回 200 + errorCode（不抛 HTTP 错误）：刷新后重试一次
	if !(data["success"] == true || data["code"] == float64(200)) {
		if s.refreshToken() {
			if d2, _, err2 := s.fetchModelConfig(); err2 == nil {
				data = d2
			}
		}
	}

	models := []map[string]any{}
	if body, ok := data["body"].(map[string]any); ok {
		if groups, ok := body["inner_models"].([]any); ok {
			for _, g := range groups {
				gm, _ := g.(map[string]any)
				if gm == nil {
					continue
				}
				owned := strOf(gm["group_name"])
				if owned == "" {
					owned = "deveco"
				}
				cfgs, _ := gm["model_configs"].([]any)
				for _, c := range cfgs {
					cm, _ := c.(map[string]any)
					if cm == nil {
						continue
					}
					id := strOf(cm["model_id"])
					if id == "" {
						continue
					}
					models = append(models, map[string]any{
						"id": id, "object": "model", "created": time.Now().Unix(), "owned_by": owned,
					})
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
}

// ---------------------------------------------------------------- /v1/chat/completions

func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(w, r) {
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
		return
	}
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
		return
	}

	stream, _ := req["stream"].(bool)
	sessionIDValue := strOf(req["session_id"])
	if sessionIDValue == "" {
		sessionIDValue = sessionID()
	}
	userMsgID := messageID()
	devecoBody := buildDevEcoBody(s.cfg, req)

	base := strings.TrimRight(s.cfg.DevEco.BaseURL, "/")
	path := "/sse/codeGenie/maas/v2"
	url := base + path + "/chat/completions"
	if !stream {
		url = base + path + "/no-stream/chat/completions"
	}
	logInfo("POST %s model=%s stream=%v", url, strOf(devecoBody["model"]), stream)

	s.callUpstream(w, url, sessionIDValue, userMsgID, devecoBody, stream)
}

func (s *server) thinkingModel(model string) bool {
	for _, m := range s.cfg.DevEco.ThinkingModels {
		if m == model {
			return true
		}
	}
	return false
}

func (s *server) callUpstream(w http.ResponseWriter, url, sessionIDValue, userMsgID string, devecoBody map[string]any, stream bool) {
	stripThinking := s.thinkingModel(strOf(devecoBody["model"]))

	post := func() (*http.Response, []byte, error) {
		payload, err := marshalJSON(devecoBody)
		if err != nil {
			return nil, nil, err
		}
		req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
		if err != nil {
			return nil, nil, err
		}
		for k, v := range s.buildHeaders(sessionIDValue, userMsgID) {
			req.Header.Set(k, v)
		}
		resp, err := s.upstream.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, nil, err
		}
		return resp, body, nil
	}

	resp, body, err := post()
	if err != nil {
		logError("上游请求异常: %v", err)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("Upstream error: %v", err))
		return
	}

	// 401 保留刷新 token 后重试的路径
	if resp.StatusCode == 401 {
		if s.refreshToken() {
			resp, body, err = post()
			if err != nil {
				logError("上游请求异常: %v", err)
				writeError(w, http.StatusBadGateway, fmt.Sprintf("Upstream error: %v", err))
				return
			}
		}
		if resp.StatusCode == 401 {
			logError("上游请求失败 HTTP 401: %s", trunc(string(body), 500))
			writeError(w, http.StatusBadGateway, fmt.Sprintf("Upstream HTTP error: %s", trunc(string(body), 500)))
			return
		}
	}
	// 非 200 的上游错误：统一摘出 error 对象并按语义映射状态码（如限流 → 429）
	if resp.StatusCode != 200 {
		e := extractHTTPError(resp.StatusCode, body)
		status := upstreamErrorStatus(e)
		logWarn("上游 HTTP %d: %v", status, e)
		writeJSON(w, status, map[string]any{"error": e})
		return
	}
	if stream {
		if e := extractSSEError(string(body)); e != nil {
			status := upstreamErrorStatus(e)
			logWarn("上游流式错误 HTTP %d: %v", status, e)
			writeJSON(w, status, map[string]any{"error": e})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, transformSSE(string(body), stripThinking))
		return
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("Upstream error: invalid JSON: %v", err))
		return
	}
	if _, hasErr := data["error"]; hasErr {
		if _, hasChoices := data["choices"]; !hasChoices {
			e, _ := data["error"].(map[string]any)
			if e == nil {
				e = map[string]any{"message": strOf(data["error"])}
			}
			status := upstreamErrorStatus(e)
			logWarn("上游错误 HTTP %d: %v", status, e)
			writeJSON(w, status, map[string]any{"error": e})
			return
		}
	}
	stripThinkingNonstream(data)
	writeJSON(w, http.StatusOK, data)
}

// ---------------------------------------------------------------- 请求体构造

func normalizeMessages(messages []any) []map[string]any {
	out := []map[string]any{}
	for _, m := range messages {
		mm, _ := m.(map[string]any)
		if mm == nil {
			continue
		}
		role := strOf(mm["role"])
		if role == "" {
			role = "user"
		}
		content, hasContent := mm["content"]
		if !hasContent {
			content = ""
		}
		if parts, ok := content.([]any); ok {
			var texts []string
			for _, p := range parts {
				pm, _ := p.(map[string]any)
				if pm == nil {
					continue
				}
				switch strOf(pm["type"]) {
				case "text":
					texts = append(texts, strOf(pm["text"]))
				case "image_url":
					iu, _ := pm["image_url"].(map[string]any)
					texts = append(texts, "[image: "+strOf(iu["url"])+"]")
				}
			}
			content = strings.Join(texts, "\n")
		}
		item := map[string]any{"role": role, "content": content}
		for _, k := range []string{"name", "tool_calls", "tool_call_id", "function_call"} {
			if v, ok := mm[k]; ok {
				item[k] = v
			}
		}
		out = append(out, item)
	}
	return out
}

func buildDevEcoBody(cfg *Config, req map[string]any) map[string]any {
	model := strOf(req["model"])
	if model == "" {
		model = cfg.DevEco.Model
	}
	msgsRaw, _ := req["messages"].([]any)
	messages := normalizeMessages(msgsRaw)
	stream, _ := req["stream"].(bool)

	var maxTokens any = float64(32000)
	if v, ok := req["max_tokens"]; ok {
		maxTokens = v
	}

	body := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": maxTokens,
		"stream":     stream,
	}
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}

	if v, ok := req["tools"]; ok {
		body["tools"] = v
	}
	if v, ok := req["tool_choice"]; ok {
		switch tc := v.(type) {
		case map[string]any:
			// DevEco 后端只接受字符串枚举，OpenAI 对象形式统一映射为 required
			if strOf(tc["type"]) == "function" {
				body["tool_choice"] = "required"
			} else {
				body["tool_choice"] = "auto"
			}
		case string:
			if tc == "none" || tc == "auto" || tc == "required" {
				body["tool_choice"] = tc
			} else {
				body["tool_choice"] = "auto"
			}
		default:
			body["tool_choice"] = "auto"
		}
	}
	for _, key := range []string{"temperature", "top_p", "frequency_penalty", "presence_penalty", "stop", "seed"} {
		if v, ok := req[key]; ok {
			body[key] = v
		}
	}
	return body
}

// ---------------------------------------------------------------- 思维链剥离 / 错误转译

const thinkEnd = "</think>"

func stripThinkingNonstream(payload map[string]any) {
	choices, _ := payload["choices"].([]any)
	for _, c := range choices {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		msg, _ := cm["message"].(map[string]any)
		if msg == nil {
			continue
		}
		content, _ := msg["content"].(string)
		idx := strings.Index(content, thinkEnd)
		if idx < 0 {
			continue
		}
		think, answer := content[:idx], content[idx+len(thinkEnd):]
		msg["content"] = strings.TrimLeftFunc(answer, unicode.IsSpace)
		if strings.TrimSpace(think) != "" {
			msg["reasoning_content"] = think
		}
	}
}

func sseChunk(template map[string]any, delta map[string]any) string {
	chunk := map[string]any{}
	for _, k := range []string{"id", "object", "created", "model"} {
		if v, ok := template[k]; ok {
			chunk[k] = v
		}
	}
	if _, ok := chunk["object"]; !ok {
		chunk["object"] = "chat.completion.chunk"
	}
	chunk["choices"] = []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}
	b, _ := marshalJSON(chunk)
	return "data: " + string(b) + "\n\n"
}

// transformSSE — 转发上游 SSE；strip=true 时把首个 </think> 之前的思维链
// 转入 reasoning_content（跨 chunk 兼容；流结束仍无边界则整段按正文兜底）。
func transformSSE(raw string, strip bool) string {
	var out strings.Builder
	holding := strip
	buf := ""
	var template map[string]any

	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(line[6:])
		if payload == "" { // 上游偶发空 data: 帧，直接跳过
			continue
		}
		if payload == "[DONE]" {
			if holding && buf != "" {
				out.WriteString(sseChunk(template, map[string]any{"content": buf}))
			}
			out.WriteString("data: [DONE]\n\n")
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			out.WriteString("data: " + payload + "\n\n")
			continue
		}
		template = chunk

		choices, _ := chunk["choices"].([]any)
		for _, c := range choices {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			d, _ := cm["delta"].(map[string]any)
			if d == nil {
				continue
			}
			if _, ok := d["role"]; !ok {
				d["role"] = "assistant"
			}
		}

		if holding && len(choices) > 0 {
			// ponytail: 上游恒为单 choice，思维链剥离只处理第一个 choice
			cm, _ := choices[0].(map[string]any)
			d, _ := cm["delta"].(map[string]any)
			content, _ := d["content"].(string)
			if content != "" {
				buf += content
				if i := strings.Index(buf, thinkEnd); i >= 0 {
					think, rest := buf[:i], buf[i+len(thinkEnd):]
					holding, buf = false, ""
					if strings.TrimSpace(think) != "" {
						out.WriteString(sseChunk(template, map[string]any{"role": "assistant", "reasoning_content": think}))
					}
					d["content"] = strings.TrimLeftFunc(rest, unicode.IsSpace)
				} else {
					d["content"] = "" // 思维链阶段：内容暂存缓冲，仅透传结构帧保活
				}
			}
			if holding && buf != "" && cm["finish_reason"] != nil {
				// 流结束仍无 </think>：整段按正文兜底（宁可不剥离，也不丢内容）
				out.WriteString(sseChunk(template, map[string]any{"content": buf}))
				holding, buf = false, ""
			}
		}

		b, err := marshalJSON(chunk)
		if err != nil {
			continue
		}
		out.WriteString("data: " + string(b) + "\n\n")
	}
	return out.String()
}

func extractHTTPError(status int, body []byte) map[string]any {
	var obj map[string]any
	if json.Unmarshal(body, &obj) == nil {
		if e, ok := obj["error"].(map[string]any); ok {
			return e
		}
		for _, k := range []string{"errorMsg", "message", "detail"} {
			if msg := strOf(obj[k]); msg != "" {
				return map[string]any{"message": msg, "code": strconv.Itoa(status)}
			}
		}
	}
	return map[string]any{"message": fmt.Sprintf("Upstream HTTP %d: %s", status, trunc(string(body), 300)), "code": strconv.Itoa(status)}
}

func extractSSEError(text string) map[string]any {
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(line[6:])), &obj) != nil {
			continue
		}
		if e, ok := obj["error"].(map[string]any); ok {
			return e
		}
	}
	return nil
}

func upstreamErrorStatus(err map[string]any) int {
	if strOf(err["type"]) == "UserSessionLimitExceeded" {
		return http.StatusTooManyRequests
	}
	if code, err2 := strconv.Atoi(strOf(err["code"])); err2 == nil && code >= 400 && code <= 599 {
		return code
	}
	return http.StatusBadGateway
}
