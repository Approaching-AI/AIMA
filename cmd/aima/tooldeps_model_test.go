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

	waitForDiscoveredModelEvent(t, sub, "new-model")
	drainExplorerEvents(sub)

	if _, err := deps.ScanModels(ctx); err != nil {
		t.Fatalf("second ScanModels: %v", err)
	}
	assertNoDiscoveredModelEvent(t, sub, "new-model")
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

func TestInferModelClassUsesCatalogOverride(t *testing.T) {
	ma := &knowledge.ModelAsset{
		Metadata: knowledge.ModelMetadata{
			Name:       "mooer-asr-1.5b",
			Type:       "asr",
			ModelClass: "component",
		},
	}
	variant := &knowledge.ModelVariant{Format: "safetensors"}

	if got := inferModelClass(ma, nil); got != "component" {
		t.Fatalf("inferModelClass(nil variant) = %q, want component", got)
	}
	if got := inferModelClass(ma, variant); got != "component" {
		t.Fatalf("inferModelClass(safetensors variant) = %q, want component", got)
	}
}

func TestIsDeployableModelRecord(t *testing.T) {
	tests := []struct {
		name  string
		model *state.Model
		want  bool
	}{
		{name: "llm", model: &state.Model{Name: "qwen", Type: "llm", Format: "safetensors", ModelClass: "dense"}, want: true},
		{name: "embedding", model: &state.Model{Name: "embed", Type: "embedding", Format: "safetensors", ModelClass: "dense"}, want: true},
		{name: "asr pipeline", model: &state.Model{Name: "asr", Type: "asr", Format: "onnx", ModelClass: "pipeline"}, want: true},
		{name: "tts pipeline", model: &state.Model{Name: "tts", Type: "tts", Format: "mnn", ModelClass: "pipeline"}, want: true},
		{name: "punctuation component", model: &state.Model{Name: "punc", Type: "nlp", Format: "onnx", ModelClass: "component"}},
		{name: "vad component", model: &state.Model{Name: "vad", Type: "vad", Format: "onnx", ModelClass: "component"}},
		{name: "asr component", model: &state.Model{Name: "online-asr", Type: "asr", Format: "onnx", ModelClass: "component"}},
		{name: "hidden catalog component", model: &state.Model{Name: "mooer", Type: "asr", Format: "safetensors", ModelClass: "component"}},
		{name: "nil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDeployableModelRecord(tt.model); got != tt.want {
				t.Fatalf("isDeployableModelRecord() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScanModelsReturnsCanonicalDeployableModels(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	root := t.TempDir()
	qwenPath := filepath.Join(root, "gptq-Qwen3-8B")
	embedPath := filepath.Join(root, "Qwen3-Embedding-0.6B")
	offlineASRPath := filepath.Join(root, "speech_paraformer-offline-onnx")
	onlineASRPath := filepath.Join(root, "speech_paraformer-online-onnx")
	vadPath := filepath.Join(root, "speech_fsmn_vad-onnx")
	puncPath := filepath.Join(root, "punc_ct-transformer-onnx")
	ttsPath := filepath.Join(root, "litetts")

	for _, path := range []string{qwenPath, embedPath} {
		if err := writeScanModelFixture(path, 11*1024*1024); err != nil {
			t.Fatalf("writeScanModelFixture(%s): %v", path, err)
		}
	}
	for path, task := range map[string]string{
		offlineASRPath: "speech-recognition",
		onlineASRPath:  "speech-recognition-online",
		vadPath:        "voice-activity-detection",
		puncPath:       "punctuation",
	} {
		if err := writeONNXScanFixture(path, task); err != nil {
			t.Fatalf("writeONNXScanFixture(%s): %v", path, err)
		}
	}
	if err := os.MkdirAll(ttsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll tts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ttsPath, "model.mnn"), []byte("mnn"), 0o644); err != nil {
		t.Fatalf("WriteFile mnn: %v", err)
	}

	t.Setenv("AIMA_MODEL_DIR", root)
	t.Setenv("HOME", t.TempDir())
	cat := &knowledge.Catalog{ModelAssets: []knowledge.ModelAsset{
		catalogLocalModel("qwen3-8b", "llm", "qwen", "safetensors", qwenPath),
		catalogLocalModel("qwen3-emb-0.6b", "embedding", "qwen", "safetensors", embedPath),
		catalogLocalModel("funasr-paraformer-onnx", "asr", "funasr", "onnx", offlineASRPath),
		catalogLocalModel("litetts-mnn", "tts", "litetts", "mnn", ttsPath),
	}}

	deps := &mcp.ToolDeps{}
	buildModelDeps(&appContext{
		cat:     cat,
		db:      db,
		dataDir: t.TempDir(),
	}, deps, func(context.Context, string, func(string, string), func(int64, int64)) error {
		return nil
	}, NewDownloadTracker(filepath.Join(t.TempDir(), "downloads")))

	data, err := deps.ScanModels(ctx)
	if err != nil {
		t.Fatalf("ScanModels: %v", err)
	}
	var got []*state.Model
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal scan data: %v", err)
	}
	want := map[string]bool{
		"qwen3-8b":               true,
		"qwen3-emb-0.6b":         true,
		"funasr-paraformer-onnx": true,
		"litetts-mnn":            true,
	}
	if len(got) != len(want) {
		t.Fatalf("scan result count = %d, want %d: %+v", len(got), len(want), got)
	}
	for _, m := range got {
		if !want[m.Name] {
			t.Fatalf("unexpected deployable scan result: %+v", m)
		}
	}

	all, err := db.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	components := map[string]bool{}
	for _, m := range all {
		if m.ModelClass == "component" {
			components[m.Name] = true
		}
	}
	for _, name := range []string{"speech_paraformer-online-onnx", "speech_fsmn_vad-onnx", "punc_ct-transformer-onnx"} {
		if !components[name] {
			t.Errorf("component %q was not retained and classified in model.list", name)
		}
	}
}

func catalogLocalModel(name, modelType, family, format, path string) knowledge.ModelAsset {
	return knowledge.ModelAsset{
		Metadata: knowledge.ModelMetadata{Name: name, Type: modelType, Family: family},
		Storage: knowledge.ModelStorage{
			Formats: []string{format},
			Sources: []knowledge.ModelSource{{Type: "local_path", Path: path, Format: format}},
		},
	}
}

func writeONNXScanFixture(dir, task string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	config, err := json.Marshal(map[string]string{"task": task})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "configuration.json"), config, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "model.onnx"), []byte("onnx"), 0o644)
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

func waitForDiscoveredModelEvent(t *testing.T, sub <-chan agent.ExplorerEvent, modelName string) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub:
			if ev.Type != agent.EventModelDiscovered {
				continue
			}
			if ev.Model == modelName {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for model.discovered event for %s", modelName)
		}
	}
}

func drainExplorerEvents(sub <-chan agent.ExplorerEvent) {
	for {
		select {
		case <-sub:
		default:
			return
		}
	}
}

func assertNoDiscoveredModelEvent(t *testing.T, sub <-chan agent.ExplorerEvent, modelName string) {
	t.Helper()
	timeout := time.After(150 * time.Millisecond)
	for {
		select {
		case ev := <-sub:
			if ev.Type == agent.EventModelDiscovered && ev.Model == modelName {
				t.Fatalf("unexpected duplicate event on second scan: %+v", ev)
			}
		case <-timeout:
			return
		}
	}
}
