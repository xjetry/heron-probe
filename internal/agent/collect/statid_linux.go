//go:build linux

package collect

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// statID 取路径的 st_dev 与 st_ino，st_dev 折成 mountinfo 同构的 "major:minor" 键：
// 识别用它把 /proc 文件对到挂载（判来源）、给挂载根 cgroup 目录标身份（容器重建即换）。
// statfs 的类型判 /sys/fs/cgroup 是不是 cgroup2；stat 不到（路径不存在等）把错误原样交回，
// 由识别按"读不出"处理。
func statID(path string) (string, uint64, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return "", 0, err
	}
	return devKey(st.Dev), st.Ino, nil
}

func fsType(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Type), nil
}

// devKey 把 userspace 的 st_dev 编码折成 "major:minor"（与 mountinfo 第 3 列、
// findmnt 的 MAJ:MIN 同构）。Go 的 Sys() 直接给内核译好的 dev_t，不经过 glibc。
func devKey(dev uint64) string {
	return strconv.FormatUint(uint64(unix.Major(dev)), 10) + ":" + strconv.FormatUint(uint64(unix.Minor(dev)), 10)
}
