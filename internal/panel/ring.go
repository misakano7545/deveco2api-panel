// ring.go 固定容量日志环形缓冲（并发安全），面板日志页的唯一数据源。
//
// 装配期把 logfmt.SetSink 指到 Add：stdout 仍是持久出口，环只是「最近 N 行」的
// 观测窗口，进程重启即空——不落盘，也不参与任何业务决策。
package panel

import (
	"sync"

	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
)

// Ring 定长环形日志缓冲：满了丢最旧的一行，写入恒为 O(1)。
type Ring struct {
	mu    sync.Mutex
	max   int
	lines []logfmt.Line
}

// NewRing 构建容量为 capacity 的环（<=0 视为 1）。
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 1
	}
	return &Ring{max: capacity, lines: make([]logfmt.Line, 0, capacity)}
}

// Capacity 返回环容量（面板 API 用它校验 limit 上界）。
func (r *Ring) Capacity() int { return r.max }

// Add 追加一行（logfmt sink 回调）。
func (r *Ring) Add(l logfmt.Line) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) == r.max {
		copy(r.lines, r.lines[1:])
		r.lines = r.lines[:r.max-1]
	}
	r.lines = append(r.lines, l)
}

// Snapshot 返回最近 limit 行的副本（limit<=0 返回全部）。返回副本，
// 调用方序列化时不受并发写入影响。
func (r *Ring) Snapshot(limit int) []logfmt.Line {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.lines)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]logfmt.Line, limit)
	copy(out, r.lines[n-limit:])
	return out
}
