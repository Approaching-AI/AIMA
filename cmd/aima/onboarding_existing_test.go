package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
)

func TestHandleOnboardingUseExistingSetsLLMConfig(t *testing.T) {
	proxyServer := proxy.NewServer()
	proxyServer.RegisterBackend("qwen3.6-35b-a3b-bf16", &proxy.Backend{
		ModelName:     "qwen3.6-35b-a3b-bf16",
		EngineType:    "vllm",
		Address:       "127.0.0.1:18310",
		BasePath:      "/v1",
		Ready:         true,
		UpstreamModel: "qwen3.6-35b-a3b-bf16",
	})

	config := map[string]string{}
	handler := handleOnboardingUseExisting(&appContext{proxy: proxyServer}, &mcp.ToolDeps{
		SetConfig: func(ctx context.Context, key, value string) error {
			config[key] = value
			return nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-use-existing", strings.NewReader(`{"model":"qwen3.6-35b-a3b-bf16"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := config["llm.endpoint"]; got != "http://localhost:6188/v1" {
		t.Fatalf("llm.endpoint = %q", got)
	}
	if got := config["llm.model"]; got != "qwen3.6-35b-a3b-bf16" {
		t.Fatalf("llm.model = %q", got)
	}
	if got := config["onboarding_completed"]; got != "true" {
		t.Fatalf("onboarding_completed = %q", got)
	}
}

func TestHandleOnboardingUseExistingRejectsExplicitUnreadyBackend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ready   bool
		address string
	}{
		{name: "not ready", ready: false, address: "127.0.0.1:18310"},
		{name: "missing address", ready: true, address: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxyServer := proxy.NewServer()
			proxyServer.RegisterBackend("qwen3.6-35b-a3b-bf16", &proxy.Backend{
				ModelName:  "qwen3.6-35b-a3b-bf16",
				EngineType: "vllm",
				Address:    tc.address,
				BasePath:   "/v1",
				Ready:      tc.ready,
			})

			config := map[string]string{}
			handler := handleOnboardingUseExisting(&appContext{proxy: proxyServer}, &mcp.ToolDeps{
				SetConfig: func(ctx context.Context, key, value string) error {
					config[key] = value
					return nil
				},
			})

			req := httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-use-existing", strings.NewReader(`{"model":"qwen3.6-35b-a3b-bf16"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusConflict, rec.Body.String())
			}
			if len(config) != 0 {
				t.Fatalf("config should not be written for unready backend: %#v", config)
			}
		})
	}
}

func TestBuildOnboardingDepsRunningServicesSeparatesProxyEndpointFromBackendEndpoint(t *testing.T) {
	proxyServer := proxy.NewServer()
	proxyServer.RegisterBackend("qwen3.6-35b-a3b-bf16", &proxy.Backend{
		ModelName:  "qwen3.6-35b-a3b-bf16",
		EngineType: "vllm",
		Address:    "127.0.0.1:18310",
		BasePath:   "/v1",
		Ready:      true,
	})

	deps := buildOnboardingDepsStruct(&appContext{proxy: proxyServer}, nil)
	services := deps.ListRunningServices(context.Background())
	if len(services) != 1 {
		t.Fatalf("len(services) = %d, want 1", len(services))
	}
	if got := services[0].Endpoint; got != "http://localhost:6188/v1" {
		t.Fatalf("Endpoint = %q, want AIMA proxy endpoint", got)
	}
	if got := services[0].BackendEndpoint; got != "http://127.0.0.1:18310/v1" {
		t.Fatalf("BackendEndpoint = %q, want backend direct endpoint", got)
	}
}
