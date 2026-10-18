package panel

import (
	"bytes"
	"testing"
)

// 前端是 go:embed 的静态资产，编译器不管它对不对：两处踩过的坑各留一条断言。
//  1. 配置文件是 config.json（Python 版的 config.toml 早就不存在了），
//     密钥门的提示写错文件名 = 用户照着找不到密钥；
//  2. 后台轮询每 5s 会带 401 走一次密钥门，已打开时不能重置输入框，
//     否则正在输入的密钥被清空，提交上去永远「密钥不正确」。
func TestKeyGateAssets(t *testing.T) {
	if bytes.Contains(indexHTML, []byte("config.toml")) {
		t.Fatal("面板页面不应再提到 config.toml（配置文件是 config.json）")
	}
	if !bytes.Contains(indexHTML, []byte("config.json 中的 server.api_key")) {
		t.Fatal("密钥门提示应指向 config.json 的 server.api_key")
	}
	// 清空输入框必须带「门本来是关的」这个前提。
	if !bytes.Contains(appJS, []byte("if (!wasOn) { $('keyInput').value = '';")) {
		t.Fatal("showKeyGate 不能无条件清空输入框：轮询的 401 会清掉用户正在输入的密钥")
	}
}
