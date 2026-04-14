package state

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestSchemaIncludesModelVariantGPUCountMin(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	rows, err := db.RawDB().QueryContext(ctx, "PRAGMA table_info(model_variants)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(model_variants): %v", err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var (
			cid        int
			name       string
			typ        string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &primaryKey); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if name == "gpu_count_min" {
			found = true
			if !strings.EqualFold(typ, "INTEGER") {
				t.Errorf("gpu_count_min type = %q, want INTEGER", typ)
			}
			break
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	if !found {
		t.Fatal("expected model_variants.gpu_count_min column")
	}
}

func TestDeletedDeploymentTombstones(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	deleteAt := time.Now().UTC().Truncate(time.Second)
	if err := db.MarkDeletedDeployments(ctx, deleteAt, "qwen3-8b-vllm", "qwen3-8b"); err != nil {
		t.Fatalf("MarkDeletedDeployments: %v", err)
	}

	marks, err := db.ListDeletedDeploymentsSince(ctx, deleteAt.Add(-1*time.Second))
	if err != nil {
		t.Fatalf("ListDeletedDeploymentsSince: %v", err)
	}
	if len(marks) != 2 {
		t.Fatalf("len(marks) = %d, want 2", len(marks))
	}
	if got := marks["qwen3-8b-vllm"]; !got.Equal(deleteAt) {
		t.Fatalf("marks[qwen3-8b-vllm] = %v, want %v", got, deleteAt)
	}

	if err := db.PruneDeletedDeploymentsBefore(ctx, deleteAt.Add(1*time.Second)); err != nil {
		t.Fatalf("PruneDeletedDeploymentsBefore: %v", err)
	}
	marks, err = db.ListDeletedDeploymentsSince(ctx, deleteAt.Add(-1*time.Second))
	if err != nil {
		t.Fatalf("ListDeletedDeploymentsSince(after prune): %v", err)
	}
	if len(marks) != 0 {
		t.Fatalf("len(marks after prune) = %d, want 0", len(marks))
	}
}

func TestOpenConcurrent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	for i := 0; i < 8; i++ {
		dbPath := filepath.Join(dir, fmt.Sprintf("aima-%d.db", i))
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make(chan *DB, 2)
		errs := make(chan error, 2)

		openFn := func() {
			defer wg.Done()
			<-start
			db, err := Open(ctx, dbPath)
			if err != nil {
				errs <- err
				return
			}
			results <- db
		}

		wg.Add(2)
		go openFn()
		go openFn()
		close(start)
		wg.Wait()
		close(errs)
		close(results)

		var gotErr error
		for err := range errs {
			if gotErr == nil {
				gotErr = err
			}
		}
		for db := range results {
			_ = db.Close()
		}
		if gotErr != nil {
			t.Fatalf("concurrent Open(%s): %v", dbPath, gotErr)
		}
	}
}

func TestLookupEngineExecutionHintsResolvesTypeToHardwareCompat(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO hardware_profiles (id, name, gpu_arch) VALUES ('nvidia-rtx4090-x86', 'RTX 4090', 'Ada')`); err != nil {
		t.Fatalf("insert hardware profile: %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO engine_assets (id, type) VALUES ('sglang-kt-ada', 'sglang-kt')`); err != nil {
		t.Fatalf("insert engine asset: %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO engine_hardware_compat (engine_id, hardware_id, cpu_offload, ssd_offload, npu_offload)
		 VALUES ('sglang-kt-ada', 'nvidia-rtx4090-x86', 1, 0, 0)`); err != nil {
		t.Fatalf("insert engine compat: %v", err)
	}

	hints, err := db.LookupEngineExecutionHints(ctx, "sglang-kt", "nvidia-rtx4090-x86")
	if err != nil {
		t.Fatalf("LookupEngineExecutionHints: %v", err)
	}
	if !hints.CPUOffload || hints.SSDOffload || hints.NPUOffload {
		t.Fatalf("hints = %#v, want CPU offload only", hints)
	}
}

func TestBuildHeterogeneousObservationUsesHintsNotEngineName(t *testing.T) {
	got := BuildHeterogeneousObservation(EngineExecutionHints{CPUOffload: true}, map[string]any{
		"n_gpu_layers":        40,
		"threadpool_count":    2,
		"mem_fraction_static": 0.85,
	}, map[string]any{
		"ram_usage_mib":       32768,
		"cpu_usage_pct":       61.5,
		"vram_usage_mib":      84424,
		"gpu_utilization_pct": 58.0,
	})
	if got["path"] != "gpu+cpu" {
		t.Fatalf("path = %#v, want gpu+cpu", got["path"])
	}
	if got["cpu_offload"] != true {
		t.Fatalf("cpu_offload = %#v, want true", got["cpu_offload"])
	}
	if got["n_gpu_layers"] != 40 {
		t.Fatalf("n_gpu_layers = %#v, want 40", got["n_gpu_layers"])
	}
	if got["threadpool_count"] != 2 {
		t.Fatalf("threadpool_count = %#v, want 2", got["threadpool_count"])
	}
	if _, ok := got["mem_fraction_static"]; ok {
		t.Fatalf("unexpected generic config key in observation: %#v", got)
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

func TestUpsertOpenQuestion_SeedsCatalogStatusAndPreservesRuntimeResolution(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	if err := db.UpsertOpenQuestion(ctx, "oq-001", "stack:hami", "question", "test", "hypothesis", "untested", ""); err != nil {
		t.Fatalf("UpsertOpenQuestion initial: %v", err)
	}
	if err := db.UpsertOpenQuestion(ctx, "oq-001", "stack:hami", "question", "test", "hypothesis", "confirmed_incompatible", "known finding"); err != nil {
		t.Fatalf("UpsertOpenQuestion catalog update: %v", err)
	}

	q, err := db.GetOpenQuestion(ctx, "oq-001")
	if err != nil {
		t.Fatalf("GetOpenQuestion: %v", err)
	}
	if q.Status != "confirmed_incompatible" {
		t.Fatalf("status = %q, want confirmed_incompatible", q.Status)
	}
	if q.ActualResult != "known finding" {
		t.Fatalf("actual_result = %q, want known finding", q.ActualResult)
	}

	if err := db.ResolveOpenQuestion(ctx, "oq-001", "tested", "runtime result", "apple-m4-arm64"); err != nil {
		t.Fatalf("ResolveOpenQuestion: %v", err)
	}
	if err := db.UpsertOpenQuestion(ctx, "oq-001", "stack:hami", "question", "test", "hypothesis", "untested", ""); err != nil {
		t.Fatalf("UpsertOpenQuestion preserve runtime: %v", err)
	}

	q, err = db.GetOpenQuestion(ctx, "oq-001")
	if err != nil {
		t.Fatalf("GetOpenQuestion after resolve: %v", err)
	}
	if q.Status != "tested" {
		t.Fatalf("status after resolve = %q, want tested", q.Status)
	}
	if q.ActualResult != "runtime result" {
		t.Fatalf("actual_result after resolve = %q, want runtime result", q.ActualResult)
	}
	if q.Hardware != "apple-m4-arm64" {
		t.Fatalf("hardware = %q, want apple-m4-arm64", q.Hardware)
	}
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

func TestUpdateConfigStatus(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	cfg := &Configuration{
		ID:         "cfg-001",
		HardwareID: "nvidia-gb10-arm64",
		EngineID:   "vllm-nightly",
		ModelID:    "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.8}`,
		ConfigHash: "abc123",
		Status:     "experiment",
		Source:     "benchmark",
	}
	if err := db.InsertConfiguration(ctx, cfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}

	t.Run("promote to golden", func(t *testing.T) {
		if err := db.UpdateConfigStatus(ctx, "cfg-001", "golden"); err != nil {
			t.Fatalf("UpdateConfigStatus: %v", err)
		}
		got, err := db.GetConfiguration(ctx, "cfg-001")
		if err != nil {
			t.Fatalf("GetConfiguration: %v", err)
		}
		if got.Status != "golden" {
			t.Errorf("Status = %q, want %q", got.Status, "golden")
		}
	})

	t.Run("archive", func(t *testing.T) {
		if err := db.UpdateConfigStatus(ctx, "cfg-001", "archived"); err != nil {
			t.Fatalf("UpdateConfigStatus: %v", err)
		}
		got, _ := db.GetConfiguration(ctx, "cfg-001")
		if got.Status != "archived" {
			t.Errorf("Status = %q, want %q", got.Status, "archived")
		}
	})

	t.Run("nonexistent config", func(t *testing.T) {
		err := db.UpdateConfigStatus(ctx, "does-not-exist", "golden")
		if err == nil {
			t.Fatal("expected error for nonexistent config")
		}
	})
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

func TestFindGoldenBenchmark(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	// Insert a golden config
	cfg := &Configuration{
		ID: "cfg-golden-1", HardwareID: "hw1", EngineID: "eng1", ModelID: "model1",
		Config: `{"concurrency":4}`, ConfigHash: "hash-golden-1",
		Status: "golden", Source: "benchmark",
	}
	if err := db.InsertConfiguration(ctx, cfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}

	// Insert a benchmark for it
	br := &BenchmarkResult{
		ID: "bench-1", ConfigID: "cfg-golden-1", Concurrency: 4,
		ThroughputTPS: 100.0, Modality: "text",
	}
	if err := db.InsertBenchmarkResult(ctx, br); err != nil {
		t.Fatalf("InsertBenchmarkResult: %v", err)
	}

	t.Run("finds golden with benchmark", func(t *testing.T) {
		c, b, err := db.FindGoldenBenchmark(ctx, "hw1", "eng1", "model1", "text")
		if err != nil {
			t.Fatalf("FindGoldenBenchmark: %v", err)
		}
		if c == nil {
			t.Fatal("expected non-nil config")
		}
		if c.ID != "cfg-golden-1" {
			t.Errorf("config ID = %q, want cfg-golden-1", c.ID)
		}
		if b == nil {
			t.Fatal("expected non-nil benchmark")
		}
		if b.ThroughputTPS != 100.0 {
			t.Errorf("ThroughputTPS = %f, want 100.0", b.ThroughputTPS)
		}
	})

	t.Run("no golden for different triple", func(t *testing.T) {
		c, b, err := db.FindGoldenBenchmark(ctx, "hw2", "eng1", "model1", "text")
		if err != nil {
			t.Fatalf("FindGoldenBenchmark: %v", err)
		}
		if c != nil || b != nil {
			t.Error("expected nil config and benchmark for non-matching triple")
		}
	})

	t.Run("golden without benchmark", func(t *testing.T) {
		cfg2 := &Configuration{
			ID: "cfg-golden-2", HardwareID: "hw2", EngineID: "eng2", ModelID: "model2",
			Config: `{}`, ConfigHash: "hash-golden-2",
			Status: "golden", Source: "benchmark",
		}
		if err := db.InsertConfiguration(ctx, cfg2); err != nil {
			t.Fatalf("InsertConfiguration: %v", err)
		}
		c, b, err := db.FindGoldenBenchmark(ctx, "hw2", "eng2", "model2", "text")
		if err != nil {
			t.Fatalf("FindGoldenBenchmark: %v", err)
		}
		if c == nil {
			t.Fatal("expected non-nil config")
		}
		if b != nil {
			t.Error("expected nil benchmark for golden config without benchmarks")
		}
	})

	t.Run("filters by modality", func(t *testing.T) {
		if err := db.InsertBenchmarkResult(ctx, &BenchmarkResult{
			ID: "bench-vlm-1", ConfigID: "cfg-golden-1", Concurrency: 1,
			ThroughputTPS: 12.0, Modality: "vlm",
		}); err != nil {
			t.Fatalf("InsertBenchmarkResult(vlm): %v", err)
		}
		c, b, err := db.FindGoldenBenchmark(ctx, "hw1", "eng1", "model1", "vlm")
		if err != nil {
			t.Fatalf("FindGoldenBenchmark(vlm): %v", err)
		}
		if c == nil || b == nil {
			t.Fatal("expected golden config with vlm benchmark")
		}
		if b.Modality != "vlm" {
			t.Fatalf("benchmark modality = %q, want vlm", b.Modality)
		}
		if b.ID != "bench-vlm-1" {
			t.Fatalf("benchmark id = %q, want bench-vlm-1", b.ID)
		}
	})
}
