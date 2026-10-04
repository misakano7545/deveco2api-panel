// Package config config.json 的读写（结构对齐 workbuddy2api-panel）。
//
// 单独成包而不是放在 cmd/server 里：网关（cmd/server）与登录（cmd/login）两个
// 二进制读写同一份配置文件与同一份凭证，结构复制两份必然漂移。
//
// 字段名与 Python 版 config.toml 完全同名（server.host/api_key、deveco.*、logging.level），
// 只是容器换成 JSON —— 两个实现不再共用同一份文件（Python 侧仍是 TOML），
// 字段一一对应，转换是机械的：把 section.key 拍平成同名 JSON 键即可。
//
// 文件里有 access_token / jwt_token，落盘固定 0600、走 tmp + rename 原子替换。
package config

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/misakano7545/deveco2api-panel/internal/auth"
)

// AuthConfig 凭证段。
type AuthConfig struct {
	JWTToken     string `json:"jwt_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
	UserName     string `json:"user_name"`
}

// DevEcoConfig 上游段。
type DevEcoConfig struct {
	BaseURL           string   `json:"base_url"`
	AuthURL           string   `json:"auth_url"`
	TempTokenCheckURL string   `json:"temp_token_check_url"`
	JWTTokenCheckURL  string   `json:"jwt_token_check_url"`
	AppID             string   `json:"app_id"`
	CallbackPort      int      `json:"callback_port"`
	Model             string   `json:"model"`
	Client            string   `json:"client"`
	Project           string   `json:"project"`
	UserAgent         string   `json:"user_agent"`
	KeepaliveHours    float64  `json:"keepalive_hours"`
	ThinkingModels    []string `json:"thinking_models"`
	// SessionReuse 客户端未带 session_id 时复用同一个上游会话。
	//
	// 实测（v0.1.0）：上游真正会拦的是「新建会话」——约 5 次/分就 429
	// (UserSessionLimitExceeded)，而同一会话内连发 64 次（8 并发 / 17s）全 200。
	// 官方口径的 50 次/分是请求配额，不是新会话配额。true 时未带 session_id 的请求
	// 共用一个会话（按 session_ttl_minutes 轮换），把实际可用速率从 ~5 次/分提到
	// 上游请求配额量级；false 保持「每请求新会话」的老行为。
	SessionReuse      bool       `json:"session_reuse"`
	SessionTTLMinutes int        `json:"session_ttl_minutes"`
	Auth              AuthConfig `json:"auth"`
}

// ServerConfig 网关监听与鉴权段。
type ServerConfig struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	APIKey string `json:"api_key"`
}

// LoggingConfig 日志段。
type LoggingConfig struct {
	Level string `json:"level"`
}

// Config 全量配置。path/mu 是运行期字段，不参与序列化。
type Config struct {
	Server  ServerConfig  `json:"server"`
	DevEco  DevEcoConfig  `json:"deveco"`
	Logging LoggingConfig `json:"logging"`

	path string
	mu   sync.Mutex
}

// Default 默认配置（文件缺项由它兜底：JSON 未给出的键保持默认值）。
func Default() *Config {
	return &Config{
		Server: ServerConfig{Host: "127.0.0.1", Port: 10102},
		DevEco: DevEcoConfig{
			BaseURL:           "https://cn.devecostudio.huawei.com",
			AuthURL:           "console/DevEcoIDE/apply",
			TempTokenCheckURL: "authrouter/auth/api/temptoken/check",
			JWTTokenCheckURL:  "authrouter/auth/api/jwToken/check",
			AppID:             "1008",
			CallbackPort:      10101,
			Model:             "GLM-5.1",
			Client:            "cli",
			Project:           "global",
			UserAgent:         "deveco/0.2.0",
			KeepaliveHours:    6.0,
			ThinkingModels:    []string{"GLM-5.3"},
		},
		Logging: LoggingConfig{Level: "INFO"},
	}
}

// Load 读取配置文件。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, err
	}
	cfg.path = path
	return cfg, nil
}

// SetPath 设置落盘路径（Load 之外由测试/装配使用）。
func (c *Config) SetPath(path string) {
	c.mu.Lock()
	c.path = path
	c.mu.Unlock()
}

// Path 当前配置文件路径。
func (c *Config) Path() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path
}

// Tokens 配置里的凭证快照（装配 auth.Store 用）。
func (c *Config) Tokens() auth.Tokens {
	c.mu.Lock()
	defer c.mu.Unlock()
	return auth.Tokens{
		JWTToken:     c.DevEco.Auth.JWTToken,
		AccessToken:  c.DevEco.Auth.AccessToken,
		RefreshToken: c.DevEco.Auth.RefreshToken,
		UserID:       c.DevEco.Auth.UserID,
		UserName:     c.DevEco.Auth.UserName,
	}
}

// Save 原子写回：MarshalIndent(2 空格) → tmp(0600) → rename，与参考项目一致。
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(c.path, out)
}

// SaveTokens 更新凭证并落盘（auth.Store 的 save 回调）。
func (c *Config) SaveTokens(t auth.Tokens) error {
	c.mu.Lock()
	c.DevEco.Auth = AuthConfig{
		JWTToken:     t.JWTToken,
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		UserID:       t.UserID,
		UserName:     t.UserName,
	}
	c.mu.Unlock()
	return c.Save()
}

// writeAtomic 写临时文件（0600：文件里有 token）+ rename 替换。
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
