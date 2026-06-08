//go:build !windows

package hal

import "context"

// detectPlatformGPU is a no-op on non-Windows hosts. Linux AMD detection is
// handled by the SMI probe chain and detectAMDDRM (sysfs); the Windows CIM
// fallback lives in detect_windows.go.
func detectPlatformGPU(_ context.Context, _ CommandRunner) *GPUInfo {
	return nil
}
