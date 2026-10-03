package server

// import_test.go 面板凭证导入端点：鉴权、两种格式、字段校验、覆盖语义与落盘。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

const importJSON = `{"auth":{"jwt_token":"new.jwt.token","access_token":"AT-new",` +
	`"refresh_token":"RT-new","user_id":"U9","user_name":"导入号"}}`

func postRaw(t *testing.T, url, key, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest("POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestPanelImportConfig(t *testing.T) {
	ts, _, cfg := newTestServer(t, newMock())
	ep := ts.URL + "/panel/api/import/config"

	// 无 key → 401（与 /v1/*、其它面板接口同口径）
	if code, _ := postRaw(t, ep, "", importJSON); code != 401 {
		t.Fatalf("无 key 应 401，实际 %d", code)
	}
	// 坏 JSON → 400
	if code, _ := postRaw(t, ep, "test-key", `{"auth":`); code != 400 {
		t.Fatalf("坏 JSON 应 400，实际 %d", code)
	}
	// 缺 access_token → 400
	if code, _ := postRaw(t, ep, "test-key", `{"auth":{"jwt_token":"x.y.z"}}`); code != 400 {
		t.Fatalf("缺 access_token 应 400，实际 %d", code)
	}
	// 既不是 JSON 也不是 base64 → 400
	if code, _ := postRaw(t, ep, "test-key", "!!! not base64 !!!"); code != 400 {
		t.Fatalf("非法体应 400，实际 %d", code)
	}

	// 正常 JSON：200 + 回显被替换的旧身份（测试夹具登录的是 jwt-1/old-token，无 user_id）
	code, raw := postRaw(t, ep, "test-key", importJSON)
	if code != 200 {
		t.Fatalf("导入应 200，实际 %d %s", code, raw)
	}
	var res struct {
		OK       bool           `json:"ok"`
		Replaced map[string]any `json:"replaced"`
		Account  map[string]any `json:"account"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Account["user_id"] != "U9" || res.Account["user_name"] != "导入号" {
		t.Fatalf("响应账号不对: %s", raw)
	}
	if res.Replaced["user_id"] != "" {
		t.Fatalf("应回显被替换的空身份，实际 %v", res.Replaced)
	}

	// 生效：面板状态读的是内存 token，立即反映新账号
	code, raw = doJSON(t, "GET", ts.URL+"/panel/api/status", "test-key", nil)
	if code != 200 || !strings.Contains(string(raw), `"U9"`) {
		t.Fatalf("导入后状态未生效: %d %s", code, raw)
	}
	// 落盘：config.json 里已是新凭证（重启后仍是这个号）
	onDisk, err := os.ReadFile(cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "AT-new") || !strings.Contains(string(onDisk), `"U9"`) {
		t.Fatalf("未落盘: %s", onDisk)
	}

	// base64 形式：同一份 JSON 编码后照样收
	code, raw = postRaw(t, ep, "test-key", base64.StdEncoding.EncodeToString([]byte(importJSON)))
	if code != 200 {
		t.Fatalf("base64 导入应 200，实际 %d %s", code, raw)
	}
	// 扁平写法（不带 auth 外层）也收
	if code, raw = postRaw(t, ep, "test-key", `{"jwt_token":"a.b.c","access_token":"AT2"}`); code != 200 {
		t.Fatalf("扁平写法应 200，实际 %d %s", code, raw)
	}
}
