//go:build linux

package hal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectXH2ANPU(t *testing.T) {
	root := t.TempDir()
	for name, driver := range map[string]string{
		"0000:01:00.0": xh2aDriver,
		"0000:02:00.0": xh2aDriver,
		"0000:03:00.0": "unrelated-driver",
	} {
		deviceDir := filepath.Join(root, name)
		if err := os.Mkdir(deviceDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "drivers", driver), filepath.Join(deviceDir, "driver")); err != nil {
			t.Fatal(err)
		}
	}

	npu := detectXH2ANPU(root)
	if npu == nil {
		t.Fatal("expected XH2A NPU")
	}
	if npu.Vendor != "houmo" || npu.Name != "Houmo XH2A IPU" || npu.Driver != xh2aDriver || npu.Count != 2 {
		t.Fatalf("unexpected NPU: %+v", npu)
	}
}

func TestDetectXH2ANPUAbsent(t *testing.T) {
	if npu := detectXH2ANPU(t.TempDir()); npu != nil {
		t.Fatalf("expected no XH2A NPU, got %+v", npu)
	}
}
