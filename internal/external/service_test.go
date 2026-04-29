package external

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeOpenAIModelsEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "whisper-large-v3-hf"},
				{"id": "qwen3-asr"},
			},
		})
	}))
	defer server.Close()

	svc, err := Probe(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if svc.BaseURL != server.URL {
		t.Fatalf("BaseURL = %q, want %q", svc.BaseURL, server.URL)
	}
	if svc.Kind != "openai" {
		t.Fatalf("Kind = %q, want openai", svc.Kind)
	}
	if svc.Status != "reachable" {
		t.Fatalf("Status = %q, want reachable", svc.Status)
	}
	if got := svc.Models; len(got) != 2 || got[0] != "whisper-large-v3-hf" || got[1] != "qwen3-asr" {
		t.Fatalf("Models = %#v, want whisper-large-v3-hf and qwen3-asr", got)
	}
}

func TestProbeHealthzServiceWithModels(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			http.NotFound(w, r)
		case "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"models": []map[string]any{
					{"model_name": "SenseVoiceSmall"},
					{"name": "pyannote"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc, err := Probe(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if svc.Kind != "healthz" {
		t.Fatalf("Kind = %q, want healthz", svc.Kind)
	}
	if got := svc.Models; len(got) != 2 || got[0] != "SenseVoiceSmall" || got[1] != "pyannote" {
		t.Fatalf("Models = %#v, want SenseVoiceSmall and pyannote", got)
	}
}

func TestScanContinuesAfterUnreachableEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "reachable-model"}},
		})
	}))
	defer server.Close()

	services, err := Scan(context.Background(), ScanOptions{
		Endpoints: []string{"http://127.0.0.1:1", server.URL},
		Client:    server.Client(),
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("len(services) = %d, want 1", len(services))
	}
	if got := services[0].Models; len(got) != 1 || got[0] != "reachable-model" {
		t.Fatalf("Models = %#v, want reachable-model", got)
	}
}
