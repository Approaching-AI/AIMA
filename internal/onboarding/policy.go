package onboarding

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// FirstRunPolicy contains product policy for the read-only onboarding guide.
// Production wiring loads it from catalog/onboarding-policy.yaml; tests and
// alternate embeddings can inject a policy through Deps.
type FirstRunPolicy struct {
	RecommendationAllowlists map[string][]string     `yaml:"recommendation_allowlists,omitempty" json:"recommendation_allowlists,omitempty"`
	NativeGuardrail          NativeFirstRunGuardrail `yaml:"native_guardrail" json:"native_guardrail"`
}

type NativeFirstRunGuardrail struct {
	Disabled                 bool                 `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	WildcardGPUArch          string               `yaml:"wildcard_gpu_arch,omitempty" json:"wildcard_gpu_arch,omitempty"`
	SkipDiscreteAccelerators *bool                `yaml:"skip_discrete_accelerators,omitempty" json:"skip_discrete_accelerators,omitempty"`
	RAMUtilizationPenalties  []UtilizationPenalty `yaml:"ram_utilization_penalties,omitempty" json:"ram_utilization_penalties,omitempty"`
	ParameterCountPenalties  []ParameterPenalty   `yaml:"parameter_count_penalties,omitempty" json:"parameter_count_penalties,omitempty"`
	MaxPenalty               int                  `yaml:"max_penalty,omitempty" json:"max_penalty,omitempty"`
}

type UtilizationPenalty struct {
	Above   float64 `yaml:"above" json:"above"`
	Penalty int     `yaml:"penalty" json:"penalty"`
}

type ParameterPenalty struct {
	AboveBillion float64 `yaml:"above_billion" json:"above_billion"`
	Penalty      int     `yaml:"penalty" json:"penalty"`
}

func ParseFirstRunPolicyYAML(data []byte) (*FirstRunPolicy, error) {
	var doc struct {
		Kind     string          `yaml:"kind"`
		FirstRun *FirstRunPolicy `yaml:"first_run"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if strings.TrimSpace(doc.Kind) != "onboarding_policy" {
		return nil, fmt.Errorf("onboarding policy kind must be onboarding_policy")
	}
	if doc.FirstRun == nil {
		return nil, fmt.Errorf("onboarding policy missing first_run")
	}
	policy := *doc.FirstRun
	if err := policy.validate(); err != nil {
		return nil, err
	}
	return &policy, nil
}

func effectiveFirstRunPolicy(deps *Deps) FirstRunPolicy {
	if deps != nil && deps.FirstRunPolicy != nil {
		return *deps.FirstRunPolicy
	}
	return FirstRunPolicy{}
}

func (p FirstRunPolicy) validate() error {
	for profile, models := range p.RecommendationAllowlists {
		if strings.TrimSpace(profile) == "" {
			return fmt.Errorf("first_run.recommendation_allowlists contains an empty hardware profile")
		}
		if len(models) == 0 {
			return fmt.Errorf("first_run.recommendation_allowlists.%s must contain at least one model", profile)
		}
		for i, model := range models {
			if strings.TrimSpace(model) == "" {
				return fmt.Errorf("first_run.recommendation_allowlists.%s[%d] must not be empty", profile, i)
			}
		}
	}

	guardrail := p.NativeGuardrail
	if guardrail.Disabled {
		return nil
	}
	if strings.TrimSpace(guardrail.WildcardGPUArch) == "" {
		return fmt.Errorf("first_run.native_guardrail.wildcard_gpu_arch is required")
	}
	if guardrail.SkipDiscreteAccelerators == nil {
		return fmt.Errorf("first_run.native_guardrail.skip_discrete_accelerators is required")
	}
	if len(guardrail.RAMUtilizationPenalties) == 0 && len(guardrail.ParameterCountPenalties) == 0 {
		return fmt.Errorf("first_run.native_guardrail must define at least one penalty list")
	}
	if guardrail.MaxPenalty <= 0 {
		return fmt.Errorf("first_run.native_guardrail.max_penalty must be positive")
	}
	for i, threshold := range guardrail.RAMUtilizationPenalties {
		if threshold.Above < 0 {
			return fmt.Errorf("first_run.native_guardrail.ram_utilization_penalties[%d].above must be non-negative", i)
		}
		if threshold.Penalty <= 0 {
			return fmt.Errorf("first_run.native_guardrail.ram_utilization_penalties[%d].penalty must be positive", i)
		}
	}
	for i, threshold := range guardrail.ParameterCountPenalties {
		if threshold.AboveBillion < 0 {
			return fmt.Errorf("first_run.native_guardrail.parameter_count_penalties[%d].above_billion must be non-negative", i)
		}
		if threshold.Penalty <= 0 {
			return fmt.Errorf("first_run.native_guardrail.parameter_count_penalties[%d].penalty must be positive", i)
		}
	}
	return nil
}

func (p FirstRunPolicy) allowsRecommendation(hardwareProfile, modelName string) bool {
	for profile, models := range p.RecommendationAllowlists {
		if !strings.EqualFold(strings.TrimSpace(profile), strings.TrimSpace(hardwareProfile)) {
			continue
		}
		for _, model := range models {
			if strings.EqualFold(strings.TrimSpace(model), strings.TrimSpace(modelName)) {
				return true
			}
		}
		return false
	}
	return true
}
