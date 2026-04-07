package central

import (
	"context"
	"testing"
)

// storeTestSuite runs the same tests against any CentralStore implementation.
func storeTestSuite(t *testing.T, store CentralStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("Migrate", func(t *testing.T) {
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		// Idempotent — second call must not fail
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate (idempotent): %v", err)
		}
	})

	t.Run("DeviceRoundTrip", func(t *testing.T) {
		d := Device{ID: "dev-1", GPUArch: "Ada", HardwareProfile: "nvidia-rtx4090"}
		if err := store.UpsertDevice(ctx, d); err != nil {
			t.Fatalf("UpsertDevice: %v", err)
		}
		devs, err := store.ListDevices(ctx)
		if err != nil {
			t.Fatalf("ListDevices: %v", err)
		}
		if len(devs) < 1 {
			t.Fatal("expected at least 1 device")
		}
		found := false
		for _, dev := range devs {
			if dev.ID == "dev-1" {
				found = true
				if dev.GPUArch != "Ada" {
					t.Fatalf("GPUArch = %q, want Ada", dev.GPUArch)
				}
			}
		}
		if !found {
			t.Fatal("device dev-1 not found")
		}

		// Upsert again — should not duplicate
		d.GPUArch = "Blackwell"
		if err := store.UpsertDevice(ctx, d); err != nil {
			t.Fatalf("UpsertDevice (update): %v", err)
		}
		devs2, _ := store.ListDevices(ctx)
		count := 0
		for _, dev := range devs2 {
			if dev.ID == "dev-1" {
				count++
				if dev.GPUArch != "Blackwell" {
					t.Fatalf("GPUArch after upsert = %q, want Blackwell", dev.GPUArch)
				}
			}
		}
		if count != 1 {
			t.Fatalf("device count = %d, want 1", count)
		}
	})

	t.Run("ConfigurationRoundTrip", func(t *testing.T) {
		c := Configuration{
			ID:         "cfg-1",
			DeviceID:   "dev-1",
			Hardware:   "nvidia-rtx4090",
			EngineType: "vllm",
			Model:      "qwen3-8b",
			Config:     `{"gpu_memory_utilization":0.9}`,
			ConfigHash: "hash-cfg-1",
			Status:     "experiment",
			CreatedAt:  "2026-01-01T00:00:00Z",
			UpdatedAt:  "2026-01-01T00:00:00Z",
		}
		if err := store.InsertConfiguration(ctx, c); err != nil {
			t.Fatalf("InsertConfiguration: %v", err)
		}

		exists, err := store.ConfigExistsByHash(ctx, "hash-cfg-1")
		if err != nil {
			t.Fatalf("ConfigExistsByHash: %v", err)
		}
		if !exists {
			t.Fatal("expected config to exist by hash")
		}

		notExists, _ := store.ConfigExistsByHash(ctx, "nonexistent")
		if notExists {
			t.Fatal("expected nonexistent hash to return false")
		}

		configs, err := store.QueryConfigurations(ctx, ConfigFilter{Hardware: "nvidia-rtx4090"})
		if err != nil {
			t.Fatalf("QueryConfigurations: %v", err)
		}
		if len(configs) != 1 {
			t.Fatalf("configs = %d, want 1", len(configs))
		}
		if configs[0].ID != "cfg-1" {
			t.Fatalf("config ID = %q, want cfg-1", configs[0].ID)
		}

		// Filter that doesn't match
		empty, _ := store.QueryConfigurations(ctx, ConfigFilter{Hardware: "no-match"})
		if len(empty) != 0 {
			t.Fatalf("expected 0 configs, got %d", len(empty))
		}
	})

	t.Run("BenchmarkRoundTrip", func(t *testing.T) {
		b := BenchmarkResult{
			ID:            "bench-1",
			ConfigID:      "cfg-1",
			DeviceID:      "dev-1",
			Concurrency:   2,
			ThroughputTPS: 42.5,
			TTFTP50ms:     120.0,
			TestedAt:      "2026-01-01T01:00:00Z",
		}
		if err := store.InsertBenchmark(ctx, b); err != nil {
			t.Fatalf("InsertBenchmark: %v", err)
		}

		results, err := store.ListBenchmarksForSync(ctx, []string{"cfg-1"}, "")
		if err != nil {
			t.Fatalf("ListBenchmarksForSync: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("benchmarks = %d, want 1", len(results))
		}
		if results[0].ThroughputTPS != 42.5 {
			t.Fatalf("ThroughputTPS = %f, want 42.5", results[0].ThroughputTPS)
		}
	})

	t.Run("KnowledgeNoteRoundTrip", func(t *testing.T) {
		n := KnowledgeNote{
			ID:         "note-1",
			Title:      "Test note",
			Content:    "Some content",
			Confidence: "high",
			CreatedAt:  "2026-01-01T00:00:00Z",
		}
		if err := store.UpsertKnowledgeNote(ctx, n); err != nil {
			t.Fatalf("UpsertKnowledgeNote: %v", err)
		}
		notes, err := store.ListKnowledgeNotes(ctx)
		if err != nil {
			t.Fatalf("ListKnowledgeNotes: %v", err)
		}
		if len(notes) != 1 {
			t.Fatalf("notes = %d, want 1", len(notes))
		}
		if notes[0].Title != "Test note" {
			t.Fatalf("Title = %q, want 'Test note'", notes[0].Title)
		}

		// Upsert — should overwrite
		n.Title = "Updated note"
		if err := store.UpsertKnowledgeNote(ctx, n); err != nil {
			t.Fatalf("UpsertKnowledgeNote (update): %v", err)
		}
		notes2, _ := store.ListKnowledgeNotes(ctx)
		if len(notes2) != 1 {
			t.Fatalf("notes after upsert = %d, want 1", len(notes2))
		}
		if notes2[0].Title != "Updated note" {
			t.Fatalf("Title after upsert = %q, want 'Updated note'", notes2[0].Title)
		}
	})

	t.Run("AdvisoryRoundTrip", func(t *testing.T) {
		a := Advisory{
			ID:        "adv-1",
			Type:      "recommendation",
			Severity:  "info",
			Hardware:  "nvidia-rtx4090",
			Title:     "Try vLLM",
			Summary:   "vLLM performs well on RTX 4090",
			Details:   `{"engine":"vllm"}`,
			Confidence: "high",
			CreatedAt: "2026-01-01T00:00:00Z",
		}
		if err := store.InsertAdvisory(ctx, a); err != nil {
			t.Fatalf("InsertAdvisory: %v", err)
		}
		advs, err := store.ListAdvisories(ctx, AdvisoryFilter{Type: "recommendation"})
		if err != nil {
			t.Fatalf("ListAdvisories: %v", err)
		}
		if len(advs) != 1 {
			t.Fatalf("advisories = %d, want 1", len(advs))
		}
		if advs[0].Title != "Try vLLM" {
			t.Fatalf("Title = %q, want 'Try vLLM'", advs[0].Title)
		}

		// Feedback
		if err := store.UpdateAdvisoryFeedback(ctx, "adv-1", "helpful", true); err != nil {
			t.Fatalf("UpdateAdvisoryFeedback: %v", err)
		}
		advs2, _ := store.ListAdvisories(ctx, AdvisoryFilter{})
		found := false
		for _, a := range advs2 {
			if a.ID == "adv-1" {
				found = true
				if a.Feedback != "helpful" || !a.Accepted {
					t.Fatalf("feedback not updated: %+v", a)
				}
			}
		}
		if !found {
			t.Fatal("advisory adv-1 not found after feedback")
		}
	})

	t.Run("AnalysisRunRoundTrip", func(t *testing.T) {
		r := AnalysisRun{
			ID:            "run-1",
			Type:          "gap_scan",
			Status:        "completed",
			Summary:       "Found 3 gaps",
			AdvisoryCount: 3,
			DurationMs:    1500,
			CreatedAt:     "2026-01-01T00:00:00Z",
		}
		if err := store.InsertAnalysisRun(ctx, r); err != nil {
			t.Fatalf("InsertAnalysisRun: %v", err)
		}
		runs, err := store.ListAnalysisRuns(ctx, 10)
		if err != nil {
			t.Fatalf("ListAnalysisRuns: %v", err)
		}
		if len(runs) != 1 {
			t.Fatalf("runs = %d, want 1", len(runs))
		}
		if runs[0].Summary != "Found 3 gaps" {
			t.Fatalf("Summary = %q, want 'Found 3 gaps'", runs[0].Summary)
		}
	})

	t.Run("ScenarioRoundTrip", func(t *testing.T) {
		s := Scenario{
			ID:          "scn-1",
			Name:        "dual-model",
			Description: "Two models on one GPU",
			Hardware:    "nvidia-rtx4090",
			Models:      `["qwen3-8b","llama3-8b"]`,
			Config:      `{"partition":"50-50"}`,
			Source:      "advisor",
			CreatedAt:   "2026-01-01T00:00:00Z",
		}
		if err := store.InsertScenario(ctx, s); err != nil {
			t.Fatalf("InsertScenario: %v", err)
		}
		scenarios, err := store.ListScenarios(ctx, ScenarioFilter{Hardware: "nvidia-rtx4090"})
		if err != nil {
			t.Fatalf("ListScenarios: %v", err)
		}
		if len(scenarios) != 1 {
			t.Fatalf("scenarios = %d, want 1", len(scenarios))
		}
		if scenarios[0].Name != "dual-model" {
			t.Fatalf("Name = %q, want 'dual-model'", scenarios[0].Name)
		}
	})

	t.Run("Stats", func(t *testing.T) {
		stats, err := store.Stats(ctx)
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if stats.Devices < 1 {
			t.Fatalf("Devices = %d, want >= 1", stats.Devices)
		}
		if stats.Configurations < 1 {
			t.Fatalf("Configurations = %d, want >= 1", stats.Configurations)
		}
		if stats.Advisories < 1 {
			t.Fatalf("Advisories = %d, want >= 1", stats.Advisories)
		}
	})

	t.Run("CoverageMatrix", func(t *testing.T) {
		coverage, err := store.CoverageMatrix(ctx)
		if err != nil {
			t.Fatalf("CoverageMatrix: %v", err)
		}
		if len(coverage) < 1 {
			t.Fatal("expected at least 1 coverage entry")
		}
	})

	t.Run("SyncFilter", func(t *testing.T) {
		configs, err := store.ListConfigurationsForSync(ctx, SyncFilter{Since: "2025-12-01T00:00:00Z", Limit: 10})
		if err != nil {
			t.Fatalf("ListConfigurationsForSync: %v", err)
		}
		if len(configs) < 1 {
			t.Fatal("expected at least 1 config from sync")
		}
	})
}
