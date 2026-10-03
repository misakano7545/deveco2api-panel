package main

// ids.go — 复刻 id.py（即 packages/opencode/src/id/id.ts）的 ID 生成。

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const idChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var (
	idMu      sync.Mutex
	idLastTS  int64
	idCounter uint64
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
	idMu.Lock()
	ts := time.Now().UnixMilli()
	if ts != idLastTS {
		idLastTS = ts
		idCounter = 0
	}
	idCounter++
	c := idCounter
	idMu.Unlock()

	now := (uint64(ts) << 12) + c
	if descending {
		now = ^now
	}
	now &= (1 << 48) - 1
	tb := []byte{byte(now >> 40), byte(now >> 32), byte(now >> 24), byte(now >> 16), byte(now >> 8), byte(now)}
	return prefix + "_" + hex.EncodeToString(tb) + randomBase62(14)
}

func sessionID() string { return createID("ses", true) }
func messageID() string { return createID("msg", false) }

// chatID 生成 32 位小写十六进制（16 随机字节）。
func chatID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
