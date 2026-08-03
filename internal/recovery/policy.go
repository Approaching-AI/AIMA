// Package recovery defines catalog-driven deployment recovery settings.
package recovery

import (
	"fmt"
	"time"
)

type Policy struct {
	Enabled             bool  `json:"enabled"`
	CheckIntervalS      int   `json:"check_interval_s"`
	ConsecutiveFailures int   `json:"consecutive_failures"`
	MaxAttempts         int   `json:"max_attempts"`
	WindowS             int   `json:"window_s"`
	BackoffS            []int `json:"backoff_s"`
	StableResetS        int   `json:"stable_reset_s"`
}

type PolicyPatch struct {
	Enabled             *bool `json:"enabled,omitempty"`
	CheckIntervalS      *int  `json:"check_interval_s,omitempty"`
	ConsecutiveFailures *int  `json:"consecutive_failures,omitempty"`
	MaxAttempts         *int  `json:"max_attempts,omitempty"`
	WindowS             *int  `json:"window_s,omitempty"`
	BackoffS            []int `json:"backoff_s,omitempty"`
	StableResetS        *int  `json:"stable_reset_s,omitempty"`
}

func DefaultPolicy() Policy {
	return Policy{
		Enabled:             true,
		CheckIntervalS:      5,
		ConsecutiveFailures: 3,
		MaxAttempts:         3,
		WindowS:             600,
		BackoffS:            []int{2, 10, 30},
		StableResetS:        600,
	}
}

func ResolvePolicy(base Policy, patches ...PolicyPatch) (Policy, error) {
	resolved := base
	resolved.BackoffS = append([]int(nil), base.BackoffS...)
	for _, patch := range patches {
		if patch.Enabled != nil {
			resolved.Enabled = *patch.Enabled
		}
		if patch.CheckIntervalS != nil {
			resolved.CheckIntervalS = *patch.CheckIntervalS
		}
		if patch.ConsecutiveFailures != nil {
			resolved.ConsecutiveFailures = *patch.ConsecutiveFailures
		}
		if patch.MaxAttempts != nil {
			resolved.MaxAttempts = *patch.MaxAttempts
		}
		if patch.WindowS != nil {
			resolved.WindowS = *patch.WindowS
		}
		if len(patch.BackoffS) > 0 {
			resolved.BackoffS = append([]int(nil), patch.BackoffS...)
		}
		if patch.StableResetS != nil {
			resolved.StableResetS = *patch.StableResetS
		}
	}
	if err := validate(resolved); err != nil {
		return Policy{}, err
	}
	return resolved, nil
}

func (p Policy) Backoff(attempt int) time.Duration {
	if len(p.BackoffS) == 0 {
		return 0
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(p.BackoffS) {
		attempt = len(p.BackoffS) - 1
	}
	return time.Duration(p.BackoffS[attempt]) * time.Second
}

func validate(p Policy) error {
	if err := validateRange("check_interval_s", p.CheckIntervalS, 1, 300); err != nil {
		return err
	}
	if err := validateRange("consecutive_failures", p.ConsecutiveFailures, 1, 20); err != nil {
		return err
	}
	if err := validateRange("max_attempts", p.MaxAttempts, 1, 20); err != nil {
		return err
	}
	if err := validateRange("window_s", p.WindowS, 1, 86400); err != nil {
		return err
	}
	if err := validateRange("stable_reset_s", p.StableResetS, 1, 86400); err != nil {
		return err
	}
	for _, seconds := range p.BackoffS {
		if err := validateRange("backoff_s", seconds, 1, 3600); err != nil {
			return err
		}
	}
	return nil
}

func validateRange(name string, value, min, max int) error {
	if value < min || value > max {
		return fmt.Errorf("%s must be between %d and %d", name, min, max)
	}
	return nil
}
