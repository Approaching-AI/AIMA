package onboarding

import "testing"

func TestParseFirstRunPolicyYAMLAllowsBooleanOverrides(t *testing.T) {
	policy, err := ParseFirstRunPolicyYAML([]byte(`
kind: onboarding_policy
first_run:
  default_modality_score: 2
  modality_scores:
    llm: 30
  native_guardrail:
    wildcard_gpu_arch: "*"
    skip_discrete_accelerators: false
    max_penalty: 7
    ram_utilization_penalties:
      - above: 0.5
        penalty: 3
`))
	if err != nil {
		t.Fatalf("ParseFirstRunPolicyYAML: %v", err)
	}
	if policy.NativeGuardrail.SkipDiscreteAccelerators == nil {
		t.Fatal("skip_discrete_accelerators pointer is nil")
	}
	if *policy.NativeGuardrail.SkipDiscreteAccelerators {
		t.Fatal("skip_discrete_accelerators = true, want false")
	}
	if policy.NativeGuardrail.MaxPenalty != 7 {
		t.Fatalf("max_penalty = %d, want 7", policy.NativeGuardrail.MaxPenalty)
	}
}

func TestParseFirstRunPolicyYAMLRejectsMissingPolicyData(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "missing first_run",
			raw:  `kind: onboarding_policy`,
		},
		{
			name: "wrong kind",
			raw: `
kind: model_asset
first_run:
  default_modality_score: 2
  modality_scores:
    llm: 30
  native_guardrail:
    wildcard_gpu_arch: "*"
    skip_discrete_accelerators: true
    max_penalty: 7
    ram_utilization_penalties:
      - above: 0.5
        penalty: 3
`,
		},
		{
			name: "missing wildcard",
			raw: `
kind: onboarding_policy
first_run:
  default_modality_score: 2
  modality_scores:
    llm: 30
  native_guardrail:
    skip_discrete_accelerators: true
    max_penalty: 7
    ram_utilization_penalties:
      - above: 0.5
        penalty: 3
`,
		},
		{
			name: "missing explicit boolean",
			raw: `
kind: onboarding_policy
first_run:
  default_modality_score: 2
  modality_scores:
    llm: 30
  native_guardrail:
    wildcard_gpu_arch: "*"
    max_penalty: 7
    ram_utilization_penalties:
      - above: 0.5
        penalty: 3
`,
		},
		{
			name: "missing penalties",
			raw: `
kind: onboarding_policy
first_run:
  default_modality_score: 2
  modality_scores:
    llm: 30
  native_guardrail:
    wildcard_gpu_arch: "*"
    skip_discrete_accelerators: true
    max_penalty: 7
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseFirstRunPolicyYAML([]byte(tt.raw)); err == nil {
				t.Fatal("ParseFirstRunPolicyYAML succeeded, want error")
			}
		})
	}
}
