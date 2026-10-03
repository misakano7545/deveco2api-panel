// thinking.go 思维链剥离：上游把 GLM 系模型的思维链混在 content 里，
// OpenAI 兼容客户端期望它单独落在 reasoning_content。
//
// 非流式：出现 </think> 就地切开（零成本，无边界则原样透传）。
// 流式：按 thinking_models 名单缓冲 content，跨 chunk 找 </think>；
// 流结束仍无边界则整段按正文兜底（宁可不剥离，也不丢内容）。
package upstream

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/misakano7545/deveco2api-panel/internal/jsonval"
)

const thinkEnd = "</think>"

// StripThinkingNonstream 就地剥离非流式响应里的思维链。
func StripThinkingNonstream(payload map[string]any) {
	choices, _ := payload["choices"].([]any)
	for _, c := range choices {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		msg, _ := cm["message"].(map[string]any)
		if msg == nil {
			continue
		}
		content, _ := msg["content"].(string)
		idx := strings.Index(content, thinkEnd)
		if idx < 0 {
			continue
		}
		think, answer := content[:idx], content[idx+len(thinkEnd):]
		msg["content"] = strings.TrimLeftFunc(answer, unicode.IsSpace)
		if strings.TrimSpace(think) != "" {
			msg["reasoning_content"] = think
		}
	}
}

// sseChunk 用原帧模板造一个 delta 帧（id/object/created/model 沿用上游）。
func sseChunk(template map[string]any, delta map[string]any) string {
	chunk := map[string]any{}
	for _, k := range []string{"id", "object", "created", "model"} {
		if v, ok := template[k]; ok {
			chunk[k] = v
		}
	}
	if _, ok := chunk["object"]; !ok {
		chunk["object"] = "chat.completion.chunk"
	}
	chunk["choices"] = []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}
	b, _ := jsonval.Marshal(chunk)
	return "data: " + string(b) + "\n\n"
}

// TransformSSE 转发上游 SSE；strip=true 时把首个 </think> 之前的思维链
// 转入 reasoning_content（跨 chunk 兼容；空 data: 帧跳过不转发）。
func TransformSSE(raw string, strip bool) string {
	var out strings.Builder
	holding := strip
	buf := ""
	var template map[string]any

	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(line[6:])
		if payload == "" { // 上游偶发空 data: 帧，直接跳过
			continue
		}
		if payload == "[DONE]" {
			if holding && buf != "" {
				out.WriteString(sseChunk(template, map[string]any{"content": buf}))
			}
			out.WriteString("data: [DONE]\n\n")
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			out.WriteString("data: " + payload + "\n\n")
			continue
		}
		template = chunk

		choices, _ := chunk["choices"].([]any)
		for _, c := range choices {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			d, _ := cm["delta"].(map[string]any)
			if d == nil {
				continue
			}
			if _, ok := d["role"]; !ok {
				d["role"] = "assistant"
			}
		}

		if holding && len(choices) > 0 {
			// ponytail: 上游恒为单 choice，思维链剥离只处理第一个 choice
			cm, _ := choices[0].(map[string]any)
			d, _ := cm["delta"].(map[string]any)
			content, _ := d["content"].(string)
			if content != "" {
				buf += content
				if i := strings.Index(buf, thinkEnd); i >= 0 {
					think, rest := buf[:i], buf[i+len(thinkEnd):]
					holding, buf = false, ""
					if strings.TrimSpace(think) != "" {
						out.WriteString(sseChunk(template, map[string]any{"role": "assistant", "reasoning_content": think}))
					}
					d["content"] = strings.TrimLeftFunc(rest, unicode.IsSpace)
				} else {
					d["content"] = "" // 思维链阶段：内容暂存缓冲，仅透传结构帧保活
				}
			}
			if holding && buf != "" && cm["finish_reason"] != nil {
				// 流结束仍无 </think>：整段按正文兜底（宁可不剥离，也不丢内容）
				out.WriteString(sseChunk(template, map[string]any{"content": buf}))
				holding, buf = false, ""
			}
		}

		b, err := jsonval.Marshal(chunk)
		if err != nil {
			continue
		}
		out.WriteString("data: " + string(b) + "\n\n")
	}
	return out.String()
}
