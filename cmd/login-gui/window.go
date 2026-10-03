//go:build !webview

// window.go 默认构建（无 -tags webview）：用系统浏览器打开登录页，
// 完成后把凭证块写到文件并打印路径。无 GUI 依赖的机器走这条。
package main

import (
	"github.com/misakano7545/deveco2api-panel/internal/auth"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
)

// runWindow 打开登录页并等待结果；返回非 nil 表示失败。
func runWindow(loginURL string, done <-chan result, outPath string) error {
	logfmt.Infof("用系统浏览器打开登录页（需要内嵌窗口请用 -tags webview 构建）")
	auth.OpenBrowser(loginURL)

	r := <-done
	if r.err != nil {
		logfmt.Errorf("登录失败: %v", r.err)
		return r.err
	}
	path, err := writeBlob(outPath, r.blob)
	if err != nil {
		logfmt.Errorf("写凭证块失败: %v", err)
		return err
	}
	logfmt.Infof("登录完成：%s", accountLabel(r.tok))
	logfmt.Infof("凭证块已写入 %s（粘贴到服务器面板的「导入」页；base64 形式在其 base64 字段同样可用）", path)
	return nil
}
