package relay

// relay.go — 登录中继（无头 / 远程服务器场景），对 login_relay.py 的移植。
//
// 用户浏览器（可经隧道）访问本中继，中继把华为登录全流程反代到用户浏览器；
// 收尾时 consent 页构造的 localhost 回调被改写为 /cb/<port>/callback，转发给
// 本机等待器（auth.go 的回调服务器），由等待器换取并保存 token。
//
// ponytail: 剪裁三处——CDP 无头接力（Python 里 E1 反代模式已不触发，属死代码）、
// /retry 与 /debug_complete（依赖 CDP）、会话文件持久化（/status 已实时可见）。

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/misakano7545/deveco2api-panel/internal/auth"
	"github.com/misakano7545/deveco2api-panel/internal/jsonval"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
)

const (
	hostID1    = "id1.cloud.huawei.com"
	hostOAuth  = "oauth-login.cloud.huawei.com"
	hostOAuth1 = "oauth-login1.cloud.huawei.com"
	hostDevEco = "cn.devecostudio.huawei.com"
)

var prefixRoutes = []struct{ Prefix, Base string }{
	{"/CAS/", "https://" + hostID1},
	{"/DimensionalCode/", "https://" + hostID1},
	{"/IDMW/", "https://" + hostID1},
	{"/cch5/", "https://" + hostID1},
	{"/uniportal/", "https://" + hostID1},
	{"/remoteLogin", "https://" + hostID1},
	{"/AMW/", "https://" + hostID1},
	{"/oauth2/v3/loginCallback", "https://" + hostOAuth1},
	{"/oauth2/", "https://" + hostOAuth},
	{"/static_rss_vue3/", "https://" + hostOAuth},
	{"/authrouter/", "https://" + hostDevEco},
	{"/console/", "https://" + hostDevEco},
	{"/devspaceapi/", "https://" + hostDevEco},
}

type relayConfig struct {
	Port       int
	AccessKey  string
	Nonce      string
	WaiterPort int
	WaiterLog  string
}

type relayCookie struct {
	Name, Value, Domain, Path string
	Secure, HTTPOnly          bool
}

type relaySession struct {
	mu          sync.Mutex
	cookies     []relayCookie
	phase       string // waiting | completing | done | error
	detail      string
	intercepted string
	transitions []string
}

var (
	relayCfg  relayConfig
	relaySess = &relaySession{phase: "waiting"}
)

func relayApplyURL() string {
	return fmt.Sprintf("https://%s/console/DevEcoIDE/apply?port=%d&appid=1008&code=%s",
		hostDevEco, relayCfg.WaiterPort, relayCfg.Nonce)
}

func relayForwardPath() string {
	return "/authrouter/forward?redirect_url=" + url.QueryEscape(relayApplyURL())
}

// ---------------------------------------------------------------- session

func (s *relaySession) note(msg string) {
	s.mu.Lock()
	s.transitions = append(s.transitions, time.Now().Format("15:04:05")+" "+msg)
	if len(s.transitions) > 60 {
		s.transitions = s.transitions[len(s.transitions)-60:]
	}
	s.mu.Unlock()
	logfmt.Infof("[session] %s", msg)
}

func (s *relaySession) setPhase(phase, detail string) {
	s.mu.Lock()
	s.phase, s.detail = phase, detail
	s.mu.Unlock()
}

func (s *relaySession) reset() {
	s.mu.Lock()
	s.cookies, s.phase, s.detail, s.intercepted, s.transitions = nil, "waiting", "", "", nil
	s.mu.Unlock()
}

// ---------------------------------------------------------------- cookie jar

func domainMatch(host, cdomain string) bool {
	cd := strings.ToLower(cdomain)
	if strings.HasPrefix(cd, ".") {
		d := cd[1:]
		return host == d || strings.HasSuffix(host, "."+d)
	}
	return host == cd
}

