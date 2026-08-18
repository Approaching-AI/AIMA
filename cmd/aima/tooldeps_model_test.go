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

func mustOpenTooldepsDB(t *testing.T) *state.DB {
	t.Helper()
	db, err := state.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func boolPtr(value bool) *bool {
	return &value
}

func TestAnnotateModelsFromCatalogAddsDeploymentScenario(t *testing.T) {
	models := []*state.Model{{Name: "deepseek-v4-flash-0731"}}
	cat := &knowledge.Catalog{ModelAssets: []knowledge.ModelAsset{{
		Metadata:     knowledge.ModelMetadata{Name: "deepseek-v4-flash-0731"},
		Capabilities: knowledge.ModelCapabilities{DeploymentScenario: "deepseek-v4-flash-dspark-2node"},
	}}}
	annotateModelsFromCatalog(models, cat)
	if got := models[0].DeploymentScenario; got != "deepseek-v4-flash-dspark-2node" {
		t.Fatalf("deployment scenario = %q", got)
	}
}

func TestScanModelsPublishesModelDiscoveredOnlyForNewModels(t *testing.T) {
	ctx := context.Background()
	db := mustOpenTooldepsDB(t)

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
	db := mustOpenTooldepsDB(t)

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

	waitForDiscoveredModelEvent(t, sub, "import-me")
}

func TestRegisterCatalogLocalModelsRegistersLocalPathAssets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := mustOpenTooldepsDB(t)
	modelDir := filepath.Join(t.TempDir(), "funasr")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "configuration.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cat := &knowledge.Catalog{
		ModelAssets: []knowledge.ModelAsset{
			{
				Metadata: knowledge.ModelMetadata{
					Name:       "funasr-paraformer-onnx",
					Type:       "asr",
					Family:     "funasr",
					ModelClass: "pipeline",
				},
				UI: knowledge.ModelUI{
					Role:          "deployable",
					DisplayNote:   "Speech recognition pipeline",
					DisplayNoteZh: "语音识别流水线",
				},
				Capabilities: knowledge.ModelCapabilities{StandaloneDeploy: boolPtr(true)},
				Storage: knowledge.ModelStorage{
					Formats: []string{"onnx"},
					Sources: []knowledge.ModelSource{
						{Type: "local_path", Path: modelDir},
					},
				},
				Variants: []knowledge.ModelVariant{
					{
						Name:   "funasr-paraformer-onnx-cpu-arm64",
						Format: "onnx",
					},
				},
			},
		},
	}

	if err := registerCatalogLocalModels(ctx, cat, db); err != nil {
		t.Fatalf("registerCatalogLocalModels: %v", err)
	}

	model, err := db.GetModel(ctx, "funasr-paraformer-onnx")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if model.Path != modelDir {
		t.Fatalf("Path = %q, want %q", model.Path, modelDir)
	}
	if model.Type != "asr" {
		t.Fatalf("Type = %q, want asr", model.Type)
	}
	if model.Format != "onnx" {
		t.Fatalf("Format = %q, want onnx", model.Format)
	}
	if model.ModelClass != "pipeline" {
		t.Fatalf("ModelClass = %q, want pipeline", model.ModelClass)
	}
	if model.UIRole != "deployable" {
		t.Fatalf("UIRole = %q, want deployable", model.UIRole)
	}
	if model.StandaloneDeploy == nil || !*model.StandaloneDeploy {
		t.Fatalf("StandaloneDeploy = %v, want true", model.StandaloneDeploy)
	}
	if model.SizeBytes != 0 {
		t.Fatalf("SizeBytes = %d, want 0 for catalog-local directory with unknown scanned size", model.SizeBytes)
	}
}

func TestRegisterCatalogLocalModelsUsesFirstExistingLocalPathCandidate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := mustOpenTooldepsDB(t)
	root := t.TempDir()
	missingDir := filepath.Join(root, "missing")
	modelDir := filepath.Join(root, "existing")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("MkdirAll modelDir: %v", err)
	}

	cat := &knowledge.Catalog{
		ModelAssets: []knowledge.ModelAsset{{
			Metadata: knowledge.ModelMetadata{Name: "catalog-local-model", Type: "asr", Family: "funasr"},
			Storage: knowledge.ModelStorage{
				Formats: []string{"onnx"},
				Sources: []knowledge.ModelSource{
					{Type: "local_path", Path: missingDir},
					{Type: "local_path", Path: modelDir},
				},
			},
		}},
	}

	if err := registerCatalogLocalModels(ctx, cat, db); err != nil {
		t.Fatalf("registerCatalogLocalModels: %v", err)
	}

	model, err := db.GetModel(ctx, "catalog-local-model")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if model.Path != modelDir {
		t.Fatalf("Path = %q, want first existing candidate %q", model.Path, modelDir)
	}
}

