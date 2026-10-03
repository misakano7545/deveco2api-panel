// Package auth 华为账号登录、回调收尾与 token 维护（auth.py 的逐行移植）。
//
// 三条链路都在这里，供 cmd/login（交互登录/中继）与 internal/server（保活、
// 401 重试、状态展示）共用：
//   - 本地回调登录：起 127.0.0.1 回调服务器 → 浏览器授权 → tempToken 换 jwt；
//   - 无头/远程登录：internal/relay 把华为登录页反代回本机回调（含隧道）；
//   - token 维护：jwt 刷新 access_token（jwt 约 30 天，服务侧按间隔保活）。
//
// Store 是唯一持有 token 的地方：读走 Tokens()，写只经 Refresh/Set（同时落盘）。
package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/misakano7545/deveco2api-panel/internal/jsonval"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
)

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Tokens 一次登录得到的全部凭证。
type Tokens struct {
	JWTToken     string
	AccessToken  string
	RefreshToken string
	UserID       string
	UserName     string
}

// Config 上游端点（来自 config.toml 的 [deveco] 段，由 main 注入）。
type Config struct {
	BaseURL      string
	AppID        string
	AuthURL      string
	CallbackPort int
}

// Store 持有当前 token 并负责刷新落盘。
type Store struct {
	cfg  Config
	save func(Tokens) error // 落盘回调（main 注入写 TOML）；nil = 仅内存

	mu         sync.Mutex
	tok        Tokens
	lastAt     time.Time
	lastOK     bool
	lastErr    string
	refreshing bool
}

// New 构建 Store。save 由 main 注入（把 Tokens 写回配置文件）。
func New(cfg Config, tok Tokens, save func(Tokens) error) *Store {
	return &Store{cfg: cfg, tok: tok, save: save}
}

// Tokens 返回当前凭证快照。
func (s *Store) Tokens() Tokens {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tok
}

// LastRefresh 返回最近一次刷新时间与结果（面板状态页展示）。
func (s *Store) LastRefresh() (time.Time, bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAt, s.lastOK, s.lastErr
}

func (s *Store) set(t Tokens) {
	s.mu.Lock()
	s.tok = t
	sv := s.save
	s.mu.Unlock()
	if sv != nil {
		if err := sv(t); err != nil {
			logfmt.Warnf("保存配置失败: %v", err)
		}
	}
}

// applyUserInfo 从 userInfo 响应更新 token 字段并落盘。
func (s *Store) applyUserInfo(data map[string]any) {
	ui, _ := data["userInfo"].(map[string]any)
	if ui == nil {
		return
	}
	t := s.Tokens()
	t.AccessToken = jsonval.Str(ui["accessToken"])
	t.RefreshToken = jsonval.Str(ui["refreshToken"])
	t.UserID = jsonval.Str(ui["userId"])
	t.UserName = jsonval.Str(ui["name"])
	s.set(t)
}

// Refresh 用 jwt 刷新 access_token；成功返回 true 并落盘。
// 任何调用方（保活循环、401 重试、面板查询）都走这里，刷新状态只有一个来源。
func (s *Store) Refresh() bool {
	jwt := s.Tokens().JWTToken
	if jwt == "" {
		return false
	}
	data, err := refreshAccessToken(s.cfg.BaseURL, jwt)
	s.mu.Lock()
	s.lastAt, s.lastOK = time.Now(), err == nil
	s.lastErr = ""
	if err != nil {
		s.lastErr = err.Error()
	}
	s.mu.Unlock()
	if err != nil {
		logfmt.Errorf("刷新 access_token 失败: %v", err)
		return false
	}
	s.applyUserInfo(data)
	logfmt.Infof("access_token 刷新成功")
	return true
}

