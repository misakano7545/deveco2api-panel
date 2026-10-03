package main

// config.go — config.toml 的读写（与 Python 版 / 上游格式完全兼容）。

import (
	"os"
	"sync"

	"github.com/BurntSushi/toml"
)

type AuthConfig struct {
	JWTToken     string `toml:"jwt_token"`
	AccessToken  string `toml:"access_token"`
	RefreshToken string `toml:"refresh_token"`
	UserID       string `toml:"user_id"`
	UserName     string `toml:"user_name"`
}

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

type ServerConfig struct {
	Host   string `toml:"host"`
	Port   int    `toml:"port"`
	APIKey string `toml:"api_key"`
}

type LoggingConfig struct {
	Level string `toml:"level"`
}

type Config struct {
	Server  ServerConfig  `toml:"server"`
	DevEco  DevEcoConfig  `toml:"deveco"`
	Logging LoggingConfig `toml:"logging"`

	path string
	mu   sync.Mutex
}

func defaultConfig() *Config {
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

func loadConfig(path string) (*Config, error) {
	cfg := defaultConfig()
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, err
	}
	cfg.path = path
	return cfg, nil
}

// save 原子写回（tmp + rename），与 Python 版一致。
func (c *Config) save() error {
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
