package openclaw

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteConfigReplacesReadOnlyExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only replacement semantics differ on Windows")
	}

	configPath := filepath.Join(t.TempDir(), "openclaw.json")
	if err := os.WriteFile(configPath, []byte("{\"old\":true}\n"), 0444); err != nil {
		t.Fatalf("WriteFile old config: %v", err)
	}

	if err := WriteConfig(configPath, map[string]any{"new": true}); err != nil {
		t.Fatalf("WriteConfig should atomically replace read-only config: %v", err)
	}

	cfg, err := ReadConfig(configPath)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg["new"] != true {
		t.Fatalf("new config flag = %v, want true", cfg["new"])
	}
	if _, ok := cfg["old"]; ok {
		t.Fatalf("old config key should be replaced: %#v", cfg)
	}
}
