package main

// main.go — 登录入口（二进制 deveco2api-login）。
//
// 两种登录方式：
//
//	deveco2api-login                     本机浏览器登录（回调走 127.0.0.1）
//	deveco2api-login --relay [--tunnel]  无头/远程：起登录中继，浏览器经隧道访问
//
// 独立二进制的原因与参考项目一致：网关只负责转发，登录是一次性动作且需要
// 交互式终端（浏览器 / 打印中继地址）。

import (
	"flag"
	"os"
	"path/filepath"
	"time"

	"github.com/misakano7545/deveco2api-panel/internal/auth"
	"github.com/misakano7545/deveco2api-panel/internal/config"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
	"github.com/misakano7545/deveco2api-panel/internal/relay"
)

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径")
	relayMode := flag.Bool("relay", false, "无头/远程登录：起登录中继，浏览器（可经隧道）完成授权")
	relayPort := flag.Int("relay-port", 8788, "登录中继监听端口（默认 8788）")
	accessKey := flag.String("access-key", "", "登录中继访问口令（默认随机生成并打印）")
	tunnel := flag.Bool("tunnel", false, "自动启动 cloudflared 快速隧道并打印外网地址")
	timeoutSec := flag.Int("timeout", 600, "等待浏览器回调超时秒数（默认 600）")
	noBrowser := flag.Bool("no-browser", false, "登录时不自动打开浏览器")
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
	logfmt.SetLevel(cfg.Logging.Level)

	authCfg := auth.Config{
		BaseURL:      cfg.DevEco.BaseURL,
		AppID:        cfg.DevEco.AppID,
		AuthURL:      cfg.DevEco.AuthURL,
		CallbackPort: cfg.DevEco.CallbackPort,
	}
	timeout := time.Duration(*timeoutSec) * time.Second

	if *relayMode {
		if err := relay.LoginViaRelay(relay.Options{
			AuthConfig: authCfg,
			RelayPort:  *relayPort,
			AccessKey:  *accessKey,
			Timeout:    timeout,
			Tunnel:     *tunnel,
			ConfigPath: abs,
			Save:       cfg.SaveTokens,
		}); err != nil {
			logfmt.Errorf("登录失败: %v", err)
			os.Exit(1)
		}
		return
	}

	store := auth.New(authCfg, cfg.Tokens(), cfg.SaveTokens)
	if err := store.LoginInteractive(timeout, *noBrowser); err != nil {
		logfmt.Errorf("登录失败: %v", err)
		os.Exit(1)
	}
	logfmt.Infof("token 已保存到 %s", abs)
}
