package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIClient_TextResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			json.NewEncoder(w).Encode(chatResponse{
				Choices: []chatChoice{
					{Message: chatMessage{Role: "assistant", Content: "Hello!"}},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("test-model"))
	resp, err := client.ChatCompletion(context.Background(), []Message{
		{Role: "user", Content: "Hi"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Errorf("content = %q, want Hello!", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("tool calls = %d, want 0", len(resp.ToolCalls))
	}
}

func TestOpenAIClient_ChatCompletionStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("Decode request: %v", err)
		}
		if stream, _ := req["stream"].(bool); !stream {
			t.Fatalf("stream = %v, want true", req["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer is not flushable")
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("test-model"))
	var deltas []CompletionDelta
	resp, err := client.ChatCompletionStream(context.Background(), []Message{
		{Role: "user", Content: "Hi"},
	}, nil, func(delta CompletionDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	if resp.Content != "Hello" {
		t.Fatalf("content = %q, want Hello", resp.Content)
	}
	if resp.ReasoningContent != "thinking" {
		t.Fatalf("reasoning = %q, want thinking", resp.ReasoningContent)
	}
	if len(deltas) != 3 {
		t.Fatalf("deltas = %d, want 3", len(deltas))
	}
}

func TestOpenAIClient_ChatCompletionStream_ToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer is not flushable")
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"hardware__detect\",\"arguments\":\"\"}}]}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"verbose\\\":\"}}]}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"true}\"}}]}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("test-model"))
	var deltas []CompletionDelta
	resp, err := client.ChatCompletionStream(context.Background(), []Message{
		{Role: "user", Content: "Hi"},
	}, []ToolDefinition{
		{
			Name:        "hardware.detect",
			Description: "detect hardware",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
	}, func(delta CompletionDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "hardware.detect" {
		t.Fatalf("tool name = %q, want hardware.detect", resp.ToolCalls[0].Name)
	}
	if resp.ToolCalls[0].Arguments != `{"verbose":true}` {
		t.Fatalf("tool args = %q", resp.ToolCalls[0].Arguments)
	}
	if len(deltas) != 3 {
		t.Fatalf("deltas = %d, want 3", len(deltas))
	}
	if len(deltas[0].ToolCalls) != 1 {
		t.Fatalf("first delta tool calls = %d, want 1", len(deltas[0].ToolCalls))
	}
	if deltas[0].ToolCalls[0].Name != "hardware.detect" {
		t.Fatalf("first delta tool name = %q, want hardware.detect", deltas[0].ToolCalls[0].Name)
	}
}

func TestOpenAIClient_ToolCallsResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			json.NewEncoder(w).Encode(chatResponse{
				Choices: []chatChoice{
					{Message: chatMessage{
						Role: "assistant",
						ToolCalls: []chatToolCall{
							{
								ID:   "call_1",
								Type: "function",
								Function: chatFunction{
									Name:      "hardware.detect",
									Arguments: `{"verbose":true}`,
								},
							},
							{
								ID:   "call_2",
								Type: "function",
								Function: chatFunction{
									Name:      "model.list",
									Arguments: `{}`,
								},
							},
						},
					}},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("test-model"))
	resp, err := client.ChatCompletion(context.Background(), []Message{
		{Role: "user", Content: "What hardware?"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "hardware.detect" {
		t.Errorf("tool[0].Name = %q, want hardware.detect", resp.ToolCalls[0].Name)
	}
	if resp.ToolCalls[0].ID != "call_1" {
		t.Errorf("tool[0].ID = %q, want call_1", resp.ToolCalls[0].ID)
	}
	if resp.ToolCalls[0].Arguments != `{"verbose":true}` {
		t.Errorf("tool[0].Arguments = %q", resp.ToolCalls[0].Arguments)
	}
	if resp.ToolCalls[1].Name != "model.list" {
		t.Errorf("tool[1].Name = %q, want model.list", resp.ToolCalls[1].Name)
	}
}

func TestOpenAIClient_AuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{
				{Message: chatMessage{Role: "assistant", Content: "ok"}},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("m"), WithAPIKey("sk-test-123"))
	_, err := client.ChatCompletion(context.Background(), []Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if gotAuth != "Bearer sk-test-123" {
		t.Errorf("auth = %q, want Bearer sk-test-123", gotAuth)
	}
}

func TestOpenAIClient_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("m"))
	_, err := client.ChatCompletion(context.Background(), []Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err == nil {
		t.Fatal("expected error for HTTP 429")
	}
}

func TestOpenAIClient_ModelAutoDiscover(t *testing.T) {
	var requestedModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			json.NewEncoder(w).Encode(modelsResponse{
				Data: []modelData{
					{ID: "qwen3-8b"},
					{ID: "qwen3.5-35b-a3b"},
				},
			})
		case "/v1/chat/completions":
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			requestedModel, _ = req["model"].(string)
			json.NewEncoder(w).Encode(chatResponse{
				Choices: []chatChoice{
					{Message: chatMessage{Role: "assistant", Content: "ok"}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL + "/v1") // no WithModel
	_, err := client.ChatCompletion(context.Background(), []Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if requestedModel != "qwen3.5-35b-a3b" {
		t.Errorf("model = %q, want qwen3.5-35b-a3b (strongest available)", requestedModel)
	}
}

func TestOpenAIClient_ToolDefinitionsSent(t *testing.T) {
	var reqBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			json.NewDecoder(r.Body).Decode(&reqBody)
			json.NewEncoder(w).Encode(chatResponse{
				Choices: []chatChoice{
					{Message: chatMessage{Role: "assistant", Content: "ok"}},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("m"))
	tools := []ToolDefinition{
		{Name: "hw.detect", Description: "Detect hardware", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	_, err := client.ChatCompletion(context.Background(), []Message{
		{Role: "user", Content: "test"},
	}, tools)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	var receivedTools []chatTool
	if err := json.Unmarshal(reqBody["tools"], &receivedTools); err != nil {
		t.Fatalf("unmarshal tools: %v", err)
	}
	if len(receivedTools) != 1 {
		t.Fatalf("tools sent = %d, want 1", len(receivedTools))
	}
	// Wire name should be sanitized: "hw.detect" → "hw__detect"
	if receivedTools[0].Function.Name != "hw__detect" {
		t.Errorf("tool name = %q, want hw__detect", receivedTools[0].Function.Name)
	}
}

func TestOpenAIClient_Available(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(modelsResponse{Data: []modelData{{ID: "m"}}})
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL + "/v1")
	if !client.Available(context.Background()) {
		t.Error("Available() = false, want true")
	}

	srv.Close()
	client2 := NewOpenAIClient(srv.URL + "/v1")
	if client2.Available(context.Background()) {
		t.Error("Available() = true after close, want false")
	}
}

func TestOpenAIClient_Available_NoModels_NoDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(modelsResponse{Data: []modelData{}})
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL + "/v1")
	if client.Available(context.Background()) {
		t.Error("Available() = true with empty model list, want false")
	}
}

func TestOpenAIClient_Available_UsesFleetDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(modelsResponse{Data: []modelData{}})
	}))
	defer srv.Close()

	discover := func(ctx context.Context, apiKey string) []FleetEndpoint {
		return []FleetEndpoint{{BaseURL: "http://10.0.0.1:6188/v1", Model: "remote-model"}}
	}

	client := NewOpenAIClient(srv.URL+"/v1", WithDiscoverFunc(discover))
	if !client.Available(context.Background()) {
		t.Error("Available() = false with fleet fallback, want true")
	}
}

func TestOpenAIClient_Available_ConfiguredModelMustExist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(modelsResponse{Data: []modelData{{ID: "other-model"}}})
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("expected-model"))
	if client.Available(context.Background()) {
		t.Error("Available() = true when configured model is missing, want false")
	}
}

func TestOpenAIClient_ConfiguredModelCaseInsensitive(t *testing.T) {
	var requestedModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			json.NewEncoder(w).Encode(modelsResponse{Data: []modelData{{ID: "qwen3.5-35b-a3b"}}})
		case "/v1/chat/completions":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("Decode request: %v", err)
			}
			requestedModel, _ = req["model"].(string)
			json.NewEncoder(w).Encode(chatResponse{
				Choices: []chatChoice{
					{Message: chatMessage{Role: "assistant", Content: "ok"}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("Qwen3.5-35B-A3B"))
	if !client.Available(context.Background()) {
		t.Fatal("Available() = false, want true for case-insensitive model match")
	}
	if _, err := client.ChatCompletion(context.Background(), []Message{{Role: "user", Content: "test"}}, nil); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if requestedModel != "qwen3.5-35b-a3b" {
		t.Fatalf("requested model = %q, want qwen3.5-35b-a3b", requestedModel)
	}
}

func TestOpenAIClient_FleetDiscovery_EmptyModels(t *testing.T) {
	// Local server returns empty model list
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(modelsResponse{Data: []modelData{}})
	}))
	defer srv.Close()

	// Fleet discover returns a remote endpoint
	discoverCalled := false
	discover := func(ctx context.Context, apiKey string) []FleetEndpoint {
		discoverCalled = true
		return []FleetEndpoint{{BaseURL: srv.URL + "/v1", Model: "remote-qwen3"}}
	}

	client := NewOpenAIClient(srv.URL+"/v1", WithDiscoverFunc(discover))
	model, err := client.resolveModel(context.Background())
	if err != nil {
		t.Fatalf("resolveModel: %v", err)
	}
	if !discoverCalled {
		t.Error("expected discover function to be called")
	}
	if model != "remote-qwen3" {
		t.Errorf("model = %q, want remote-qwen3", model)
	}
}

func TestOpenAIClient_FleetDiscovery_Unreachable(t *testing.T) {
	// Fleet discover returns a remote endpoint when local is unreachable
	discover := func(ctx context.Context, apiKey string) []FleetEndpoint {
		return []FleetEndpoint{{BaseURL: "http://10.0.0.1:6188/v1", Model: "remote-model"}}
	}

	client := NewOpenAIClient("http://127.0.0.1:1/v1", WithDiscoverFunc(discover))
	model, err := client.resolveModel(context.Background())
	if err != nil {
		t.Fatalf("resolveModel: %v", err)
	}
	if model != "remote-model" {
		t.Errorf("model = %q, want remote-model", model)
	}
}

func TestOpenAIClient_FleetFallbackDoesNotRewriteConfiguredEndpoint(t *testing.T) {
	localReady := false
	localCalls := 0
	remoteCalls := 0

	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			models := []map[string]any{}
			if localReady {
				models = append(models, map[string]any{
					"model_name":            "local-qwen3.5-35b-a3b",
					"ready":                 true,
					"remote":                false,
					"parameter_count":       "35B",
					"context_window_tokens": 16384,
				})
			}
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "models": models})
		case "/v1/models":
			if !localReady {
				json.NewEncoder(w).Encode(modelsResponse{Data: []modelData{}})
				return
			}
			json.NewEncoder(w).Encode(modelsResponse{Data: []modelData{{ID: "local-qwen3.5-35b-a3b"}}})
		case "/v1/chat/completions":
			localCalls++
			json.NewEncoder(w).Encode(chatResponse{
				Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "local"}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer local.Close()

	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		remoteCalls++
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "remote"}}},
		})
	}))
	defer remote.Close()

	client := NewOpenAIClient(local.URL+"/v1", WithDiscoverFunc(func(ctx context.Context, apiKey string) []FleetEndpoint {
		return []FleetEndpoint{{BaseURL: remote.URL + "/v1", Model: "remote-model"}}
	}))

	resp, err := client.ChatCompletion(context.Background(), []Message{{Role: "user", Content: "test"}}, nil)
	if err != nil {
		t.Fatalf("first ChatCompletion: %v", err)
	}
	if resp.Content != "remote" {
		t.Fatalf("first response = %q, want remote", resp.Content)
	}
	if client.Endpoint() != local.URL+"/v1" {
		t.Fatalf("Endpoint() = %q, want configured local endpoint", client.Endpoint())
	}

	client.invalidateTargetCache()
	localReady = true

	resp, err = client.ChatCompletion(context.Background(), []Message{{Role: "user", Content: "test again"}}, nil)
	if err != nil {
		t.Fatalf("second ChatCompletion: %v", err)
	}
	if resp.Content != "local" {
		t.Fatalf("second response = %q, want local", resp.Content)
	}
	if localCalls != 1 {
		t.Fatalf("local chat calls = %d, want 1", localCalls)
	}
	if remoteCalls != 1 {
		t.Fatalf("remote chat calls = %d, want 1", remoteCalls)
	}
}