func TestRegisterCatalogLocalModelsPreservesExistingScannedSize(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := mustOpenTooldepsDB(t)
	modelDir := filepath.Join(t.TempDir(), "catalog-model")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("MkdirAll modelDir: %v", err)
	}
	if err := db.UpsertScannedModel(ctx, &state.Model{
		ID:        "existing-catalog-model",
		Name:      "catalog-local-model",
		Type:      "asr",
		Path:      modelDir,
		Format:    "onnx",
		SizeBytes: 12345,
		Status:    "registered",
	}); err != nil {
		t.Fatalf("UpsertScannedModel(existing): %v", err)
	}

	cat := &knowledge.Catalog{
		ModelAssets: []knowledge.ModelAsset{{
			Metadata: knowledge.ModelMetadata{Name: "catalog-local-model", Type: "asr", Family: "funasr"},
			UI:       knowledge.ModelUI{Role: "deployable"},
			Storage: knowledge.ModelStorage{
				Formats: []string{"onnx"},
				Sources: []knowledge.ModelSource{{Type: "local_path", Path: modelDir}},
			},
		}},
	}

	if err := registerCatalogLocalModels(ctx, cat, db); err != nil {
		t.Fatalf("registerCatalogLocalModels: %v", err)
	}

	model, err := db.GetModel(ctx, "catalog-local-model")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if model.SizeBytes != 12345 {
		t.Fatalf("SizeBytes = %d, want preserved scanned size 12345", model.SizeBytes)
	}
	if model.UIRole != "deployable" {
		t.Fatalf("UIRole = %q, want deployable", model.UIRole)
	}
}

func TestListModelsDoesNotRegisterCatalogLocalModels(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := mustOpenTooldepsDB(t)
	modelDir := filepath.Join(t.TempDir(), "funasr")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	deps := &mcp.ToolDeps{}
	buildModelDeps(&appContext{
		cat: &knowledge.Catalog{
			ModelAssets: []knowledge.ModelAsset{{
				Metadata: knowledge.ModelMetadata{Name: "funasr-paraformer-onnx", Type: "asr", Family: "funasr"},
				Storage: knowledge.ModelStorage{
					Formats: []string{"onnx"},
					Sources: []knowledge.ModelSource{{Type: "local_path", Path: modelDir}},
				},
			}},
		},
		db:      db,
		dataDir: t.TempDir(),
	}, deps, func(context.Context, string, func(string, string), func(int64, int64)) error {
		return nil
	}, NewDownloadTracker(filepath.Join(t.TempDir(), "downloads")))

	if _, err := deps.ListModels(ctx); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if _, err := db.GetModel(ctx, "funasr-paraformer-onnx"); err == nil {
		t.Fatal("expected ListModels to remain read-only and not register catalog local models")
	}
}

func TestListModelsAnnotatesCatalogDisplayFields(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := mustOpenTooldepsDB(t)
	if err := db.UpsertScannedModel(ctx, &state.Model{
		ID:     "catalog-model",
		Name:   "catalog-model",
		Type:   "asr",
		Path:   filepath.Join(t.TempDir(), "catalog-model"),
		Format: "onnx",
		Status: "registered",
	}); err != nil {
		t.Fatalf("UpsertScannedModel: %v", err)
	}

	deps := &mcp.ToolDeps{}
	buildModelDeps(&appContext{
		cat: &knowledge.Catalog{
			ModelAssets: []knowledge.ModelAsset{{
				Metadata: knowledge.ModelMetadata{
					Name:       "catalog-model",
					Type:       "asr",
					Family:     "funasr",
					ModelClass: "pipeline",
				},
				UI: knowledge.ModelUI{
					Role:          "component",
					DisplayNote:   "Catalog note",
					DisplayNoteZh: "目录说明",
				},
				Capabilities: knowledge.ModelCapabilities{StandaloneDeploy: boolPtr(false)},
			}},
		},
		db:      db,
		dataDir: t.TempDir(),
	}, deps, func(context.Context, string, func(string, string), func(int64, int64)) error {
		return nil
	}, NewDownloadTracker(filepath.Join(t.TempDir(), "downloads")))

	data, err := deps.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	var models []state.Model
	if err := json.Unmarshal(data, &models); err != nil {
		t.Fatalf("Unmarshal ListModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("model count = %d, want 1", len(models))
	}
	model := models[0]
	if model.UIRole != "component" {
		t.Fatalf("UIRole = %q, want component", model.UIRole)
	}
	if model.UIDisplayNoteZh != "目录说明" {
		t.Fatalf("UIDisplayNoteZh = %q, want 目录说明", model.UIDisplayNoteZh)
	}
	if model.StandaloneDeploy == nil || *model.StandaloneDeploy {
		t.Fatalf("StandaloneDeploy = %v, want false", model.StandaloneDeploy)
	}
}

func TestRegisterCatalogLocalModelsDoesNotDeleteSameNameDifferentPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := mustOpenTooldepsDB(t)

	userDir := filepath.Join(t.TempDir(), "user-copy")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("MkdirAll userDir: %v", err)
	}
	if err := db.UpsertScannedModel(ctx, &state.Model{
		ID:         "user-funasr",
		Name:       "funasr-paraformer-onnx",
		Type:       "asr",
		Path:       userDir,
		Format:     "onnx",
		ModelClass: "pipeline",
		Status:     "registered",
	}); err != nil {
		t.Fatalf("UpsertScannedModel(user): %v", err)
	}

	catalogDir := filepath.Join(t.TempDir(), "catalog-copy")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll catalogDir: %v", err)
	}

	cat := &knowledge.Catalog{
		ModelAssets: []knowledge.ModelAsset{{
			Metadata: knowledge.ModelMetadata{
				Name:       "funasr-paraformer-onnx",
				Type:       "asr",
				Family:     "funasr",
				ModelClass: "pipeline",
			},
			Storage: knowledge.ModelStorage{
				Formats: []string{"onnx"},
				Sources: []knowledge.ModelSource{
					{Type: "local_path", Path: catalogDir},
				},
			},
		}},
	}

	if err := registerCatalogLocalModels(ctx, cat, db); err != nil {
		t.Fatalf("registerCatalogLocalModels: %v", err)
	}

	models, err := db.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2", len(models))
	}

	foundUser := false
	foundCatalog := false
	for _, model := range models {
		switch model.Path {
		case userDir:
			foundUser = true
		case catalogDir:
			foundCatalog = true
		}
	}
	if !foundUser || !foundCatalog {
		t.Fatalf("expected both user and catalog records to remain, foundUser=%v foundCatalog=%v", foundUser, foundCatalog)
	}
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

