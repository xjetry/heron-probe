//go:build !linux && !darwin

package collect

import (
	"errors"
	"runtime"

	"github.com/xjetry/heron-probe/internal/clock"
)

func NewPlatform(string, clock.Clock, []string, []string) (*Collector, error) {
	return nil, errors.New("metrics collection is not implemented for " + runtime.GOOS)
}

// DefaultNetExclude 在不支持采集的平台上没有意义。
func DefaultNetExclude() []string { return nil }