func TestOpenAIClient_RouteStatus_SelectsBestLocalModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"models": []map[string]any{
				{
					"model_name":            "qwen3-8b",
					"ready":                 true,
					"parameter_count":       "8B",
					"context_window_tokens": 8192,
				},
				{
					"model_name":            "qwen3.5-35b-a3b",
					"ready":                 true,
					"parameter_count":       "35B",
					"context_window_tokens": 16384,
				},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL + "/v1")
	status := client.RouteStatus(context.Background())
	if !status.Available {
		t.Fatal("RouteStatus().Available = false, want true")
	}
	if status.SelectionReason != "best_local_model" {
		t.Fatalf("SelectionReason = %q, want best_local_model", status.SelectionReason)
	}
	if status.Selected == nil || status.Selected.Model != "qwen3.5-35b-a3b" {
		t.Fatalf("Selected = %+v, want qwen3.5-35b-a3b", status.Selected)
	}
	if len(status.ConfiguredEndpointProbe.Models) != 2 {
		t.Fatalf("probe models = %d, want 2", len(status.ConfiguredEndpointProbe.Models))
	}
	if status.ConfiguredEndpointProbe.Models[0].Model != "qwen3.5-35b-a3b" {
		t.Fatalf("first probe model = %q, want qwen3.5-35b-a3b", status.ConfiguredEndpointProbe.Models[0].Model)
	}
}

