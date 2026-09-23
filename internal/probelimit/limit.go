// Package probelimit 统一 hub 与 agent 的探测预算，避免两侧入口采用不同的限制。
package probelimit

import "time"

const (
	MinIntervalS    = 5
	MaxIntervalS    = 3600
	MinTimeoutMs    = 100
	MaxTimeoutMs    = 5000
	MaxTasksPerNode = 64
	MaxTargetLen    = 253
	MaxResultAge    = 120 * time.Second
)
