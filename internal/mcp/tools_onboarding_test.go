package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOnboardingStartActionDispatchesThroughToolDeps(t *testing.T) {
	s := NewServer()

	var gotLocale string
	registerOnboardingTools(s, &ToolDeps{
		OnboardingStart: func(ctx context.Context, locale string) (json.RawMessage, error) {
			gotLocale = locale
			return json.RawMessage(`{"next_command":"aima run qwen3-4b"}`), nil
		},
	})

	result, err := s.ExecuteTool(context.Background(), "onboarding", json.RawMessage(`{"action":"start","locale":"zh"}`))
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %+v", result)
	}
	if gotLocale != "zh" {
		t.Fatalf("locale = %q, want zh", gotLocale)
	}
	if len(result.Content) != 1 || result.Content[0].Text != `{"next_command":"aima run qwen3-4b"}` {
		t.Fatalf("unexpected result: %+v", result)
	}
}