func TestOpenAIClient_RouteStatus_FallsBackWhenConfiguredModelUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"models": []map[string]any{
				{
					"model_name":            "qwen3.6-35b-a3b",
					"ready":                 true,
					"parameter_count":       "35B",
					"context_window_tokens": 131072,
				},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("qwen3.5-35b-a3b"))
	status := client.RouteStatus(context.Background())
	if !status.Available {
		t.Fatal("RouteStatus().Available = false, want true")
	}
	if status.SelectionReason != "configured_model_unavailable_local_fallback" {
		t.Fatalf("SelectionReason = %q, want configured_model_unavailable_local_fallback", status.SelectionReason)
	}
	if status.Selected == nil || status.Selected.Model != "qwen3.6-35b-a3b" {
		t.Fatalf("Selected = %+v, want qwen3.6-35b-a3b", status.Selected)
	}
	if status.ConfiguredModel != "qwen3.5-35b-a3b" {
		t.Fatalf("ConfiguredModel = %q, want stale configured model", status.ConfiguredModel)
	}
}

func TestOpenAIClient_RouteStatus_UsesFleetFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"models": []map[string]any{},
		})
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithDiscoverFunc(func(ctx context.Context, apiKey string) []FleetEndpoint {
		return []FleetEndpoint{
			{BaseURL: "http://10.0.0.2:6188/v1", Model: "qwen3-8b", ParameterCount: "8B", ContextWindowTokens: 8192},
			{BaseURL: "http://10.0.0.3:6188/v1", Model: "qwen3.5-35b-a3b", ParameterCount: "35B", ContextWindowTokens: 16384},
		}
	}))

	status := client.RouteStatus(context.Background())
	if !status.Available {
		t.Fatal("RouteStatus().Available = false, want true")
	}
	if status.SelectionReason != "fleet_fallback" {
		t.Fatalf("SelectionReason = %q, want fleet_fallback", status.SelectionReason)
	}
	if status.Selected == nil || status.Selected.Model != "qwen3.5-35b-a3b" {
		t.Fatalf("Selected = %+v, want qwen3.5-35b-a3b", status.Selected)
	}
	if len(status.FleetCandidates) != 2 {
		t.Fatalf("fleet candidates = %d, want 2", len(status.FleetCandidates))
	}
	if status.FleetCandidates[0].Model != "qwen3.5-35b-a3b" {
		t.Fatalf("first fleet candidate = %q, want qwen3.5-35b-a3b", status.FleetCandidates[0].Model)
	}
}

