package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
)

func TestBuildOnboardingStatusJSONIncludesRunningProxyBackends(t *testing.T) {
	proxyServer := proxy.NewServer()
	proxyServer.RegisterBackend("qwen3.6-35b-a3b-bf16", &proxy.Backend{
		ModelName:           "qwen3.6-35b-a3b-bf16",
		UpstreamModel:       "qwen3.6-35b-a3b-bf16",
		EngineType:          "vllm",
		Address:             "127.0.0.1:18310",
		BasePath:            "/v1",
		Ready:               true,
		ParameterCount:      "35B",
		ContextWindowTokens: 32768,
	})
	ac := &appContext{proxy: proxyServer}

	raw, err := buildOnboardingStatusJSON(context.Background(), ac, &mcp.ToolDeps{})
	if err != nil {
		t.Fatalf("buildOnboardingStatusJSON: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}

	services, ok := body["running_services"].([]any)
	if !ok || len(services) != 1 {
		t.Fatalf("running_services = %#v, want one proxy backend", body["running_services"])
	}
	svc := services[0].(map[string]any)
	if got := svc["model"]; got != "qwen3.6-35b-a3b-bf16" {
		t.Fatalf("service model = %#v", got)
	}
	if got := svc["engine"]; got != "vllm" {
		t.Fatalf("service engine = %#v", got)
	}
	if got := svc["endpoint"]; got != "http://localhost:6188/v1" {
		t.Fatalf("service endpoint = %#v", got)
	}
	if got := svc["backend_endpoint"]; got != "http://127.0.0.1:18310/v1" {
		t.Fatalf("service backend_endpoint = %#v", got)
	}

	best := body["best_choice"].(map[string]any)
	if got := best["action"]; got != "use_existing" {
		t.Fatalf("best choice action = %#v", got)
	}
	if got := best["model"]; got != "qwen3.6-35b-a3b-bf16" {
		t.Fatalf("best choice model = %#v", got)
	}
}
