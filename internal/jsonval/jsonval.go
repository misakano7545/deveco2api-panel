// Package jsonval 上游 JSON 的无类型取值 helper。
//
// 上游响应解成 map[string]any 后，取值处到处要防 nil 与多类型；各包各写一份
// strOf 必然漂移（语义对齐 Python 的 str()），收敛到这里只保留一份。
package jsonval

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Str 把 JSON 取出的值字符串化：nil → ""（fmt.Sprint(nil) 会得到 "<nil>"，这里不要），
// 数字/布尔按 fmt.Sprint，与 Python str() 同形（面板与日志都依赖这个形状）。
func Str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// FirstNonEmpty 返回第一个非空串（userId 这类字段：userInfo 缺失时回落到 jwt 声明）。
func FirstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Marshal 序列化为 JSON，与 Python json.dumps 同口径：不转义 < > &
// （上游与前端都直接看这些字符），且不带尾随换行（json.Encoder 会补一个）。
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