func TestListModelsFoldsCatalogAliasesIntoCanonicalModel(t *testing.T) {
	ctx := context.Background()
	db := mustOpenTooldepsDB(t)

	for _, m := range []*state.Model{
		{
			ID:           "canonical",
			Name:         "qwen3.6-35b-a3b",
			Type:         "llm",
			Path:         "/home/qujing/.aima/models/qwen3.6-35b-a3b",
			Format:       "safetensors",
			SizeBytes:    71903776776,
			Quantization: "bf16",
			Status:       "registered",
		},
		{
			ID:           "alias",
			Name:         "qwen3.6-35b-a3b-bf16",
			Type:         "llm",
			Path:         "/home/qujing/.aima/models/qwen3.6-35b-a3b-bf16",
			Format:       "safetensors",
			SizeBytes:    71903776776,
			Quantization: "bf16",
			Status:       "registered",
		},
		{
			ID:           "awq",
			Name:         "qwen3.6-35b-a3b-awq",
			Type:         "llm",
			Path:         "/home/qujing/.aima/models/qwen3.6-35b-a3b-awq",
			Format:       "awq",
			SizeBytes:    24481313587,
			Quantization: "awq",
			Status:       "registered",
		},
	} {
		if err := db.InsertModel(ctx, m); err != nil {
			t.Fatalf("InsertModel(%s): %v", m.Name, err)
		}
	}

	deps := &mcp.ToolDeps{}
	buildModelDeps(&appContext{
		cat: &knowledge.Catalog{ModelAssets: []knowledge.ModelAsset{
			{Metadata: knowledge.ModelMetadata{
				Name:    "qwen3.6-35b-a3b",
				Aliases: []string{"qwen3.6-35b-a3b-bf16", "qwen36-35b-a3b-bf16"},
			}},
		}},
		db:      db,
		dataDir: t.TempDir(),
	}, deps, func(context.Context, string, func(string, string), func(int64, int64)) error {
		return nil
	}, NewDownloadTracker(filepath.Join(t.TempDir(), "downloads")))

	data, err := deps.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	var listed []state.Model
	if err := json.Unmarshal(data, &listed); err != nil {
		t.Fatalf("Unmarshal list data: %v", err)
	}

	names := make(map[string]state.Model)
	for _, m := range listed {
		names[m.Name] = m
	}
	if _, ok := names["qwen3.6-35b-a3b-bf16"]; ok {
		t.Fatalf("alias model was listed separately: %+v", listed)
	}
	if _, ok := names["qwen3.6-35b-a3b"]; !ok {
		t.Fatalf("canonical model missing from list: %+v", listed)
	}
	if _, ok := names["qwen3.6-35b-a3b-awq"]; !ok {
		t.Fatalf("distinct quantized variant missing from list: %+v", listed)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d models, want 2: %+v", len(listed), listed)
	}
}

