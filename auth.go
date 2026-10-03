package main

// auth.go — 华为账号登录与 token 维护（对 auth.py 的逐行移植）。

import (
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
	"time"
)

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var authHTTP = &http.Client{Timeout: 30 * time.Second}

func strOf(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

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
	resp, err := authHTTP.Do(req)
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
	return strings.ToLower(strings.TrimSpace(strOf(userInfo["realName"]))) == "true"
}

// testAccessToken — 兼容 success:true 与 code:200 两种响应形态。
func testAccessToken(baseURL, token string) bool {
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

func exchangeTempToken(baseURL, tempToken, appID string) (string, error) {
	base := strings.TrimRight(baseURL, "/")
	q := url.Values{"tempToken": {tempToken}, "site": {"CN"}, "version": {"1.0.0"}, "appid": {appID}}
	logInfo("GET %s", base+"/authrouter/auth/api/temptoken/check")
	resp, body, err := browserGet(base+"/authrouter/auth/api/temptoken/check?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("换取 jwtToken 失败，HTTP %d: %s", resp.StatusCode, string(body))
	}
	tok := strings.TrimSpace(string(body))
	if strings.Count(tok, ".") != 2 {
		return "", fmt.Errorf("返回的 jwtToken 格式不正确: %s", trunc(tok, 80))
	}
	return tok, nil
}

func checkJWTToken(baseURL, jwt string) (map[string]any, error) {
	u := strings.TrimRight(baseURL, "/") + "/authrouter/auth/api/jwToken/check"
	logInfo("GET %s", u)
	resp, body, err := browserGet(u, map[string]string{"refresh": "false", "jwtToken": jwt})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("校验 jwtToken 失败，HTTP %d: %s", resp.StatusCode, trunc(string(body), 300))
	}
	var data map[string]any
	if json.Unmarshal(body, &data) != nil {
		return nil, fmt.Errorf("校验 jwtToken 返回非 JSON: %s", trunc(string(body), 300))
	}
	status, _ := data["status"].(bool)
	ui, _ := data["userInfo"].(map[string]any)
	if !status || ui == nil {
		return nil, fmt.Errorf("jwtToken 校验未通过: %v", data)
	}
	if !isRealName(ui) {
		logWarn("账号实名认证状态为 false：内置模型可能无法调用，请先用官方 DevEco Code 或浏览器完成华为账号实名后再试")
	}
	return data, nil
}

func refreshAccessToken(baseURL, jwt string) (map[string]any, error) {
	u := strings.TrimRight(baseURL, "/") + "/authrouter/auth/api/jwToken/check"
	logInfo("GET %s (refresh=true)", u)
	resp, body, err := browserGet(u, map[string]string{"refresh": "true", "jwtToken": jwt})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("刷新 jwtToken 失败，HTTP %d: %s", resp.StatusCode, trunc(string(body), 300))
	}
	var data map[string]any
	if json.Unmarshal(body, &data) != nil {
		return nil, fmt.Errorf("刷新 jwtToken 返回非 JSON: %s", trunc(string(body), 300))
	}
	status, _ := data["status"].(bool)
	ui, _ := data["userInfo"].(map[string]any)
	if !status || ui == nil {
		return nil, fmt.Errorf("刷新 jwtToken 未通过: %v", data)
	}
	if !isRealName(ui) {
		logWarn("账号实名认证状态为 false：内置模型可能无法调用")
	}
	return data, nil
}

func jwtClaim(jwt, key string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return ""
	}
	payload := parts[1]
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return strOf(m[key])
}

func applyUserInfo(cfg *Config, data map[string]any) {
	ui, _ := data["userInfo"].(map[string]any)
	if ui == nil {
		return
	}
	cfg.DevEco.Auth.AccessToken = strOf(ui["accessToken"])
	cfg.DevEco.Auth.RefreshToken = strOf(ui["refreshToken"])
	cfg.DevEco.Auth.UserID = strOf(ui["userId"])
	cfg.DevEco.Auth.UserName = strOf(ui["name"])
}

// ---------------------------------------------------------------- 登录回调服务器

type loginCallback struct {
	code     string
	res      chan map[string]string
	server   *http.Server
	listener net.Listener
	port     int
}

var callbackPorts = []int{10101, 34567, 34568, 34569, 34570}

