package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/mcp"
)

func TestNormalizeFeedbackStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantStatus string
		wantAccept bool
		wantErr    bool
	}{
		{name: "validated", input: "validated", wantStatus: "validated", wantAccept: true},
		{name: "accepted alias", input: "accepted", wantStatus: "validated", wantAccept: true},
		{name: "rejected", input: "rejected", wantStatus: "rejected", wantAccept: false},
		{name: "unsupported", input: "partial", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, accepted, err := normalizeFeedbackStatus(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeFeedbackStatus(%q) error = %v", tt.input, err)
			}
			if status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", status, tt.wantStatus)
			}
			if accepted != tt.wantAccept {
				t.Fatalf("accepted = %v, want %v", accepted, tt.wantAccept)
			}
		})
	}
}

func TestNormalizeCentralAdvisoryListFiltersLegacyPayloads(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"advisories": [
			{
				"id": "adv-pending",
				"type": "recommendation",
				"hardware": "nvidia-gb10-arm64",
				"model": "qwen3-8b",
				"engine": "vllm",
				"summary": "try the golden config",
				"content_json": {"config":{"gpu_memory_utilization":0.8}}
			},
			{
				"id": "adv-old",
				"type": "gap",
				"hardware": "nvidia-rtx4090-x86",
				"status": "accepted"
			}
		]
	}`)

	items, err := normalizeCentralAdvisoryList(body)
	if err != nil {
		t.Fatalf("normalizeCentralAdvisoryList: %v", err)
	}
	items = filterNormalizedAdvisories(items, "nvidia-gb10-arm64", "pending")
	if len(items) != 1 {
		t.Fatalf("filtered items = %d, want 1", len(items))
	}

	var got map[string]any
	if err := json.Unmarshal(items[0], &got); err != nil {
		t.Fatalf("unmarshal normalized advisory: %v", err)
	}
	if got["id"] != "adv-pending" {
		t.Fatalf("id = %v, want adv-pending", got["id"])
	}
	if got["type"] != "config_recommend" {
		t.Fatalf("type = %v, want config_recommend", got["type"])
	}
	if got["status"] != "pending" {
		t.Fatalf("status = %v, want pending", got["status"])
	}
	if got["target_hardware"] != "nvidia-gb10-arm64" {
		t.Fatalf("target_hardware = %v, want nvidia-gb10-arm64", got["target_hardware"])
	}
	if got["target_model"] != "qwen3-8b" {
		t.Fatalf("target_model = %v, want qwen3-8b", got["target_model"])
	}
	if got["target_engine"] != "vllm" {
		t.Fatalf("target_engine = %v, want vllm", got["target_engine"])
	}
	content, ok := got["content"].(map[string]any)
	if !ok {
		t.Fatalf("content type = %T, want object", got["content"])
	}
	if _, ok := content["config"].(map[string]any); !ok {
		t.Fatalf("content.config missing: %#v", content)
	}
}

func TestNormalizeCentralScenarioListAppliesSourceAndHardwareFilters(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"scenarios": [
			{
				"id": "scn-advisor",
				"name": "advisor-scn",
				"hardware": "nvidia-gb10-arm64",
				"source": "advisor",
				"models": "[\"qwen3-8b\"]"
			},
			{
				"id": "scn-analyzer",
				"name": "analyzer-scn",
				"hardware_profile": "nvidia-rtx4090-x86",
				"source": "analyzer"
			}
		]
	}`)

	items, err := normalizeCentralScenarioList(body, "advisor")
	if err != nil {
		t.Fatalf("normalizeCentralScenarioList: %v", err)
	}
	items = filterNormalizedScenarios(items, "nvidia-gb10-arm64")
	if len(items) != 1 {
		t.Fatalf("filtered items = %d, want 1", len(items))
	}

	var got map[string]any
	if err := json.Unmarshal(items[0], &got); err != nil {
		t.Fatalf("unmarshal normalized scenario: %v", err)
	}
	if got["id"] != "scn-advisor" {
		t.Fatalf("id = %v, want scn-advisor", got["id"])
	}
	if got["source"] != "advisor" {
		t.Fatalf("source = %v, want advisor", got["source"])
	}
	models, ok := got["models"].([]any)
	if !ok || len(models) != 1 || models[0] != "qwen3-8b" {
		t.Fatalf("models = %#v, want [qwen3-8b]", got["models"])
	}
}

