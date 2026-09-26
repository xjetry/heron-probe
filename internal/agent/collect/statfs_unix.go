//go:build linux || darwin

package collect

import "golang.org/x/sys/unix"

// statfs 按 df 的口径：total = 全部块，used = 全部块 − 空闲块（含 root 保留）。
// APFS 上同一容器内各卷的 statfs 给出的是容器的块数与空闲块，used 因此是整个容器的占用。
func statfs(path string) (uint64, uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, (st.Blocks - st.Bfree) * bs, nil
}