func TestOpenAIClient_RouteStatus_ConfiguredModelWithoutDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			json.NewEncoder(w).Encode(chatResponse{
				Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "ok"}}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("kimi-k2"))
	status := client.RouteStatus(context.Background())
	if !status.Available {
		t.Fatal("RouteStatus().Available = false, want true")
	}
	if status.SelectionReason != "configured_model_endpoint_unverified" {
		t.Fatalf("SelectionReason = %q, want configured_model_endpoint_unverified", status.SelectionReason)
	}
	if status.Selected == nil || status.Selected.Model != "kimi-k2" {
		t.Fatalf("Selected = %+v, want kimi-k2", status.Selected)
	}
	if status.ConfiguredEndpointProbe.Error == "" {
		t.Fatal("configured endpoint probe error = empty, want discovery failure detail")
	}
}

func TestOpenAIClient_FleetDiscovery_NilFunc(t *testing.T) {
	// No discover function — original error propagated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(modelsResponse{Data: []modelData{}})
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL + "/v1") // no WithDiscoverFunc
	_, err := client.resolveModel(context.Background())
	if err == nil {
		t.Fatal("expected error when no models and no discover func")
	}
}

func TestOpenAIClient_FleetDiscovery_NoEndpoints(t *testing.T) {
	// Discover function returns empty — original error propagated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(modelsResponse{Data: []modelData{}})
	}))
	defer srv.Close()

	discover := func(ctx context.Context, apiKey string) []FleetEndpoint {
		return nil
	}

	client := NewOpenAIClient(srv.URL+"/v1", WithDiscoverFunc(discover))
	_, err := client.resolveModel(context.Background())
	if err == nil {
		t.Fatal("expected error when discover returns no endpoints")
	}
}

