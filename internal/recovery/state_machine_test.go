package recovery

import (
	"context"
	"testing"
	"time"
)

func TestReconcilerClaimDefensivelyCopiesTypedContainers(t *testing.T) {
	configMap := map[string]string{"mode": "original"}
	configSlice := []string{"first", "second"}
	configBytes := []byte("payload")
	intent := Intent{
		Name:   "typed-config",
		Config: map[string]any{"map": configMap, "slice": configSlice, "bytes": configBytes},
		Policy: Policy{BackoffS: []int{1, 2, 3}},
	}
	ctx := WithReconcilerClaim(context.Background(), intent)

	configMap["mode"] = "mutated"
	configSlice[0] = "mutated"
	configBytes[0] = 'X'
	intent.Policy.BackoffS[0] = 99
	claim, ok := ReconcilerClaim(ctx)
	if !ok {
		t.Fatal("ReconcilerClaim missing")
	}
	if got := claim.Config["map"].(map[string]string)["mode"]; got != "original" {
		t.Fatalf("claimed map value = %q, want original", got)
	}
	if got := claim.Config["slice"].([]string)[0]; got != "first" {
		t.Fatalf("claimed slice value = %q, want first", got)
	}
	if got := string(claim.Config["bytes"].([]byte)); got != "payload" {
		t.Fatalf("claimed bytes = %q, want payload", got)
	}
	if claim.Policy.BackoffS[0] != 1 {
		t.Fatalf("claimed backoff = %v, want defensive copy", claim.Policy.BackoffS)
	}

	claim.Config["map"].(map[string]string)["mode"] = "changed again"
	claim.Policy.BackoffS[0] = 77
	again, ok := ReconcilerClaim(ctx)
	if !ok || again.Config["map"].(map[string]string)["mode"] != "original" || again.Policy.BackoffS[0] != 1 {
		t.Fatalf("stored claim was mutated through returned copy: %+v", again)
	}
}

func TestEvaluateRecoveryTransitions(t *testing.T) {
	base := Intent{
		Name:          "m",
		DesiredState:  DesiredRunning,
		RecoveryState: StateHealthy,
		Policy:        DefaultPolicy(),
	}
	cases := []struct {
		name   string
		mutate func(*Intent)
		obs    Observation
		now    time.Time
		action Action
		state  string
	}{
		{
			name:   "stopped never recovers",
			mutate: func(i *Intent) { i.DesiredState = DesiredStopped },
			obs:    Observation{Exists: false},
			action: ActionNone,
			state:  StateHealthy,
		},
		{
			name:   "disabled policy never recovers",
			mutate: func(i *Intent) { i.Policy.Enabled = false },
			obs:    Observation{Exists: false},
			action: ActionNone,
			state:  StateHealthy,
		},
		{
			name:   "first failed probe only counts",
			obs:    Observation{Exists: true, Ready: false, Phase: "running"},
			action: ActionNone,
			state:  StateHealthy,
		},
		{
			name:   "third failed probe waits",
			mutate: func(i *Intent) { i.ConsecutiveFailureCount = 2 },
			obs:    Observation{Exists: true, Ready: false},
			now:    time.Unix(1, 0),
			action: ActionNone,
			state:  StateWaiting,
		},
		{
			name: "due native wait recovers",
			mutate: func(i *Intent) {
				i.Runtime = "native"
				i.RecoveryState = StateWaiting
				i.NextAttemptAt = time.Unix(9, 0)
			},
			obs:    Observation{Exists: false},
			now:    time.Unix(10, 0),
			action: ActionRecover,
			state:  StateRecovering,
		},
		{
			name: "attempt limit quarantines",
			mutate: func(i *Intent) {
				i.AttemptCount = 3
				i.WindowStartedAt = time.Unix(1, 0)
			},
			obs:    Observation{Exists: false},
			now:    time.Unix(10, 0),
			action: ActionQuarantine,
			state:  StateQuarantined,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			intent := cloneIntentForTest(base)
			if tt.mutate != nil {
				tt.mutate(&intent)
			}
			got := Evaluate(intent, tt.obs, tt.now)
			if got.Action != tt.action || got.Intent.RecoveryState != tt.state {
				t.Fatalf("Evaluate() = action %q, state %q; want action %q, state %q", got.Action, got.Intent.RecoveryState, tt.action, tt.state)
			}
			if tt.name == "first failed probe only counts" && got.Intent.ConsecutiveFailureCount != 1 {
				t.Fatalf("consecutive failures = %d, want 1", got.Intent.ConsecutiveFailureCount)
			}
			if tt.name == "due native wait recovers" && got.Intent.AttemptCount != 1 {
				t.Fatalf("proposed attempts = %d, want 1", got.Intent.AttemptCount)
			}
		})
	}
}

