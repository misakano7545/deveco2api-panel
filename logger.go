package main

// logger.go — 与 Python 版相同的 [HH:MM:SS][LEVEL] 行格式。

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	logMu    sync.Mutex
	logLevel = 1 // 0=debug 1=info 2=warning 3=error
)

func setLogLevel(s string) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		logLevel = 0
	case "WARNING":
		logLevel = 2
	case "ERROR":
		logLevel = 3
	default:
		logLevel = 1
	}
}

func logf(lv int, name, format string, args ...any) {
	if lv < logLevel {
		return
	}
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("15:04:05")
	logMu.Lock()
	fmt.Fprintf(os.Stdout, "[%s][%s] %s\n", ts, name, msg)
	logMu.Unlock()
	logRingMu.Lock()
	logRing = append(logRing, logRecord{Time: ts, Level: name, Msg: msg})
	if len(logRing) > logRingMax {
		logRing = logRing[len(logRing)-logRingMax:]
	}
	logRingMu.Unlock()
}

// ── 环形日志缓冲（面板「日志」页读取）─────────────────────────────

type logRecord struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

const logRingMax = 500

var (
	logRingMu sync.Mutex
	logRing   []logRecord
)

func recentLogs(limit int) []logRecord {
	logRingMu.Lock()
	defer logRingMu.Unlock()
	n := len(logRing)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]logRecord, limit)
	copy(out, logRing[n-limit:])
	return out
}

func logDebug(format string, a ...any) { logf(0, "DEBUG", format, a...) }
func logInfo(format string, a ...any)  { logf(1, "INFO", format, a...) }
func logWarn(format string, a ...any)  { logf(2, "WARNING", format, a...) }
func logError(format string, a ...any) { logf(3, "ERROR", format, a...) }

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
