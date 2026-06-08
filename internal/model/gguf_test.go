package model

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsMMProjFile(t *testing.T) {
	tests := map[string]bool{
		"mmproj-F16":              true,
		"mmproj-Qwen3.5-27B-BF16": true,
		"Qwen3.5-27B-mmproj":      true,
		"Qwen3.5-27B-Q4_K_M":      false,
		"GLM-4.7-Flash-Q4_K_M":    false,
	}
	for name, want := range tests {
		if got := isMMProjFile(name); got != want {
			t.Errorf("isMMProjFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestGroupGGUFModels(t *testing.T) {
	t.Run("split shards collapse to one model, primary = first shard", func(t *testing.T) {
		// deliberately out of order; mmproj + a single file mixed in
		files := []string{
			`/d/Qwen3-Coder-Next-Q4_K_M-00002-of-00004.gguf`,
			`/d/Qwen3-Coder-Next-Q4_K_M-00001-of-00004.gguf`,
			`/d/Qwen3-Coder-Next-Q4_K_M-00004-of-00004.gguf`,
			`/d/Qwen3-Coder-Next-Q4_K_M-00003-of-00004.gguf`,
			`/d/mmproj-Qwen3.5-27B-BF16.gguf`,
			`/d/GLM-4.7-Flash-Q4_K_M.gguf`,
		}
		groups := groupGGUFModels(files)
		if len(groups) != 2 {
			t.Fatalf("got %d groups, want 2: %+v", len(groups), groups)
		}
		shard := groups[0]
		if shard.name != "Qwen3-Coder-Next-Q4_K_M" {
			t.Errorf("group name = %q", shard.name)
		}
		if filepath.Base(shard.primary) != "Qwen3-Coder-Next-Q4_K_M-00001-of-00004.gguf" {
			t.Errorf("primary = %q, want the -00001- shard", shard.primary)
		}
		if len(shard.parts) != 4 {
			t.Errorf("parts = %d, want 4", len(shard.parts))
		}
		single := groups[1]
		if single.name != "GLM-4.7-Flash-Q4_K_M" || len(single.parts) != 1 {
			t.Errorf("single = %+v", single)
		}
	})

	t.Run("same base name in different dirs are separate models", func(t *testing.T) {
		files := []string{
			`/a/M-00001-of-00002.gguf`, `/a/M-00002-of-00002.gguf`,
			`/b/M-00001-of-00002.gguf`, `/b/M-00002-of-00002.gguf`,
		}
		if got := len(groupGGUFModels(files)); got != 2 {
			t.Fatalf("got %d groups, want 2", got)
		}
	})

	t.Run("mmproj-only input yields nothing", func(t *testing.T) {
		if got := groupGGUFModels([]string{`/d/mmproj-F16.gguf`}); len(got) != 0 {
			t.Fatalf("got %d groups, want 0", len(got))
		}
	})
}

func TestDetectGGUFModels_ShardsMMProjAndSize(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Big-Q4_K_M-00001-of-00003.gguf", 100)
	write("Big-Q4_K_M-00002-of-00003.gguf", 200)
	write("Big-Q4_K_M-00003-of-00003.gguf", 300)
	write("mmproj-Big-BF16.gguf", 50)
	write("Small-Q8_0.gguf", 500)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	pattern := ModelPattern{weightExts: []string{".gguf"}, format: "gguf"}
	models := detectGGUFModels(dir, entries, pattern, 0)

	if len(models) != 2 {
		t.Fatalf("got %d models, want 2 (split group + single, mmproj excluded): %+v", len(models), models)
	}
	byName := map[string]*ModelInfo{}
	for _, m := range models {
		byName[m.Name] = m
	}
	big, ok := byName["Big-Q4_K_M"]
	if !ok {
		t.Fatalf("missing collapsed split model; got %v", byName)
	}
	if big.SizeBytes != 600 {
		t.Errorf("split model size = %d, want 600 (sum of shards)", big.SizeBytes)
	}
	if filepath.Base(big.Path) != "Big-Q4_K_M-00001-of-00003.gguf" {
		t.Errorf("split model Path = %q, want first shard", big.Path)
	}
	if _, ok := byName["Small-Q8_0"]; !ok {
		t.Errorf("missing single-file model; got %v", byName)
	}
	if _, ok := byName["mmproj-Big-BF16"]; ok {
		t.Errorf("mmproj projector should not be a standalone model")
	}
}
