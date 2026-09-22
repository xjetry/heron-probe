//go:build linux

package collect

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/xjetry/probe/internal/clock"
)

func NewPlatform(version string, clk clock.Clock, netInclude, netExclude []string) (*Collector, error) {
	return &Collector{FS: os.DirFS("/"), DiskUsage: statfs, Clock: clk, NetInclude: netInclude, NetExclude: netExclude, Version: version}, nil
}

// statfs 按 df 的口径：total = 全部块，used = 全部块 − 空闲块（含 root 保留）。
func statfs(path string) (uint64, uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, (st.Blocks - st.Bfree) * bs, nil
}