// Ensure 保证有一个可用的 access_token：现有 token 有效则直接用，
// 否则用 jwt 刷新，再不行走交互登录。
func (s *Store) Ensure(timeout time.Duration, noBrowser bool) error {
	t := s.Tokens()
	if t.AccessToken != "" {
		logfmt.Infof("检测现有 access_token 是否有效")
		if VerifyAccess(s.cfg.BaseURL, t.AccessToken) {
			logfmt.Infof("现有 access_token 有效")
			return nil
		}
		logfmt.Warnf("现有 access_token 已失效")
	}
	if t.JWTToken != "" {
		logfmt.Infof("尝试使用现有 jwt_token 刷新 access_token")
		if s.Refresh() {
			return nil
		}
		logfmt.Warnf("刷新失败，将重新登录")
	}
	logfmt.Warnf("开始华为账号登录流程")
	return s.LoginInteractive(timeout, noBrowser)
}

// VerifyAccess 探测 access_token 是否可用（兼容 success:true 与 code:200 两种形态）。
func VerifyAccess(baseURL, token string) bool {
	u := strings.TrimRight(baseURL, "/") + "/codeGenie/modelConfig?localVersion=0&pluginVersion=CLI.0.2.0"
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "deveco/0.2.0")
	req.Header.Set("Accept", "*/*")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	var data map[string]any
	if json.NewDecoder(resp.Body).Decode(&data) != nil {
		return false
	}
	if v, ok := data["success"].(bool); ok && v {
		return true
	}
	if v, ok := data["code"].(float64); ok && v == 200 {
		return true
	}
	return false
}

// JWTDaysLeft jwt 剩余有效天数（无法解析 exp 时 nil）。
func JWTDaysLeft(jwt string) *float64 {
	claims := jwtClaims(jwt)
	if claims == nil {
		return nil
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= 0 {
		return nil
	}
	d := time.Until(time.Unix(int64(exp), 0)).Hours() / 24
	return &d
}

// ---------------------------------------------------------------- 上游端点

func browserGet(rawURL string, headers map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept-Language", "zh-CN")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return resp, body, nil
}

func isRealName(userInfo map[string]any) bool {
	return strings.ToLower(strings.TrimSpace(jsonval.Str(userInfo["realName"]))) == "true"
}

func exchangeTempToken(baseURL, tempToken, appID string) (string, error) {
	base := strings.TrimRight(baseURL, "/")
	q := url.Values{"tempToken": {tempToken}, "site": {"CN"}, "version": {"1.0.0"}, "appid": {appID}}
	logfmt.Infof("GET %s", base+"/authrouter/auth/api/temptoken/check")
	resp, body, err := browserGet(base+"/authrouter/auth/api/temptoken/check?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("换取 jwtToken 失败，HTTP %d: %s", resp.StatusCode, string(body))
	}
	tok := strings.TrimSpace(string(body))
	if strings.Count(tok, ".") != 2 {
		return "", fmt.Errorf("返回的 jwtToken 格式不正确: %s", logfmt.Truncate(tok, 80))
	}
	return tok, nil
}

func checkJWTToken(baseURL, jwt string) (map[string]any, error) {
	u := strings.TrimRight(baseURL, "/") + "/authrouter/auth/api/jwToken/check"
	logfmt.Infof("GET %s", u)
	resp, body, err := browserGet(u, map[string]string{"refresh": "false", "jwtToken": jwt})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("校验 jwtToken 失败，HTTP %d: %s", resp.StatusCode, logfmt.Truncate(string(body), 300))
	}
	var data map[string]any
	if json.Unmarshal(body, &data) != nil {
		return nil, fmt.Errorf("校验 jwtToken 返回非 JSON: %s", logfmt.Truncate(string(body), 300))
	}
	status, _ := data["status"].(bool)
	ui, _ := data["userInfo"].(map[string]any)
	if !status || ui == nil {
		return nil, fmt.Errorf("jwtToken 校验未通过: %v", data)
	}
	if !isRealName(ui) {
		logfmt.Warnf("账号实名认证状态为 false：内置模型可能无法调用，请先用官方 DevEco Code 或浏览器完成华为账号实名后再试")
	}
	return data, nil
}

