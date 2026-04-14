package main

import "testing"

func TestParseImportedKnowledgeEnvelope_NormalizesCentralSyncShape(t *testing.T) {
	raw := []byte(`{
		"schema_version": 1,
		"data": {
			"configurations": [
				{
					"id":"cfg-1",
					"hardware_id":"nvidia-gb10-arm64",
					"engine_id":"sglang",
					"model_id":"Qwen2.5-Coder-3B-Instruct",
					"config":{"tensor_parallel_size":2,"max_model_len":8192},
					"created_at":"2026-04-14 03:02:50+00",
					"updated_at":"2026-04-14T03:05:00Z"
				}
			],
			"benchmark_results": [
				{"id":"bench-1","config_id":"cfg-1","modality":"embedding","tested_at":"2026-04-14 03:06:00+00"}
			],
			"knowledge_notes": [
				{"id":"note-1","created_at":"2026-04-14 03:07:00+00","content":"leave content untouched"}
			]
		}
	}`)

	envelope, err := parseImportedKnowledgeEnvelope(raw)
	if err != nil {
		t.Fatalf("parseImportedKnowledgeEnvelope: %v", err)
	}

	cfg := envelope.Data.Configurations[0]
	if got := cfg.Config; got != `{"tensor_parallel_size":2,"max_model_len":8192}` && got != `{"max_model_len":8192,"tensor_parallel_size":2}` {
		t.Fatalf("config = %q, want normalized JSON object string", got)
	}
	if got := cfg.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-04-14T03:02:50Z" {
		t.Fatalf("created_at = %q, want normalized RFC3339 time", got)
	}
	if got := envelope.Data.BenchmarkResults[0].TestedAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-04-14T03:06:00Z" {
		t.Fatalf("tested_at = %q, want normalized RFC3339 time", got)
	}
	if got := envelope.Data.KnowledgeNotes[0].CreatedAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-04-14T03:07:00Z" {
		t.Fatalf("note created_at = %q, want normalized RFC3339 time", got)
	}
}

func TestNormalizeImportedConfigJSON_AcceptsQuotedJSONString(t *testing.T) {
	got, err := normalizeImportedConfigJSON([]byte(`"{\"tensor_parallel_size\":2}"`))
	if err != nil {
		t.Fatalf("normalizeImportedConfigJSON: %v", err)
	}
	if got != `{"tensor_parallel_size":2}` {
		t.Fatalf("config = %q, want decoded JSON string", got)
	}
}

func TestParseImportedTimestamp_LeavesEmptyTimestampZero(t *testing.T) {
	got, err := parseImportedTimestamp("")
	if err != nil {
		t.Fatalf("parseImportedTimestamp: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("timestamp = %v, want zero", got)
	}
}
