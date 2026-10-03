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

// appVersion 展示用版本号（与 Python 版保持一致）。
const appVersion = "0.1.0"

func main() {
	cfgPath := flag.String("config", "config.toml", "配置文件路径")
	host := flag.String("host", "", "监听 host")
	port := flag.Int("port", 0, "监听 port")
	loginOnly := flag.Bool("login", false, "仅执行登录并保存 token")
	loginRelay := flag.Bool("login-relay", false, "无头/远程登录：启动登录中继，浏览器（可经隧道）完成授权")
	relayPort := flag.Int("relay-port", 8788, "登录中继监听端口（默认 8788）")
	accessKey := flag.String("access-key", "", "登录中继访问口令（默认随机生成并打印）")
	tunnel := flag.Bool("tunnel", false, "自动启动 cloudflared 快速隧道并打印外网地址")
	timeoutSec := flag.Int("timeout", 600, "等待浏览器回调超时秒数（默认 600）")
	noBrowser := flag.Bool("no-browser", false, "登录时不自动打开浏览器")
	flag.Parse()

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

	if *loginRelay {
		if err := loginViaRelay(cfg, abs, *relayPort, *accessKey, time.Duration(*timeoutSec)*time.Second, *tunnel); err != nil {
			logError("登录失败: %v", err)
			os.Exit(1)
		}
		return
	}

	access, err := ensureAuth(cfg, time.Duration(*timeoutSec)*time.Second, *noBrowser)
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