func refreshAccessToken(baseURL, jwt string) (map[string]any, error) {
	u := strings.TrimRight(baseURL, "/") + "/authrouter/auth/api/jwToken/check"
	logfmt.Infof("GET %s (refresh=true)", u)
	resp, body, err := browserGet(u, map[string]string{"refresh": "true", "jwtToken": jwt})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("刷新 jwtToken 失败，HTTP %d: %s", resp.StatusCode, logfmt.Truncate(string(body), 300))
	}
	var data map[string]any
	if json.Unmarshal(body, &data) != nil {
		return nil, fmt.Errorf("刷新 jwtToken 返回非 JSON: %s", logfmt.Truncate(string(body), 300))
	}
	status, _ := data["status"].(bool)
	ui, _ := data["userInfo"].(map[string]any)
	if !status || ui == nil {
		return nil, fmt.Errorf("刷新 jwtToken 未通过: %v", data)
	}
	if !isRealName(ui) {
		logfmt.Warnf("账号实名认证状态为 false：内置模型可能无法调用")
	}
	return data, nil
}

func jwtClaims(jwt string) map[string]any {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return nil
	}
	payload := parts[1]
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func jwtClaim(jwt, key string) string { return jsonval.Str(jwtClaims(jwt)[key]) }

// ---------------------------------------------------------------- 登录回调服务器

// Callback 本地回调等待器：华为授权完成后浏览器会带 tempToken 回到 127.0.0.1。
type Callback struct {
	code     string
	res      chan map[string]string
	server   *http.Server
	listener net.Listener
	Port     int
}

var callbackPorts = []int{10101, 34567, 34568, 34569, 34570}

func (c *Callback) handle(w http.ResponseWriter, r *http.Request) {
	// 兼容 GET 与任意 Content-Type 的表单体（与 Python 版 _collect_params 一致）
	params := url.Values{}
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			params.Set(k, v)
		}
	}
	if r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" {
		if body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(bytes.TrimSpace(body)) > 0 {
			if vals, err := url.ParseQuery(string(body)); err == nil {
				for k, vs := range vals {
					for _, v := range vs {
						params.Set(k, v)
					}
				}
			}
		}
	}
	get := func(k string) string { return params.Get(k) }
	code, tempToken, siteID, quit := get("code"), get("tempToken"), get("siteId"), get("quit")

	if code != c.code {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<h1>Waiting for authorization...</h1><p>Code mismatch, still waiting.</p>")
		return
	}
	select {
	case c.res <- map[string]string{"code": code, "tempToken": tempToken, "siteId": siteID, "quit": quit}:
	default:
	}
	location := "https://cn.devecostudio.huawei.com/console/DevEcoCode/loginSuccess"
	if quit == "true" || quit == "access_denied" || tempToken == "" || siteID != "1" {
		location = "https://cn.devecostudio.huawei.com/console/DevEcoCode/loginFailed"
	}
	http.Redirect(w, r, location, http.StatusFound)
}

// StartCallbackServer 在 127.0.0.1 起回调等待服务器：port 占用时依次回落到
// 官方客户端用的其余端口（34567-34570）。
func StartCallbackServer(port int, expectedCode string) (*Callback, error) {
	ports := append([]int{port}, callbackPorts[1:]...)
	for _, p := range ports {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		cb := &Callback{code: expectedCode, res: make(chan map[string]string, 1), listener: ln, Port: p}
		mux := http.NewServeMux()
		mux.HandleFunc("/", cb.handle)
		cb.server = &http.Server{Handler: mux}
		go func() { _ = cb.server.Serve(ln) }()
		return cb, nil
	}
	return nil, fmt.Errorf("所有端口均被占用: %v", ports)
}

// Wait 等待回调参数（中继模式下由 internal/relay 转发进来）；超时返回错误。
func (c *Callback) Wait(timeout time.Duration) (map[string]string, error) {
	select {
	case res := <-c.res:
		_ = c.server.Close()
		return res, nil
	case <-time.After(timeout):
		_ = c.server.Close()
		return nil, fmt.Errorf("等待浏览器回调超时")
	}
}

// Close 关闭等待器（调用方已自行取到回调时的清理路径）。
func (c *Callback) Close() { _ = c.server.Close() }

// ---------------------------------------------------------------- 登录流程

