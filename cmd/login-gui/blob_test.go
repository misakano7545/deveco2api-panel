package main

// blob_test.go 凭证块产物自测：面板两种格式都必须能收，且文件权限不放大。

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/misakano7545/deveco2api-panel/internal/auth"
)

func TestBuildBlobShape(t *testing.T) {
	tok := auth.Tokens{
		JWTToken: "h.p.s", AccessToken: "AT", RefreshToken: "RT",
		UserID: "30086000811119974", UserName: "Misa****",
	}
	blob := buildBlob(tok)

	// JSON 块：面板按 {"auth":{...}} 解析（字段名与 config.AuthConfig 一致）
	var doc struct {
		Auth map[string]string `json:"auth"`
	}
	if err := json.Unmarshal([]byte(blob.JSON), &doc); err != nil {
		t.Fatalf("JSON 块不可解析: %v (%s)", err, blob.JSON)
	}
	if doc.Auth["jwt_token"] != "h.p.s" || doc.Auth["access_token"] != "AT" ||
		doc.Auth["user_id"] != tok.UserID || doc.Auth["user_name"] != tok.UserName {
		t.Fatalf("字段不对: %v", doc.Auth)
	}

	// base64 块：必须能解回同一份 JSON（面板会自动试解）
	dec, err := base64.StdEncoding.DecodeString(blob.Base64)
	if err != nil {
		t.Fatalf("base64 块不可解: %v", err)
	}
	if string(dec) != blob.JSON {
		t.Fatalf("base64 解出的内容与 JSON 不一致: %s", dec)
	}
}

func TestWriteBlobMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-block.json")
	blob := buildBlob(auth.Tokens{JWTToken: "h.p.s", AccessToken: "AT"})
	got, err := writeBlob(path, blob)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("凭证块含 token，权限应为 0600，实际 %v", fi.Mode().Perm())
	}
}
