package main

import (
	"reflect"
	"testing"
)

func TestLoadOnboardingFirstRunPolicyFromCatalog(t *testing.T) {
	policy := loadOnboardingFirstRunPolicy()
	if policy == nil {
		t.Fatal("loadOnboardingFirstRunPolicy returned nil")
	}
	guardrail := policy.NativeGuardrail
	if guardrail.WildcardGPUArch != "*" {
		t.Fatalf("wildcard_gpu_arch = %q, want *", guardrail.WildcardGPUArch)
	}
	if guardrail.SkipDiscreteAccelerators == nil || !*guardrail.SkipDiscreteAccelerators {
		t.Fatalf("skip_discrete_accelerators = %#v, want true", guardrail.SkipDiscreteAccelerators)
	}
	if len(guardrail.RAMUtilizationPenalties) == 0 {
		t.Fatal("ram_utilization_penalties is empty")
	}
	if len(guardrail.ParameterCountPenalties) == 0 {
		t.Fatal("parameter_count_penalties is empty")
	}
	wantModels := []string{"qwen3-8b", "qwen3-emb-0.6b", "funasr-paraformer-onnx", "litetts-mnn"}
	gotModels := policy.RecommendationAllowlists["moore-threads-m1000-soc-arm64"]
	if !reflect.DeepEqual(gotModels, wantModels) {
		t.Fatalf("AIBook recommendation allowlist = %#v, want %#v", gotModels, wantModels)
	}
}
