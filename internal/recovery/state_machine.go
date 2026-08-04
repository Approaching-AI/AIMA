package recovery

import (
	"reflect"
	"strings"
	"time"
)

// Action describes the work the reconciler should perform after committing a
// Decision's proposed Intent.
type Action string

const (
	ActionNone       Action = "none"
	ActionRecover    Action = "recover"
	ActionQuarantine Action = "quarantine"
)

// Observation is the runtime status used to evaluate recovery. It is a value
// object so Evaluate can remain deterministic and side-effect free.
type Observation struct {
	Exists   bool
	Ready    bool
	Phase    string
	Restarts int
	ExitCode *int
	Error    string
	Stalled  bool
}

// Decision contains the next durable recovery state and any action that must
// be performed only after that state is committed.
type Decision struct {
	Intent Intent
	Action Action
}

// Evaluate derives the next recovery state from one runtime observation. It
// never persists, starts, stops, or waits for anything; callers must commit
// the proposed Intent before performing its Action.
func Evaluate(intent Intent, observation Observation, now time.Time) Decision {
	next := cloneIntent(intent)
	if intent.DesiredState == DesiredStopped || !intent.Policy.Enabled || intent.RecoveryState == StateQuarantined {
		return Decision{Intent: next, Action: ActionNone}
	}

	if observation.ExitCode != nil {
		exitCode := *observation.ExitCode
		next.LastExitCode = &exitCode
	}
	if observation.Error != "" {
		next.LastError = SanitizeText(observation.Error)
	}

	windowExpired := recoveryWindowExpired(next, now)
	if windowExpired {
		resetRecoveryWindow(&next)
	}

	containerRuntime := next.Runtime == "docker" || next.Runtime == "k3s"
	restartDelta := 0
	if containerRuntime && observation.Exists && observation.Ready && needsContainerRestartBaseline(next) {
		if observation.Restarts > 0 {
			next.ObservedRestartCount = observation.Restarts
		}
	} else if containerRuntime && observation.Restarts > next.ObservedRestartCount {
		restartDelta = observation.Restarts - next.ObservedRestartCount
		next.ObservedRestartCount = observation.Restarts
		next.AttemptCount += restartDelta
		if next.WindowStartedAt.IsZero() {
			next.WindowStartedAt = now
		}
		next.HealthySince = time.Time{}
	}
	if restartDelta > 0 && next.AttemptCount >= next.Policy.MaxAttempts {
		return quarantineDecision(next, now)
	}

	if observation.Exists && observation.Ready {
		return healthyDecision(next, now)
	}
	if !containerRuntime && observation.Exists && !observation.Stalled && strings.EqualFold(observation.Phase, "starting") {
		next.HealthySince = time.Time{}
		return Decision{Intent: next, Action: ActionNone}
	}

	next.HealthySince = time.Time{}
	next.ConsecutiveFailureCount++
	if next.AttemptCount >= next.Policy.MaxAttempts {
		return quarantineDecision(next, now)
	}
	if !containerRuntime && next.RecoveryState == StateWaiting && next.NextAttemptAt.IsZero() {
		next.NextAttemptAt = now.Add(recoveryBackoff(next.Policy, next.AttemptCount))
		return Decision{Intent: next, Action: ActionNone}
	}

	if containerRuntime {
		if restartDelta > 0 || next.ConsecutiveFailureCount >= next.Policy.ConsecutiveFailures {
			next.RecoveryState = StateWaiting
		}
		return Decision{Intent: next, Action: ActionNone}
	}

	if next.RecoveryState == StateWaiting {
		if attemptDue(next.NextAttemptAt, now) {
			next.RecoveryState = StateRecovering
			next.AttemptCount++
			next.NextAttemptAt = time.Time{}
			if next.WindowStartedAt.IsZero() {
				next.WindowStartedAt = now
			}
			return Decision{Intent: next, Action: ActionRecover}
		}
	}

	if next.ConsecutiveFailureCount >= next.Policy.ConsecutiveFailures {
		if next.RecoveryState != StateWaiting {
			next.RecoveryState = StateWaiting
			next.NextAttemptAt = now.Add(recoveryBackoff(next.Policy, next.AttemptCount))
			if next.WindowStartedAt.IsZero() {
				next.WindowStartedAt = now
			}
		}
	}
	return Decision{Intent: next, Action: ActionNone}
}