func jarDelete(name, host, path string) {
	relaySess.mu.Lock()
	defer relaySess.mu.Unlock()
	kept := relaySess.cookies[:0]
	for _, x := range relaySess.cookies {
		if x.Name == name && domainMatch(host, x.Domain) && x.Path == path {
			continue
		}
		kept = append(kept, x)
	}
	relaySess.cookies = kept
}

func jarSet(c relayCookie) {
	relaySess.mu.Lock()
	defer relaySess.mu.Unlock()
	kept := relaySess.cookies[:0]
	for _, x := range relaySess.cookies {
		if x.Name == c.Name && x.Domain == c.Domain && x.Path == c.Path {
			continue
		}
		kept = append(kept, x)
	}
	relaySess.cookies = append(kept, c)
}

// jarUpdate — 语义与 Python 版一致：带 Domain 属性（无论前导点）→ 后缀匹配；无属性 → 仅主机匹配。
func jarUpdate(resp *http.Response, reqHost string) {
	for _, c := range resp.Cookies() {
		cdomain := reqHost
		if c.Domain != "" {
			cdomain = "." + strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		}
		cpath := c.Path
		if cpath == "" {
			cpath = "/"
		}
		if c.MaxAge < 0 || (!c.Expires.IsZero() && c.Expires.Before(time.Now())) {
			jarDelete(c.Name, reqHost, cpath)
			continue
		}
		jarSet(relayCookie{Name: c.Name, Value: c.Value, Domain: cdomain, Path: cpath, Secure: c.Secure, HTTPOnly: c.HttpOnly})
		logfmt.Infof("[jar+] %s @%s%s", c.Name, cdomain, cpath)
	}
}

func jarHeader(host, path string) string {
	relaySess.mu.Lock()
	defer relaySess.mu.Unlock()
	var out []string
	for _, c := range relaySess.cookies {
		if domainMatch(host, c.Domain) && strings.HasPrefix(path, c.Path) {
			out = append(out, c.Name+"="+c.Value)
		}
	}
	return strings.Join(out, "; ")
}

// ---------------------------------------------------------------- rewriting

var (
	quotedHosts = func() []string {
		hosts := []string{hostID1, hostOAuth, hostOAuth1, hostDevEco}
		for i, h := range hosts {
			hosts[i] = regexp.QuoteMeta(h)
		}
		return hosts
	}()
	hostAlt = strings.Join(quotedHosts, "|")

	attrRe    = regexp.MustCompile(`(?i)(\b(?:href|src|action|formaction|data-src|data-href|poster)\s*=\s*)(?:"([^"]*)"|'([^']*)')`)
	integRe   = regexp.MustCompile(`(?i)\s+integrity\s*=\s*(?:"[^"]*"|'[^']*')`)
	metaCSPRe = regexp.MustCompile(`(?i)<meta[^>]*http-equiv\s*=\s*["']?Content-Security-Policy["']?[^>]*>`)
	hostURLRe = regexp.MustCompile(`(?i)^(?:https?:)?//(?:` + hostAlt + `)(?::\d{1,5})?(/.*)?$`)
	// JS/JSON 里的绝对华为地址（含 \/\/ 与 \\/\\/ 两种转义形态）→ 中继自身 origin
	jsHostRe = regexp.MustCompile(`(?i)https?:(?://|\\/\\/|\\\\/\\\\/)(?:` + hostAlt + `)(?::\d{1,5})?`)
)

func rewriteURL(u string) string {
	m := hostURLRe.FindStringSubmatch(strings.TrimSpace(u))
	if m == nil {
		return u
	}
	if m[1] == "" {
		return "/"
	}
	return m[1]
}

func rewriteHTML(text string) string {
	text = attrRe.ReplaceAllStringFunc(text, func(match string) string {
		g := attrRe.FindStringSubmatch(match)
		attr := g[1]
		v, quote := g[2], `"`
		if g[2] == "" && !strings.Contains(match, `"`) {
			v, quote = g[3], "'"
		}
		nv := rewriteURL(v)
		if nv == v {
			return match
		}
		return attr + quote + nv + quote
	})
	text = integRe.ReplaceAllString(text, "")
	text = metaCSPRe.ReplaceAllString(text, "")
	return text
}

