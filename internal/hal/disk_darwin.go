//go:build darwin

package hal

import (
	"os"
	"path/filepath"
	"syscall"
)

func diskStats(path string) (freeMiB, totalMiB int64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	freeMiB = int64(stat.Bavail) * int64(stat.Bsize) / (1024 * 1024)
	totalMiB = int64(stat.Blocks) * int64(stat.Bsize) / (1024 * 1024)
	return
}

func listVolumes() []VolumeInfo {
	var volumes []VolumeInfo

	// Root volume
	free, total := diskStats("/")
	if total > 0 {
		volumes = append(volumes, VolumeInfo{MountPoint: "/", TotalMiB: total, FreeMiB: free})
	}

	// External volumes under /Volumes
	entries, err := os.ReadDir("/Volumes")
	if err != nil {
		return volumes
	}
	for _, e := range entries {
		mp := filepath.Join("/Volumes", e.Name())
		f, t := diskStats(mp)
		if t > 0 && t != total { // skip if same as root (APFS container)
			volumes = append(volumes, VolumeInfo{MountPoint: mp, TotalMiB: t, FreeMiB: f})
		}
	}

	return volumes
}
