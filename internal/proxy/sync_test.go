package proxy

import "testing"

func TestSyncBackends_Empty(t *testing.T) {
	s := NewServer()
	s.RegisterBackend("old-model", &Backend{ModelName: "old-model", Address: "1.2.3.4:8000", Ready: true})

	SyncBackends(s, nil)

	if len(s.ListBackends()) != 0 {
		t.Errorf("expected 0 backends after empty sync, got %d", len(s.ListBackends()))
	}
}

func TestSyncBackends_ReadyDeployment(t *testing.T) {
	s := NewServer()
	SyncBackends(s, []*DeploymentInfo{
		{
			Name:    "qwen3-8b-vllm",
			Phase:   "running",
			Ready:   true,
			Address: "10.42.0.73:8000",
			Labels:  map[string]string{"aima.dev/model": "qwen3-8b", "aima.dev/engine": "vllm"},
		},
	})

	backends := s.ListBackends()
	b, ok := backends["qwen3-8b"]
	if !ok {
		t.Fatal("expected backend for qwen3-8b")
	}
	if b.Address != "10.42.0.73:8000" {
		t.Errorf("address = %q, want %q", b.Address, "10.42.0.73:8000")
	}
	if !b.Ready {
		t.Error("expected Ready=true")
	}
	if b.EngineType != "vllm" {
		t.Errorf("engine = %q, want %q", b.EngineType, "vllm")
	}
}

func TestSyncBackends_NotReady(t *testing.T) {
	s := NewServer()
	SyncBackends(s, []*DeploymentInfo{
		{
			Name:   "qwen3-8b-vllm",
			Phase:  "pending",
			Ready:  false,
			Labels: map[string]string{"aima.dev/model": "qwen3-8b"},
		},
	})

	backends := s.ListBackends()
	b, ok := backends["qwen3-8b"]
	if !ok {
		t.Fatal("expected backend entry for not-ready deployment")
	}
	if b.Ready {
		t.Error("expected Ready=false")
	}
}

func TestSyncBackends_Removed(t *testing.T) {
	s := NewServer()
	s.RegisterBackend("old-model", &Backend{ModelName: "old-model", Address: "1.2.3.4:8000", Ready: true})
	s.RegisterBackend("keep-model", &Backend{ModelName: "keep-model", Address: "1.2.3.5:8000", Ready: true})

	SyncBackends(s, []*DeploymentInfo{
		{
			Name:    "keep-model-vllm",
			Phase:   "running",
			Ready:   true,
			Address: "1.2.3.5:8000",
			Labels:  map[string]string{"aima.dev/model": "keep-model", "aima.dev/engine": "vllm"},
		},
	})

	backends := s.ListBackends()
	if _, ok := backends["old-model"]; ok {
		t.Error("old-model should have been removed")
	}
	if _, ok := backends["keep-model"]; !ok {
		t.Error("keep-model should still exist")
	}
}

func TestSyncBackends_LabelFallback(t *testing.T) {
	s := NewServer()
	SyncBackends(s, []*DeploymentInfo{
		{
			Name:    "my-deployment",
			Phase:   "running",
			Ready:   true,
			Address: "1.2.3.4:8000",
			Labels:  map[string]string{}, // no aima.dev/model label
		},
	})

	backends := s.ListBackends()
	if _, ok := backends["my-deployment"]; !ok {
		t.Error("expected backend keyed by deployment Name when label is missing")
	}
}
