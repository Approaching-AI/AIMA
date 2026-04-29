package main

import (
	"context"
	"testing"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/proxy"
)

func TestRegisterExternalServiceBackends(t *testing.T) {
	proxyServer := proxy.NewServer()
	service := externalServiceOverview{
		BaseURL: "http://127.0.0.1:8004",
		Kind:    "openai",
		Models:  []string{"whisper-large-v3-hf"},
	}

	imported, err := registerExternalServiceBackends(proxyServer, service, nil)
	if err != nil {
		t.Fatalf("registerExternalServiceBackends: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1", imported)
	}

	backends := proxyServer.ListBackends()
	backend := backends["whisper-large-v3-hf"]
	if backend == nil {
		t.Fatal("backend for whisper-large-v3-hf missing")
	}
	if !backend.External {
		t.Fatal("External = false, want true")
	}
	if backend.Address != "127.0.0.1:8004" {
		t.Fatalf("Address = %q, want 127.0.0.1:8004", backend.Address)
	}
	if backend.BasePath != "" {
		t.Fatalf("BasePath = %q, want empty", backend.BasePath)
	}
	if backend.UpstreamModel != "whisper-large-v3-hf" {
		t.Fatalf("UpstreamModel = %q, want whisper-large-v3-hf", backend.UpstreamModel)
	}
}

func TestRegisterExternalServiceBackendsImportsHealthzASRService(t *testing.T) {
	proxyServer := proxy.NewServer()
	service := externalServiceOverview{
		BaseURL: "http://127.0.0.1:8009",
		Kind:    "healthz",
		Models:  []string{"SenseVoiceSmall", "pyannote"},
	}

	imported, err := registerExternalServiceBackends(proxyServer, service, []string{"local-stt"})
	if err != nil {
		t.Fatalf("registerExternalServiceBackends: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1", imported)
	}
	backend := proxyServer.ListBackends()["local-stt"]
	if backend == nil {
		t.Fatal("local-stt backend missing")
	}
	if backend.EngineType != "external-asr" {
		t.Fatalf("EngineType = %q, want external-asr", backend.EngineType)
	}
	if got := backend.PathOverrides["/v1/audio/transcriptions"]; got != "/v1/asr" {
		t.Fatalf("transcriptions override = %q, want /v1/asr", got)
	}
}

func TestRestoreImportedExternalServicesOnlyRestoresReachableScanResults(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, svc := range []*state.ExternalService{
		{
			ID:         "external-reachable",
			BaseURL:    "http://127.0.0.1:8004",
			Kind:       "openai",
			Status:     "reachable",
			Source:     "scan",
			ModelsJSON: `["reachable-model"]`,
		},
		{
			ID:         "external-stale",
			BaseURL:    "http://127.0.0.1:8011",
			Kind:       "openai",
			Status:     "reachable",
			Source:     "scan",
			ModelsJSON: `["stale-model"]`,
		},
	} {
		if err := db.UpsertExternalService(ctx, svc); err != nil {
			t.Fatalf("UpsertExternalService: %v", err)
		}
		if err := db.SetExternalServiceImported(ctx, svc.BaseURL, true); err != nil {
			t.Fatalf("SetExternalServiceImported: %v", err)
		}
	}

	proxyServer := proxy.NewServer()
	err = restoreImportedExternalServices(ctx, db, proxyServer, map[string]struct{}{
		"http://127.0.0.1:8004": {},
	})
	if err != nil {
		t.Fatalf("restoreImportedExternalServices: %v", err)
	}

	backends := proxyServer.ListBackends()
	if backends["reachable-model"] == nil {
		t.Fatal("reachable-model backend missing")
	}
	if backends["stale-model"] != nil {
		t.Fatal("stale-model backend should not be restored")
	}
}
