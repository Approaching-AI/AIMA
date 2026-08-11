package openclaw

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeManagedJSON(t *testing.T, configPath string, st *ManagedState) {
	t.Helper()
	data, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(ManagedStatePath(configPath), data, 0o644); err != nil {
		t.Fatalf("write managed json: %v", err)
	}
}

func newExcludeTestDeps(configPath string) *Deps {
	return &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"qwen3-8b":    {ModelName: "qwen3-8b", EngineType: "vllm", Address: "http://127.0.0.1:8000", Ready: true},
			"glm-4.1v-9b": {ModelName: "glm-4.1v-9b", EngineType: "vllm", Address: "http://127.0.0.1:8001", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
		MCPCommand: "/usr/local/bin/aima",
	}
}

// A model marked excluded in managed state must be skipped by Sync even though
// it is a ready local backend; non-excluded models still sync.
func TestSyncSkipsExcludedModel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "openclaw.json")
	writeManagedJSON(t, configPath, &ManagedState{Version: 1, ExcludedModels: []string{"qwen3-8b"}})
	deps := newExcludeTestDeps(configPath)

	result, err := Sync(context.Background(), deps, true)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	for _, m := range result.LLMModels {
		if m.ID == "qwen3-8b" {
			t.Fatalf("excluded model qwen3-8b should not be synced, but it appears in LLMModels")
		}
	}
	found := false
	for _, m := range result.VLMModels {
		if m.ID == "glm-4.1v-9b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("non-excluded model glm-4.1v-9b should still be synced")
	}
}

// Exclude marks a model as revoked (persisted); Include clears the mark. The
// marker survives the reconciling Sync that each runs.
func TestExcludeThenIncludeTogglesMarker(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "openclaw.json")
	deps := newExcludeTestDeps(configPath)

	if err := Exclude(context.Background(), deps, "qwen3-8b"); err != nil {
		t.Fatalf("Exclude failed: %v", err)
	}
	st, err := ReadManagedState(configPath)
	if err != nil {
		t.Fatalf("read managed: %v", err)
	}
	if !st.IsExcluded("qwen3-8b") {
		t.Fatalf("after Exclude, qwen3-8b should be marked excluded; got %v", st.ExcludedModels)
	}

	if err := Include(context.Background(), deps, "qwen3-8b"); err != nil {
		t.Fatalf("Include failed: %v", err)
	}
	st, err = ReadManagedState(configPath)
	if err != nil {
		t.Fatalf("read managed: %v", err)
	}
	if st.IsExcluded("qwen3-8b") {
		t.Fatalf("after Include, qwen3-8b should NOT be marked excluded; got %v", st.ExcludedModels)
	}
}

// Inspect (openclaw status) surfaces the revoked models so users can see why a
// model isn't in OpenClaw and what to Include to bring it back.
func TestInspectReportsExcludedModels(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "openclaw.json")
	writeManagedJSON(t, configPath, &ManagedState{Version: 1, ExcludedModels: []string{"qwen3-8b"}})
	deps := newExcludeTestDeps(configPath)

	st, err := Inspect(context.Background(), deps)
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	found := false
	for _, m := range st.ExcludedModels {
		if m == "qwen3-8b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Inspect should report excluded model qwen3-8b; got %v", st.ExcludedModels)
	}
}

// Excluding is idempotent and does not duplicate entries.
func TestExcludeIsIdempotent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "openclaw.json")
	deps := newExcludeTestDeps(configPath)

	for i := 0; i < 3; i++ {
		if err := Exclude(context.Background(), deps, "qwen3-8b"); err != nil {
			t.Fatalf("Exclude failed: %v", err)
		}
	}
	st, _ := ReadManagedState(configPath)
	count := 0
	for _, m := range st.ExcludedModels {
		if m == "qwen3-8b" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected qwen3-8b listed once, got %d", count)
	}
}
