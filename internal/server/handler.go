// Package server OpenAI 兼容网关的 HTTP 表面：/v1/models、/v1/chat/completions、
// /health，并挂载 /panel/。鉴权与面板同口径（internal/httpauth，空 api_key = 放行）。
//
// 分工：跟华为说话全在 internal/upstream（含 SSE 转发与错误转译），这里只做
// HTTP 编排——鉴权、会话粘性 chat-id、401 刷新重试、状态码映射、写响应。
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/misakano7545/deveco2api-panel/internal/auth"
	"github.com/misakano7545/deveco2api-panel/internal/httpauth"
	"github.com/misakano7545/deveco2api-panel/internal/jsonval"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
	"github.com/misakano7545/deveco2api-panel/internal/session"
	"github.com/misakano7545/deveco2api-panel/internal/upstream"
)

// Config 网关依赖（main 装配注入）。
type Config struct {
	Version        string
	APIKey         string // 空 = 不鉴权（仅本机/私网用法）
	Listen         string // 观测展示用
	Upstream       *upstream.Client
	Auth           *auth.Store
	Panel          http.Handler // nil = 不挂载 /panel/
	KeepaliveHours float64      // <=0 不启用保活
}

// Handler 网关 handler。
type Handler struct {
	cfg Config
	mux *http.ServeMux

	chatMu  sync.Mutex
	chatIDs map[string]string // session_id → chat-id（同一会话沿用同一 chat-id）

	stop      chan struct{}
	startedAt time.Time
}

// New 构建网关。
func New(cfg Config) *Handler {
	h := &Handler{
		cfg:       cfg,
		mux:       http.NewServeMux(),
		chatIDs:   map[string]string{},
		stop:      make(chan struct{}),
		startedAt: time.Now(),
	}
	h.routes()
	return h
}