func needsContainerRestartBaseline(intent Intent) bool {
	return intent.RecoveryState == StateHealthy &&
		intent.AttemptCount == 0 &&
		intent.ConsecutiveFailureCount == 0 &&
		intent.ObservedRestartCount == 0 &&
		intent.WindowStartedAt.IsZero() &&
		intent.HealthySince.IsZero()
}

func quarantineDecision(intent Intent, now time.Time) Decision {
	intent.RecoveryState = StateQuarantined
	intent.NextAttemptAt = time.Time{}
	if intent.Runtime == "docker" || intent.Runtime == "k3s" {
		intent.NextAttemptAt = now
	}
	return Decision{Intent: intent, Action: ActionQuarantine}
}

func healthyDecision(intent Intent, now time.Time) Decision {
	intent.RecoveryState = StateHealthy
	intent.ConsecutiveFailureCount = 0
	intent.NextAttemptAt = time.Time{}
	if intent.HealthySince.IsZero() {
		intent.HealthySince = now
	}
	if stableLongEnough(intent.HealthySince, intent.Policy.StableResetS, now) {
		intent.AttemptCount = 0
		intent.WindowStartedAt = time.Time{}
	}
	return Decision{Intent: intent, Action: ActionNone}
}

func recoveryWindowExpired(intent Intent, now time.Time) bool {
	if intent.WindowStartedAt.IsZero() || intent.Policy.WindowS <= 0 {
		return false
	}
	return !now.Before(intent.WindowStartedAt.Add(time.Duration(intent.Policy.WindowS) * time.Second))
}

func resetRecoveryWindow(intent *Intent) {
	intent.AttemptCount = 0
	intent.ConsecutiveFailureCount = 0
	intent.WindowStartedAt = time.Time{}
	intent.NextAttemptAt = time.Time{}
	intent.RecoveryState = StateHealthy
}

func attemptDue(nextAttemptAt, now time.Time) bool {
	return !nextAttemptAt.IsZero() && !now.Before(nextAttemptAt)
}

func stableLongEnough(healthySince time.Time, stableResetS int, now time.Time) bool {
	if healthySince.IsZero() || stableResetS <= 0 {
		return false
	}
	return !now.Before(healthySince.Add(time.Duration(stableResetS) * time.Second))
}

func recoveryBackoff(policy Policy, attempt int) time.Duration {
	if len(policy.BackoffS) == 0 {
		return DefaultPolicy().Backoff(attempt)
	}
	return policy.Backoff(attempt)
}

func cloneIntent(intent Intent) Intent {
	copy := intent
	copy.Policy.BackoffS = append([]int(nil), intent.Policy.BackoffS...)
	copy.Config = cloneConfig(intent.Config)
	if intent.LastExitCode != nil {
		exitCode := *intent.LastExitCode
		copy.LastExitCode = &exitCode
	}
	return copy
}

func cloneConfig(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	copy := make(map[string]any, len(config))
	for key, value := range config {
		copy[key] = cloneConfigValue(value)
	}
	return copy
}

func cloneConfigValue(value any) any {
	cloned := cloneConfigReflect(reflect.ValueOf(value), make(map[configCloneVisit]reflect.Value))
	if !cloned.IsValid() {
		return nil
	}
	return cloned.Interface()
}

type configCloneVisit struct {
	typ reflect.Type
	ptr uintptr
}

func cloneConfigReflect(value reflect.Value, seen map[configCloneVisit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneConfigReflect(value.Elem(), seen)
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := configCloneVisit{typ: value.Type(), ptr: value.Pointer()}
		if cloned, ok := seen[visit]; ok {
			return cloned
		}
		result := reflect.New(value.Type().Elem())
		seen[visit] = result
		result.Elem().Set(cloneConfigReflect(value.Elem(), seen))
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := configCloneVisit{typ: value.Type(), ptr: value.Pointer()}
		if cloned, ok := seen[visit]; ok {
			return cloned
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		seen[visit] = result
		iterator := value.MapRange()
		for iterator.Next() {
			result.SetMapIndex(
				cloneConfigReflect(iterator.Key(), seen),
				cloneConfigReflect(iterator.Value(), seen),
			)
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := configCloneVisit{typ: value.Type(), ptr: value.Pointer()}
		if cloned, ok := seen[visit]; ok {
			return cloned
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		seen[visit] = result
		for i := 0; i < value.Len(); i++ {
			result.Index(i).Set(cloneConfigReflect(value.Index(i), seen))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			result.Index(i).Set(cloneConfigReflect(value.Index(i), seen))
		}
		return result
	default:
		return value
	}
}
