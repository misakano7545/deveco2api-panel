package server

// panel_test.go — 面板路由 / 鉴权 / 数据接口自测。

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/misakano7545/deveco2api-panel/internal/jsonval"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
)

func TestPanelRoutes(t *testing.T) {
	ts, _, _ := newTestServer(t, newMock())
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// 页面：匿名可加载 + 安全头
	resp, err := http.Get(ts.URL + "/panel/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "DevEco2API") {
		t.Fatalf("面板页异常: %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("面板安全头缺失")
	}

	// /panel → /panel/ 跳转（ServeMux 自动重定向，301）；/ → /panel/ 跳转
	resp, err = noRedirect.Get(ts.URL + "/panel")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 301 || !strings.HasSuffix(resp.Header.Get("Location"), "/panel/") {
		t.Fatalf("/panel 应跳 /panel/，实际 %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, err = noRedirect.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || !strings.HasSuffix(resp.Header.Get("Location"), "/panel/") {
		t.Fatalf("/ 应跳 /panel/，实际 %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	// 前端脚本
	resp, err = http.Get(ts.URL + "/panel/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(js), "/panel/api/status") {
		t.Fatalf("app.js 异常: %d", resp.StatusCode)
	}

	// API：无 key 401（本测试 cfg 设置了 api_key），带 key 200
	if code, _ := doJSON(t, "GET", ts.URL+"/panel/api/status", "", nil); code != 401 {
		t.Fatalf("无 key 应 401，实际 %d", code)
	}

	code, raw := doJSON(t, "GET", ts.URL+"/panel/api/status", "test-key", nil)
	if code != 200 {
		t.Fatalf("status 应 200，实际 %d %s", code, raw)
	}
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st["service"] != "deveco2api-panel" || st["auth_required"] != true {
		t.Fatalf("status 字段不对: %s", raw)
	}
	if _, ok := st["uptime_seconds"].(float64); !ok {
		t.Fatalf("缺少 uptime_seconds: %s", raw)
	}
	ka, _ := st["keepalive"].(map[string]any)
	if ka == nil || ka["hours"] != float64(6) {
		t.Fatalf("keepalive 字段不对: %s", raw)
	}

	// 日志：写入一条后应能在环形缓冲里读到
	logfmt.Infof("panel test %s", "marker")
	code, raw = doJSON(t, "GET", ts.URL+"/panel/api/logs?limit=50", "test-key", nil)
	if code != 200 || !strings.Contains(string(raw), "panel test marker") {
		t.Fatalf("logs 未返回日志行: %d %s", code, raw)
	}

	// 模型：mock 上游两个模型，GLM-5.3 应标记为已剥离思维链
	code, raw = doJSON(t, "GET", ts.URL+"/panel/api/models", "test-key", nil)
	if code != 200 {
		t.Fatalf("models 应 200，实际 %d %s", code, raw)
	}
	var md struct {
		Count  int              `json:"count"`
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(raw, &md); err != nil {
		t.Fatal(err)
	}
	if md.Count != 2 {
		t.Fatalf("模型数应为 2，实际 %d: %s", md.Count, raw)
	}
	stripped := map[string]bool{}
	for _, m := range md.Models {
		stripped[jsonval.Str(m["id"])] = m["thinking_stripped"] == true
	}
	if !stripped["GLM-5.3"] || stripped["GLM-5.1"] {
		t.Fatalf("思维链剥离标记不对: %v", stripped)
	}
}