func TestEvaluateNativeStartingIsNeutralUntilStalled(t *testing.T) {
	now := time.Unix(9_000, 0)
	intent := Intent{
		Name: "loading", Runtime: "native", DesiredState: DesiredRunning,
		RecoveryState: StateHealthy, Policy: DefaultPolicy(),
		ConsecutiveFailureCount: 2, HealthySince: now.Add(-time.Minute),
	}

	neutral := Evaluate(intent, Observation{Exists: true, Phase: "starting"}, now)
	if neutral.Action != ActionNone || neutral.Intent.RecoveryState != StateHealthy || neutral.Intent.ConsecutiveFailureCount != 2 {
		t.Fatalf("neutral starting observation = %+v", neutral)
	}
	if !neutral.Intent.HealthySince.IsZero() {
		t.Fatalf("neutral starting observation retained healthy clock: %+v", neutral.Intent)
	}

	stalled := Evaluate(intent, Observation{Exists: true, Phase: "starting", Stalled: true}, now)
	if stalled.Intent.RecoveryState != StateWaiting || stalled.Intent.ConsecutiveFailureCount != 3 {
		t.Fatalf("stalled starting observation = %+v, want waiting after third failure", stalled)
	}
}

func TestEvaluateNativeHealthTimeoutTriggersRecovery(t *testing.T) {
	now := time.Unix(9_100, 0)
	intent := Intent{
		Name:                    "health-timeout",
		Runtime:                 "native",
		DesiredState:            DesiredRunning,
		RecoveryState:           StateHealthy,
		Policy:                  DefaultPolicy(),
		ConsecutiveFailureCount: DefaultPolicy().ConsecutiveFailures - 1,
	}

	decision := Evaluate(intent, Observation{
		Exists:  true,
		Phase:   "failed",
		Error:   "health check timed out before deployment became ready",
		Stalled: true,
	}, now)
	if decision.Action != ActionNone || decision.Intent.RecoveryState != StateWaiting {
		t.Fatalf("health timeout decision = %+v, want recovery waiting", decision)
	}
	if decision.Intent.NextAttemptAt.IsZero() {
		t.Fatalf("health timeout did not schedule a recovery attempt: %+v", decision.Intent)
	}
}

func TestEvaluateUsesExactBoundariesForWaitingAndStableReset(t *testing.T) {
	now := time.Unix(1_000, 0)
	p := DefaultPolicy()
	intent := Intent{
		DesiredState:  DesiredRunning,
		RecoveryState: StateWaiting,
		Policy:        p,
		NextAttemptAt: now,
	}
	got := Evaluate(intent, Observation{}, now)
	if got.Action != ActionRecover || got.Intent.AttemptCount != 1 {
		t.Fatalf("due wait = %+v, want recovery proposing attempt 1", got)
	}

	healthySince := now.Add(-time.Duration(p.StableResetS) * time.Second)
	stable := Intent{
		DesiredState:            DesiredRunning,
		RecoveryState:           StateHealthy,
		Policy:                  p,
		AttemptCount:            2,
		ConsecutiveFailureCount: 4,
		WindowStartedAt:         now.Add(-30 * time.Second),
		NextAttemptAt:           now.Add(time.Second),
		HealthySince:            healthySince,
	}
	got = Evaluate(stable, Observation{Exists: true, Ready: true}, now)
	if got.Action != ActionNone || got.Intent.AttemptCount != 0 || got.Intent.ConsecutiveFailureCount != 0 || !got.Intent.WindowStartedAt.IsZero() || !got.Intent.NextAttemptAt.IsZero() {
		t.Fatalf("stable reset = %+v, want counters and recovery schedule cleared", got.Intent)
	}
	if !got.Intent.HealthySince.Equal(healthySince) {
		t.Fatalf("healthy since = %s, want %s", got.Intent.HealthySince, healthySince)
	}

	stable.HealthySince = healthySince.Add(time.Second)
	got = Evaluate(stable, Observation{Exists: true, Ready: true}, now)
	if got.Intent.AttemptCount != 2 || got.Intent.WindowStartedAt.IsZero() {
		t.Fatalf("pre-stable reset = %+v, want existing recovery window retained", got.Intent)
	}
}

