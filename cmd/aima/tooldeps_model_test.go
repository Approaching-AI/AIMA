package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/knowledge"
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
					Name:   "funasr-paraformer-onnx",
					Type:   "asr",
					Family: "funasr",
				},
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
}