func TestPullAdvisoriesToEventBusDedupesPublishedItems(t *testing.T) {
	t.Parallel()

	bus := agent.NewEventBus()
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)

	ac := &appContext{eventBus: bus}
	deps := &mcp.ToolDeps{
		SyncPullAdvisories: func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`[
				{"id":"adv-1","status":"pending","target_hardware":"nvidia-gb10-arm64"},
				{"id":"adv-1","status":"pending","target_hardware":"nvidia-gb10-arm64"}
			]`), nil
		},
		SyncPullScenarios: func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`[
				{"id":"scn-1","hardware_profile":"nvidia-gb10-arm64"},
				{"id":"scn-1","hardware_profile":"nvidia-gb10-arm64"}
			]`), nil
		},
	}

	advisories, scenarios, advisoryEvents, scenarioEvents := pullAdvisoriesToEventBus(context.Background(), ac, deps)
	if len(advisories) != 1 {
		t.Fatalf("advisories = %d, want 1", len(advisories))
	}
	if len(scenarios) != 1 {
		t.Fatalf("scenarios = %d, want 1", len(scenarios))
	}
	if advisoryEvents != 1 {
		t.Fatalf("advisoryEvents = %d, want 1", advisoryEvents)
	}
	if scenarioEvents != 1 {
		t.Fatalf("scenarioEvents = %d, want 1", scenarioEvents)
	}

	gotTypes := []string{(<-sub).Type, (<-sub).Type}
	if gotTypes[0] != agent.EventCentralAdvisory && gotTypes[1] != agent.EventCentralAdvisory {
		t.Fatalf("event types = %#v, want advisory included", gotTypes)
	}
	if gotTypes[0] != agent.EventCentralScenario && gotTypes[1] != agent.EventCentralScenario {
		t.Fatalf("event types = %#v, want scenario included", gotTypes)
	}
}

func TestPullAdvisoriesToEventBusWithoutEventBusStillReturnsItems(t *testing.T) {
	t.Parallel()

	ac := &appContext{}
	deps := &mcp.ToolDeps{
		SyncPullAdvisories: func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`[{"id":"adv-1","status":"delivered","target_hardware":"nvidia-rtx4090-x86"}]`), nil
		},
		SyncPullScenarios: func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`[{"id":"scn-1","hardware_profile":"nvidia-rtx4090-x86"}]`), nil
		},
	}

	advisories, scenarios, advisoryEvents, scenarioEvents := pullAdvisoriesToEventBus(context.Background(), ac, deps)
	if len(advisories) != 1 {
		t.Fatalf("advisories = %d, want 1", len(advisories))
	}
	if len(scenarios) != 1 {
		t.Fatalf("scenarios = %d, want 1", len(scenarios))
	}
	if advisoryEvents != 0 {
		t.Fatalf("advisoryEvents = %d, want 0", advisoryEvents)
	}
	if scenarioEvents != 0 {
		t.Fatalf("scenarioEvents = %d, want 0", scenarioEvents)
	}
}

func TestBuildSyncURLEncodesSince(t *testing.T) {
	t.Parallel()

	got, err := buildSyncURL("http://localhost:18080", "2026-04-07 07:37:48", "dev-42")
	if err != nil {
		t.Fatalf("buildSyncURL: %v", err)
	}
	want := "http://localhost:18080/api/v1/sync?device_id=dev-42&since=2026-04-07+07%3A37%3A48"
	if got != want {
		t.Fatalf("buildSyncURL = %q, want %q", got, want)
	}
}

