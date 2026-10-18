package main

// wiring.go 装配：把 config.toml 的字段接到各 internal 组件的依赖上。
// 依赖接线只在这里和 cmd/login 出现，internal 包之间不互相构造。

import (
	"path/filepath"

	"github.com/misakano7545/deveco2api-panel/internal/auth"
	"github.com/misakano7545/deveco2api-panel/internal/config"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
	"github.com/misakano7545/deveco2api-panel/internal/panel"
	"github.com/misakano7545/deveco2api-panel/internal/reqlog"
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
	reqlog  *reqlog.Recorder
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
		VisionModels:   cfg.DevEco.VisionModels,
		Token:          func() string { return store.Tokens().AccessToken },
	})

	// 请求记录：指标始终在内存；JSONL 归档落在配置文件同级的 request-logs/，
	// 随 config.json 一起搬（与 workbuddy2api-panel 的 state 同级目录同思路）。
	// 写盘失败不影响聊天请求（队列满即丢弃并计数）。
	reqLog := reqlog.New(reqlog.Config{
		Dir:           filepath.Join(filepath.Dir(cfgPath), "request-logs"),
		Enabled:       cfg.Logging.RequestArchiveEnabled,
		RetentionDays: cfg.Logging.RequestRetentionDays,
		MaxBytes:      int64(cfg.Logging.RequestArchiveMaxMB) << 20,
	})
	if st := reqLog.Snapshot().Archive; st.Enabled {
		logfmt.Infof("请求记录已启用；JSONL 归档 %s（保留 %d 天，上限 %d MiB）",
			st.Dir, cfg.Logging.RequestRetentionDays, cfg.Logging.RequestArchiveMaxMB)
	} else {
		logfmt.Infof("请求记录已启用；JSONL 归档已关闭（只留进程内最近 100 条）")
	}

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
		RequestLog:     reqLog,
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

			SessionReuse:      cfg.DevEco.SessionReuse,
			SessionTTLMinutes: cfg.DevEco.SessionTTLMinutes,

			RequestLog:       reqLog,
			RecordClientInfo: cfg.Logging.RequestClientInfo,
		}),
		console: console,
		auth:    store,
		reqlog:  reqLog,
	}
}
