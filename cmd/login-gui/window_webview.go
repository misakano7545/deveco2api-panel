//go:build webview

// window_webview.go 内嵌窗口构建（go build -tags webview，需 cgo +
// WebView2 / WKWebView / WebKitGTK，三平台均用系统自带的 webview 运行时）。
//
// 窗口先加载华为授权页；登录完成后切到结果页，凭证块可直接复制去面板「导入」。
package main

import (
	"html"
	"strings"

	webview "github.com/webview/webview_go"

	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
)

func runWindow(loginURL string, done <-chan result, _ string) error {
	w := webview.New(false) // false = 关调试，不暴露 CDP 端口
	defer w.Destroy()
	w.SetTitle("DevEco 登录 — deveco2api")
	w.SetSize(560, 780, webview.HintNone)

	go func() {
		r := <-done
		w.Dispatch(func() { // 窗口操作回主线程
			w.SetHtml(statusPage(r))
			if r.err != nil {
				logfmt.Errorf("窗口已切到失败页: %v", r.err)
			} else {
				logfmt.Infof("窗口已切到结果页：%s", accountLabel(r.tok))
			}
		})
	}()

	w.Navigate(loginURL)
	w.Run() // 阻塞到用户关窗
	return nil
}

// statusPage 结果页：成功给出可复制的凭证块，失败给出原因。
func statusPage(r result) string {
	var body string
	if r.err != nil {
		body = `<div class="card bad"><div class="t">登录失败</div><div class="m">` +
			html.EscapeString(r.err.Error()) + `</div></div>
			<p class="hint">可在终端重跑 <code>deveco2api-gui</code> 重试；若窗口内登录被拦截，用 <code>-tags webview</code> 之外的方式（系统浏览器）再试一次。</p>`
	} else {
		body = `<div class="card ok"><div class="t">登录成功</div>
			<div class="m">账号：<b>` + html.EscapeString(accountLabel(r.tok)) + `</b></div></div>
			<p class="hint">把下面任意一块整体复制，粘贴到服务器面板的「导入」页（两种格式都收）：</p>
			<div class="lab">JSON</div><textarea id="j" readonly>` + html.EscapeString(r.blob.JSON) + `</textarea>
			<button onclick="cp('j',this)">复制 JSON</button>
			<div class="lab">base64</div><textarea id="b" readonly>` + html.EscapeString(r.blob.Base64) + `</textarea>
			<button onclick="cp('b',this)">复制 base64</button>
			<p class="hint">粘贴后关掉本窗口即可。</p>`
	}
	return `<!doctype html><meta charset="utf-8"><title>DevEco 登录</title>
<style>
 body{font:14px/1.6 -apple-system,"Segoe UI","Noto Sans SC",sans-serif;margin:0;padding:18px;background:#111;color:#e8e8ea}
 .card{border-radius:10px;padding:14px 16px;margin-bottom:12px}
 .card.ok{background:#12271b;border:1px solid #2c5a3a}
 .card.bad{background:#2a1416;border:1px solid #6b2b31}
 .t{font-size:16px;font-weight:600;margin-bottom:4px}
 .m{color:#c9c9cf}
 .hint{color:#9a9aa3;font-size:13px}
 .lab{margin:10px 0 4px;color:#9a9aa3;font-size:12px;letter-spacing:.04em}
 textarea{width:100%;height:68px;box-sizing:border-box;background:#1b1b1f;color:#e8e8ea;
   border:1px solid #33333a;border-radius:8px;padding:8px;font:12px/1.5 ui-monospace,Menlo,Consolas,monospace;resize:vertical}
 button{margin-top:6px;padding:7px 12px;border-radius:8px;border:1px solid #3a3a44;background:#232329;color:#e8e8ea;cursor:pointer}
 button:hover{background:#2b2b33}
 code{background:#1b1b1f;padding:1px 5px;border-radius:5px}
</style>
` + body + `
<script>
function cp(id,btn){
  const t=document.getElementById(id); t.focus(); t.select();
  let ok=false;
  try{ ok=document.execCommand('copy'); }catch(e){}
  if(!ok && navigator.clipboard){ navigator.clipboard.writeText(t.value); ok=true; }
  btn.textContent = ok ? '已复制' : '请手动复制（已全选）';
  setTimeout(()=>{ btn.textContent = btn.textContent.startsWith('已复制') ? '已复制' : btn.textContent; }, 2000);
}
</script>`
}

// 保留 strings 引用点：状态页里没有任何外部资源（CSP 友好，全部内联字符串）。
var _ = strings.TrimSpace
