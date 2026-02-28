//go:build linux

package hal

import (
	"os"
	"strings"
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
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}

	realFS := map[string]bool{
		"ext4": true, "ext3": true, "ext2": true,
		"xfs": true, "btrfs": true, "zfs": true,
		"ntfs": true, "vfat": true, "f2fs": true,
		"apfs": true, "hfs": true,
	}

	seen := map[string]bool{}
	var volumes []VolumeInfo

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		device, mountPoint, fsType := fields[0], fields[1], fields[2]

		if !realFS[fsType] || seen[device] {
			continue
		}
		seen[device] = true

		free, total := diskStats(mountPoint)
		if total == 0 {
			continue
		}

		volumes = append(volumes, VolumeInfo{
			MountPoint: mountPoint,
			Device:     device,
			TotalMiB:   total,
			FreeMiB:    free,
		})
	}

	return volumes
}