func TestBuildSyncURLWithoutDeviceIDOmitsParam(t *testing.T) {
	t.Parallel()

	got, err := buildSyncURL("http://localhost:18080", "", "")
	if err != nil {
		t.Fatalf("buildSyncURL: %v", err)
	}
	want := "http://localhost:18080/api/v1/sync"
	if got != want {
		t.Fatalf("buildSyncURL = %q, want %q", got, want)
	}
}

func TestWithDeviceIDAppendsQueryParam(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		inURL    string
		deviceID string
		want     string
	}{
		{"no-existing-query", "https://c/api/v1/ingest", "dev-1", "https://c/api/v1/ingest?device_id=dev-1"},
		{"with-existing-query", "https://c/api/v1/advisories?hardware=abc", "dev-2", "https://c/api/v1/advisories?hardware=abc&device_id=dev-2"},
		{"empty-device-id-unchanged", "https://c/api/v1/ingest?x=1", "", "https://c/api/v1/ingest?x=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withDeviceID(tc.inURL, tc.deviceID); got != tc.want {
				t.Errorf("withDeviceID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildCentralIngestPayloadNormalizesEmbeddedConfigJSON(t *testing.T) {
	t.Parallel()

	exportData := []byte(`{
		"stats": {"configurations": 1, "benchmark_results": 1, "knowledge_notes": 1},
		"data": {
			"configurations": [
				{
					"id": "cfg-1",
					"hardware_id": "nvidia-gb10-arm64",
					"engine_id": "vllm-nightly",
					"model_id": "qwen3-8b",
					"config": "{\"gpu_memory_utilization\":0.8}",
					"config_hash": "hash-1"
				}
			],
			"benchmark_results": [
				{"id": "bench-1", "config_id": "cfg-1", "throughput_tps": 42.5}
			],
			"knowledge_notes": [
				{"id": "note-1", "title": "ok", "content": "done"}
			]
		}
	}`)

	payload, stats, err := buildCentralIngestPayload(exportData, "device-1", "Blackwell", "nvidia-gb10-arm64")
	if err != nil {
		t.Fatalf("buildCentralIngestPayload: %v", err)
	}
	if stats["configurations"] != 1 || stats["benchmark_results"] != 1 || stats["knowledge_notes"] != 1 {
		t.Fatalf("stats = %#v, want all counts = 1", stats)
	}

	var got struct {
		SchemaVersion   int               `json:"schema_version"`
		DeviceID        string            `json:"device_id"`
		GPUArch         string            `json:"gpu_arch"`
		HardwareProfile string            `json:"hardware_profile"`
		Configs         []json.RawMessage `json:"configurations"`
		Benchmarks      []json.RawMessage `json:"benchmarks"`
		Notes           []json.RawMessage `json:"knowledge_notes"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.SchemaVersion != 1 || got.DeviceID != "device-1" || got.GPUArch != "Blackwell" || got.HardwareProfile != "nvidia-gb10-arm64" {
		t.Fatalf("header = %+v", got)
	}
	if len(got.Configs) != 1 || len(got.Benchmarks) != 1 || len(got.Notes) != 1 {
		t.Fatalf("payload counts = cfg:%d bench:%d note:%d", len(got.Configs), len(got.Benchmarks), len(got.Notes))
	}

	var cfg struct {
		ID              string          `json:"id"`
		HardwareProfile string          `json:"hardware_profile"`
		Config          json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(got.Configs[0], &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.ID != "cfg-1" {
		t.Fatalf("config id = %q, want cfg-1", cfg.ID)
	}
	if cfg.HardwareProfile != "nvidia-gb10-arm64" {
		t.Fatalf("config hardware_profile = %q, want nvidia-gb10-arm64", cfg.HardwareProfile)
	}
	if string(cfg.Config) != `{"gpu_memory_utilization":0.8}` {
		t.Fatalf("config payload = %s, want raw JSON object", string(cfg.Config))
	}
}
