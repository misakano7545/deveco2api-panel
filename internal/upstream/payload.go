// payload.go OpenAI 请求体 → 上游请求体的构造（对齐 proxy.py build_deveco_body）。
//
// 上游只认自己的字段集：这里做白名单映射（未列出的 OpenAI 字段不透传），
// 并把上游不接受的形态就地归一（tool_choice 对象 → 字符串枚举）。
package upstream

import (
	"strings"

	"github.com/misakano7545/deveco2api-panel/internal/jsonval"
)

// BuildBody 把 OpenAI 兼容请求体转成上游请求体。
func (c *Client) BuildBody(req map[string]any) map[string]any {
	model := jsonval.Str(req["model"])
	if model == "" {
		model = c.cfg.Model
	}
	msgsRaw, _ := req["messages"].([]any)
	stream, _ := req["stream"].(bool)

	var maxTokens any = float64(32000)
	if v, ok := req["max_tokens"]; ok {
		maxTokens = v
	}

	body := map[string]any{
		"model":      model,
		"messages":   normalizeMessages(msgsRaw),
		"max_tokens": maxTokens,
		"stream":     stream,
	}
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}

	if v, ok := req["tools"]; ok {
		body["tools"] = v
	}
	if v, ok := req["tool_choice"]; ok {
		switch tc := v.(type) {
		case map[string]any:
			// DevEco 后端只接受字符串枚举，OpenAI 对象形式统一映射为 required
			if jsonval.Str(tc["type"]) == "function" {
				body["tool_choice"] = "required"
			} else {
				body["tool_choice"] = "auto"
			}
		case string:
			if tc == "none" || tc == "auto" || tc == "required" {
				body["tool_choice"] = tc
			} else {
				body["tool_choice"] = "auto"
			}
		default:
			body["tool_choice"] = "auto"
		}
	}
	for _, key := range []string{"temperature", "top_p", "frequency_penalty", "presence_penalty", "stop", "seed"} {
		if v, ok := req[key]; ok {
			body[key] = v
		}
	}
	return body
}

// normalizeMessages 归一消息列表：多模态 content 数组压成纯文本
// （图片降级为 [image: url]，上游对话接口不吃结构化 content）。
func normalizeMessages(messages []any) []map[string]any {
	out := []map[string]any{}
	for _, m := range messages {
		mm, _ := m.(map[string]any)
		if mm == nil {
			continue
		}
		role := jsonval.Str(mm["role"])
		if role == "" {
			role = "user"
		}
		content, hasContent := mm["content"]
		if !hasContent {
			content = ""
		}
		if parts, ok := content.([]any); ok {
			var texts []string
			for _, p := range parts {
				pm, _ := p.(map[string]any)
				if pm == nil {
					continue
				}
				switch jsonval.Str(pm["type"]) {
				case "text":
					texts = append(texts, jsonval.Str(pm["text"]))
				case "image_url":
					iu, _ := pm["image_url"].(map[string]any)
					texts = append(texts, "[image: "+jsonval.Str(iu["url"])+"]")
				}
			}
			content = strings.Join(texts, "\n")
		}
		item := map[string]any{"role": role, "content": content}
		for _, k := range []string{"name", "tool_calls", "tool_call_id", "function_call"} {
			if v, ok := mm[k]; ok {
				item[k] = v
			}
		}
		out = append(out, item)
	}
	return out
}
