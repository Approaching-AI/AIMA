package model

import (
	"os"
	"strings"
	"testing"
)

func TestDedupePaths(t *testing.T) {
	got := dedupePaths([]string{"a", "b", "", "a", "c", "b", ""})
	want := []string{"a", "b", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dedupePaths = %v, want %v", got, want)
	}
}

func TestDefaultScanPaths_MultiPathEnv(t *testing.T) {
	sep := string(os.PathListSeparator)
	a := t.TempDir()
	b := t.TempDir()
	t.Setenv("AIMA_MODEL_DIR", a+sep+b)

	paths := DefaultScanPaths()
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	if !set[a] || !set[b] {
		t.Errorf("DefaultScanPaths missing one of the AIMA_MODEL_DIR entries\n got: %v\n want both: %q, %q", paths, a, b)
	}

	// no duplicates in the returned list
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Errorf("duplicate path in DefaultScanPaths: %q", p)
		}
		seen[p] = true
	}
}

func TestDefaultScanPaths_EnvOverrides(t *testing.T) {
	hf := t.TempDir()
	om := t.TempDir()
	t.Setenv("AIMA_MODEL_DIR", "")
	t.Setenv("HF_HOME", hf)
	t.Setenv("OLLAMA_MODELS", om)

	paths := DefaultScanPaths()
	wantHF := hf + string(os.PathSeparator) + "hub"
	var foundHF, foundOM bool
	for _, p := range paths {
		if p == wantHF {
			foundHF = true
		}
		if p == om {
			foundOM = true
		}
	}
	if !foundHF {
		t.Errorf("HF_HOME/hub not in scan paths: want %q in %v", wantHF, paths)
	}
	if !foundOM {
		t.Errorf("OLLAMA_MODELS not in scan paths: want %q in %v", om, paths)
	}
}
