package panel

import (
	"fmt"
	"testing"

	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
)

func logLine(i int) logfmt.Line {
	return logfmt.Line{Time: "00:00:00", Level: "INFO", Msg: fmt.Sprintf("m%d", i)}
}

func TestRingCapacityAndSnapshot(t *testing.T) {
	r := NewRing(3)
	for i := 1; i <= 5; i++ {
		r.Add(logLine(i))
	}
	if got := r.Capacity(); got != 3 {
		t.Fatalf("容量应为 3，实际 %d", got)
	}
	// 超容量：保留最新 3 条（3,4,5）
	all := r.Snapshot(0)
	if len(all) != 3 || all[0].Msg != "m3" || all[2].Msg != "m5" {
		t.Fatalf("环形覆盖不对: %+v", all)
	}
	// limit 生效且返回副本
	last2 := r.Snapshot(2)
	if len(last2) != 2 || last2[0].Msg != "m4" || last2[1].Msg != "m5" {
		t.Fatalf("limit 语义不对: %+v", last2)
	}
	last2[0].Msg = "mutated"
	if r.Snapshot(2)[0].Msg != "m4" {
		t.Fatal("Snapshot 应返回副本")
	}
	// 空环
	if got := NewRing(2).Snapshot(5); len(got) != 0 {
		t.Fatalf("空环应返回空: %+v", got)
	}
}
