package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"

	state "github.com/jguan/aima/internal"
)

func TestScanModelsPublishesModelDiscoveredOnlyForNewModels(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	root := t.TempDir()
	if err := writeScanModelFixture(filepath.Join(root, "new-model"), 11*1024*1024); err != nil {
		t.Fatalf("writeScanModelFixture: %v", err)
	}

	t.Setenv("AIMA_MODEL_DIR", root)
	t.Setenv("HOME", t.TempDir())

	bus := agent.NewEventBus()
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)

	deps := &mcp.ToolDeps{}
	buildModelDeps(&appContext{
		cat:      &knowledge.Catalog{},
		db:       db,
		dataDir:  t.TempDir(),
		eventBus: bus,
	}, deps, func(context.Context, string, func(string, string), func(int64, int64)) error {
		return nil
	}, NewDownloadTracker(filepath.Join(t.TempDir(), "downloads")))

	data, err := deps.ScanModels(ctx)
	if err != nil {
		t.Fatalf("ScanModels: %v", err)
	}
	var models []map[string]any
	if err := json.Unmarshal(data, &models); err != nil {
		t.Fatalf("Unmarshal scan data: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected at least one scanned model")
	}

	select {
	case ev := <-sub:
		if ev.Type != agent.EventModelDiscovered {
			t.Fatalf("event type = %q, want %q", ev.Type, agent.EventModelDiscovered)
		}
		if ev.Model != "new-model" {
			t.Fatalf("event model = %q, want new-model", ev.Model)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for model.discovered event")
	}

	if _, err := deps.ScanModels(ctx); err != nil {
		t.Fatalf("second ScanModels: %v", err)
	}
	select {
	case ev := <-sub:
		t.Fatalf("unexpected duplicate event on second scan: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestImportModelPublishesModelDiscovered(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srcRoot := t.TempDir()
	dataDir := t.TempDir()
	modelDir := filepath.Join(srcRoot, "import-me")
	if err := writeScanModelFixture(modelDir, 512); err != nil {
		t.Fatalf("writeScanModelFixture: %v", err)
	}

	bus := agent.NewEventBus()
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)

	deps := &mcp.ToolDeps{}
	buildModelDeps(&appContext{
		cat:      &knowledge.Catalog{},
		db:       db,
		dataDir:  dataDir,
		eventBus: bus,
	}, deps, func(context.Context, string, func(string, string), func(int64, int64)) error {
		return nil
	}, NewDownloadTracker(filepath.Join(t.TempDir(), "downloads")))

	data, err := deps.ImportModel(ctx, modelDir)
	if err != nil {
		t.Fatalf("ImportModel: %v", err)
	}
	var imported map[string]any
	if err := json.Unmarshal(data, &imported); err != nil {
		t.Fatalf("Unmarshal import data: %v", err)
	}
	if imported["name"] != "import-me" {
		t.Fatalf("imported name = %v, want import-me", imported["name"])
	}

	select {
	case ev := <-sub:
		if ev.Type != agent.EventModelDiscovered {
			t.Fatalf("event type = %q, want %q", ev.Type, agent.EventModelDiscovered)
		}
		if ev.Model != "import-me" {
			t.Fatalf("event model = %q, want import-me", ev.Model)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for import model.discovered event")
	}
}

func writeScanModelFixture(dir string, weightSize int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	config := []byte(`{"model_type":"llama","hidden_size":4096,"num_hidden_layers":32,"num_attention_heads":32}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), config, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "model.safetensors"), make([]byte, weightSize), 0o644)
}