func TestEvaluateExpiresWindowBeforeNativeFailureIsCounted(t *testing.T) {
	now := time.Unix(2_000, 0)
	p := DefaultPolicy()
	intent := Intent{
		Runtime:                 "native",
		DesiredState:            DesiredRunning,
		RecoveryState:           StateWaiting,
		Policy:                  p,
		AttemptCount:            p.MaxAttempts,
		ConsecutiveFailureCount: p.ConsecutiveFailures,
		WindowStartedAt:         now.Add(-time.Duration(p.WindowS) * time.Second),
		NextAttemptAt:           now.Add(-time.Second),
	}
	got := Evaluate(intent, Observation{}, now)
	if got.Action != ActionNone || got.Intent.RecoveryState != StateHealthy || got.Intent.AttemptCount != 0 || got.Intent.ConsecutiveFailureCount != 1 || !got.Intent.WindowStartedAt.IsZero() {
		t.Fatalf("expired window = %+v, want fresh failed-probe count", got)
	}

	intent.WindowStartedAt = now.Add(-time.Duration(p.WindowS-1) * time.Second)
	got = Evaluate(intent, Observation{}, now)
	if got.Action != ActionQuarantine || got.Intent.RecoveryState != StateQuarantined {
		t.Fatalf("pre-expiry window = %+v, want existing attempt limit enforced", got)
	}
}

func TestEvaluateMaxAttemptReadinessRuling(t *testing.T) {
	now := time.Unix(8_000, 0)
	p := DefaultPolicy()
	unexpiredWindow := now.Add(-time.Second)
	expiredWindow := now.Add(-time.Duration(p.WindowS) * time.Second)

	cases := []struct {
		name                 string
		runtime              string
		recoveryState        string
		attemptCount         int
		observedRestartCount int
		windowStartedAt      time.Time
		observation          Observation
		wantAction           Action
		wantState            string
		wantAttemptCount     int
		wantRestartCount     int
		wantWindowStartedAt  time.Time
		wantHealthySince     time.Time
	}{
		{
			name:                "native ready at max retains unexpired window",
			runtime:             "native",
			recoveryState:       StateRecovering,
			attemptCount:        p.MaxAttempts,
			windowStartedAt:     unexpiredWindow,
			observation:         Observation{Exists: true, Ready: true},
			wantAction:          ActionNone,
			wantState:           StateHealthy,
			wantAttemptCount:    p.MaxAttempts,
			wantWindowStartedAt: unexpiredWindow,
			wantHealthySince:    now,
		},
		{
			name:             "native ready at max starts healthy clock without a window",
			runtime:          "native",
			recoveryState:    StateRecovering,
			attemptCount:     p.MaxAttempts,
			observation:      Observation{Exists: true, Ready: true},
			wantAction:       ActionNone,
			wantState:        StateHealthy,
			wantAttemptCount: p.MaxAttempts,
			wantHealthySince: now,
		},
		{
			name:             "native ready at max resets an expired window",
			runtime:          "native",
			recoveryState:    StateRecovering,
			attemptCount:     p.MaxAttempts,
			windowStartedAt:  expiredWindow,
			observation:      Observation{Exists: true, Ready: true},
			wantAction:       ActionNone,
			wantState:        StateHealthy,
			wantAttemptCount: 0,
			wantHealthySince: now,
		},
		{
			name:                "native unready at max quarantines",
			runtime:             "native",
			recoveryState:       StateRecovering,
			attemptCount:        p.MaxAttempts,
			windowStartedAt:     unexpiredWindow,
			observation:         Observation{Exists: true, Ready: false},
			wantAction:          ActionQuarantine,
			wantState:           StateQuarantined,
			wantAttemptCount:    p.MaxAttempts,
			wantWindowStartedAt: unexpiredWindow,
		},
		{
			name:                "native missing at max quarantines",
			runtime:             "native",
			recoveryState:       StateRecovering,
			attemptCount:        p.MaxAttempts,
			windowStartedAt:     unexpiredWindow,
			observation:         Observation{Exists: false},
			wantAction:          ActionQuarantine,
			wantState:           StateQuarantined,
			wantAttemptCount:    p.MaxAttempts,
			wantWindowStartedAt: unexpiredWindow,
		},
		{
			name:                 "container restart delta reaches max despite readiness",
			runtime:              "k3s",
			recoveryState:        StateHealthy,
			attemptCount:         p.MaxAttempts - 1,
			observedRestartCount: 7,
			windowStartedAt:      unexpiredWindow,
			observation:          Observation{Exists: true, Ready: true, Restarts: 8},
			wantAction:           ActionQuarantine,
			wantState:            StateQuarantined,
			wantAttemptCount:     p.MaxAttempts,
			wantRestartCount:     8,
			wantWindowStartedAt:  unexpiredWindow,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			intent := Intent{
				Runtime:              tt.runtime,
				DesiredState:         DesiredRunning,
				RecoveryState:        tt.recoveryState,
				Policy:               p,
				AttemptCount:         tt.attemptCount,
				ObservedRestartCount: tt.observedRestartCount,
				WindowStartedAt:      tt.windowStartedAt,
			}

			got := Evaluate(intent, tt.observation, now)
			if got.Action != tt.wantAction || got.Intent.RecoveryState != tt.wantState {
				t.Fatalf("Evaluate() = action %q, state %q; want action %q, state %q", got.Action, got.Intent.RecoveryState, tt.wantAction, tt.wantState)
			}
			if got.Intent.AttemptCount != tt.wantAttemptCount {
				t.Fatalf("attempts = %d, want %d", got.Intent.AttemptCount, tt.wantAttemptCount)
			}
			if got.Intent.ObservedRestartCount != tt.wantRestartCount {
				t.Fatalf("observed restarts = %d, want %d", got.Intent.ObservedRestartCount, tt.wantRestartCount)
			}
			if !got.Intent.WindowStartedAt.Equal(tt.wantWindowStartedAt) {
				t.Fatalf("window started at = %s, want %s", got.Intent.WindowStartedAt, tt.wantWindowStartedAt)
			}
			if !got.Intent.HealthySince.Equal(tt.wantHealthySince) {
				t.Fatalf("healthy since = %s, want %s", got.Intent.HealthySince, tt.wantHealthySince)
			}
		})
	}
}

