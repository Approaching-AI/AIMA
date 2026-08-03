package recovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"
)

// Store is the durable Intent subset required by the recovery controller.
type Store interface {
	ListRunnableDeploymentIntents(context.Context) ([]*Intent, error)
	CompareAndSwapDeploymentIntent(context.Context, *Intent, int64) (bool, error)
	LogRecoveryEvent(context.Context, AuditEvent) error
}

type ObserveFunc func(context.Context, Intent) (Observation, error)
type ApplyFunc func(context.Context, Intent) error
type DeleteFunc func(context.Context, Intent) error

const (
	AuditSourceReconciler = "reconciler"

	AuditRecoveryAttempt             = "recovery_attempt"
	AuditRecoveryApplied             = "recovery_applied"
	AuditRecoveryFailed              = "recovery_failed"
	AuditRetryScheduled              = "retry_scheduled"
	AuditRestartObserved             = "restart_observed"
	AuditRecovered                   = "recovered"
	AuditQuarantined                 = "quarantined"
	AuditQuarantineEnforced          = "quarantine_enforced"
	AuditQuarantineEnforcementFailed = "quarantine_enforcement_failed"

	AuditResultCommitted = "committed"
	AuditResultApplied   = "applied"
	AuditResultFailed    = "failed"
	AuditResultScheduled = "scheduled"
	AuditResultObserved  = "observed"
	AuditResultHealthy   = "healthy"
	AuditResultEnforced  = "enforced"
)

// AuditEvent is the redacted, durable record emitted for a recovery decision
// or Runtime side effect. It intentionally contains identity and lifecycle
// metadata only; deployment config and credentials never enter this payload.
type AuditEvent struct {
	Type                 string `json:"type"`
	Source               string `json:"source"`
	Deployment           string `json:"deployment"`
	Model                string `json:"model,omitempty"`
	Runtime              string `json:"runtime"`
	EngineAsset          string `json:"engine_asset,omitempty"`
	EngineVersion        string `json:"engine_version,omitempty"`
	Result               string `json:"result"`
	Reason               string `json:"reason,omitempty"`
	RecoveryState        string `json:"recovery_state"`
	AttemptCount         int    `json:"attempt_count"`
	ObservedRestartCount int    `json:"observed_restart_count"`
	Revision             int64  `json:"revision"`
	NextAttemptAt        string `json:"next_attempt_at,omitempty"`
}

const controllerHeartbeat = time.Second

// Controller evaluates persisted deployment intent and commits each decision
// before delegating a Runtime side effect.
type Controller struct {
	store   Store
	observe ObserveFunc
	apply   ApplyFunc
	delete  DeleteFunc

	now   func() time.Time
	after func(time.Duration) <-chan time.Time

	runMu       sync.Mutex
	lastChecked map[string]time.Time
}

func NewController(store Store, observe ObserveFunc, apply ApplyFunc, delete DeleteFunc) *Controller {
	return &Controller{
		store:       store,
		observe:     observe,
		apply:       apply,
		delete:      delete,
		now:         time.Now,
		after:       time.After,
		lastChecked: make(map[string]time.Time),
	}
}

// RunOnce evaluates every runnable Intent once. Per-Intent failures are joined
// after all other Intents have been processed.
func (c *Controller) RunOnce(ctx context.Context) error {
	return c.runOnce(ctx, false)
}

// Start runs an immediate pass and then serial, cancellable scheduled passes.
func (c *Controller) Start(ctx context.Context) {
	if c == nil {
		return
	}
	if err := c.runOnce(ctx, false); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("deployment recovery pass failed", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.after(controllerHeartbeat):
			if err := c.runOnce(ctx, true); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("deployment recovery pass failed", "error", err)
			}
		}
	}
}