func (c *loginCallback) handle(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	get := func(k string) string { return r.Form.Get(k) }
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

func startCallbackServer(port int, expectedCode string) (*loginCallback, error) {
	ports := append([]int{port}, callbackPorts[1:]...)
	for _, p := range ports {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		cb := &loginCallback{code: expectedCode, res: make(chan map[string]string, 1), listener: ln, port: p}
		mux := http.NewServeMux()
		mux.HandleFunc("/", cb.handle)
		cb.server = &http.Server{Handler: mux}
		go func() { _ = cb.server.Serve(ln) }()
		return cb, nil
	}
	return nil, fmt.Errorf("所有端口均被占用: %v", ports)
}

func (c *loginCallback) wait(timeout time.Duration) (map[string]string, error) {
	select {
	case res := <-c.res:
		_ = c.server.Close()
		return res, nil
	case <-time.After(timeout):
		_ = c.server.Close()
		return nil, fmt.Errorf("等待浏览器回调超时")
	}
}

// ---------------------------------------------------------------- 登录与收尾

type LoginResult struct {
	JWTToken     string
	AccessToken  string
	RefreshToken string
	UserID       string
	UserName     string
}

func finalizeLogin(cfg *Config, callback map[string]string) (*LoginResult, error) {
	baseURL := strings.TrimRight(cfg.DevEco.BaseURL, "/")
	appID := cfg.DevEco.AppID

	if q := callback["quit"]; q == "true" || q == "access_denied" {
		return nil, fmt.Errorf("用户在浏览器中取消了授权")
	}
	if callback["siteId"] != "1" {
		return nil, fmt.Errorf("不支持的 region，siteId=%s（目前只支持 siteId=1 中国区）", callback["siteId"])
	}
	tempToken := strings.SplitN(callback["tempToken"], "&", 2)[0]
	if tempToken == "" {
		return nil, fmt.Errorf("回调缺少 tempToken: %v", callback)
	}

	logInfo("收到回调，tempToken=%s...", trunc(tempToken, 24))
	jwt, err := exchangeTempToken(baseURL, tempToken, appID)
	if err != nil {
		return nil, err
	}
	logInfo("获得 jwtToken: %s...", trunc(jwt, 64))
	data, err := checkJWTToken(baseURL, jwt)
	if err != nil {
		return nil, err
	}
	ui, _ := data["userInfo"].(map[string]any)
	return &LoginResult{
		JWTToken:     jwt,
		AccessToken:  strOf(ui["accessToken"]),
		RefreshToken: strOf(ui["refreshToken"]),
		UserID:       firstNonEmpty(strOf(ui["userId"]), jwtClaim(jwt, "userId")),
		UserName:     firstNonEmpty(strOf(ui["name"]), jwtClaim(jwt, "userName")),
	}, nil
}

func saveLoginResult(cfg *Config, res *LoginResult) error {
	cfg.DevEco.Auth = AuthConfig{
		JWTToken:     res.JWTToken,
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		UserID:       res.UserID,
		UserName:     res.UserName,
	}
	if err := cfg.save(); err != nil {
		return err
	}
	logInfo("登录信息已保存到 %s", cfg.path)
	return nil
}

func openBrowser(u string) {
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
		logWarn("自动打开浏览器失败: %v", err)
	}
}

func loginInteractive(cfg *Config, timeout time.Duration, noBrowser bool) (*LoginResult, error) {
	baseURL := strings.TrimRight(cfg.DevEco.BaseURL, "/")

	secretBytes := make([]byte, 16)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, err
	}
	clientSecret := hex.EncodeToString(secretBytes)

	logInfo("DevEco Code 华为账号登录")
	logInfo("baseUrl: %s", baseURL)
	logInfo("clientSecret: %s", clientSecret)

	cb, err := startCallbackServer(cfg.DevEco.CallbackPort, clientSecret)
	if err != nil {
		return nil, err
	}
	logInfo("本地回调服务器已启动: http://127.0.0.1:%d/callback", cb.port)

	loginURL := fmt.Sprintf("%s/%s?port=%d&appid=%s&code=%s", baseURL, cfg.DevEco.AuthURL, cb.port, cfg.DevEco.AppID, clientSecret)
	logInfo("请在浏览器中完成华为账号授权：\n    %s", loginURL)
	logInfo("若浏览器与服务器不同机：先在本机执行 ssh -L %d:127.0.0.1:%d <user>@<服务器>，再打开上面的 URL（回调经隧道送回服务器）", cb.port, cb.port)
	if !noBrowser {
		openBrowser(loginURL)
	}

	callback, err := cb.wait(timeout)
	if err != nil {
		return nil, err
	}
	return finalizeLogin(cfg, callback)
}

func ensureAuth(cfg *Config, timeout time.Duration, noBrowser bool) (string, error) {
	base := strings.TrimRight(cfg.DevEco.BaseURL, "/")

	if cfg.DevEco.Auth.AccessToken != "" {
		logInfo("检测现有 access_token 是否有效")
		if testAccessToken(base, cfg.DevEco.Auth.AccessToken) {
			logInfo("现有 access_token 有效")
			return cfg.DevEco.Auth.AccessToken, nil
		}
		logWarn("现有 access_token 已失效")
	}

	if cfg.DevEco.Auth.JWTToken != "" {
		logInfo("尝试使用现有 jwt_token 刷新 access_token")
		if data, err := refreshAccessToken(base, cfg.DevEco.Auth.JWTToken); err == nil {
			applyUserInfo(cfg, data)
			if err := cfg.save(); err != nil {
				logWarn("保存配置失败: %v", err)
			}
			logInfo("access_token 刷新成功")
			return cfg.DevEco.Auth.AccessToken, nil
		} else {
			logWarn("刷新失败，将重新登录: %v", err)
		}
	}

	logWarn("开始华为账号登录流程")
	res, err := loginInteractive(cfg, timeout, noBrowser)
	if err != nil {
		return "", err
	}
	if err := saveLoginResult(cfg, res); err != nil {
		return "", err
	}
	return res.AccessToken, nil
}
