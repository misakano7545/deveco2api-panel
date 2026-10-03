package config

// config_test.go — 落盘/回读与权限：文件里有 token，权限与缺项兜底是硬约束。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTripAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.SetPath(path)
	cfg.Server.APIKey = "k1"
	cfg.DevEco.Auth.AccessToken = "at-1"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("配置含 token，权限应为 0600，实际 %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("临时文件应已被 rename 掉")
	}

	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Server.APIKey != "k1" || back.Tokens().AccessToken != "at-1" || back.Path() != path {
		t.Fatalf("回读不一致: %+v path=%s", back.Server, back.Path())
	}

	// 缺项兜底：只给一段也要能起来，其余取默认
	if err := os.WriteFile(path, []byte(`{"server":{"api_key":"k2"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Server.APIKey != "k2" || p.Server.Port != 10102 || p.DevEco.Model != "GLM-5.1" {
		t.Fatalf("缺项未回落到默认: %+v %+v", p.Server, p.DevEco)
	}
}