func rewriteTextBody(text, origin string) string {
	// 先替换 localhost 回调（避免第二步注入的 origin 再被本规则二次匹配）
	for _, p := range []string{"http://localhost:", "http://127.0.0.1:"} {
		text = strings.ReplaceAll(text, p, origin+"/cb/")
	}
	return jsHostRe.ReplaceAllString(text, origin)
}

func rewriteLocation(loc string) string {
	if loc == "" {
		return loc
	}
	return rewriteURL(loc)
}

var dropRespHeaders = map[string]bool{
	"content-security-policy": true, "content-security-policy-report-only": true,
	"strict-transport-security": true, "x-frame-options": true, "expect-ct": true,
	"report-to": true, "nel": true, "set-cookie": true, "set-cookie2": true,
	"content-length": true, "content-encoding": true,
	"transfer-encoding": true, "connection": true, "keep-alive": true,
}

func routeFor(path string) string {
	for _, r := range prefixRoutes {
		if strings.HasPrefix(path, r.Prefix) {
			return r.Base
		}
	}
	return ""
}

// ---------------------------------------------------------------- waiter 联动

func waiterDone() bool {
	if relayCfg.WaiterLog == "" {
		return false
	}
	f, err := os.Open(relayCfg.WaiterLog)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	size := st.Size()
	start := int64(0)
	if size > 8000 {
		start = size - 8000
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return false
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return false
	}
	return strings.Contains(string(tail), "LOGIN_OK")
}

func watchWaiter() {
	for i := 0; i < 180; i++ {
		if waiterDone() {
			relaySess.setPhase("done", "登录与授权已完成，token 已保存。")
			relaySess.note("waiter: 检测到 LOGIN_OK ✅ 全部完成")
			return
		}
		time.Sleep(time.Second)
	}
	relaySess.setPhase("error", "回调已送达等待器，但未检测到完成确认。请重新运行登录。")
	relaySess.note("waiter: 等待超时")
}

// ---------------------------------------------------------------- 代理

var relayHTTPClient = &http.Client{
	Timeout: 35 * time.Second,
	// 不跟随跳转（与 Python follow_redirects=False 一致）；无 Jar，cookie 自管
	CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
}

