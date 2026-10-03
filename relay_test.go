package main

// relay_test.go — 离线自测：登录中继链路（对齐 Python 版 test_login_relay.py，无华为调用）。

import (
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestFinalizeLoginScenarios(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/authrouter/auth/api/temptoken/check", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "h.p.s") // 假 jwt（3 段）
	})
	mux.HandleFunc("/authrouter/auth/api/jwToken/check", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, 200, map[string]any{"status": true, "userInfo": map[string]any{
			"accessToken": "AT", "refreshToken": "RT", "userId": "U1", "name": "N", "realName": true,
		}})
	})
	up := httptest.NewServer(mux)
	defer up.Close()

	cfg := defaultConfig()
	cfg.DevEco.BaseURL = up.URL

	res, err := finalizeLogin(cfg, map[string]string{"code": "x", "tempToken": "tt1", "siteId": "1", "quit": ""})
	if err != nil || res.AccessToken != "AT" || res.UserID != "U1" {
		t.Fatalf("正常收尾应成功: %v %+v", err, res)
	}
	if _, err := finalizeLogin(cfg, map[string]string{"siteId": "1", "quit": "access_denied"}); err == nil {
		t.Fatal("取消授权应报错")
	}
	if _, err := finalizeLogin(cfg, map[string]string{"tempToken": "tt", "siteId": "2"}); err == nil {
		t.Fatal("非中国区应报错")
	}
	if _, err := finalizeLogin(cfg, map[string]string{"siteId": "1"}); err == nil {
		t.Fatal("缺 tempToken 应报错")
	}
}

func TestRelayChain(t *testing.T) {
	state := t.TempDir()
	waiterLog := filepath.Join(state, "deveco-login.log")
	if err := os.WriteFile(waiterLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	key, nonce := "test-key", "testnonce123"
	waiterPort := freePort(t)
	cb, err := startCallbackServer(waiterPort, nonce)
	if err != nil {
		t.Fatal(err)
	}
	defer cb.server.Close()

	relayCfg = relayConfig{AccessKey: key, Nonce: nonce, WaiterPort: cb.port, WaiterLog: waiterLog}
	relaySess.reset()
	ts := httptest.NewServer(relayHandler())
	defer ts.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// 口令门禁
	resp, err := client.Get(ts.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("无口令应 403，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 入口跳转（带口令；浏览器会记住 rk cookie）
	resp, err = client.Get(ts.URL + "/?k=" + key)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 307 || !strings.Contains(resp.Header.Get("Location"), "authrouter/forward") {
		t.Fatalf("入口应 307 → forward，实际 %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp.Body.Close()

	// LOGIN_OK 检测
	if waiterDone() {
		t.Fatal("初始不应判定完成")
	}
	if err := os.WriteFile(waiterLog, []byte("LOGIN_OK\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waiterDone() {
		t.Fatal("写入 LOGIN_OK 后应判定完成")
	}
	if err := os.WriteFile(waiterLog, nil, 0o644); err != nil { // 复位，供回调链路用
		t.Fatal(err)
	}

	// 回调经中继转发 → 等待器收到（浏览器凭 rk cookie 过门禁）
	resp, err = client.PostForm(ts.URL+"/cb/"+strconv.Itoa(cb.port)+"/callback",
		url.Values{"code": {nonce}, "tempToken": {"tk1"}, "siteId": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 303 {
		t.Fatalf("回调应 303 → /finish，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()

	select {
	case got := <-cb.res:
		if got["tempToken"] != "tk1" {
			t.Fatalf("等待器收到的参数不对: %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待器未收到回调")
	}

	// finish 页面可访问
	resp, err = client.Get(ts.URL + "/finish")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "DevEco") {
		t.Fatalf("finish 页面异常: %d", resp.StatusCode)
	}
}

func TestRewriteFunctions(t *testing.T) {
	if got := rewriteURL("https://id1.cloud.huawei.com/CAS/portal/loginAuth.html?a=1"); got != "/CAS/portal/loginAuth.html?a=1" {
		t.Fatalf("rewriteURL 不对: %s", got)
	}
	if got := rewriteURL("/already/relative"); got != "/already/relative" {
		t.Fatalf("相对地址不应改写: %s", got)
	}

	html := `<a href="https://cn.devecostudio.huawei.com/console/x">x</a>` +
		`<script src="/a.js" integrity="sha384-abc"></script>` +
		`<meta http-equiv="Content-Security-Policy" content="default-src 'self'">`
	out := rewriteHTML(html)
	if strings.Contains(out, "cn.devecostudio.huawei.com") || strings.Contains(out, "integrity") ||
		strings.Contains(out, "Content-Security-Policy") || !strings.Contains(out, `href="/console/x"`) {
		t.Fatalf("HTML 重写不完整: %s", out)
	}

	js := `location.replace("http://localhost:10101/callback?x=1"); fetch("https:\/\/id1.cloud.huawei.com\/CAS\/a")`
	out2 := rewriteTextBody(js, "https://relay.example")
	if !strings.Contains(out2, "https://relay.example/cb/10101/callback") ||
		strings.Contains(out2, "localhost:10101") || strings.Contains(out2, "id1.cloud.huawei.com") {
		t.Fatalf("JS 重写不对: %s", out2)
	}

	// 隧道 URL 解析：排除日志里的 api.trycloudflare.com 干扰项
	if findTunnelURL("|  https://struggle-huntington-lowest.trycloudflare.com  |") == "" {
		t.Fatal("应命中真实隧道地址")
	}
	if findTunnelURL("requesting on https://api.trycloudflare.com/v2/tunnels") != "" {
		t.Fatal("不应命中 api.trycloudflare.com")
	}
}
