//go:build !windows

package main

import "syscall"

// diskSpace 返回数据目录所在磁盘的剩余和总空间（字节）。
func diskSpace(dir string) (free, total int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize), true
}
