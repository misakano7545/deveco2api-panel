// Command login-gui 独立登录器：内嵌窗口里完成华为授权，产出可直接导入的凭证块。
//
// 与面板解耦——不认识面板地址、不向面板发任何请求。产物两种用法：
//   - 这台就是服务器：--config config.json 直接落盘（同 deveco2api-login）
//   - 服务器在别处：把窗口里的凭证块（JSON 或 base64）粘贴到服务器面板的「导入」页
//
// 默认构建（无 -tags webview）退回系统浏览器 + 把凭证块写文件，供无 GUI 依赖的机器用。
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/misakano7545/deveco2api-panel/internal/auth"
	"github.com/misakano7545/deveco2api-panel/internal/config"
	"github.com/misakano7545/deveco2api-panel/internal/jsonval"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
)

// result 一次登录的结局（成功带凭证块，失败带错误）。
type result struct {
	tok  auth.Tokens
	blob blobText
	err  error
}

// blobText 同一份凭证的两种写法：面板两种都收。
type blobText struct {
	JSON   string
	Base64 string
}

// buildBlob 生成导入用凭证块（只含 auth 字段，不含任何服务器配置）。
func buildBlob(t auth.Tokens) blobText {
	body, err := jsonval.Marshal(map[string]any{"auth": map[string]any{
		"jwt_token":     t.JWTToken,
		"access_token":  t.AccessToken,
		"refresh_token": t.RefreshToken,
		"user_id":       t.UserID,
		"user_name":     t.UserName,
	}})
	if err != nil {
		return blobText{}
	}
	return blobText{JSON: string(body), Base64: base64.StdEncoding.EncodeToString(body)}
}

// writeBlob 凭证块落盘（0600），返回路径。默认写到 config 同目录的 auth-block.json。
func writeBlob(path string, blob blobText) (string, error) {
	if path == "" {
		path = "auth-block.json"
	}
	if err := os.WriteFile(path, []byte(blob.JSON+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func accountLabel(t auth.Tokens) string {
	if t.UserName == "" {
		return t.UserID
	}
	return t.UserName + "(" + t.UserID + ")"
}

func main() {
	cfgPath := flag.String("config", "", "本机 config.json 路径（留空 = 只出凭证块，不落盘）")
	outPath := flag.String("out", "", "凭证块输出文件（默认 auth-block.json）")
	timeoutSec := flag.Int("timeout", 600, "等待授权超时秒数（默认 600）")
	flag.Parse()

	var save func(auth.Tokens) error
	cfg := config.Default()
	if *cfgPath != "" {
		abs, err := filepath.Abs(*cfgPath)
		if err != nil {
			logfmt.Errorf("解析配置路径失败: %v", err)
			os.Exit(1)
		}
		if cfg, err = config.Load(abs); err != nil {
			logfmt.Errorf("加载配置失败: %v", err)
			os.Exit(1)
		}
		save = cfg.SaveTokens
	}
	logfmt.SetLevel(cfg.Logging.Level)

	authCfg := auth.Config{
		BaseURL:      cfg.DevEco.BaseURL,
		AppID:        cfg.DevEco.AppID,
		AuthURL:      cfg.DevEco.AuthURL,
		CallbackPort: cfg.DevEco.CallbackPort,
	}
	store := auth.New(authCfg, cfg.Tokens(), save)

	loginURL, cb, err := store.PrepareLogin()
	if err != nil {
		logfmt.Errorf("准备登录失败: %v", err)
		os.Exit(1)
	}

	done := make(chan result, 1)
	go func() {
		callback, err := cb.Wait(time.Duration(*timeoutSec) * time.Second)
		if err != nil {
			done <- result{err: err}
			return
		}
		tok, err := auth.Finalize(authCfg, callback)
		if err != nil {
			done <- result{err: err}
			return
		}
		if save != nil {
			if err := store.Save(tok); err != nil {
				done <- result{err: fmt.Errorf("保存到 %s 失败: %w", *cfgPath, err)}
				return
			}
			logfmt.Infof("token 已写入 %s", *cfgPath)
		}
		done <- result{tok: tok, blob: buildBlob(tok)}
	}()

	if err := runWindow(loginURL, done, *outPath); err != nil {
		os.Exit(1)
	}
}
