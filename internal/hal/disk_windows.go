//go:build windows

package hal

import (
	"syscall"
	"unsafe"
)

func diskStats(path string) (freeMiB, totalMiB int64) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.NewProc("GetDiskFreeSpaceExW")

	var freeBytesAvailable, totalBytes, totalFreeBytes int64

	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0
	}

	ret, _, _ := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if ret == 0 {
		return 0, 0
	}

	return freeBytesAvailable / (1024 * 1024), totalBytes / (1024 * 1024)
}

func listVolumes() []VolumeInfo {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getLogicalDrives := kernel32.NewProc("GetLogicalDrives")

	mask, _, _ := getLogicalDrives.Call()
	if mask == 0 {
		return nil
	}

	var volumes []VolumeInfo
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		drive := string(rune('A'+i)) + ":\\"
		free, total := diskStats(drive)
		if total == 0 {
			continue
		}
		volumes = append(volumes, VolumeInfo{
			MountPoint: drive,
			TotalMiB:   total,
			FreeMiB:    free,
		})
	}

	return volumes
}
