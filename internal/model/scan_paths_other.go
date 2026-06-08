//go:build !windows

package model

// platformExtraScanPaths has no extra locations on non-Windows hosts; Linux
// server conventions (/mnt/data/models, /opt/*/models) are handled directly in
// DefaultScanPaths.
func platformExtraScanPaths() []string { return nil }