func buildUpstreamHeaders(r *http.Request, host, path string) http.Header {
	drop := map[string]bool{"host": true, "cookie": true, "content-length": true, "accept-encoding": true,
		"referer": true, "origin": true, "connection": true, "accept-charset": true}
	h := http.Header{}
	for k, vs := range r.Header {
		if drop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	h.Set("Accept-Encoding", "gzip, deflate")
	if ck := jarHeader(host, path); ck != "" {
		h.Set("Cookie", ck)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		if pu, err := url.Parse(ref); err == nil {
			rp := pu.Path
			if pu.RawQuery != "" {
				rp += "?" + pu.RawQuery
			}
			if rb := routeFor(rp); rb != "" {
				h.Set("Referer", rb+rp)
			}
		}
	}
	if og := r.Header.Get("Origin"); og != "" {
		if pu, err := url.Parse(og); err == nil {
			p := pu.Path
			if p == "" {
				p = "/"
			}
			if ob := routeFor(p); ob != "" {
				h.Set("Origin", ob)
			}
		}
	}
	return h
}

func requestOrigin(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

// writeRelayResponse — 解压 → 按类型重写 → 头处理 → （隧道加速用）压缩。
func writeRelayResponse(w http.ResponseWriter, up *http.Response, origin, acceptEncoding string) int {
	raw, _ := io.ReadAll(up.Body)
	body := raw
	switch strings.ToLower(up.Header.Get("Content-Encoding")) {
	case "gzip":
		if len(body) > 0 {
			if d, err := gzipDecompress(body); err == nil {
				body = d
			}
		}
	case "deflate":
		if len(body) > 0 {
			if d, err := flateDecompress(body); err == nil {
				body = d
			}
		}
	}
	ct := up.Header.Get("Content-Type")
	if len(body) > 0 {
		if strings.Contains(ct, "text/html") {
			body = []byte(rewriteHTML(string(body)))
		} else if strings.Contains(ct, "javascript") || strings.Contains(ct, "json") || strings.Contains(ct, "text/css") {
			body = []byte(rewriteTextBody(string(body), origin))
		}
	}

	headers := http.Header{}
	for k, vs := range up.Header {
		if dropRespHeaders[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			headers.Add(k, v)
		}
	}
	if loc := headers.Get("Location"); loc != "" {
		headers.Set("Location", rewriteLocation(loc))
	}
	if strings.Contains(ct, "text/html") && headers.Get("Cache-Control") == "" {
		headers.Set("Cache-Control", "no-store")
	}
	// 隧道链路对大响应吞吐低：文本类资源在源头压缩，显著加速加载
	if len(body) > 1024 && strings.Contains(strings.ToLower(acceptEncoding), "gzip") &&
		containsAny(ct, "text/", "javascript", "json", "xml", "svg") &&
		headers.Get("Content-Encoding") == "" {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write(body); err == nil && gz.Close() == nil {
			body = buf.Bytes()
			headers.Set("Content-Encoding", "gzip")
		}
	}

	for k, vs := range headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(up.StatusCode)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
	return len(raw)
}

func gzipDecompress(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func flateDecompress(b []byte) ([]byte, error) {
	if zr, err := zlib.NewReader(bytes.NewReader(b)); err == nil {
		defer zr.Close()
		return io.ReadAll(zr)
	}
	// 无 zlib 头的 raw deflate
	fr := flate.NewReader(bytes.NewReader(b))
	defer fr.Close()
	return io.ReadAll(fr)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func handleRelayAny(w http.ResponseWriter, r *http.Request) {
	path, query := r.URL.Path, r.URL.RawQuery
	base := routeFor(path)
	if base == "" {
		logfmt.Warnf("[miss] %s %s", r.Method, logfmt.Truncate(path, 120))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "relay: no route for "+path)
		return
	}
	host := strings.SplitN(strings.TrimPrefix(base, "https://"), "/", 2)[0]
	upstreamURL := base + path
	if query != "" {
		upstreamURL += "?" + query
	}
	var bodyBytes []byte
	if r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" || r.Method == "DELETE" {
		bodyBytes, _ = io.ReadAll(r.Body)
	}
	req, err := http.NewRequest(r.Method, upstreamURL, bytes.NewReader(bodyBytes))
	if err != nil {
		writeError(w, http.StatusBadGateway, "relay upstream error: "+err.Error())
		return
	}
	req.Header = buildUpstreamHeaders(r, host, path)
	req.Host = host
	up, err := relayHTTPClient.Do(req)
	if err != nil {
		logfmt.Errorf("[err] %s %s -> %v", r.Method, logfmt.Truncate(path, 100), err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "relay upstream error: "+err.Error())
		return
	}
	defer up.Body.Close()
	jarUpdate(up, host)
	if up.StatusCode >= 301 && up.StatusCode <= 308 {
		logfmt.Infof("[loc] %s %s -> %s", r.Method, logfmt.Truncate(path, 70), logfmt.Truncate(up.Header.Get("Location"), 200))
	}
	n := writeRelayResponse(w, up, requestOrigin(r), r.Header.Get("Accept-Encoding"))
	logfmt.Infof("[px] %s %s -> %d %s (%db)", r.Method, logfmt.Truncate(path, 90), up.StatusCode, host, n)
}

// ---------------------------------------------------------------- routes

func handleRelayEntry(w http.ResponseWriter, r *http.Request) {
	relaySess.mu.Lock()
	phase := relaySess.phase
	relaySess.mu.Unlock()
	if phase == "completing" || phase == "done" {
		http.Redirect(w, r, "/finish", http.StatusTemporaryRedirect)
		return
	}
	http.Redirect(w, r, relayForwardPath(), http.StatusTemporaryRedirect)
}

func handleRelayFinish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, relayFinishHTML)
}

