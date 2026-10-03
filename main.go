package main

// main.go — DevEco2API（Go 版）入口。

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	cfgPath := flag.String("config", "config.toml", "配置文件路径")
	host := flag.String("host", "", "监听 host")
	port := flag.Int("port", 0, "监听 port")
	loginOnly := flag.Bool("login", false, "仅执行登录并保存 token")
	loginRelay := flag.Bool("login-relay", false, "无头/远程登录（Go 版暂未移植）")
	noBrowser := flag.Bool("no-browser", false, "登录时不自动打开浏览器")
	flag.Parse()

	if *loginRelay {
		logError("Go 版暂未移植中继登录（--login-relay）：请用 Python 版，或使用 --login + ssh -L 隧道方式")
		os.Exit(1)
	}

	abs, err := filepath.Abs(*cfgPath)
	if err != nil {
		logError("解析配置路径失败: %v", err)
		os.Exit(1)
	}
	cfg, err := loadConfig(abs)
	if err != nil {
		logError("加载配置失败: %v", err)
		os.Exit(1)
	}
	setLogLevel(cfg.Logging.Level)

	access, err := ensureAuth(cfg, 10*time.Minute, *noBrowser)
	if err != nil {
		logError("认证失败: %v", err)
		os.Exit(1)
	}
	if *loginOnly {
		return
	}
	logInfo("access_token 已就绪: %s...", trunc(access, 16))

	h := cfg.Server.Host
	if *host != "" {
		h = *host
	}
	p := cfg.Server.Port
	if *port != 0 {
		p = *port
	}

	srv := newServer(cfg, abs)
	logInfo("启动 OpenAI 兼容 API: http://%s:%d/v1/chat/completions", h, p)
	srv.startKeepalive()

	if err := http.ListenAndServe(fmt.Sprintf("%s:%d", h, p), srv.routes()); err != nil {
		logError("服务退出: %v", err)
		os.Exit(1)
	}
}
