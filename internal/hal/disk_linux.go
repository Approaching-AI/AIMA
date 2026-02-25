//go:build linux

package hal

import "syscall"

func diskFreeMiB(path string) int64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0
	}
	// Available blocks * block size, converted to MiB
	return int64(stat.Bavail) * int64(stat.Bsize) / (1024 * 1024)
}
