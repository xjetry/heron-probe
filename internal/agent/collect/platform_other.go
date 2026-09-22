//go:build !linux

package collect

import (
	"errors"
	"runtime"

	"github.com/xjetry/probe/internal/clock"
)

func NewPlatform(string, clock.Clock, []string, []string) (*Collector, error) {
	return nil, errors.New("metrics collection is not implemented for " + runtime.GOOS)
}