func TestEvaluateCountsContainerRestartDeltasAndQuarantines(t *testing.T) {
	now := time.Unix(3_000, 0)
	p := DefaultPolicy()
	intent := Intent{
		Runtime:              "docker",
		DesiredState:         DesiredRunning,
		RecoveryState:        StateHealthy,
		Policy:               p,
		AttemptCount:         1,
		ObservedRestartCount: 4,
		WindowStartedAt:      now.Add(-time.Second),
	}
	got := Evaluate(intent, Observation{Exists: true, Ready: false, Restarts: 6}, now)
	if got.Action != ActionQuarantine || got.Intent.RecoveryState != StateQuarantined || got.Intent.AttemptCount != 3 || got.Intent.ObservedRestartCount != 6 {
		t.Fatalf("restart delta = %+v, want delta counted and quarantine", got)
	}
	if !got.Intent.NextAttemptAt.Equal(now) {
		t.Fatalf("quarantine enforcement time = %s, want %s", got.Intent.NextAttemptAt, now)
	}

	unchanged := Evaluate(got.Intent, Observation{Exists: true, Ready: false, Restarts: 6}, now.Add(time.Second))
	if unchanged.Action != ActionNone || unchanged.Intent.AttemptCount != 3 || unchanged.Intent.ObservedRestartCount != 6 {
		t.Fatalf("unchanged restarts = %+v, want no repeated accounting", unchanged)
	}
	permanent := Evaluate(got.Intent, Observation{Exists: false, Restarts: 9}, now.Add(time.Hour))
	if permanent.Action != ActionNone || permanent.Intent.RecoveryState != StateQuarantined || permanent.Intent.AttemptCount != 3 {
		t.Fatalf("quarantined intent = %+v, want unchanged", permanent)
	}

}

