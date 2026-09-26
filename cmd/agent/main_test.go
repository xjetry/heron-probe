package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/testlog"
)

// scripts/e2e.sh 从 agent 的启动行按整秒读出 request_timeout 与 initial_interval，推出告警恢复的等待上限。
// 这里经 runRun 同一个 newLogger 与 logStarting 产出启动行，按脚本同形的 testlog.WholeSeconds 取值再与常量
// 比较：字段缺失、改名、不再是整秒写法或不跟常量走时 make ci 先红，而不是等到 e2e 才停下。
func TestStartingLineStatesReportTiming(t *testing.T) {
	var out bytes.Buffer
	logStarting(newLogger(&out), "http://hub.example")
	for _, f := range []struct {
		key  string
		want time.Duration
	}{
		{"request_timeout", requestTimeout},
		{"initial_interval", initialInterval},
	} {
		got, ok := testlog.WholeSeconds(out.String(), "agent starting", f.key)
		if !ok || got != f.want {
			t.Errorf("starting line: %s read as %v (ok=%v), want whole seconds equal to %v; log: %s", f.key, got, ok, f.want, out.String())
		}
	}
}