func TestIsDeployableModelRecord(t *testing.T) {
	tests := []struct {
		name  string
		model *state.Model
		want  bool
	}{
		{name: "llm", model: &state.Model{Name: "qwen", Type: "llm", Format: "safetensors", ModelClass: "dense"}, want: true},
		{name: "vision language", model: &state.Model{Name: "vlm", Type: "vlm", Format: "safetensors", ModelClass: "dense"}, want: true},
		{name: "image pipeline", model: &state.Model{Name: "image", Type: "image_gen", Format: "safetensors", ModelClass: "diffusion"}, want: true},
		{name: "asr pipeline", model: &state.Model{Name: "asr", Type: "asr", Format: "onnx", ModelClass: "pipeline"}, want: true},
		{name: "asr component", model: &state.Model{Name: "vad", Type: "asr", Format: "onnx", ModelClass: "component"}},
		{name: "draft", model: &state.Model{Name: "draft", Type: "llm", Format: "safetensors", ModelClass: "dense", UIRole: "draft"}},
		{name: "explicitly disabled", model: &state.Model{Name: "disabled", Type: "llm", StandaloneDeploy: boolPtr(false)}},
		{name: "explicitly enabled component", model: &state.Model{Name: "enabled", Type: "asr", ModelClass: "component", StandaloneDeploy: boolPtr(true)}, want: true},
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

func TestListDeployableModelRecordsAppliesCatalogCapabilities(t *testing.T) {
	ctx := context.Background()
	db := mustOpenTooldepsDB(t)
	for _, model := range []*state.Model{
		{ID: "chat", Name: "chat", Type: "llm", Path: "/models/chat", Format: "safetensors", ModelClass: "dense"},
		{ID: "component", Name: "component", Type: "asr", Path: "/models/component", Format: "onnx", ModelClass: "pipeline"},
	} {
		if err := db.UpsertScannedModel(ctx, model); err != nil {
			t.Fatalf("UpsertScannedModel(%s): %v", model.Name, err)
		}
	}
	cat := &knowledge.Catalog{ModelAssets: []knowledge.ModelAsset{{
		Metadata:     knowledge.ModelMetadata{Name: "component", Type: "asr"},
		UI:           knowledge.ModelUI{Role: "component"},
		Capabilities: knowledge.ModelCapabilities{StandaloneDeploy: boolPtr(false)},
	}}}

	models, err := listDeployableModelRecords(ctx, db, cat)
	if err != nil {
		t.Fatalf("listDeployableModelRecords: %v", err)
	}
	if len(models) != 1 || models[0].Name != "chat" {
		t.Fatalf("deployable models = %+v, want only chat", models)
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

// A speculative draft head (e.g. DFlash/MTP), declared only as a variant's
// speculative_config.model in the catalog, must be marked non-standalone so the
// UI does not offer to deploy it on its own. Its parent model stays deployable.
func TestAnnotateModelsFromCatalog_SpeculativeDraftNotStandalone(t *testing.T) {
	cat := &knowledge.Catalog{
		ModelAssets: []knowledge.ModelAsset{{
			Metadata: knowledge.ModelMetadata{
				Name:    "qwen3.6-35b-a3b",
				Aliases: []string{"Qwen3.6-35B-A3B"},
			},
			Variants: []knowledge.ModelVariant{{
				Name: "dflash",
				DefaultConfig: map[string]any{
					"speculative_config": map[string]any{
						"method": "dflash",
						"model":  "/models/Qwen3.6-35B-A3B-DFlash",
					},
				},
			}},
		}},
	}
	models := []*state.Model{
		{Name: "Qwen3.6-35B-A3B"},               // parent: stays deployable
		{Name: "Qwen3.6-35B-A3B-DFlash"},        // draft (safetensors)
		{Name: "Qwen3.6-35B-A3B-DFlash-Q4_K_M"}, // draft (gguf quant)
	}

	annotateModelsFromCatalog(models, cat)

	byName := make(map[string]*state.Model, len(models))
	for _, m := range models {
		byName[m.Name] = m
	}

	for _, name := range []string{"Qwen3.6-35B-A3B-DFlash", "Qwen3.6-35B-A3B-DFlash-Q4_K_M"} {
		m := byName[name]
		if m.StandaloneDeploy == nil || *m.StandaloneDeploy {
			t.Errorf("%s: StandaloneDeploy = %v, want non-nil false", name, m.StandaloneDeploy)
		}
		if m.UIRole != "draft" {
			t.Errorf("%s: UIRole = %q, want %q", name, m.UIRole, "draft")
		}
	}

	parent := byName["Qwen3.6-35B-A3B"]
	if parent.StandaloneDeploy != nil && !*parent.StandaloneDeploy {
		t.Errorf("parent model must not be marked non-standalone")
	}
	if parent.UIRole == "draft" {
		t.Errorf("parent model must not be tagged as a draft")
	}
}