func TestEvaluateBaselinesHealthyContainerHistoricalRestarts(t *testing.T) {
	now := time.Unix(3_100, 0)
	intent := Intent{
		Runtime:       "docker",
		DesiredState:  DesiredRunning,
		RecoveryState: StateHealthy,
		Policy:        DefaultPolicy(),
	}

	got := Evaluate(intent, Observation{Exists: true, Ready: true, Restarts: 3}, now)
	if got.Action != ActionNone || got.Intent.RecoveryState != StateHealthy {
		t.Fatalf("initial healthy observation = %+v, want healthy without action", got)
	}
	if got.Intent.AttemptCount != 0 || got.Intent.ObservedRestartCount != 3 {
		t.Fatalf("initial restart baseline = %+v, want baseline 3 with zero attempts", got.Intent)
	}
	if !got.Intent.WindowStartedAt.IsZero() || !got.Intent.HealthySince.Equal(now) {
		t.Fatalf("initial healthy timing = %+v, want no recovery window and healthy clock at %s", got.Intent, now)
	}

	restarted := Evaluate(got.Intent, Observation{Exists: false, Restarts: 4}, now.Add(time.Second))
	if restarted.Intent.AttemptCount != 1 || restarted.Intent.ObservedRestartCount != 4 {
		t.Fatalf("subsequent restart = %+v, want one counted restart delta", restarted.Intent)
	}
}