func (c *Controller) runOnce(ctx context.Context, respectInterval bool) error {
	if c == nil || c.store == nil {
		return fmt.Errorf("recovery controller store is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.runMu.Lock()
	defer c.runMu.Unlock()

	intents, err := c.store.ListRunnableDeploymentIntents(ctx)
	if err != nil {
		return sanitizeControllerError("list runnable deployment intents", err)
	}
	now := c.now().UTC()
	var failures []error
	for _, stored := range intents {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		if stored == nil || stored.DesiredState != DesiredRunning || !stored.Policy.Enabled || stored.RecoveryState == StateQuarantined {
			continue
		}
		if respectInterval && !c.intentDue(*stored, now) {
			continue
		}
		c.lastChecked[stored.Name] = now
		if err := c.reconcileIntent(ctx, *stored); err != nil {
			wrapped := fmt.Errorf("reconcile deployment %s: %w", stored.Name, err)
			slog.Warn("deployment recovery intent failed", "deployment", stored.Name, "error", err)
			failures = append(failures, wrapped)
		}
	}
	return errors.Join(failures...)
}

func (c *Controller) reconcileIntent(ctx context.Context, intent Intent) error {
	if c.observe == nil {
		return fmt.Errorf("observe function is unavailable")
	}
	observation, err := c.observe(ctx, cloneIntent(intent))
	if err != nil {
		return sanitizeControllerError("observe", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := c.now().UTC()
	decision := Evaluate(intent, observation, now)
	if reflect.DeepEqual(intent, decision.Intent) && decision.Action == ActionNone {
		return nil
	}

	committed, ok, err := c.commit(ctx, decision.Intent, intent.Revision)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	c.auditDecision(ctx, intent, committed, decision.Action)
	if err := ctx.Err(); err != nil {
		return err
	}

	switch decision.Action {
	case ActionRecover:
		if c.apply == nil {
			return c.recordApplyFailure(ctx, committed, fmt.Errorf("apply function is unavailable"), c.now().UTC())
		}
		claimCtx := WithReconcilerClaim(ctx, committed)
		if err := c.apply(claimCtx, cloneIntent(committed)); err != nil {
			return c.recordApplyFailure(ctx, committed, err, c.now().UTC())
		}
		c.emitAudit(ctx, auditEvent(committed, AuditRecoveryApplied, AuditResultApplied, ""))
	case ActionQuarantine:
		if committed.Runtime != "docker" && committed.Runtime != "k3s" {
			return nil
		}
		if c.delete == nil {
			return c.recordQuarantineDeleteFailure(ctx, committed, fmt.Errorf("delete function is unavailable"))
		}
		claimCtx := WithReconcilerClaim(ctx, committed)
		if err := c.delete(claimCtx, cloneIntent(committed)); err != nil {
			return c.recordQuarantineDeleteFailure(ctx, committed, err)
		}
		c.emitAudit(ctx, auditEvent(committed, AuditQuarantineEnforced, AuditResultEnforced, "runtime object deleted"))
	}
	return nil
}

func (c *Controller) commit(ctx context.Context, intent Intent, expectedRevision int64) (Intent, bool, error) {
	updated, err := c.store.CompareAndSwapDeploymentIntent(ctx, &intent, expectedRevision)
	if err != nil {
		return Intent{}, false, sanitizeControllerError("commit decision", err)
	}
	if !updated {
		return Intent{}, false, nil
	}
	intent.Revision = expectedRevision + 1
	return intent, true, nil
}

func (c *Controller) recordApplyFailure(ctx context.Context, committed Intent, applyErr error, now time.Time) error {
	failure := Evaluate(committed, Observation{Exists: false, Error: applyErr.Error()}, now).Intent
	persisted, ok, err := c.commit(ctx, failure, committed.Revision)
	if err != nil {
		return errors.Join(sanitizeControllerError("apply", applyErr), err)
	}
	if ok {
		c.emitAudit(ctx, auditEvent(persisted, AuditRecoveryFailed, AuditResultFailed, persisted.LastError))
		if persisted.RecoveryState == StateQuarantined {
			c.emitAudit(ctx, auditEvent(persisted, AuditQuarantined, AuditResultCommitted, persisted.LastError))
		} else if persisted.RecoveryState == StateWaiting && !persisted.NextAttemptAt.IsZero() {
			c.emitAudit(ctx, auditEvent(persisted, AuditRetryScheduled, AuditResultScheduled, persisted.LastError))
		}
	}
	return sanitizeControllerError("apply", applyErr)
}

func (c *Controller) recordQuarantineDeleteFailure(ctx context.Context, committed Intent, deleteErr error) error {
	failure := cloneIntent(committed)
	failure.LastError = SanitizeText(deleteErr.Error())
	persisted, ok, err := c.commit(ctx, failure, committed.Revision)
	if err != nil {
		return errors.Join(sanitizeControllerError("delete quarantined deployment", deleteErr), err)
	}
	if ok {
		c.emitAudit(ctx, auditEvent(persisted, AuditQuarantineEnforcementFailed, AuditResultFailed, persisted.LastError))
	}
	return sanitizeControllerError("delete quarantined deployment", deleteErr)
}

func (c *Controller) auditDecision(ctx context.Context, previous, committed Intent, action Action) {
	switch action {
	case ActionRecover:
		c.emitAudit(ctx, auditEvent(committed, AuditRecoveryAttempt, AuditResultCommitted, committed.LastError))
	case ActionQuarantine:
		c.emitAudit(ctx, auditEvent(committed, AuditQuarantined, AuditResultCommitted, committed.LastError))
	case ActionNone:
		if previous.RecoveryState != StateHealthy && committed.RecoveryState == StateHealthy {
			c.emitAudit(ctx, auditEvent(committed, AuditRecovered, AuditResultHealthy, "health check ready"))
		}
		if committed.Runtime == "docker" || committed.Runtime == "k3s" {
			if committed.ObservedRestartCount > previous.ObservedRestartCount {
				c.emitAudit(ctx, auditEvent(committed, AuditRestartObserved, AuditResultObserved, "runtime restart count increased"))
			}
			return
		}
		if committed.RecoveryState == StateWaiting && !committed.NextAttemptAt.IsZero() &&
			(previous.RecoveryState != StateWaiting || !committed.NextAttemptAt.Equal(previous.NextAttemptAt)) {
			c.emitAudit(ctx, auditEvent(committed, AuditRetryScheduled, AuditResultScheduled, committed.LastError))
		}
	}
}

func auditEvent(intent Intent, eventType, result, reason string) AuditEvent {
	event := AuditEvent{
		Type:                 eventType,
		Source:               AuditSourceReconciler,
		Deployment:           intent.Name,
		Model:                intent.Model,
		Runtime:              intent.Runtime,
		EngineAsset:          intent.EngineAsset,
		EngineVersion:        intent.EngineVersion,
		Result:               result,
		Reason:               SanitizeText(reason),
		RecoveryState:        intent.RecoveryState,
		AttemptCount:         intent.AttemptCount,
		ObservedRestartCount: intent.ObservedRestartCount,
		Revision:             intent.Revision,
	}
	if !intent.NextAttemptAt.IsZero() {
		event.NextAttemptAt = intent.NextAttemptAt.UTC().Format(time.RFC3339)
	}
	return event
}

func (c *Controller) emitAudit(ctx context.Context, event AuditEvent) {
	if c == nil || c.store == nil {
		return
	}
	if err := c.store.LogRecoveryEvent(ctx, event); err != nil {
		slog.Warn("deployment recovery audit failed", "deployment", event.Deployment, "event", event.Type, "error", SanitizeText(err.Error()))
	}
}

func sanitizeControllerError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return fmt.Errorf("%s: %s", operation, SanitizeText(err.Error()))
}

func (c *Controller) intentDue(intent Intent, now time.Time) bool {
	last := c.lastChecked[intent.Name]
	if last.IsZero() {
		return true
	}
	interval := time.Duration(intent.Policy.CheckIntervalS) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	return !now.Before(last.Add(interval))
}
