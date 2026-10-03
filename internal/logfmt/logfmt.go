// Package logfmt 统一日志行格式与文本截断 helper。
//
// 行格式 [HH:MM:SS][LEVEL] msg 与 Python 版逐字一致：两个实现可互换，
// 对着同一份日志口径排障。stdout 是唯一持久出口；SetSink 另把每行镜像给
// 面板环形缓冲（internal/panel.Ring），供 /panel/api/logs 读取。
package logfmt

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Line 单条日志行（面板日志页的数据形态，字段名即前端契约）。
type Line struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

const (
	levelDebug = iota
	levelInfo
	levelWarn
	levelError
)

var (
	mu    sync.Mutex
	level = levelInfo
	sink  func(Line)
)

// SetLevel 设置最小输出级别（DEBUG/INFO/WARNING/ERROR）；其它值按 INFO。
func SetLevel(s string) {
	mu.Lock()
	defer mu.Unlock()
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		level = levelDebug
	case "WARNING":
		level = levelWarn
	case "ERROR":
		level = levelError
	default:
		level = levelInfo
	}
}

// SetSink 注册日志镜像出口（面板环形缓冲）。装配期调用一次；nil = 只写 stdout。
func SetSink(fn func(Line)) {
	mu.Lock()
	defer mu.Unlock()
	sink = fn
}

func logf(lv int, name, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("15:04:05")
	mu.Lock()
	if lv < level {
		mu.Unlock()
		return
	}
	fmt.Fprintf(os.Stdout, "[%s][%s] %s\n", ts, name, msg)
	fn := sink
	mu.Unlock() // 先解锁再回调：sink 内部若再打日志也不会自锁
	if fn != nil {
		fn(Line{Time: ts, Level: name, Msg: msg})
	}
}

func Debugf(format string, a ...any) { logf(levelDebug, "DEBUG", format, a...) }
func Infof(format string, a ...any)  { logf(levelInfo, "INFO", format, a...) }
func Warnf(format string, a ...any)  { logf(levelWarn, "WARNING", format, a...) }
func Errorf(format string, a ...any) { logf(levelError, "ERROR", format, a...) }

// Truncate 截断到 n 字节上限。切点落在多字节字符中间时回退到 rune 边界：
// 上游错误 body 多为中文，按字节硬切会在日志里留半截乱码。
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
