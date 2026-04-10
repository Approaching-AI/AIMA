package central

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func setupTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv, err := New(Config{
		APIKey: "test-key",
		DBPath: filepath.Join(t.TempDir(), "handler_test.db"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	httpSrv := httptest.NewServer(srv.mux)
	t.Cleanup(httpSrv.Close)

	return srv, httpSrv
}

func doRequest(t *testing.T, method, url string, body any, apiKey string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, &buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func TestHandleAdviseRecommend(t *testing.T) {
	srv, httpSrv := setupTestServer(t)

	llm := &mockLLM{response: `{
		"engine": "vllm",
		"config": {"gmu": 0.9},
		"reasoning": "best for this GPU",
		"confidence": "high"
	}`}
	advisor := NewAdvisor(srv.Store(), llm)
	srv.SetAdvisor(advisor)

	resp := doRequest(t, "POST", httpSrv.URL+"/api/v1/advise", map[string]any{
		"action":   "recommend",
		"hardware": "nvidia-rtx4090",
		"model":    "qwen3-8b",
	}, "test-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["recommendation"] == nil {
		t.Fatal("expected recommendation in response")
	}
	if result["advisory"] == nil {
		t.Fatal("expected advisory in response")
	}
}

func TestHandleAdviseOptimize(t *testing.T) {
	srv, httpSrv := setupTestServer(t)

	llm := &mockLLM{response: `{
		"optimizations": [{"type": "parameter"}],
		"reasoning": "increase batch",
		"confidence": "medium"
	}`}
	advisor := NewAdvisor(srv.Store(), llm)
	srv.SetAdvisor(advisor)

	resp := doRequest(t, "POST", httpSrv.URL+"/api/v1/advise", map[string]any{
		"action":   "optimize",
		"hardware": "nvidia-rtx4090",
		"model":    "qwen3-8b",
		"engine":   "vllm",
	}, "test-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHandleAdviseNoAdvisor(t *testing.T) {
	_, httpSrv := setupTestServer(t)

	resp := doRequest(t, "POST", httpSrv.URL+"/api/v1/advise", map[string]any{
		"action": "recommend",
	}, "test-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestHandleListAdvisories(t *testing.T) {
	srv, httpSrv := setupTestServer(t)

	// Seed an advisory
	_ = srv.Store().InsertAdvisory(context.Background(), Advisory{
		ID:             "adv-h1",
		Type:           AdvisoryTypeConfigRecommend,
		Status:         AdvisoryStatusPending,
		Severity:       "info",
		TargetHardware: "nvidia-rtx4090",
		Title:          "Test",
		ContentJSON:    []byte(`{"engine":"vllm"}`),
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	})

	resp := doRequest(t, "GET", httpSrv.URL+"/api/v1/advisories?type=config_recommend&status=pending", nil, "test-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var advs []Advisory
	if err := json.NewDecoder(resp.Body).Decode(&advs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(advs) != 1 {
		t.Fatalf("advisories = %d, want 1", len(advs))
	}
	if advs[0].Status != AdvisoryStatusDelivered {
		t.Fatalf("status = %q, want delivered after pull", advs[0].Status)
	}
}

func TestHandleAdvisoryFeedback(t *testing.T) {
	srv, httpSrv := setupTestServer(t)

	_ = srv.Store().InsertAdvisory(context.Background(), Advisory{
		ID:             "adv-fb1",
		Type:           AdvisoryTypeConfigRecommend,
		Status:         AdvisoryStatusDelivered,
		Severity:       "info",
		TargetHardware: "nvidia-rtx4090",
		Title:          "Test",
		ContentJSON:    []byte(`{"engine":"vllm"}`),
		CreatedAt:      "2026-01-01T00:00:00Z",
	})

	resp := doRequest(t, "POST", httpSrv.URL+"/api/v1/advisories/adv-fb1/feedback", map[string]any{
		"status":   "validated",
		"feedback": "very helpful",
	}, "test-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Verify feedback stored
	advs, _ := srv.Store().ListAdvisories(context.Background(), AdvisoryFilter{})
	found := false
	for _, a := range advs {
		if a.ID == "adv-fb1" && a.Feedback == "very helpful" && a.Accepted && a.Status == AdvisoryStatusValidated {
			found = true
		}
	}
	if !found {
		t.Fatal("feedback not stored")
	}
}

func TestHandleAdvisoryFeedbackNotFound(t *testing.T) {
	_, httpSrv := setupTestServer(t)

	resp := doRequest(t, "POST", httpSrv.URL+"/api/v1/advisory/feedback", map[string]any{
		"advisory_id": "nonexistent",
		"status":      "rejected",
		"feedback":    "test",
	}, "test-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandleScenarioGenerate(t *testing.T) {
	srv, httpSrv := setupTestServer(t)

	llm := &mockLLM{response: `{
		"name": "test-scenario",
		"description": "test",
		"deployments": [],
		"total_vram_mib": 24576,
		"reasoning": "test",
		"confidence": "high"
	}`}
	advisor := NewAdvisor(srv.Store(), llm)
	srv.SetAdvisor(advisor)

	resp := doRequest(t, "POST", httpSrv.URL+"/api/v1/scenarios/generate", map[string]any{
		"hardware": "nvidia-rtx4090",
		"models":   []string{"qwen3-8b", "llama3-8b"},
	}, "test-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHandleListScenarios(t *testing.T) {
	srv, httpSrv := setupTestServer(t)

	_ = srv.Store().InsertScenario(context.Background(), Scenario{
		ID:              "scn-h1",
		Name:            "test",
		HardwareProfile: "nvidia-rtx4090",
		ScenarioYAML:    "deployments: []",
		Source:          "generated",
		Version:         1,
		CreatedAt:       "2026-01-01T00:00:00Z",
		UpdatedAt:       "2026-01-01T00:00:00Z",
	})

	resp := doRequest(t, "GET", httpSrv.URL+"/api/v1/scenarios?hardware=nvidia-rtx4090&source=generated", nil, "test-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var scenarios []Scenario
	if err := json.NewDecoder(resp.Body).Decode(&scenarios); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("scenarios = %d, want 1", len(scenarios))
	}
}

func TestHandleListAnalysis(t *testing.T) {
	srv, httpSrv := setupTestServer(t)

	_ = srv.Store().InsertAnalysisRun(context.Background(), AnalysisRun{
		ID:        "run-h1",
		Type:      AnalysisTypeGapScan,
		Status:    AnalysisStatusCompleted,
		Summary:   "test",
		StartedAt: "2026-01-01T00:00:00Z",
		CreatedAt: "2026-01-01T00:00:00Z",
	})

	resp := doRequest(t, "GET", httpSrv.URL+"/api/v1/analysis", nil, "test-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var runs []AnalysisRun
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
}

func TestHandleAdviseUnknownAction(t *testing.T) {
	srv, httpSrv := setupTestServer(t)

	llm := &mockLLM{response: `{}`}
	advisor := NewAdvisor(srv.Store(), llm)
	srv.SetAdvisor(advisor)

	resp := doRequest(t, "POST", httpSrv.URL+"/api/v1/advise", map[string]any{
		"action": "unknown",
	}, "test-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
