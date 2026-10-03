package session

import (
	"strings"
	"testing"
)

func TestIDFormats(t *testing.T) {
	s := SessionID()
	if !strings.HasPrefix(s, "ses_") || len(s) != 30 {
		t.Fatalf("session id 格式不对: %s (%d)", s, len(s))
	}
	msg := MessageID()
	if !strings.HasPrefix(msg, "msg_") || len(msg) != 30 {
		t.Fatalf("message id 格式不对: %s", msg)
	}
	c := ChatID()
	if len(c) != 32 {
		t.Fatalf("chat id 格式不对: %s", c)
	}
	for _, r := range c {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("chat id 非小写十六进制: %s", c)
		}
	}
	// 同一毫秒内连发也必须唯一（时间戳降序 + 计数器）
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := SessionID()
		if seen[id] {
			t.Fatalf("session id 重复: %s", id)
		}
		seen[id] = true
	}
}
