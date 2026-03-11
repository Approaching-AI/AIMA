package central

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestServerIngestAndSyncPreservesKnowledgeFields(t *testing.T) {
	srv, err := New(Config{
		APIKey: "test-key",
		DBPath: filepath.Join(t.TempDir(), "central.db"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	httpSrv := httptest.NewServer(srv.mux)
	defer httpSrv.Close()

	payload := map[string]any{
		"schema_version": 1,
		"device_id":      "device-a",
		"gpu_arch":       "Blackwell",
		"configurations": []map[string]any{
			{
				"id":           "cfg-001",
				"hardware_id":  "nvidia-gb10-arm64",
				"engine_id":    "vllm-nightly",
				"model_id":     "qwen3-8b",
				"slot":         "slot-a",
				"config":       json.RawMessage(`{"gpu_memory_utilization":0.8}`),
				"config_hash":  "hash-001",
				"derived_from": "cfg-000",
				"status":       "golden",
				"tags":         []string{"fast", "stable"},
				"source":       "benchmark",
				"device_id":    "device-a",
				"created_at":   "2026-03-10T07:00:00Z",
				"updated_at":   "2026-03-10T07:01:00Z",
			},
		},
		"benchmarks": []map[string]any{
			{
				"id":                "bench-001",
				"config_id":         "cfg-001",
				"concurrency":       2,
				"input_len_bucket":  "1K",
				"output_len_bucket": "128",
				"modality":          "text",
				"ttft_p50_ms":       120.5,
				"ttft_p95_ms":       150.5,
				"ttft_p99_ms":       180.5,
				"tpot_p50_ms":       12.5,
				"tpot_p95_ms":       15.5,
				"throughput_tps":    42.25,
				"qps":               1.75,
				"vram_usage_mib":    32768,
				"ram_usage_mib":     2048,
				"power_draw_watts":  85.5,
				"gpu_util_pct":      92.0,
				"error_rate":        0.01,
				"oom_occurred":      false,
				"stability":         "stable",
				"duration_s":        18,
				"sample_count":      6,
				"tested_at":         "2026-03-10T07:02:00Z",
				"agent_model":       "claude-opus-4.6",
				"notes":             "full fidelity benchmark",
			},
		},
		"knowledge_notes": []map[string]any{
			{
				"id":               "note-001",
				"title":            "Validated qwen3-8b on GB10",
				"tags":             []string{"validation", "gb10"},
				"hardware_profile": "nvidia-gb10-arm64",
				"model":            "qwen3-8b",
				"engine":           "vllm-nightly",
				"content":          "Winner config holds under concurrency 2.",
				"confidence":       "high",
				"created_at":       "2026-03-10T07:03:00Z",
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal payload: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, httpSrv.URL+"/api/v1/ingest", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest ingest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do ingest: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest status = %d, want 200", resp.StatusCode)
	}

	syncReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, httpSrv.URL+"/api/v1/sync", nil)
	if err != nil {
		t.Fatalf("NewRequest sync: %v", err)
	}
	syncReq.Header.Set("Authorization", "Bearer test-key")
	syncResp, err := http.DefaultClient.Do(syncReq)
	if err != nil {
		t.Fatalf("Do sync: %v", err)
	}
	defer syncResp.Body.Close()
	if syncResp.StatusCode != http.StatusOK {
		t.Fatalf("sync status = %d, want 200", syncResp.StatusCode)
	}

	var envelope struct {
		SchemaVersion int `json:"schema_version"`
		Data          struct {
			Configurations []struct {
				ID          string          `json:"id"`
				Slot        string          `json:"slot"`
				Status      string          `json:"status"`
				Source      string          `json:"source"`
				DerivedFrom string          `json:"derived_from"`
				Tags        []string        `json:"tags"`
				Config      json.RawMessage `json:"config"`
			} `json:"configurations"`
			BenchmarkResults []struct {
				ID              string  `json:"id"`
				InputLenBucket  string  `json:"input_len_bucket"`
				OutputLenBucket string  `json:"output_len_bucket"`
				Modality        string  `json:"modality"`
				TTFTP50ms       float64 `json:"ttft_p50_ms"`
				TPOTP50ms       float64 `json:"tpot_p50_ms"`
				ThroughputTPS   float64 `json:"throughput_tps"`
				QPS             float64 `json:"qps"`
				VRAMUsageMiB    int     `json:"vram_usage_mib"`
				RAMUsageMiB     int     `json:"ram_usage_mib"`
				GPUUtilPct      float64 `json:"gpu_util_pct"`
				ErrorRate       float64 `json:"error_rate"`
				SampleCount     int     `json:"sample_count"`
				AgentModel      string  `json:"agent_model"`
				Notes           string  `json:"notes"`
			} `json:"benchmark_results"`
			KnowledgeNotes []struct {
				ID      string   `json:"id"`
				Title   string   `json:"title"`
				Tags    []string `json:"tags"`
				Content string   `json:"content"`
			} `json:"knowledge_notes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(syncResp.Body).Decode(&envelope); err != nil {
		t.Fatalf("Decode sync response: %v", err)
	}

	if envelope.SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", envelope.SchemaVersion)
	}
	if len(envelope.Data.Configurations) != 1 {
		t.Fatalf("configurations = %d, want 1", len(envelope.Data.Configurations))
	}
	cfg := envelope.Data.Configurations[0]
	if cfg.Slot != "slot-a" || cfg.Source != "benchmark" || cfg.DerivedFrom != "cfg-000" || len(cfg.Tags) != 2 {
		t.Fatalf("configuration fidelity lost: %+v", cfg)
	}

	if len(envelope.Data.BenchmarkResults) != 1 {
		t.Fatalf("benchmark_results = %d, want 1", len(envelope.Data.BenchmarkResults))
	}
	bench := envelope.Data.BenchmarkResults[0]
	if bench.InputLenBucket != "1K" || bench.OutputLenBucket != "128" || bench.Modality != "text" {
		t.Fatalf("benchmark buckets/modality lost: %+v", bench)
	}
	if bench.TTFTP50ms != 120.5 || bench.TPOTP50ms != 12.5 || bench.ThroughputTPS != 42.25 || bench.QPS != 1.75 {
		t.Fatalf("benchmark latency/throughput lost: %+v", bench)
	}
	if bench.VRAMUsageMiB != 32768 || bench.RAMUsageMiB != 2048 || bench.GPUUtilPct != 92.0 || bench.ErrorRate != 0.01 {
		t.Fatalf("benchmark resource fidelity lost: %+v", bench)
	}
	if bench.SampleCount != 6 || bench.AgentModel != "claude-opus-4.6" || bench.Notes != "full fidelity benchmark" {
		t.Fatalf("benchmark metadata lost: %+v", bench)
	}

	if len(envelope.Data.KnowledgeNotes) != 1 {
		t.Fatalf("knowledge_notes = %d, want 1", len(envelope.Data.KnowledgeNotes))
	}
	note := envelope.Data.KnowledgeNotes[0]
	if note.Title != "Validated qwen3-8b on GB10" || note.Content == "" || len(note.Tags) != 2 {
		t.Fatalf("knowledge note fidelity lost: %+v", note)
	}
}
