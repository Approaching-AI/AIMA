//go:build windows

package model

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// platformExtraScanPaths enumerates fixed and removable drives and returns the
// conventional model directories that actually exist on each. This lets models
// stored off the system drive (e.g. D:\models, D:\lmstudio\models) be found
// without manual configuration, while staying targeted — it never walks whole
// drives, so recycle bins / system folders are not pulled in.
func platformExtraScanPaths() []string {
	var paths []string
	for _, drive := range fixedDriveRoots() {
		for _, sub := range conventionalDriveSubdirs() {
			p := filepath.Join(drive, sub)
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				paths = append(paths, p)
			}
		}
	}
	return paths
}

// conventionalDriveSubdirs are the well-known model-manager layouts probed on
// each drive root.
func conventionalDriveSubdirs() []string {
	return []string{
		"models",
		filepath.Join("lmstudio", "models"),
		filepath.Join(".lmstudio", "models"),
		filepath.Join(".cache", "lm-studio", "models"),
		filepath.Join(".ollama", "models"),
		filepath.Join(".cache", "huggingface", "hub"),
	}
}

// fixedDriveRoots returns roots ("D:\\", ...) of fixed and removable drives.
// Network and CD-ROM drives are excluded to avoid stalls on disconnected mounts.
func fixedDriveRoots() []string {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getLogicalDrives := kernel32.NewProc("GetLogicalDrives")
	getDriveType := kernel32.NewProc("GetDriveTypeW")

	mask, _, _ := getLogicalDrives.Call()
	if mask == 0 {
		return nil
	}

	const driveRemovable, driveFixed = 2, 3
	var roots []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + ":\\"
		ptr, err := syscall.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		dt, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(ptr)))
		if dt == driveFixed || dt == driveRemovable {
			roots = append(roots, root)
		}
	}
	return roots
}
