// Package session 复刻官方客户端的会话/消息 ID 生成（id.py 即
// packages/opencode/src/id/id.ts）：上游按 ID 形态归组会话与请求。
package session

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const idChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var (
	mu      sync.Mutex
	lastTS  int64
	counter uint64
)

func randomBase62(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = idChars[int(b[i])%62]
	}
	return string(b)
}

func createID(prefix string, descending bool) string {
	mu.Lock()
	ts := time.Now().UnixMilli()
	if ts != lastTS {
		lastTS = ts
		counter = 0
	}
	counter++
	c := counter
	mu.Unlock()

	now := (uint64(ts) << 12) + c
	if descending {
		now = ^now
	}
	now &= (1 << 48) - 1
	tb := []byte{byte(now >> 40), byte(now >> 32), byte(now >> 24), byte(now >> 16), byte(now >> 8), byte(now)}
	return prefix + "_" + hex.EncodeToString(tb) + randomBase62(14)
}

// SessionID 会话 ID（ses_ 前缀，时间戳降序，与官方一致）。
func SessionID() string { return createID("ses", true) }

// MessageID 消息 ID（msg_ 前缀，时间戳升序）。
func MessageID() string { return createID("msg", false) }

// ChatID 32 位小写十六进制请求 ID（16 随机字节）。
func ChatID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
