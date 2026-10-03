package main

// main.go — DevEco2API 网关入口（二进制 deveco2api-panel）。

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/misakano7545/deveco2api-panel/internal/config"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
)

// appVersion 展示用版本号（与 Python 版一致）。
const appVersion = "0.1.0"

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径")
	host := flag.String("host", "", "监听 host（默认取配置 server.host）")
	port := flag.Int("port", 0, "监听 port（默认取配置 server.port）")
	loginOnly := flag.Bool("login", false, "仅确保登录有效（过期则交互登录）后退出")
	noBrowser := flag.Bool("no-browser", false, "登录时不自动打开浏览器")
	timeoutSec := flag.Int("timeout", 600, "等待浏览器回调超时秒数")
	flag.Parse()

	abs, err := filepath.Abs(*cfgPath)
	if err != nil {
		logfmt.Errorf("解析配置路径失败: %v", err)
		os.Exit(1)
	}
	cfg, err := config.Load(abs)
	if err != nil {
		logfmt.Errorf("加载配置失败: %v", err)
		os.Exit(1)
	}

	h, p := cfg.Server.Host, cfg.Server.Port
	if *host != "" {
		h = *host
	}
	if *port != 0 {
		p = *port
	}
	listen := fmt.Sprintf("%s:%d", h, p)

	svc := wire(cfg, abs, listen)

	if err := svc.auth.Ensure(time.Duration(*timeoutSec)*time.Second, *noBrowser); err != nil {
		logfmt.Errorf("认证失败: %v", err)
		os.Exit(1)
	}
	if *loginOnly {
		return
	}
	logfmt.Infof("access_token 已就绪: %s...", logfmt.Truncate(svc.auth.Tokens().AccessToken, 16))

	svc.gateway.StartKeepalive()
	logfmt.Infof("启动 OpenAI 兼容 API: http://%s/v1/chat/completions", listen)
	logfmt.Infof("Web 控制台: http://%s/panel/", listen)
	if err := http.ListenAndServe(listen, svc.gateway); err != nil {
		logfmt.Errorf("服务退出: %v", err)
		os.Exit(1)
	}
}