type relayStatusPayload struct {
	Phase        string   `json:"phase"`
	Detail       string   `json:"detail"`
	HasIntercept bool     `json:"has_intercept"`
	Cookies      []string `json:"cookies"`
	Log          []string `json:"log"`
}

func handleRelayStatus(w http.ResponseWriter, r *http.Request) {
	relaySess.mu.Lock()
	st := relayStatusPayload{Phase: relaySess.phase, Detail: relaySess.detail,
		HasIntercept: relaySess.intercepted != "", Cookies: []string{}}
	for _, c := range relaySess.cookies {
		st.Cookies = append(st.Cookies, c.Name+"@"+c.Domain)
	}
	tr := relaySess.transitions
	if len(tr) > 40 {
		tr = tr[len(tr)-40:]
	}
	st.Log = append([]string{}, tr...)
	relaySess.mu.Unlock()
	writeJSON(w, http.StatusOK, st)
}

func handleRelayCB(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err == nil {
		var body []byte
		if r.Method == "POST" {
			body, _ = io.ReadAll(r.Body)
		}
		target := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		req, err := http.NewRequest(r.Method, target, bytes.NewReader(body))
		if err == nil {
			cl := &http.Client{Timeout: 15 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			if resp, err2 := cl.Do(req); err2 == nil {
				resp.Body.Close()
				relaySess.note(fmt.Sprintf("cb: 回调已转发到等待器 (HTTP %d)", resp.StatusCode))
				relaySess.setPhase("completing", "回调已送达，服务器正在换取 token…")
				go watchWaiter()
			} else {
				relaySess.note(fmt.Sprintf("cb: 转发失败 %v", err2))
			}
		}
	}
	http.Redirect(w, r, "/finish", http.StatusSeeOther)
}

func relayHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/finish", handleRelayFinish)
	mux.HandleFunc("/status", handleRelayStatus)
	mux.HandleFunc("/cb/{port}/callback", handleRelayCB)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			handleRelayEntry(w, r)
			return
		}
		handleRelayAny(w, r)
	})

	// 口令门禁（k 参数或 rk cookie）
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		key := r.URL.Query().Get("k")
		if key == "" {
			if c, err := r.Cookie("rk"); err == nil {
				key = c.Value
			}
		}
		if key != relayCfg.AccessKey {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "<h3>403 Forbidden</h3>")
			return
		}
		if r.URL.Query().Get("k") == relayCfg.AccessKey {
			if c, err := r.Cookie("rk"); err != nil || c.Value != relayCfg.AccessKey {
				http.SetCookie(w, &http.Cookie{Name: "rk", Value: relayCfg.AccessKey,
					HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400, Path: "/"})
			}
		}
		mux.ServeHTTP(w, r)
	})
}

const relayFinishHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>DevEco 登录 · 收尾中</title>
<style>
:root{color-scheme:light dark}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
 font-family:system-ui,-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;background:#f6f7f9;color:#202124}
