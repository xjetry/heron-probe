//go:build linux

package collect

import (
	"os"

	"github.com/xjetry/heron-probe/internal/clock"
)

func NewPlatform(version string, clk clock.Clock, netInclude, netExclude []string) (*Collector, error) {
	return &Collector{Host: &ProcFS{FS: os.DirFS("/"), DiskUsage: statfs}, Clock: clk, NetInclude: netInclude, NetExclude: netExclude, Version: version}, nil
}

// DefaultNetExclude 是本平台未给 --net-exclude 时不计入流量的网卡。
func DefaultNetExclude() []string { return linuxNetExclude }
