// Package config config.toml 的读写（与 Python 版 / 上游格式完全兼容）。
//
// 单独成包而不是放在 cmd/server 里：网关（cmd/server）与登录（cmd/login）两个
// 二进制读写同一份配置文件与同一份凭证，结构复制两份必然漂移。
package config

import (
	"os"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/misakano7545/deveco2api-panel/internal/auth"
)

// AuthConfig 凭证段（[deveco.auth]）。
type AuthConfig struct {
	JWTToken     string `toml:"jwt_token"`
	AccessToken  string `toml:"access_token"`
	RefreshToken string `toml:"refresh_token"`
	UserID       string `toml:"user_id"`
	UserName     string `toml:"user_name"`
}

// DevEcoConfig 上游段（[deveco]）。
type DevEcoConfig struct {
	BaseURL           string     `toml:"base_url"`
	AuthURL           string     `toml:"auth_url"`
	TempTokenCheckURL string     `toml:"temp_token_check_url"`
	JWTTokenCheckURL  string     `toml:"jwt_token_check_url"`
	AppID             string     `toml:"app_id"`
	CallbackPort      int        `toml:"callback_port"`
	Model             string     `toml:"model"`
	Client            string     `toml:"client"`
	Project           string     `toml:"project"`
	UserAgent         string     `toml:"user_agent"`
	KeepaliveHours    float64    `toml:"keepalive_hours"`
	ThinkingModels    []string   `toml:"thinking_models"`
	Auth              AuthConfig `toml:"auth"`
}

// ServerConfig 网关监听与鉴权段（[server]）。
type ServerConfig struct {
	Host   string `toml:"host"`
	Port   int    `toml:"port"`
	APIKey string `toml:"api_key"`
}

// LoggingConfig 日志段（[logging]）。
type LoggingConfig struct {
	Level string `toml:"level"`
}

// Config 全量配置。path/mu 是运行期字段，不参与序列化。
type Config struct {
	Server  ServerConfig  `toml:"server"`
	DevEco  DevEcoConfig  `toml:"deveco"`
	Logging LoggingConfig `toml:"logging"`

	path string
	mu   sync.Mutex
}

// Default 默认配置（配置文件缺项由它兜底）。
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
	cfg := Default()
	if _, err := toml.DecodeFile(path, cfg); err != nil {
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

// Save 原子写回（tmp + rename），与 Python 版一致。
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	tmp := c.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
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