// Finalize 收尾：校验回调参数 → tempToken 换 jwt → 校验 jwt → Tokens。
func Finalize(cfg Config, callback map[string]string) (Tokens, error) {
	baseURL := strings.TrimRight(cfg.BaseURL, "/")

	if q := callback["quit"]; q == "true" || q == "access_denied" {
		return Tokens{}, fmt.Errorf("用户在浏览器中取消了授权")
	}
	if callback["siteId"] != "1" {
		return Tokens{}, fmt.Errorf("不支持的 region，siteId=%s（目前只支持 siteId=1 中国区）", callback["siteId"])
	}
	tempToken := strings.SplitN(callback["tempToken"], "&", 2)[0]
	if tempToken == "" {
		return Tokens{}, fmt.Errorf("回调缺少 tempToken: %v", callback)
	}

	logfmt.Infof("收到回调，tempToken=%s...", logfmt.Truncate(tempToken, 24))
	jwt, err := exchangeTempToken(baseURL, tempToken, cfg.AppID)
	if err != nil {
		return Tokens{}, err
	}
	logfmt.Infof("获得 jwtToken: %s...", logfmt.Truncate(jwt, 64))
	data, err := checkJWTToken(baseURL, jwt)
	if err != nil {
		return Tokens{}, err
	}
	ui, _ := data["userInfo"].(map[string]any)
	return Tokens{
		JWTToken:     jwt,
		AccessToken:  jsonval.Str(ui["accessToken"]),
		RefreshToken: jsonval.Str(ui["refreshToken"]),
		UserID:       jsonval.FirstNonEmpty(jsonval.Str(ui["userId"]), jwtClaim(jwt, "userId")),
		UserName:     jsonval.FirstNonEmpty(jsonval.Str(ui["name"]), jwtClaim(jwt, "userName")),
	}, nil
}

// OpenBrowser 用系统默认浏览器打开 URL（CLI 与 GUI 的回退路径共用）。
func OpenBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		logfmt.Warnf("自动打开浏览器失败: %v", err)
	}
}

// LoginInteractive 本机浏览器登录：起回调服务器 → 打印授权地址 → 等回调。
func (s *Store) LoginInteractive(timeout time.Duration, noBrowser bool) error {
	loginURL, cb, err := s.PrepareLogin()
	if err != nil {
		return err
	}
	logfmt.Infof("请在浏览器中完成华为账号授权：\n    %s", loginURL)
	logfmt.Infof("若浏览器与服务器不同机：先在本机执行 ssh -L %d:127.0.0.1:%d <user>@<服务器>，再打开上面的 URL（回调经隧道送回服务器）", cb.Port, cb.Port)
	if !noBrowser {
		OpenBrowser(loginURL)
	}
	callback, err := cb.Wait(timeout)
	if err != nil {
		return err
	}
	tok, err := Finalize(s.cfg, callback)
	if err != nil {
		return err
	}
	return s.Save(tok)
}

// PrepareLogin 起本地回调等待器并返回授权 URL + 等待器，供调用方自己决定
// 怎么打开这个 URL：CLI 用系统浏览器，GUI 用内嵌窗口。回调仍固定在 127.0.0.1，
// tempToken 只交给本机的等待器，不经任何第三方。
func (s *Store) PrepareLogin() (string, *Callback, error) {
	baseURL := strings.TrimRight(s.cfg.BaseURL, "/")

	secretBytes := make([]byte, 16)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", nil, err
	}
	clientSecret := hex.EncodeToString(secretBytes)

	logfmt.Infof("DevEco Code 华为账号登录")
	logfmt.Infof("baseUrl: %s", baseURL)

	cb, err := StartCallbackServer(s.cfg.CallbackPort, clientSecret)
	if err != nil {
		return "", nil, err
	}
	logfmt.Infof("本地回调服务器已启动: http://127.0.0.1:%d/callback", cb.Port)

	loginURL := fmt.Sprintf("%s/%s?port=%d&appid=%s&code=%s", baseURL, s.cfg.AuthURL, cb.Port, s.cfg.AppID, clientSecret)
	return loginURL, cb, nil
}

// Save 保存一次登录/导入结果（内存 + 落盘）。调用方自己打面向用户的日志。
func (s *Store) Save(t Tokens) error {
	if t.JWTToken == "" {
		return fmt.Errorf("登录结果缺少 jwtToken")
	}
	s.set(t)
	return nil
}
