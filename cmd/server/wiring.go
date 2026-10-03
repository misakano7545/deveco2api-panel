package main

// wiring.go 装配：把 config.toml 的字段接到各 internal 组件的依赖上。
// 依赖接线只在这里和 cmd/login 出现，internal 包之间不互相构造。

import (
	"github.com/misakano7545/deveco2api-panel/internal/auth"
	"github.com/misakano7545/deveco2api-panel/internal/config"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
	"github.com/misakano7545/deveco2api-panel/internal/panel"
	"github.com/misakano7545/deveco2api-panel/internal/server"
	"github.com/misakano7545/deveco2api-panel/internal/upstream"
)

// panelRingMax 面板日志环容量（「日志」页可见的最近行数）。
const panelRingMax = 500

// services 装配产物。
type services struct {
	gateway *server.Handler
	console *panel.Panel
	auth    *auth.Store
}

// wire 按配置装配全部组件；listen 为实际生效的 host:port（面板展示用）。
func wire(cfg *config.Config, cfgPath, listen string) *services {
	logfmt.SetLevel(cfg.Logging.Level)

	ring := panel.NewRing(panelRingMax)
	logfmt.SetSink(ring.Add) // stdout 仍是主出口，环只是面板的观测窗口

	store := auth.New(auth.Config{
		BaseURL:      cfg.DevEco.BaseURL,
		AppID:        cfg.DevEco.AppID,
		AuthURL:      cfg.DevEco.AuthURL,
		CallbackPort: cfg.DevEco.CallbackPort,
	}, cfg.Tokens(), cfg.SaveTokens)

	up := upstream.New(upstream.Config{
		BaseURL:        cfg.DevEco.BaseURL,
		Client:         cfg.DevEco.Client,
		Project:        cfg.DevEco.Project,
		UserAgent:      cfg.DevEco.UserAgent,
		Model:          cfg.DevEco.Model,
		ThinkingModels: cfg.DevEco.ThinkingModels,
		Token:          func() string { return store.Tokens().AccessToken },
	})

	console := panel.New(panel.Config{
		Version:        appVersion,
		APIKey:         cfg.Server.APIKey,
		Listen:         listen,
		ConfigPath:     cfgPath,
		UpstreamURL:    cfg.DevEco.BaseURL,
		ModelDefault:   cfg.DevEco.Model,
		ThinkingModels: cfg.DevEco.ThinkingModels,
		KeepaliveHours: cfg.DevEco.KeepaliveHours,
		Auth:           store,
		Upstream:       up,
		Logs:           ring,
		// 导入 = 换内存 token（保活/请求立刻用新号）+ 落盘；返回被替换的旧身份给面板回显
		ImportAuth: func(t auth.Tokens) (auth.Tokens, error) {
			prev := store.Tokens()
			if err := store.Save(t); err != nil {
				return auth.Tokens{}, err
			}
			return prev, nil
		},
	})

	return &services{
		gateway: server.New(server.Config{
			Version:        appVersion,
			APIKey:         cfg.Server.APIKey,
			Listen:         listen,
			Upstream:       up,
			Auth:           store,
			Panel:          console,
			KeepaliveHours: cfg.DevEco.KeepaliveHours,
		}),
		console: console,
		auth:    store,
	}
}