func TestEvaluateRestartsUseWindowAndBackoffPolicy(t *testing.T) {
	now := time.Unix(4_000, 0)
	p := DefaultPolicy()
	intent := Intent{
		Runtime:              "k3s",
		DesiredState:         DesiredRunning,
		RecoveryState:        StateHealthy,
		Policy:               p,
		AttemptCount:         p.MaxAttempts,
		ObservedRestartCount: 1,
		WindowStartedAt:      now.Add(-time.Duration(p.WindowS) * time.Second),
	}
	got := Evaluate(intent, Observation{Exists: true, Ready: false, Restarts: 3}, now)
	if got.Action != ActionNone || got.Intent.RecoveryState != StateWaiting || got.Intent.AttemptCount != 2 || got.Intent.ObservedRestartCount != 3 || !got.Intent.WindowStartedAt.Equal(now) {
		t.Fatalf("expired container window = %+v, want new window with full delta", got)
	}

	for _, tt := range []struct {
		attempt int
		want    time.Duration
	}{
		{0, 2 * time.Second},
		{1, 10 * time.Second},
		{2, 30 * time.Second},
		{5, 30 * time.Second},
	} {
		if got := p.Backoff(tt.attempt); got != tt.want {
			t.Fatalf("backoff(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestEvaluateSchedulesNativeBackoffWithoutCountingBeforeDue(t *testing.T) {
	now := time.Unix(5_000, 0)
	p := DefaultPolicy()
	for _, tt := range []struct {
		attempt int
		want    time.Duration
	}{
		{0, 2 * time.Second},
		{1, 10 * time.Second},
		{2, 30 * time.Second},
		{5, 30 * time.Second},
	} {
		policy := p
		policy.MaxAttempts = 6
		intent := Intent{
			Runtime:                 "native",
			DesiredState:            DesiredRunning,
			RecoveryState:           StateHealthy,
			Policy:                  policy,
			AttemptCount:            tt.attempt,
			ConsecutiveFailureCount: policy.ConsecutiveFailures - 1,
		}
		got := Evaluate(intent, Observation{}, now)
		if got.Action != ActionNone || got.Intent.RecoveryState != StateWaiting || !got.Intent.NextAttemptAt.Equal(now.Add(tt.want)) {
			t.Fatalf("attempt %d schedules %+v, want wait until %s", tt.attempt, got, now.Add(tt.want))
		}
	}

	waiting := Intent{
		Runtime:                 "native",
		DesiredState:            DesiredRunning,
		RecoveryState:           StateWaiting,
		Policy:                  p,
		AttemptCount:            1,
		ConsecutiveFailureCount: p.ConsecutiveFailures,
		NextAttemptAt:           now.Add(10 * time.Second),
	}
	got := Evaluate(waiting, Observation{}, now.Add(9*time.Second))
	if got.Action != ActionNone || got.Intent.AttemptCount != 1 || !got.Intent.NextAttemptAt.Equal(waiting.NextAttemptAt) {
		t.Fatalf("not-yet-due wait = %+v, want unchanged recovery attempt", got)
	}

	for _, tt := range []struct {
		attempt    int
		wantDelay  time.Duration
		wantAction Action
		wantState  string
	}{
		{0, 2 * time.Second, ActionNone, StateWaiting},
		{1, 10 * time.Second, ActionNone, StateWaiting},
		{2, 30 * time.Second, ActionNone, StateWaiting},
		{3, 0, ActionQuarantine, StateQuarantined},
		{4, 0, ActionQuarantine, StateQuarantined},
	} {
		brokenWaiting := waiting
		brokenWaiting.AttemptCount = tt.attempt
		brokenWaiting.NextAttemptAt = time.Time{}
		got = Evaluate(brokenWaiting, Observation{}, now)
		if got.Action != tt.wantAction || got.Intent.RecoveryState != tt.wantState || got.Intent.AttemptCount != brokenWaiting.AttemptCount {
			t.Fatalf("zero next attempt %d = %+v, want action %q and state %q", tt.attempt, got, tt.wantAction, tt.wantState)
		}
		if tt.wantAction == ActionQuarantine {
			if !got.Intent.NextAttemptAt.IsZero() {
				t.Fatalf("exhausted zero next attempt %d scheduled %s, want cleared", tt.attempt, got.Intent.NextAttemptAt)
			}
			continue
		}
		wantRepair := now.Add(tt.wantDelay)
		if !got.Intent.NextAttemptAt.Equal(wantRepair) {
			t.Fatalf("zero next attempt %d scheduled %s, want %s", tt.attempt, got.Intent.NextAttemptAt, wantRepair)
		}
	}
}

func TestEvaluateDoesNotAliasIntentOrObservationReferences(t *testing.T) {
	inputExitCode := 4
	observedExitCode := 9
	input := Intent{
		DesiredState:  DesiredRunning,
		RecoveryState: StateHealthy,
		Policy: Policy{
			Enabled:             true,
			ConsecutiveFailures: 3,
			MaxAttempts:         3,
			WindowS:             600,
			BackoffS:            []int{2, 10, 30},
			StableResetS:        600,
		},
		Config: map[string]any{
			"nested": map[string]any{"value": "original"},
			"items":  []any{map[string]any{"value": "original"}},
		},
		LastExitCode: &inputExitCode,
	}
	decision := Evaluate(input, Observation{Exists: true, Ready: false, ExitCode: &observedExitCode}, time.Unix(6_000, 0))

	input.Policy.BackoffS[0] = 99
	input.Config["nested"].(map[string]any)["value"] = "input-mutated"
	input.Config["items"].([]any)[0].(map[string]any)["value"] = "input-mutated"
	inputExitCode = 40
	observedExitCode = 90
	if decision.Intent.Policy.BackoffS[0] != 2 || decision.Intent.Config["nested"].(map[string]any)["value"] != "original" || decision.Intent.Config["items"].([]any)[0].(map[string]any)["value"] != "original" || *decision.Intent.LastExitCode != 9 {
		t.Fatalf("decision aliases input or observation: %+v", decision.Intent)
	}

	decision.Intent.Policy.BackoffS[1] = 88
	decision.Intent.Config["nested"].(map[string]any)["value"] = "decision-mutated"
	*decision.Intent.LastExitCode = 8
	if input.Policy.BackoffS[1] != 10 || input.Config["nested"].(map[string]any)["value"] != "input-mutated" || inputExitCode != 40 {
		t.Fatalf("input aliases decision: %+v", input)
	}
}

func TestEvaluateKeepsPreThresholdFailureHealthy(t *testing.T) {
	p := DefaultPolicy()
	intent := Intent{
		DesiredState:            DesiredRunning,
		RecoveryState:           StateHealthy,
		Policy:                  p,
		ConsecutiveFailureCount: p.ConsecutiveFailures - 2,
	}
	got := Evaluate(intent, Observation{Exists: true, Ready: false}, time.Unix(7_000, 0))
	if got.Action != ActionNone || got.Intent.RecoveryState != StateHealthy || got.Intent.ConsecutiveFailureCount != p.ConsecutiveFailures-1 {
		t.Fatalf("pre-threshold failure = %+v, want healthy with one more failure counted", got)
	}
}

func cloneIntentForTest(intent Intent) Intent {
	copy := intent
	copy.Policy.BackoffS = append([]int(nil), intent.Policy.BackoffS...)
	copy.Config = cloneConfigForTest(intent.Config)
	if intent.LastExitCode != nil {
		exitCode := *intent.LastExitCode
		copy.LastExitCode = &exitCode
	}
	return copy
}

func cloneConfigForTest(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	copy := make(map[string]any, len(config))
	for key, value := range config {
		copy[key] = cloneConfigValueForTest(value)
	}
	return copy
}

func cloneConfigValueForTest(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneConfigForTest(typed)
	case []any:
		copy := make([]any, len(typed))
		for i, item := range typed {
			copy[i] = cloneConfigValueForTest(item)
		}
		return copy
	default:
		return value
	}
}
