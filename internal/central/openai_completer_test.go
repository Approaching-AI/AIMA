package central

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompleter(t *testing.T) {
	// Mock OpenAI server
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Fatalf("auth = %q, want Bearer test-api-key", r.Header.Get("Authorization"))
		}

		var req struct {
			Model    string              `json:"model"`
			Messages []map[string]string `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "test-model" {
			t.Fatalf("model = %q, want test-model", req.Model)
		}
		if len(req.Messages) != 2 {
			t.Fatalf("messages = %d, want 2", len(req.Messages))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{
						"content": `{"engine":"vllm","confidence":"high"}`,
					},
				},
			},
		})
	}))
	defer mock.Close()

	completer := NewOpenAICompleter(mock.URL+"/v1", "test-api-key", WithOpenAIModel("test-model"))

	result, err := completer.Complete(context.Background(), "system prompt", "user prompt")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}

	// Verify the response is valid JSON
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if parsed["engine"] != "vllm" {
		t.Fatalf("engine = %v, want vllm", parsed["engine"])
	}
}

func TestOpenAICompleterError(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer mock.Close()

	completer := NewOpenAICompleter(mock.URL+"/v1", "key")
	_, err := completer.Complete(context.Background(), "sys", "user")
	if err == nil {
		t.Fatal("expected error on 429")
	}
}

func TestOpenAICompleterNoChoices(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
	}))
	defer mock.Close()

	completer := NewOpenAICompleter(mock.URL+"/v1", "key")
	_, err := completer.Complete(context.Background(), "sys", "user")
	if err == nil {
		t.Fatal("expected error on empty choices")
	}
}

func TestNormalizeOpenAIBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "already v1", in: "https://api.example.com/v1", want: "https://api.example.com/v1"},
		{name: "coding base", in: "https://api.kimi.com/coding", want: "https://api.kimi.com/coding/v1"},
		{name: "chat completions path", in: "https://api.example.com/v1/chat/completions", want: "https://api.example.com/v1"},
		{name: "trailing slash", in: "https://api.example.com/v1/", want: "https://api.example.com/v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeOpenAIBaseURL(tt.in); got != tt.want {
				t.Fatalf("normalizeOpenAIBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