func TestOpenAIClient_ReasoningContentPreserved(t *testing.T) {
	var reqBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&reqBody)
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{
				{Message: chatMessage{Role: "assistant", Content: "result", ReasoningContent: "thought about it"}},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("m"))
	resp, err := client.ChatCompletion(context.Background(), []Message{
		{Role: "user", Content: "test"},
		{Role: "assistant", Content: "", ReasoningContent: "let me think"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	// Verify reasoning_content was sent in request
	var msgs []map[string]any
	if err := json.Unmarshal(reqBody["messages"], &msgs); err != nil {
		t.Fatalf("unmarshal messages: %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	rc, _ := msgs[1]["reasoning_content"].(string)
	if rc != "let me think" {
		t.Errorf("request reasoning_content = %q, want %q", rc, "let me think")
	}

	// Verify reasoning_content was parsed from response
	if resp.ReasoningContent != "thought about it" {
		t.Errorf("response ReasoningContent = %q, want %q", resp.ReasoningContent, "thought about it")
	}
}

func TestOpenAIClient_ExtraParams(t *testing.T) {
	var reqBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&reqBody)
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{
				{Message: chatMessage{Role: "assistant", Content: "ok"}},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("m"), WithExtraParams(map[string]any{
		"temperature": 0.6,
		"top_p":       0.95,
	}))
	_, err := client.ChatCompletion(context.Background(), []Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if temp, ok := reqBody["temperature"].(float64); !ok || temp != 0.6 {
		t.Errorf("temperature = %v, want 0.6", reqBody["temperature"])
	}
	if topP, ok := reqBody["top_p"].(float64); !ok || topP != 0.95 {
		t.Errorf("top_p = %v, want 0.95", reqBody["top_p"])
	}
	// model/messages must not be overridden by extra params
	if reqBody["model"] != "m" {
		t.Errorf("model = %v, want m", reqBody["model"])
	}
}

func TestOpenAIClient_ContentAlwaysPresent(t *testing.T) {
	var reqBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&reqBody)
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{
				{Message: chatMessage{Role: "assistant", Content: "ok"}},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("m"))
	// Send assistant message with empty Content (should still serialize as "content":"")
	_, err := client.ChatCompletion(context.Background(), []Message{
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "c1", Name: "hw.detect", Arguments: "{}"}}},
		{Role: "tool", Content: `{"gpu":"RTX 4060"}`, ToolCallID: "c1"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	// Verify "content" field is present even when empty
	raw := string(reqBody["messages"])
	if !strings.Contains(raw, `"content":""`) {
		t.Errorf("expected empty content field to be present in JSON, got: %s", raw)
	}
}

func TestOpenAIClient_DefaultTimeoutUsesLoopbackPolicy(t *testing.T) {
	local := NewOpenAIClient("http://127.0.0.1:6188/v1")
	if local.httpClient.Timeout != 30*time.Minute {
		t.Fatalf("local timeout = %v, want %v", local.httpClient.Timeout, 30*time.Minute)
	}
	remote := NewOpenAIClient("https://api.openai.com/v1")
	if remote.httpClient.Timeout != 5*time.Minute {
		t.Fatalf("remote timeout = %v, want %v", remote.httpClient.Timeout, 5*time.Minute)
	}
}

func TestOpenAIClient_ContextPreflightBlocksKnownOverflow(t *testing.T) {
	chatCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"models": []map[string]any{{
					"model_name":            "tiny-model",
					"context_window_tokens": 256,
				}},
			})
		case "/v1/chat/completions":
			chatCalled = true
			json.NewEncoder(w).Encode(chatResponse{
				Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "ok"}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("tiny-model"))
	_, err := client.ChatCompletion(context.Background(), []Message{
		{Role: "system", Content: strings.Repeat("system prompt ", 200)},
		{Role: "user", Content: strings.Repeat("user prompt ", 200)},
	}, []ToolDefinition{
		{Name: "deploy.apply", Description: strings.Repeat("x", 512), InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)},
	})
	if err == nil {
		t.Fatal("expected context preflight error")
	}
	if !strings.Contains(err.Error(), "preflight") || !strings.Contains(err.Error(), "context window 256") {
		t.Fatalf("error = %q, want context preflight message", err)
	}
	if chatCalled {
		t.Fatal("chat endpoint should not be called when preflight fails")
	}
}

func TestOpenAIClient_ContextPreflightIgnoredForNonAIMAEndpoint(t *testing.T) {
	chatCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			http.NotFound(w, r)
		case "/v1/chat/completions":
			chatCalled = true
			json.NewEncoder(w).Encode(chatResponse{
				Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "ok"}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewOpenAIClient(srv.URL+"/v1", WithModel("m"))
	_, err := client.ChatCompletion(context.Background(), []Message{
		{Role: "user", Content: "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if !chatCalled {
		t.Fatal("expected chat endpoint to be called")
	}
}