// ServeHTTP 主入口（ListenAndServe 用它）。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// Uptime 进程运行时长（诊断/展示）。
func (h *Handler) Uptime() time.Duration { return time.Since(h.startedAt) }

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	h.mux.HandleFunc("GET /v1/models", h.withAuth(h.models))
	h.mux.HandleFunc("POST /v1/chat/completions", h.withAuth(h.chat))
	if h.cfg.Panel != nil {
		h.mux.Handle("/panel/", h.cfg.Panel) // /panel → /panel/ 由 ServeMux 自动重定向
	}
	h.mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		if h.cfg.Panel != nil {
			http.Redirect(w, r, "/panel/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
}

// withAuth Bearer 鉴权（空 api_key 恒放行）。401 文案与 Python 版一致。
func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !httpauth.VerifyBearer(r, h.cfg.APIKey) {
			writeError(w, http.StatusUnauthorized, "Invalid API key")
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := jsonval.Marshal(v)
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

// pyFloat 按 Python str(float) 的形态打印（6 → "6.0"），保持日志口径一致。
func pyFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func (h *Handler) chatIDFor(sessionID string) string {
	h.chatMu.Lock()
	defer h.chatMu.Unlock()
	if id, ok := h.chatIDs[sessionID]; ok {
		return id
	}
	id := session.ChatID()
	h.chatIDs[sessionID] = id
	return id
}

// ---------------------------------------------------------------- 保活

// StartKeepalive 起后台保活：每 keepalive_hours 刷一次 access_token
// （jwt 约 30 天，上游 token 无过期字段可读，只能按间隔刷 + 请求侧 401 兜底）。
func (h *Handler) StartKeepalive() {
	hours := h.cfg.KeepaliveHours
	if hours <= 0 {
		logfmt.Infof("token 保活未启用（deveco.keepalive_hours = 0）")
		return
	}
	logfmt.Infof("token 保活已启用：每 %s 小时自动刷新一次", pyFloat(hours))
	interval := time.Duration(hours * 3600 * float64(time.Second))
	go func() {
		fails := 0
		for {
			select {
			case <-h.stop:
				return
			case <-time.After(interval):
			}
			if h.cfg.Auth.Refresh() {
				fails = 0
				logfmt.Infof("token 保活刷新成功")
				continue
			}
			fails++
			if fails >= 3 {
				logfmt.Errorf("token 保活连续 %d 次失败，如持续失败请重新登录: deveco2api-login", fails)
			} else {
				logfmt.Warnf("token 保活刷新失败（连续 %d 次）", fails)
			}
		}
	}()
}

// StopKeepalive 停保活循环（测试与优雅退出用）。
func (h *Handler) StopKeepalive() { close(h.stop) }

// ---------------------------------------------------------------- /v1/models

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	data, status, err := h.cfg.Upstream.ModelConfig()
	if err != nil {
		// token 失效有时是 HTTP 401/403，刷新后重试一次
		if status == 401 || status == 403 {
			if h.cfg.Auth.Refresh() {
				data, status, err = h.cfg.Upstream.ModelConfig()
			}
		}
		if err != nil {
			logfmt.Errorf("获取模型列表失败: %v (HTTP %d)", err, status)
			writeError(w, http.StatusBadGateway, fmt.Sprintf("Upstream error: %v", err))
			return
		}
	}
	// token 失效时上游也可能返回 200 + errorCode（不抛 HTTP 错误）：刷新后重试一次
	if !(data["success"] == true || data["code"] == float64(200)) {
		if h.cfg.Auth.Refresh() {
			if d2, _, err2 := h.cfg.Upstream.ModelConfig(); err2 == nil {
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
				owned := jsonval.Str(gm["group_name"])
				if owned == "" {
					owned = "deveco"
				}
				cfgs, _ := gm["model_configs"].([]any)
				for _, c := range cfgs {
					cm, _ := c.(map[string]any)
					if cm == nil {
						continue
					}
					id := jsonval.Str(cm["model_id"])
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

func (h *Handler) chat(w http.ResponseWriter, r *http.Request) {
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
	sessionID := jsonval.Str(req["session_id"])
	if sessionID == "" {
		sessionID = session.SessionID()
	}
	msgID := session.MessageID()
	body := h.cfg.Upstream.BuildBody(req)
	logfmt.Infof("POST %s model=%s stream=%v", h.cfg.Upstream.URL(stream), jsonval.Str(body["model"]), stream)

	h.forward(w, sessionID, msgID, h.chatIDFor(sessionID), body, stream)
}

func (h *Handler) forward(w http.ResponseWriter, sessionID, msgID, chatID string, body map[string]any, stream bool) {
	stripThinking := h.cfg.Upstream.StripsThinking(jsonval.Str(body["model"]))

	post := func() (int, []byte, error) {
		// token 每次现取（upstream 侧回调 Auth.Tokens），刷新后重试自然带新 token
		return h.cfg.Upstream.Chat(sessionID, msgID, chatID, body)
	}

	status, raw, err := post()
	if err != nil {
		logfmt.Errorf("上游请求异常: %v", err)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("Upstream error: %v", err))
		return
	}

	// 401 保留刷新 token 后重试的路径
	if status == 401 {
		if h.cfg.Auth.Refresh() {
			status, raw, err = post()
			if err != nil {
				logfmt.Errorf("上游请求异常: %v", err)
				writeError(w, http.StatusBadGateway, fmt.Sprintf("Upstream error: %v", err))
				return
			}
		}
		if status == 401 {
			logfmt.Errorf("上游请求失败 HTTP 401: %s", logfmt.Truncate(string(raw), 500))
			writeError(w, http.StatusBadGateway, fmt.Sprintf("Upstream HTTP error: %s", logfmt.Truncate(string(raw), 500)))
			return
		}
	}
	// 非 200 的上游错误：统一摘出 error 对象并按语义映射状态码（如限流 → 429）
	if status != 200 {
		e := upstream.ExtractHTTPError(status, raw)
		code := upstream.ErrorStatus(e)
		logfmt.Warnf("上游 HTTP %d: %v", code, e)
		writeJSON(w, code, map[string]any{"error": e})
		return
	}
	if stream {
		if e := upstream.ExtractSSEError(string(raw)); e != nil {
			code := upstream.ErrorStatus(e)
			logfmt.Warnf("上游流式错误 HTTP %d: %v", code, e)
			writeJSON(w, code, map[string]any{"error": e})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, upstream.TransformSSE(string(raw), stripThinking))
		return
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("Upstream error: invalid JSON: %v", err))
		return
	}
	if _, hasErr := data["error"]; hasErr {
		if _, hasChoices := data["choices"]; !hasChoices {
			e, _ := data["error"].(map[string]any)
			if e == nil {
				e = map[string]any{"message": jsonval.Str(data["error"])}
			}
			code := upstream.ErrorStatus(e)
			logfmt.Warnf("上游错误 HTTP %d: %v", code, e)
			writeJSON(w, code, map[string]any{"error": e})
			return
		}
	}
	upstream.StripThinkingNonstream(data)
	writeJSON(w, http.StatusOK, data)
}
