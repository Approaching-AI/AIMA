package state

import (
	"context"
	"testing"
)

func mustOpen(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenClose(t *testing.T) {
	db := mustOpen(t)
	if db == nil {
		t.Fatal("expected non-nil DB")
	}
}

func TestModelCRUD(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	m := &Model{
		ID:             "m-001",
		Name:           "qwen3-8b",
		Type:           "llm",
		Path:           "/data/models/qwen3-8b",
		Format:         "safetensors",
		SizeBytes:      16_000_000_000,
		DetectedArch:   "qwen",
		DetectedParams: "8B",
		Status:         "registered",
	}

	t.Run("insert and get", func(t *testing.T) {
		if err := db.InsertModel(ctx, m); err != nil {
			t.Fatalf("InsertModel: %v", err)
		}
		got, err := db.GetModel(ctx, "m-001")
		if err != nil {
			t.Fatalf("GetModel: %v", err)
		}
		if got.Name != "qwen3-8b" {
			t.Errorf("Name = %q, want %q", got.Name, "qwen3-8b")
		}
		if got.SizeBytes != 16_000_000_000 {
			t.Errorf("SizeBytes = %d, want %d", got.SizeBytes, 16_000_000_000)
		}
		if got.Status != "registered" {
			t.Errorf("Status = %q, want %q", got.Status, "registered")
		}
		if got.CreatedAt.IsZero() {
			t.Error("CreatedAt should be set")
		}
	})

	t.Run("list", func(t *testing.T) {
		models, err := db.ListModels(ctx)
		if err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if len(models) != 1 {
			t.Fatalf("len = %d, want 1", len(models))
		}
	})

	t.Run("update status", func(t *testing.T) {
		if err := db.UpdateModelStatus(ctx, "m-001", "downloading"); err != nil {
			t.Fatalf("UpdateModelStatus: %v", err)
		}
		got, _ := db.GetModel(ctx, "m-001")
		if got.Status != "downloading" {
			t.Errorf("Status = %q, want %q", got.Status, "downloading")
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := db.DeleteModel(ctx, "m-001"); err != nil {
			t.Fatalf("DeleteModel: %v", err)
		}
		_, err := db.GetModel(ctx, "m-001")
		if err == nil {
			t.Fatal("expected error after delete")
		}
	})

	t.Run("get nonexistent", func(t *testing.T) {
		_, err := db.GetModel(ctx, "does-not-exist")
		if err == nil {
			t.Fatal("expected error for nonexistent model")
		}
	})
}

func TestEngineCRUD(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	e := &Engine{
		ID:        "e-001",
		Type:      "vllm",
		Image:     "vllm/vllm-openai",
		Tag:       "latest",
		SizeBytes: 8_500_000_000,
		Platform:  "linux/arm64",
		Available: true,
	}

	t.Run("insert and get", func(t *testing.T) {
		if err := db.InsertEngine(ctx, e); err != nil {
			t.Fatalf("InsertEngine: %v", err)
		}
		got, err := db.GetEngine(ctx, "e-001")
		if err != nil {
			t.Fatalf("GetEngine: %v", err)
		}
		if got.Type != "vllm" {
			t.Errorf("Type = %q, want %q", got.Type, "vllm")
		}
		if got.Image != "vllm/vllm-openai" {
			t.Errorf("Image = %q, want %q", got.Image, "vllm/vllm-openai")
		}
		if !got.Available {
			t.Error("Available should be true")
		}
	})

	t.Run("list", func(t *testing.T) {
		engines, err := db.ListEngines(ctx)
		if err != nil {
			t.Fatalf("ListEngines: %v", err)
		}
		if len(engines) != 1 {
			t.Fatalf("len = %d, want 1", len(engines))
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := db.DeleteEngine(ctx, "e-001"); err != nil {
			t.Fatalf("DeleteEngine: %v", err)
		}
		_, err := db.GetEngine(ctx, "e-001")
		if err == nil {
			t.Fatal("expected error after delete")
		}
	})
}

func TestKnowledgeNoteCRUD(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	n := &KnowledgeNote{
		ID:              "n-001",
		Title:           "vLLM on GB10 tuning",
		Tags:            []string{"vllm", "gb10", "tuning"},
		HardwareProfile: "nvidia-gb10-arm64",
		Model:           "qwen3-8b",
		Engine:          "vllm",
		Content:         "kind: knowledge_note\nrecommendation:\n  config:\n    gpu_memory_utilization: 0.85",
		Confidence:      "high",
	}

	t.Run("insert", func(t *testing.T) {
		if err := db.InsertNote(ctx, n); err != nil {
			t.Fatalf("InsertNote: %v", err)
		}
	})

	t.Run("search by hardware", func(t *testing.T) {
		notes, err := db.SearchNotes(ctx, NoteFilter{HardwareProfile: "nvidia-gb10-arm64"})
		if err != nil {
			t.Fatalf("SearchNotes: %v", err)
		}
		if len(notes) != 1 {
			t.Fatalf("len = %d, want 1", len(notes))
		}
		if notes[0].Title != "vLLM on GB10 tuning" {
			t.Errorf("Title = %q, want %q", notes[0].Title, "vLLM on GB10 tuning")
		}
		if len(notes[0].Tags) != 3 {
			t.Errorf("Tags len = %d, want 3", len(notes[0].Tags))
		}
	})

	t.Run("search by model and engine", func(t *testing.T) {
		notes, err := db.SearchNotes(ctx, NoteFilter{Model: "qwen3-8b", Engine: "vllm"})
		if err != nil {
			t.Fatalf("SearchNotes: %v", err)
		}
		if len(notes) != 1 {
			t.Fatalf("len = %d, want 1", len(notes))
		}
	})

	t.Run("search no match", func(t *testing.T) {
		notes, err := db.SearchNotes(ctx, NoteFilter{Model: "nonexistent"})
		if err != nil {
			t.Fatalf("SearchNotes: %v", err)
		}
		if len(notes) != 0 {
			t.Fatalf("len = %d, want 0", len(notes))
		}
	})

	t.Run("search empty filter returns all", func(t *testing.T) {
		notes, err := db.SearchNotes(ctx, NoteFilter{})
		if err != nil {
			t.Fatalf("SearchNotes: %v", err)
		}
		if len(notes) != 1 {
			t.Fatalf("len = %d, want 1", len(notes))
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := db.DeleteNote(ctx, "n-001"); err != nil {
			t.Fatalf("DeleteNote: %v", err)
		}
		notes, _ := db.SearchNotes(ctx, NoteFilter{})
		if len(notes) != 0 {
			t.Fatalf("len = %d, want 0 after delete", len(notes))
		}
	})
}

func TestConfig(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	t.Run("set and get", func(t *testing.T) {
		if err := db.SetConfig(ctx, "data_dir", "/opt/aima/data"); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		val, err := db.GetConfig(ctx, "data_dir")
		if err != nil {
			t.Fatalf("GetConfig: %v", err)
		}
		if val != "/opt/aima/data" {
			t.Errorf("value = %q, want %q", val, "/opt/aima/data")
		}
	})

	t.Run("upsert", func(t *testing.T) {
		if err := db.SetConfig(ctx, "data_dir", "/new/path"); err != nil {
			t.Fatalf("SetConfig upsert: %v", err)
		}
		val, _ := db.GetConfig(ctx, "data_dir")
		if val != "/new/path" {
			t.Errorf("value = %q, want %q", val, "/new/path")
		}
	})

	t.Run("get nonexistent", func(t *testing.T) {
		_, err := db.GetConfig(ctx, "nonexistent")
		if err == nil {
			t.Fatal("expected error for nonexistent config key")
		}
	})
}

func TestAuditLog(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	entry := &AuditEntry{
		AgentType:     "go_agent",
		ToolName:      "deploy.apply",
		Arguments:     `{"engine":"vllm","model":"qwen3-8b"}`,
		ResultSummary: "deployed successfully",
	}

	if err := db.LogAction(ctx, entry); err != nil {
		t.Fatalf("LogAction: %v", err)
	}
}

func TestDuplicateInsert(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	m := &Model{
		ID:   "m-dup",
		Name: "test",
		Type: "llm",
		Path: "/tmp/test",
	}
	if err := db.InsertModel(ctx, m); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := db.InsertModel(ctx, m); err == nil {
		t.Fatal("expected error on duplicate insert")
	}
}
