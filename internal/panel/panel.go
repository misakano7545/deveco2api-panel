// Package panel 内嵌 Web 控制台（单账号：概览 / 模型 / 日志）。
//
// 设计约束（与 workbuddy2api-panel 同口径）：
//   - 前端 go:embed 进二进制，无外部构建步骤，随服务部署；
//   - 鉴权复用网关 api_key（Bearer），与 /v1/* 完全同口径；空 key = 不鉴权；
//   - 面板页面本身无秘密，可匿名加载，密钥只发给 /panel/api/*；
//   - 严格 CSP + 安全响应头（index.go），页面与 API 响应统一带上。
//
// 只读面板：不改写任何网关语义，运维动作（登录、改配置）走 CLI。
package panel

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/misakano7545/deveco2api-panel/internal/auth"
	"github.com/misakano7545/deveco2api-panel/internal/httpauth"
	"github.com/misakano7545/deveco2api-panel/internal/jsonval"
	"github.com/misakano7545/deveco2api-panel/internal/upstream"
)

// Config 面板依赖（main 装配注入）。
type Config struct {
	Version        string
	APIKey         string // 空 = 不鉴权（与 /v1/* 同语义）
	Listen         string
	ConfigPath     string
	UpstreamURL    string
	ModelDefault   string
	ThinkingModels []string
	KeepaliveHours float64

	Auth     *auth.Store      // 账号信息与最近刷新状态
	Upstream *upstream.Client // 模型列表实时查询
	Logs     *Ring            // nil = New 内部建一个（仅测试会这样用）

	// ImportAuth 导入凭证（cmd 注入：换内存 token + 落盘，返回被替换的旧身份）。
	// nil = 本实例未启用导入（接口回 501）。这是面板唯一的写入口。
	ImportAuth func(auth.Tokens) (auth.Tokens, error)

	StartedAt time.Time // 零值 = New 时刻
}

// Panel 控制台 handler。挂载方式：外层 mux Handle("/panel/", panel)，
// 本 mux 的 pattern 均带 /panel 前缀（外层不做前缀剥离）。
type Panel struct {
	cfg     Config
	mux     *http.ServeMux
	logs    *Ring
	started time.Time
}

// New 构建面板。
func New(cfg Config) *Panel {
	if cfg.Logs == nil {
		cfg.Logs = NewRing(500)
	}
	started := cfg.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	p := &Panel{cfg: cfg, mux: http.NewServeMux(), logs: cfg.Logs, started: started}
	p.routes()
	return p
}

// Logs 返回日志环形缓冲（装配期把 logfmt sink 指过来）。
func (p *Panel) Logs() *Ring { return p.logs }

func (p *Panel) routes() {
	p.mux.HandleFunc("GET /panel/{$}", p.index)
	p.mux.HandleFunc("GET /panel/app.js", p.appScript)
	p.mux.HandleFunc("GET /panel/api/status", p.withAuth(p.status))
	p.mux.HandleFunc("GET /panel/api/models", p.withAuth(p.models))
	p.mux.HandleFunc("GET /panel/api/logs", p.withAuth(p.logsHandler))
	p.mux.HandleFunc("POST /panel/api/import/config", p.withAuth(p.importConfig))
}

// ServeHTTP 统一入口：先写安全响应头再分发，保证页面、静态资源、API
// 与 401 响应全都带上（API 也可能在浏览器里被直接打开）。
func (p *Panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	p.mux.ServeHTTP(w, r)
}

// withAuth 与网关同口径的 Bearer 鉴权（经 httpauth 常量时间比较）。
func (p *Panel) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !httpauth.VerifyBearer(r, p.cfg.APIKey) {
			writeErr(w, http.StatusUnauthorized, "Invalid API key")
			return
		}
		next(w, r)
	}
}

// ---------------------------------------------------------------- 只读接口

// status 概览：账号 / 保活 / 端点 / 版本。
func (p *Panel) status(w http.ResponseWriter, r *http.Request) {
	tok := p.cfg.Auth.Tokens()
	lastAt, lastOK, lastErr := p.cfg.Auth.LastRefresh()

	var lastRefresh any
	if !lastAt.IsZero() {
		entry := map[string]any{"at": lastAt.Format("15:04:05"), "ok": lastOK}
		if lastErr != "" {
			entry["error"] = lastErr
		}
		lastRefresh = entry
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service":        "deveco2api-panel",
		"version":        p.cfg.Version,
		"listen":         p.cfg.Listen,
		"config_path":    p.cfg.ConfigPath,
		"auth_required":  p.cfg.APIKey != "",
		"uptime_seconds": int(time.Since(p.started).Seconds()),
		"account": map[string]any{
			"name": tok.UserName, "id": tok.UserID, "jwt_days_left": auth.JWTDaysLeft(tok.JWTToken),
		},
		"keepalive": map[string]any{
			"hours": p.cfg.KeepaliveHours, "last_refresh": lastRefresh,
		},
		"upstream":        p.cfg.UpstreamURL,
		"model_default":   p.cfg.ModelDefault,
		"thinking_models": p.cfg.ThinkingModels,
	})
}

// models 账号可见的模型列表（实时查上游；token 失效时刷一次再查）。
func (p *Panel) models(w http.ResponseWriter, r *http.Request) {
	data, status, err := p.cfg.Upstream.ModelConfig()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": fmt.Sprintf("上游查询失败 (HTTP %d): %v", status, err)})
		return
	}
	if !(data["success"] == true || data["code"] == float64(200)) {
		if !p.cfg.Auth.Refresh() {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "上游鉴权失败（token 失效且刷新失败）"})
			return
		}
		data, _, err = p.cfg.Upstream.ModelConfig()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": fmt.Sprintf("刷新后查询失败: %v", err)})
			return
		}
	}

	rows := []map[string]any{}
	body, _ := data["body"].(map[string]any)
	groups, _ := body["inner_models"].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		if gm == nil {
			continue
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
			rows = append(rows, map[string]any{
				"id": id, "group": jsonval.Str(gm["group_name"]), "group_cn": jsonval.Str(gm["group_name_cn"]),
				"context_window": cm["context_window"], "output": cm["output"],
				"thinking": jsonval.Str(cm["thinking_mode"]), "tools": jsonval.Str(cm["tool_call_mode"]),
				"thinking_stripped": p.cfg.Upstream.StripsThinking(id),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models": rows, "count": len(rows), "at": time.Now().Format("15:04:05"),
	})
}

// logsHandler 环形缓冲快照（limit 上限为环容量）。
func (p *Panel) logsHandler(w http.ResponseWriter, r *http.Request) {
	limit := 300
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= p.logs.Capacity() {
		limit = v
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": p.logs.Snapshot(limit)})
}

// writeJSON/writeErr 面板响应 helper（与网关同口径：不转义 < > &）。
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

func writeErr(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"detail": detail})
}
