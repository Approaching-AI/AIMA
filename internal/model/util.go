package model

import (
	"os"
	"path/filepath"
	"strings"
)

func normalizeModelName(path string) string {
	name := filepath.Base(path)

	// HF Hub cache: models--<org>--<repo> -> <repo>
	if strings.HasPrefix(name, "models--") {
		parts := strings.SplitN(name, "--", 3)
		if len(parts) == 3 {
			return parts[2]
		}
	}

	// If name looks like a hash, try to extract from parent path
	// (e.g., models--openbmb--MiniCPM-o-4_5/snapshots/<hash>)
	if isHexString(name) && strings.Contains(path, "models--") {
		parts := strings.Split(filepath.ToSlash(path), "/")
		for _, part := range parts {
			if strings.HasPrefix(part, "models--") {
				subParts := strings.SplitN(part, "--", 3)
				if len(subParts) == 3 {
					return subParts[2]
				}
			}
		}
	}

	return name
}

func isHexString(s string) bool {
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func findWeightFile(dir string, entries []os.DirEntry, exts []string) string {
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		for _, ext := range exts {
			if strings.HasSuffix(strings.ToLower(name), ext) {
				return filepath.Join(dir, name)
			}
		}
	}
	return ""
}

func findWeightFileRecursive(dir string, exts []string) string {
	found := ""
	walkModelFilesRecursive(dir, func(currentDir, name string) {
		if found != "" || !weightFileMatches(name, exts) {
			return
		}
		found = filepath.Join(currentDir, name)
	})
	return found
}

// findAllWeightFiles returns all weight files matching the given extensions.
func findAllWeightFiles(dir string, entries []os.DirEntry, exts []string) []string {
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		for _, ext := range exts {
			if strings.HasSuffix(strings.ToLower(name), ext) {
				files = append(files, filepath.Join(dir, name))
				break
			}
		}
	}
	return files
}

func calculateModelSize(dir string, entries []os.DirEntry, exts []string, recursive bool) int64 {
	if recursive {
		var total int64
		walkModelFilesRecursive(dir, func(currentDir, name string) {
			if !weightFileMatches(name, exts) {
				return
			}
			info, err := os.Stat(filepath.Join(currentDir, name))
			if err == nil {
				total += info.Size()
			}
		})
		return total
	}

	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if weightFileMatches(name, exts) {
			// Use os.Stat to follow symlinks (HF Hub cache uses symlinks to blobs)
			fullPath := filepath.Join(dir, name)
			info, err := os.Stat(fullPath)
			if err == nil {
				total += info.Size()
			}
		}
	}
	return total
}

func weightFileMatches(name string, exts []string) bool {
	lower := strings.ToLower(name)
	for _, ext := range exts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// hasField checks if a field exists in a map.
func hasField(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

// jsonStr extracts a string value from a map with default.
func jsonStr(m map[string]any, key, defaultVal string) string {
	v, ok := m[key]
	if !ok {
		return defaultVal
	}
	if s, ok := v.(string); ok {
		return s
	}
	return defaultVal
}

func jsonInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case uint32:
		return int(n)
	case int32:
		return int(n)
	case uint64:
		return int(n)
	case int64:
		return int(n)
	default:
		return 0
	}
}
