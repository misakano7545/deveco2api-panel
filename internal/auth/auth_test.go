package auth

// auth_test.go — 离线自测：登录收尾（回调 → tempToken 换 jwt → Tokens），无华为调用。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFinalizeScenarios(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/authrouter/auth/api/temptoken/check", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "h.p.s") // 假 jwt（3 段）
	})
	mux.HandleFunc("/authrouter/auth/api/jwToken/check", func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(map[string]any{"status": true, "userInfo": map[string]any{
			"accessToken": "AT", "refreshToken": "RT", "userId": "U1", "name": "N", "realName": true,
		}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
	up := httptest.NewServer(mux)
	defer up.Close()

	cfg := Config{BaseURL: up.URL, AppID: "1008", AuthURL: "console/DevEcoIDE/apply", CallbackPort: 10101}

	tok, err := Finalize(cfg, map[string]string{"code": "x", "tempToken": "tt1", "siteId": "1", "quit": ""})
	if err != nil || tok.AccessToken != "AT" || tok.UserID != "U1" {
		t.Fatalf("正常收尾应成功: %v %+v", err, tok)
	}
	if _, err := Finalize(cfg, map[string]string{"siteId": "1", "quit": "access_denied"}); err == nil {
		t.Fatal("取消授权应报错")
	}
	if _, err := Finalize(cfg, map[string]string{"tempToken": "tt", "siteId": "2"}); err == nil {
		t.Fatal("非中国区应报错")
	}
	if _, err := Finalize(cfg, map[string]string{"siteId": "1"}); err == nil {
		t.Fatal("缺 tempToken 应报错")
	}
}

// TestSaveRejectsEmptyJWT Store 不接受空 jwt（防把失败结果写进配置）。
func TestSaveRejectsEmptyJWT(t *testing.T) {
	s := New(Config{}, Tokens{}, nil)
	if err := s.Save(Tokens{}); err == nil {
		t.Fatal("空 jwt 应被拒绝")
	}
}
