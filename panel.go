package main

// panel.go — 内嵌 Web 控制台（单账号：概览 / 模型 / 日志）。
// 参考 workbuddy2api-panel：go:embed 进二进制、/panel/ 挂载、Bearer api_key 与
// /v1/* 同口径（空 key = 不鉴权）、页面本身无秘密（密钥只发给 /panel/api/*）、严格 CSP。

import (
	_ "embed"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

//go:embed panel.html
var panelHTML []byte

//go:embed panel.js
var panelJS []byte

const panelCSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"connect-src 'self'; img-src 'self' data:; form-action 'none'; frame-ancestors 'none'; base-uri 'none'"

func panelSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", panelCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func (s *server) registerPanel(mux *http.ServeMux) {
	mux.HandleFunc("/panel", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/panel/", http.StatusFound)
	})
	mux.HandleFunc("/panel/", s.handlePanelIndex)
	mux.HandleFunc("/panel/panel.js", s.handlePanelJS)
	mux.HandleFunc("/panel/api/status", s.handlePanelStatus)
	mux.HandleFunc("/panel/api/models", s.handlePanelModels)
	mux.HandleFunc("/panel/api/logs", s.handlePanelLogs)
}

func (s *server) handlePanelIndex(w http.ResponseWriter, r *http.Request) {
	panelSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(panelHTML)
}

func (s *server) handlePanelJS(w http.ResponseWriter, r *http.Request) {
	panelSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(panelJS)
}

// jwtDaysLeft — jwt 剩余有效天数（无法解析时 nil）。
func jwtDaysLeft(jwt string) *float64 {
	claims := jwtClaims(jwt)
	if claims == nil {
		return nil
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= 0 {
		return nil
	}
	d := time.Until(time.Unix(int64(exp), 0)).Hours() / 24
	return &d
}

func (s *server) handlePanelStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(w, r) {
		return
	}
	auth := s.cfg.DevEco.Auth
	s.refreshMu.Lock()
	lastAt, lastOK := s.lastRefreshAt, s.lastRefreshOK
	s.refreshMu.Unlock()

	var lastRefresh any
	if !lastAt.IsZero() {
		lastRefresh = map[string]any{"at": lastAt.Format("15:04:05"), "ok": lastOK}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service":        "deveco2api-panel",
		"version":        appVersion,
		"listen":         fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port),
		"config_path":    s.cfgPath,
		"auth_required":  s.cfg.Server.APIKey != "",
		"uptime_seconds": int(time.Since(s.startedAt).Seconds()),
		"account": map[string]any{
			"name": auth.UserName, "id": auth.UserID, "jwt_days_left": jwtDaysLeft(auth.JWTToken),
		},
		"keepalive": map[string]any{
			"hours": s.cfg.DevEco.KeepaliveHours, "last_refresh": lastRefresh,
		},
		"upstream":        s.cfg.DevEco.BaseURL,
		"model_default":   s.cfg.DevEco.Model,
		"thinking_models": s.cfg.DevEco.ThinkingModels,
	})
}

func (s *server) handlePanelModels(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(w, r) {
		return
	}
	data, status, err := s.fetchModelConfig()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": fmt.Sprintf("上游查询失败 (HTTP %d): %v", status, err)})
		return
	}
	// token 失效时上游可能返回 200 + errorCode：刷新后重试一次
	if !(data["success"] == true || data["code"] == float64(200)) {
		if !s.refreshToken() {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "上游鉴权失败（token 失效且刷新失败）"})
			return
		}
		data, _, err = s.fetchModelConfig()
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
			id := strOf(cm["model_id"])
			if id == "" {
				continue
			}
			rows = append(rows, map[string]any{
				"id": id, "group": strOf(gm["group_name"]), "group_cn": strOf(gm["group_name_cn"]),
				"context_window": cm["context_window"], "output": cm["output"],
				"thinking": strOf(cm["thinking_mode"]), "tools": strOf(cm["tool_call_mode"]),
				"thinking_stripped": s.thinkingModel(id),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models": rows, "count": len(rows), "at": time.Now().Format("15:04:05"),
	})
}

func (s *server) handlePanelLogs(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(w, r) {
		return
	}
	limit := 300
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= logRingMax {
		limit = v
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": recentLogs(limit)})
}
