package openclaw

import "testing"

// Item 4: SkipDefaultModel registers the provider+models but leaves OpenClaw's
// primary chat model untouched.
func TestMergeSkipDefaultModel(t *testing.T) {
	build := func(skip bool) map[string]any {
		result := &SyncResult{
			LLMModels: []ModelEntry{{
				ID: "qwen3-8b", Name: "Qwen3 8B", Input: []string{"text"},
				ContextWindow: 32768, MaxTokens: 16384,
			}},
			ProxyAddr:        "http://127.0.0.1:6188/v1",
			SkipDefaultModel: skip,
		}
		cfg, _ := MergeAIMAConfigWithState(map[string]any{}, nil, result)
		return cfg
	}

	// Default: provider registered AND primary set to the AIMA model.
	def := build(false)
	if lookupMap(def, "models", "providers", aimaLLMProviderID) == nil {
		t.Fatal("default: aima provider should be registered")
	}
	if !hasAgentDefaultModel(def, "model") {
		t.Error("default: expected agents.defaults.model to be set")
	}

	// Skip: provider STILL registered, but the primary is NOT set.
	sk := build(true)
	if lookupMap(sk, "models", "providers", aimaLLMProviderID) == nil {
		t.Error("skip: aima provider should still be registered")
	}
	if hasAgentDefaultModel(sk, "model") {
		t.Error("skip: agents.defaults.model must be left untouched")
	}
}