@media(prefers-color-scheme:dark){body{background:#16181c;color:#e8eaed}.card{background:#23262b!important;border-color:#3a3f46!important}p{color:#9aa0a6}}
.card{background:#fff;border:1px solid #e3e5e8;border-radius:16px;padding:40px 44px;max-width:440px;width:calc(100% - 48px);
 text-align:center;box-shadow:0 12px 40px rgba(0,0,0,.06)}
h1{font-size:20px;margin:0 0 10px}
p{margin:6px 0;font-size:14px;line-height:1.7;color:#5f6368}
.spin{width:34px;height:34px;margin:22px auto 6px;border-radius:50%;border:3px solid #d7dbe0;border-top-color:#4285f4;animation:r 1s linear infinite}
@keyframes r{to{transform:rotate(360deg)}}
.ok{font-size:40px;margin:10px 0 4px}
.small{font-size:12px;color:#9aa0a6;margin-top:14px}
</style></head>
<body><div class="card">
<div id="icon" class="spin"></div>
<h1 id="t">正在完成授权…</h1>
<p id="d">登录已提交，服务器正在自动完成剩余授权步骤，请稍候。</p>
<p class="small">完成动作由服务器执行，不依赖本页面；本页可随时关闭。</p>
</div>
<script>
const icon=document.getElementById('icon'),t=document.getElementById('t'),d=document.getElementById('d');
async function poll(){
 try{
  const r=await fetch('/status',{cache:'no-store'});const s=await r.json();
  if(s.phase==='done'){icon.className='ok';icon.textContent='✅';t.textContent='全部完成';d.innerHTML='华为账号登录与授权已完成，<b>token 已保存</b>。<br>可以关闭本页面。';return;}
  if(s.phase==='error'){icon.className='ok';icon.textContent='⚠️';t.textContent='收尾遇到问题';d.textContent=(s.detail||'')+' 请重新运行登录命令。';}
 }catch(e){}
 setTimeout(poll,1500);
}
poll();
</script></body></html>
`

// ---------------------------------------------------------------- 隧道与一体化登录

// RE2 不支持否定前瞻：先宽松匹配，再用 findTunnelURL 排除 api.trycloudflare.com 干扰项
var tunnelURLRe = regexp.MustCompile(`https://([a-z0-9-]+)\.trycloudflare\.com`)

func findTunnelURL(line string) string {
	for _, m := range tunnelURLRe.FindAllStringSubmatch(line, -1) {
		if m[1] != "api" {
			return m[0]
		}
	}
	return ""
}

// startCloudflared — 启动快速隧道指向本机中继。返回 (cmd, url)；不可用/失败为 (nil, "")。
func startCloudflared(timeout time.Duration) (*exec.Cmd, string) {
	if _, err := exec.LookPath("cloudflared"); err != nil {
		logfmt.Warnf("未找到 cloudflared，跳过隧道（仅本机可访问）")
		return nil, ""
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, ""
	}
	cmd := exec.Command("cloudflared", "tunnel", "--url", fmt.Sprintf("http://127.0.0.1:%d", relayCfg.Port), "--no-autoupdate")
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return nil, ""
	}
	pw.Close() // 写端由子进程持有

	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	url := ""
	deadline := time.After(timeout)
loop:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				break loop
			}
			if u := findTunnelURL(line); u != "" {
				url = u
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	// 持续排空输出，防止管道写满阻塞 cloudflared
	go func() {
		for range lines {
		}
	}()
	if url == "" {
		logfmt.Warnf("cloudflared 未成功创建隧道（%.0fs 内无地址，可能被限流/网络受限）；本次仅本机可访问，可稍后重试", timeout.Seconds())
	}
	return cmd, url
}

// LoginViaRelay 无头/远程登录：本机同时运行「回调等待器 + 登录中继」，
// 浏览器（可经隧道）经中继完成华为授权，回调经中继送回等待器换取 token。
func LoginViaRelay(o Options) error {
	stateDir := filepath.Join(filepath.Dir(o.ConfigPath), ".login-relay")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	if o.AccessKey == "" {
		b := make([]byte, 8)
		randRead(b)
		o.AccessKey = hex.EncodeToString(b)
	}
	secretBytes := make([]byte, 16)
	randRead(secretBytes)
	clientSecret := hex.EncodeToString(secretBytes)

	waiterLog := filepath.Join(stateDir, "deveco-login.log")
	if err := os.WriteFile(waiterLog, []byte{}, 0o644); err != nil { // 清掉旧的 LOGIN_OK，避免误判完成
		return err
	}

	// 1) 回调等待器（复用 auth.go 的回调服务器）
	cb, err := auth.StartCallbackServer(o.AuthConfig.CallbackPort, clientSecret)
	if err != nil {
		return err
	}
	logfmt.Infof("回调等待器已启动: http://127.0.0.1:%d/callback", cb.Port)

	// 2) 登录中继
	relayCfg = relayConfig{Port: o.RelayPort, AccessKey: o.AccessKey, Nonce: clientSecret, WaiterPort: cb.Port, WaiterLog: waiterLog}
	relaySess.reset()
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", o.RelayPort), Handler: relayHandler()}
	go func() { _ = srv.ListenAndServe() }()

	// 3) 可选隧道
	var tunnelCmd *exec.Cmd
	tunnelURL := ""
	if o.Tunnel {
		tunnelCmd, tunnelURL = startCloudflared(30 * time.Second)
	}

	entryURL := fmt.Sprintf("http://127.0.0.1:%d/?k=%s", o.RelayPort, o.AccessKey)
	if tunnelURL != "" {
		entryURL = tunnelURL + "/?k=" + o.AccessKey
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		if tunnelCmd != nil && tunnelCmd.Process != nil {
			_ = tunnelCmd.Process.Kill()
		}
	}()

	// 等中继就绪（最多 10s）
	waitReady := time.Now().Add(10 * time.Second)
	for time.Now().Before(waitReady) {
		cl := &http.Client{Timeout: 2 * time.Second}
		if resp, err := cl.Get(fmt.Sprintf("http://127.0.0.1:%d/status?k=%s", o.RelayPort, o.AccessKey)); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	logfmt.Infof("%s", strings.Repeat("=", 64))
	logfmt.Infof("请在浏览器打开以下地址，用华为账号完成登录：")
	logfmt.Infof("    %s", entryURL)
	if tunnelURL == "" {
		logfmt.Infof("（服务器无浏览器时：点对点隧道/端口转发后从本机访问；或使用 --tunnel）")
	}
	logfmt.Infof("回调等待 %d 秒，完成授权后 token 会自动写入 %s", int(o.Timeout.Seconds()), o.ConfigPath)
	logfmt.Infof("%s", strings.Repeat("=", 64))

	// 4) 等浏览器回调 → 换 token → 保存
	callback, err := cb.Wait(o.Timeout)
	if err != nil {
		return err
	}
	tok, err := auth.Finalize(o.AuthConfig, callback)
	if err != nil {
		return err
	}
	if err := o.Save(tok); err != nil {
		return err
	}
	if f, err := os.OpenFile(waiterLog, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
		_, _ = f.WriteString("LOGIN_OK\n") // 中继 /status 据此翻成完成态
		f.Close()
	}

	// 5) 给页面留出展示时间，然后收摊
	grace := time.Now().Add(30 * time.Second)
	for time.Now().Before(grace) {
		relaySess.mu.Lock()
		phase := relaySess.phase
		relaySess.mu.Unlock()
		if phase == "done" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(10 * time.Second)
	logfmt.Infof("登录完成 ✅ token 已保存到 %s", o.ConfigPath)
	return nil
}

func randRead(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
}

// ---------------------------------------------------------------- 装配接口

// Options 中继登录依赖（cmd/login 装配注入）。
type Options struct {
	AuthConfig auth.Config // 上游端点与回调等待器端口
	RelayPort  int         // 中继监听端口（浏览器访问入口）
	AccessKey  string      // 访问口令；空 = 随机生成并打印
	Timeout    time.Duration
	Tunnel     bool   // 自动起 cloudflared 快速隧道并打印外网地址
	ConfigPath string // 状态目录定位与完成提示
	Save       func(auth.Tokens) error
}

// writeJSON/writeError 中继自身的响应 helper（与网关同口径：不转义 < > &）。
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := jsonval.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"detail": detail})
}
