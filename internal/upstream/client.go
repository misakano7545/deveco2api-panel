// Package upstream DevEco Code 上游（cn.devecostudio.huawei.com）的 HTTP 客户端
// 与响应整形：模型列表、对话转发（含 SSE 转发）、请求体构造、思维链剥离与
// 上游错误转译。只负责「跟华为说话」，不碰 http.ResponseWriter——HTTP 表面
// 在 internal/server。
package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/misakano7545/deveco2api-panel/internal/jsonval"
)

// Config 上游依赖（main 装配注入）。
type Config struct {
	BaseURL        string
	Client         string // x-deveco-client：上游按客户端类型归组
	Project        string // x-deveco-project
	UserAgent      string
	Model          string   // 默认模型（请求未指定 model 时）
	ThinkingModels []string // 需要剥离思维链的模型（上游把思维链混在 content 里）
	// Token 返回当前 access_token（每次请求现取，保活/刷新后立即生效）。
	Token func() string
	// HTTP 出站客户端；nil 时用 300s 超时的默认客户端（上游单次生成可达数分钟）。
	HTTP *http.Client
}

// Client 上游客户端（并发安全：无可变状态）。
type Client struct {
	cfg  Config
	http *http.Client
}

// New 构建上游客户端。
func New(cfg Config) *Client {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 300 * time.Second}
	}
	if cfg.Token == nil {
		cfg.Token = func() string { return "" }
	}
	return &Client{cfg: cfg, http: cfg.HTTP}
}

// DefaultModel 配置里的默认模型。
func (c *Client) DefaultModel() string { return c.cfg.Model }

// StripsThinking 该模型是否需要剥离思维链（thinking_models 配置）。
func (c *Client) StripsThinking(model string) bool {
	for _, m := range c.cfg.ThinkingModels {
		if m == model {
			return true
		}
	}
	return false
}

// URL 对话端点：流式与非流式是两个不同路径。
func (c *Client) URL(stream bool) string {
	base := strings.TrimRight(c.cfg.BaseURL, "/") + "/sse/codeGenie/maas/v2"
	if stream {
		return base + "/chat/completions"
	}
	return base + "/no-stream/chat/completions"
}

func (c *Client) headers(sessionID, msgID, chatID string) map[string]string {
	return map[string]string{
		"Authorization":    "Bearer " + c.cfg.Token(),
		"Content-Type":     "application/json",
		"Chat-Id":          chatID,
		"Session-Id":       sessionID,
		"x-deveco-client":  c.cfg.Client,
		"x-deveco-project": c.cfg.Project,
		"x-deveco-request": msgID,
		"x-deveco-session": sessionID,
		"User-Agent":       c.cfg.UserAgent,
		"lang":             "en",
		"Accept":           "*/*",
	}
}

// ModelConfig 拉取账号可见的模型配置（面板与 /v1/models 共用）。
// 返回 (解析后的 JSON, HTTP 状态码, 错误)；状态码非 200 时 data 为 nil。
func (c *Client) ModelConfig() (map[string]any, int, error) {
	u := strings.TrimRight(c.cfg.BaseURL, "/") + "/codeGenie/modelConfig?localVersion=0&pluginVersion=CLI.0.2.0"
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, 200, err
	}
	return data, 200, nil
}

// Chat 转发一次对话，返回 (HTTP 状态码, 原始响应体, 传输错误)。
// 整包读完后交给调用方：上游响应形态（200+error 帧 / 200+error 对象 / 4xx）
// 的处理与状态码映射在 internal/server，靠这里给出的原始体判断。
func (c *Client) Chat(sessionID, msgID, chatID string, body map[string]any) (int, []byte, error) {
	stream, _ := body["stream"].(bool)
	payload, err := jsonval.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest("POST", c.URL(stream), bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	for k, v := range c.headers(sessionID, msgID, chatID) {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}
