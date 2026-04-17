package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/central"
	"github.com/jguan/aima/internal/mcp"
)

func TestV040ReleaseGateAdvisoryLoop_EndToEnd(t *testing.T) {
	ctx := context.Background()
	store := newReleaseGateCentralStore(t)
	server := newReleaseGateCentralServer(t, store, "test-key")
	defer server.Close()

	ac, deps, edgeDB := newReleaseGateEdgeHarness(t, server.URL, "test-key")
	defer edgeDB.Close()

	hwTarget := edgeHardwareTarget(ctx, ac)
	if err := store.InsertAdvisory(ctx, central.Advisory{
		ID:             "adv-v040-1",
		Type:           central.AdvisoryTypeConfigRecommend,
		Status:         central.AdvisoryStatusPending,
		Severity:       "info",
		TargetHardware: hwTarget.MatchValue,
		TargetModel:    "qwen3-8b",
		TargetEngine:   "vllm",
		ContentJSON:    []byte(`{"gpu_memory_utilization":0.8}`),
		Reasoning:      "validate the central recommendation on edge",
		Confidence:     "high",
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("InsertAdvisory: %v", err)
	}

	sub := ac.eventBus.Subscribe()
	defer ac.eventBus.Unsubscribe(sub)

	advisories, scenarios, advisoryEvents, scenarioEvents := pullAdvisoriesToEventBus(ctx, ac, deps)
	if len(advisories) != 1 {
		t.Fatalf("pulled advisories = %d, want 1", len(advisories))
	}
	if len(scenarios) != 0 {
		t.Fatalf("pulled scenarios = %d, want 0", len(scenarios))
	}
	if advisoryEvents != 1 {
		t.Fatalf("advisoryEvents = %d, want 1", advisoryEvents)
	}
	if scenarioEvents != 0 {
		t.Fatalf("scenarioEvents = %d, want 0", scenarioEvents)
	}

	var normalized map[string]any
	if err := json.Unmarshal(advisories[0], &normalized); err != nil {
		t.Fatalf("Unmarshal advisory: %v", err)
	}
	if normalized["id"] != "adv-v040-1" {
		t.Fatalf("advisory id = %v, want adv-v040-1", normalized["id"])
	}
	if normalized["type"] != "config_recommend" {
		t.Fatalf("advisory type = %v, want config_recommend", normalized["type"])
	}
	if normalized["status"] != "pending" {
		t.Fatalf("advisory status = %v, want pending", normalized["status"])
	}
	if normalized["target_model"] != "qwen3-8b" {
		t.Fatalf("target_model = %v, want qwen3-8b", normalized["target_model"])
	}
	if normalized["target_engine"] != "vllm" {
		t.Fatalf("target_engine = %v, want vllm", normalized["target_engine"])
	}

	select {
	case ev := <-sub:
		if ev.Type != agent.EventCentralAdvisory {
			t.Fatalf("event type = %q, want %q", ev.Type, agent.EventCentralAdvisory)
		}
		if id := edgePayloadID(ev.Advisory); id != "adv-v040-1" {
			t.Fatalf("published advisory id = %q, want adv-v040-1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for advisory event")
	}

	stored, err := store.ListAdvisories(ctx, central.AdvisoryFilter{ID: "adv-v040-1", Limit: 1})
	if err != nil {
		t.Fatalf("ListAdvisories after pull: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored advisories = %d, want 1", len(stored))
	}
	if stored[0].Status != central.AdvisoryStatusDelivered {
		t.Fatalf("central status after pull = %q, want delivered", stored[0].Status)
	}
	if stored[0].DeliveredAt == "" {
		t.Fatal("expected delivered_at to be populated after pull")
	}

	resp, err := deps.AdvisoryFeedback(ctx, "adv-v040-1", "accepted", "validated by release-gate test")
	if err != nil {
		t.Fatalf("AdvisoryFeedback: %v", err)
	}
	var feedback map[string]any
	if err := json.Unmarshal(resp, &feedback); err != nil {
		t.Fatalf("Unmarshal feedback response: %v", err)
	}
	if feedback["normalized_status"] != "validated" {
		t.Fatalf("normalized_status = %v, want validated", feedback["normalized_status"])
	}
	if accepted, _ := feedback["accepted"].(bool); !accepted {
		t.Fatalf("accepted = %v, want true", feedback["accepted"])
	}

	stored, err = store.ListAdvisories(ctx, central.AdvisoryFilter{ID: "adv-v040-1", Limit: 1})
	if err != nil {
		t.Fatalf("ListAdvisories after feedback: %v", err)
	}
	if stored[0].Status != central.AdvisoryStatusValidated {
		t.Fatalf("central status after feedback = %q, want validated", stored[0].Status)
	}
	if !stored[0].Accepted {
		t.Fatal("expected advisory accepted=true after validation feedback")
	}
	if stored[0].Feedback != "validated by release-gate test" {
		t.Fatalf("feedback = %q, want %q", stored[0].Feedback, "validated by release-gate test")
	}
	if stored[0].ValidatedAt == "" {
		t.Fatal("expected validated_at to be populated after feedback")
	}
}

func TestV040ReleaseGateScenarioSync_EndToEnd(t *testing.T) {
	ctx := context.Background()
	store := newReleaseGateCentralStore(t)
	server := newReleaseGateCentralServer(t, store, "test-key")
	defer server.Close()

	ac, deps, edgeDB := newReleaseGateEdgeHarness(t, server.URL, "test-key")
	defer edgeDB.Close()

	hwTarget := edgeHardwareTarget(ctx, ac)
	hardware := hwTarget.MatchValue
	if hardware == "" {
		hardware = "test-hardware"
	}

	if err := store.InsertScenario(ctx, central.Scenario{
		ID:              "scn-v040-1",
		Name:            "advisor-scenario",
		Description:     "scenario synced from central to edge",
		HardwareProfile: hardware,
		ScenarioYAML:    `{"deployments":[{"model":"qwen3-8b","engine":"vllm"}]}`,
		Source:          "advisor",
		Models:          `["qwen3-8b"]`,
		Version:         2,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("InsertScenario advisor: %v", err)
	}
	if err := store.InsertScenario(ctx, central.Scenario{
		ID:              "scn-v040-2",
		Name:            "analyzer-scenario",
		HardwareProfile: hardware,
		ScenarioYAML:    `{"deployments":[{"model":"glm-4.7-flash","engine":"vllm"}]}`,
		Source:          "analyzer",
		Models:          `["glm-4.7-flash"]`,
		Version:         1,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("InsertScenario analyzer: %v", err)
	}

	data, err := deps.SyncPullScenarios(ctx)
	if err != nil {
		t.Fatalf("SyncPullScenarios: %v", err)
	}
	var pulled []map[string]any
	if err := json.Unmarshal(data, &pulled); err != nil {
		t.Fatalf("Unmarshal pulled scenarios: %v", err)
	}
	if len(pulled) != 2 {
		t.Fatalf("pulled scenarios = %d, want 2", len(pulled))
	}

	advisorScenario := findScenarioByID(t, pulled, "scn-v040-1")
	if advisorScenario["source"] != "advisor" {
		t.Fatalf("source = %v, want advisor", advisorScenario["source"])
	}
	if advisorScenario["hardware_profile"] != hardware {
		t.Fatalf("hardware_profile = %v, want %q", advisorScenario["hardware_profile"], hardware)
	}
	models, ok := advisorScenario["models"].([]any)
	if !ok || len(models) != 1 || models[0] != "qwen3-8b" {
		t.Fatalf("models = %#v, want [qwen3-8b]", advisorScenario["models"])
	}
	if _, ok := advisorScenario["scenario"]; !ok {
		t.Fatalf("normalized scenario missing scenario payload: %#v", advisorScenario)
	}

	listed, err := deps.ListCentralScenarios(ctx, hardware, "advisor")
	if err != nil {
		t.Fatalf("ListCentralScenarios: %v", err)
	}
	var filtered []map[string]any
	if err := json.Unmarshal(listed, &filtered); err != nil {
		t.Fatalf("Unmarshal filtered scenarios: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("filtered scenarios = %d, want 1", len(filtered))
	}
	if filtered[0]["id"] != "scn-v040-1" {
		t.Fatalf("filtered scenario id = %v, want scn-v040-1", filtered[0]["id"])
	}
}

func newReleaseGateEdgeHarness(t *testing.T, endpoint, apiKey string) (*appContext, *mcp.ToolDeps, *state.DB) {
	t.Helper()

	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open edge DB: %v", err)
	}
	if err := db.SetConfig(ctx, "central.endpoint", endpoint); err != nil {
		t.Fatalf("SetConfig central.endpoint: %v", err)
	}
	if err := db.SetConfig(ctx, "central.api_key", apiKey); err != nil {
		t.Fatalf("SetConfig central.api_key: %v", err)
	}
	// Simulate a registered edge: canonical device.id is required by all
	// outbound Central calls since the aima-service device-registry wiring.
	if err := db.SetConfig(ctx, "device.id", "dev-release-gate"); err != nil {
		t.Fatalf("SetConfig device.id: %v", err)
	}

	ac := &appContext{
		db:       db,
		rt:       &fakeRuntime{name: "docker"},
		eventBus: agent.NewEventBus(),
	}
	deps := &mcp.ToolDeps{
		GetConfig: db.GetConfig,
		SetConfig: db.SetConfig,
	}
	buildIntegrationDeps(ac, deps)
	return ac, deps, db
}

func newReleaseGateCentralStore(t *testing.T) central.CentralStore {
	t.Helper()

	store, err := central.NewSQLiteCentralStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteCentralStore: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate central store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newReleaseGateCentralServer(t *testing.T, store central.CentralStore, apiKey string) *httptest.Server {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiKey != "" && r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/advisories":
			releaseGateHandleListAdvisories(t, store, w, r)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/advisories/") && strings.HasSuffix(r.URL.Path, "/feedback"):
			releaseGateHandleAdvisoryFeedback(t, store, w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/scenarios":
			releaseGateHandleListScenarios(t, store, w, r)
		default:
			http.NotFound(w, r)
		}
	})
	return httptest.NewServer(handler)
}

func releaseGateHandleListAdvisories(t *testing.T, store central.CentralStore, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	q := r.URL.Query()
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	hardware := firstNonEmpty(q.Get("hardware"), q.Get("target_hardware"))
	advs, err := store.ListAdvisories(r.Context(), central.AdvisoryFilter{
		Type:     q.Get("type"),
		Status:   q.Get("status"),
		Severity: q.Get("severity"),
		Hardware: hardware,
		Model:    firstNonEmpty(q.Get("model"), q.Get("target_model")),
		Engine:   firstNonEmpty(q.Get("engine"), q.Get("target_engine")),
		Limit:    limit,
	})
	if err != nil {
		t.Fatalf("ListAdvisories: %v", err)
	}
	if q.Get("status") == central.AdvisoryStatusPending {
		deliveredAt := time.Now().UTC().Format(time.RFC3339)
		for i := range advs {
			if err := store.UpdateAdvisoryStatus(r.Context(), advs[i].ID, central.AdvisoryStatusUpdate{
				Status:      central.AdvisoryStatusDelivered,
				DeliveredAt: deliveredAt,
			}); err != nil {
				t.Fatalf("UpdateAdvisoryStatus delivered: %v", err)
			}
			advs[i].Status = central.AdvisoryStatusDelivered
			advs[i].DeliveredAt = deliveredAt
		}
	}
	releaseGateWriteJSON(t, w, advs)
}

func releaseGateHandleAdvisoryFeedback(t *testing.T, store central.CentralStore, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/advisories/"), "/feedback")
	var payload struct {
		Status   string `json:"status"`
		Feedback string `json:"feedback"`
		Accepted *bool  `json:"accepted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	status := payload.Status
	if status == "" && payload.Accepted != nil {
		if *payload.Accepted {
			status = central.AdvisoryStatusValidated
		} else {
			status = central.AdvisoryStatusRejected
		}
	}
	if err := store.UpdateAdvisoryStatus(r.Context(), id, central.AdvisoryStatusUpdate{
		Status:      status,
		Feedback:    payload.Feedback,
		ValidatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	releaseGateWriteJSON(t, w, map[string]any{"ok": true, "advisory_id": id, "status": status})
}

func releaseGateHandleListScenarios(t *testing.T, store central.CentralStore, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	q := r.URL.Query()
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	hardware := firstNonEmpty(q.Get("hardware"), q.Get("hardware_profile"))
	scenarios, err := store.ListScenarios(r.Context(), central.ScenarioFilter{
		Name:       q.Get("name"),
		Hardware:   hardware,
		Source:     q.Get("source"),
		AdvisoryID: q.Get("advisory_id"),
		Limit:      limit,
	})
	if err != nil {
		t.Fatalf("ListScenarios: %v", err)
	}
	releaseGateWriteJSON(t, w, scenarios)
}

func releaseGateWriteJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("Encode JSON response: %v", err)
	}
}

func findScenarioByID(t *testing.T, items []map[string]any, id string) map[string]any {
	t.Helper()
	for _, item := range items {
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("scenario %q not found in %#v", id, items)
	return nil
}
