package recovery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type controllerTestStore struct {
	mu          sync.Mutex
	intents     []*Intent
	commits     []Intent
	audits      []AuditEvent
	expected    []int64
	casResults  []bool
	casErrors   []error
	auditErrors []error
	events      []string
	listCalls   int
	listCalled  chan struct{}
}

func (s *controllerTestStore) ListRunnableDeploymentIntents(context.Context) ([]*Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	if s.listCalled != nil {
		select {
		case s.listCalled <- struct{}{}:
		default:
		}
	}
	result := make([]*Intent, len(s.intents))
	for i, intent := range s.intents {
		if intent != nil {
			copy := cloneIntent(*intent)
			result[i] = &copy
		}
	}
	return result, nil
}

func (s *controllerTestStore) CompareAndSwapDeploymentIntent(_ context.Context, intent *Intent, expectedRevision int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "cas")
	s.commits = append(s.commits, cloneIntent(*intent))
	s.expected = append(s.expected, expectedRevision)
	index := len(s.commits) - 1
	if index < len(s.casErrors) && s.casErrors[index] != nil {
		return false, s.casErrors[index]
	}
	updated := true
	if index < len(s.casResults) {
		updated = s.casResults[index]
	}
	if !updated {
		return false, nil
	}
	for i, stored := range s.intents {
		if stored == nil || stored.Name != intent.Name || stored.Revision != expectedRevision {
			continue
		}
		copy := cloneIntent(*intent)
		copy.Revision = expectedRevision + 1
		s.intents[i] = &copy
		return true, nil
	}
	return false, nil
}

func (s *controllerTestStore) LogRecoveryEvent(_ context.Context, event AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, event)
	index := len(s.audits) - 1
	if index < len(s.auditErrors) && s.auditErrors[index] != nil {
		return s.auditErrors[index]
	}
	return nil
}

func (s *controllerTestStore) snapshot() (commits []Intent, expected []int64, events []string, listCalls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	commits = append([]Intent(nil), s.commits...)
	expected = append([]int64(nil), s.expected...)
	events = append([]string(nil), s.events...)
	return commits, expected, events, s.listCalls
}

func (s *controllerTestStore) auditSnapshot() []AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]AuditEvent(nil), s.audits...)
}

func auditTypes(audits []AuditEvent) []string {
	types := make([]string, 0, len(audits))
	for _, audit := range audits {
		types = append(types, audit.Type)
	}
	return types
}

func controllerTestIntent(name, runtimeName string, now time.Time) *Intent {
	policy := DefaultPolicy()
	return &Intent{
		Name:          name,
		Model:         name + "-model",
		EngineAsset:   "generic-engine",
		EngineVersion: "1.0.0",
		Slot:          "default",
		Runtime:       runtimeName,
		Revision:      4,
		Config:        map[string]any{"ctx_size": 4096},
		DesiredState:  DesiredRunning,
		RecoveryState: StateWaiting,
		Policy:        policy,
		NextAttemptAt: now.Add(-time.Second),
	}
}

func TestControllerRecoversDueNativeIntentWithCommittedClaim(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	intent := controllerTestIntent("native-deployment", "native", now)
	store := &controllerTestStore{intents: []*Intent{intent}}
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { return Observation{Exists: false}, nil },
		func(ctx context.Context, got Intent) error {
			store.mu.Lock()
			store.events = append(store.events, "apply")
			store.mu.Unlock()
			claim, ok := ReconcilerClaim(ctx)
			if !ok {
				t.Fatal("apply context does not contain a reconciler claim")
			}
			if claim.Revision != 5 || !reflect.DeepEqual(claim, got) {
				t.Fatalf("claim = %+v, apply intent = %+v; want identical committed revision 5", claim, got)
			}
			return nil
		},
		nil,
	)
	controller.now = func() time.Time { return now }

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	commits, expected, events, _ := store.snapshot()
	if len(commits) != 1 || expected[0] != 4 {
		t.Fatalf("commits = %+v, expected revisions = %v; want one CAS from revision 4", commits, expected)
	}
	if commits[0].RecoveryState != StateRecovering || commits[0].AttemptCount != 1 {
		t.Fatalf("committed intent = %+v, want recovering attempt 1", commits[0])
	}
	if !reflect.DeepEqual(events, []string{"cas", "apply"}) {
		t.Fatalf("events = %v, want CAS before apply", events)
	}
	audits := store.auditSnapshot()
	if got, want := auditTypes(audits), []string{AuditRecoveryAttempt, AuditRecoveryApplied}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audit types = %v, want %v", got, want)
	}
	if audits[0].Source != AuditSourceReconciler || audits[0].Deployment != intent.Name || audits[0].EngineVersion != intent.EngineVersion || audits[0].AttemptCount != 1 || audits[0].Revision != 5 {
		t.Fatalf("recovery audit = %+v, want source/deployment/version/attempt/revision", audits[0])
	}
	if audits[1].Result != AuditResultApplied {
		t.Fatalf("applied audit result = %q, want %q", audits[1].Result, AuditResultApplied)
	}
}

func TestControllerRecreatesDueMissingContainer(t *testing.T) {
	now := time.Unix(10_250, 0).UTC()
	intent := controllerTestIntent("missing-container", "docker", now)
	store := &controllerTestStore{intents: []*Intent{intent}}
	applyCalls := 0
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { return Observation{Exists: false}, nil },
		func(_ context.Context, got Intent) error {
			applyCalls++
			store.mu.Lock()
			store.events = append(store.events, "apply")
			store.mu.Unlock()
			if got.Runtime != "docker" || got.RecoveryState != StateRecovering || got.AttemptCount != 1 {
				t.Fatalf("apply intent = %+v", got)
			}
			return nil
		},
		nil,
	)
	controller.now = func() time.Time { return now }

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if applyCalls != 1 {
		t.Fatalf("apply calls = %d, want 1", applyCalls)
	}
	commits, expected, events, _ := store.snapshot()
	if len(commits) != 1 || commits[0].RecoveryState != StateRecovering || expected[0] != intent.Revision {
		t.Fatalf("commits = %+v expected revisions = %v", commits, expected)
	}
	if !reflect.DeepEqual(events, []string{"cas", "apply"}) {
		t.Fatalf("store events = %v, want committed claim before apply", events)
	}
}

func TestControllerAuditFailureDoesNotBlockRecovery(t *testing.T) {
	now := time.Unix(10_500, 0).UTC()
	intent := controllerTestIntent("audit-failure", "native", now)
	store := &controllerTestStore{
		intents:     []*Intent{intent},
		auditErrors: []error{errors.New("audit store unavailable"), errors.New("audit store unavailable")},
	}
	var applyCalls int
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { return Observation{Exists: false}, nil },
		func(context.Context, Intent) error { applyCalls++; return nil },
		nil,
	)
	controller.now = func() time.Time { return now }

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if applyCalls != 1 {
		t.Fatalf("apply calls = %d, want one despite audit failure", applyCalls)
	}
	if got := auditTypes(store.auditSnapshot()); !reflect.DeepEqual(got, []string{AuditRecoveryAttempt, AuditRecoveryApplied}) {
		t.Fatalf("audit types = %v, want both attempted events", got)
	}
}

func TestControllerCASFalsePreventsRuntimeSideEffects(t *testing.T) {
	now := time.Unix(11_000, 0).UTC()
	store := &controllerTestStore{
		intents:    []*Intent{controllerTestIntent("stale", "native", now)},
		casResults: []bool{false},
	}
	var applyCalls, deleteCalls int
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { return Observation{}, nil },
		func(context.Context, Intent) error { applyCalls++; return nil },
		func(context.Context, Intent) error { deleteCalls++; return nil },
	)
	controller.now = func() time.Time { return now }

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if applyCalls != 0 || deleteCalls != 0 {
		t.Fatalf("side effects = apply %d, delete %d; want zero after failed CAS", applyCalls, deleteCalls)
	}
}

func TestControllerSkipsStoppedDisabledAndQuarantinedIntents(t *testing.T) {
	now := time.Unix(12_000, 0).UTC()
	stopped := controllerTestIntent("stopped", "native", now)
	stopped.DesiredState = DesiredStopped
	disabled := controllerTestIntent("disabled", "native", now)
	disabled.Policy.Enabled = false
	quarantined := controllerTestIntent("quarantined", "docker", now)
	quarantined.RecoveryState = StateQuarantined
	quarantined.NextAttemptAt = time.Time{}
	store := &controllerTestStore{intents: []*Intent{stopped, disabled, quarantined}}
	var observeCalls, applyCalls, deleteCalls int
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { observeCalls++; return Observation{}, nil },
		func(context.Context, Intent) error { applyCalls++; return nil },
		func(context.Context, Intent) error { deleteCalls++; return nil },
	)

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	commits, _, _, _ := store.snapshot()
	if observeCalls != 0 || applyCalls != 0 || deleteCalls != 0 || len(commits) != 0 {
		t.Fatalf("calls = observe %d, apply %d, delete %d, CAS %d; want all zero", observeCalls, applyCalls, deleteCalls, len(commits))
	}
}

func TestControllerSuppressesNoOpCASWrites(t *testing.T) {
	now := time.Unix(13_000, 0).UTC()
	intent := controllerTestIntent("starting", "native", now)
	intent.RecoveryState = StateHealthy
	intent.NextAttemptAt = time.Time{}
	store := &controllerTestStore{intents: []*Intent{intent}}
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) {
			return Observation{Exists: true, Phase: "starting"}, nil
		},
		nil,
		nil,
	)
	controller.now = func() time.Time { return now }

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	commits, _, _, _ := store.snapshot()
	if len(commits) != 0 {
		t.Fatalf("CAS writes = %d, want zero for an unchanged neutral observation", len(commits))
	}
}

func TestControllerAuditsNativeRetrySchedule(t *testing.T) {
	now := time.Unix(13_500, 0).UTC()
	intent := controllerTestIntent("retry-schedule", "native", now)
	intent.RecoveryState = StateHealthy
	intent.NextAttemptAt = time.Time{}
	intent.ConsecutiveFailureCount = intent.Policy.ConsecutiveFailures - 1
	store := &controllerTestStore{intents: []*Intent{intent}}
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { return Observation{Exists: false}, nil },
		nil,
		nil,
	)
	controller.now = func() time.Time { return now }

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	commits, _, _, _ := store.snapshot()
	if len(commits) != 1 || commits[0].RecoveryState != StateWaiting || commits[0].NextAttemptAt.IsZero() {
		t.Fatalf("committed intent = %+v, want waiting with next attempt", commits)
	}
	audits := store.auditSnapshot()
	if got, want := auditTypes(audits), []string{AuditRetryScheduled}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audit types = %v, want %v", got, want)
	}
	if audits[0].NextAttemptAt == "" || audits[0].Result != AuditResultScheduled {
		t.Fatalf("retry audit = %+v, want scheduled result and next attempt", audits[0])
	}
}

func TestControllerAuditsRecoveredHealthTransition(t *testing.T) {
	now := time.Unix(13_750, 0).UTC()
	intent := controllerTestIntent("recovered", "native", now)
	intent.AttemptCount = 1
	store := &controllerTestStore{intents: []*Intent{intent}}
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { return Observation{Exists: true, Ready: true}, nil },
		nil,
		nil,
	)
	controller.now = func() time.Time { return now }

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	audits := store.auditSnapshot()
	if got, want := auditTypes(audits), []string{AuditRecovered}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audit types = %v, want %v", got, want)
	}
	if audits[0].RecoveryState != StateHealthy || audits[0].Result != AuditResultHealthy {
		t.Fatalf("recovered audit = %+v, want healthy result", audits[0])
	}
}

func TestControllerApplyFailurePersistsWaitingBackoff(t *testing.T) {
	now := time.Unix(14_000, 0).UTC()
	intent := controllerTestIntent("retry", "native", now)
	intent.ConsecutiveFailureCount = intent.Policy.ConsecutiveFailures - 1
	store := &controllerTestStore{intents: []*Intent{intent}}
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { return Observation{}, nil },
		func(context.Context, Intent) error { return errors.New("launch failed token=secret-value") },
		nil,
	)
	controller.now = func() time.Time { return now }

	err := controller.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "launch failed") {
		t.Fatalf("RunOnce error = %v, want apply failure", err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("RunOnce error leaked credential: %v", err)
	}
	commits, expected, events, _ := store.snapshot()
	if len(commits) != 2 || !reflect.DeepEqual(expected, []int64{4, 5}) {
		t.Fatalf("commits = %+v, expected revisions = %v; want decision then failure CAS", commits, expected)
	}
	failure := commits[1]
	if failure.RecoveryState != StateWaiting || failure.NextAttemptAt.IsZero() {
		t.Fatalf("failure intent = %+v, want waiting with backoff", failure)
	}
	if strings.Contains(failure.LastError, "secret-value") || !strings.Contains(failure.LastError, "[REDACTED]") {
		t.Fatalf("persisted error = %q, want credential redaction", failure.LastError)
	}
	if !reflect.DeepEqual(events, []string{"cas", "cas"}) {
		t.Fatalf("events = %v, want decision and failure CAS writes", events)
	}
	audits := store.auditSnapshot()
	if got, want := auditTypes(audits), []string{AuditRecoveryAttempt, AuditRecoveryFailed, AuditRetryScheduled}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audit types = %v, want %v", got, want)
	}
	if strings.Contains(audits[1].Reason, "secret-value") || !strings.Contains(audits[1].Reason, "[REDACTED]") {
		t.Fatalf("failure audit reason = %q, want redacted", audits[1].Reason)
	}
}

func TestControllerFinalApplyFailureQuarantinesNativeIntent(t *testing.T) {
	now := time.Unix(15_000, 0).UTC()
	intent := controllerTestIntent("final-attempt", "native", now)
	intent.AttemptCount = intent.Policy.MaxAttempts - 1
	store := &controllerTestStore{intents: []*Intent{intent}}
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { return Observation{}, nil },
		func(context.Context, Intent) error { return errors.New("launch failed") },
		nil,
	)
	controller.now = func() time.Time { return now }

	if err := controller.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce error = nil, want final apply failure")
	}
	commits, _, _, _ := store.snapshot()
	if len(commits) != 2 || commits[1].RecoveryState != StateQuarantined || commits[1].AttemptCount != intent.Policy.MaxAttempts {
		t.Fatalf("commits = %+v, want final failed attempt quarantined", commits)
	}
	if got, want := auditTypes(store.auditSnapshot()), []string{AuditRecoveryAttempt, AuditRecoveryFailed, AuditQuarantined}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audit types = %v, want %v", got, want)
	}
}

func TestControllerApplyFailureBackoffUsesFailureTime(t *testing.T) {
	decisionTime := time.Unix(15_500, 0).UTC()
	failureTime := decisionTime.Add(20 * time.Second)
	intent := controllerTestIntent("slow-failure", "native", decisionTime)
	intent.ConsecutiveFailureCount = intent.Policy.ConsecutiveFailures - 1
	store := &controllerTestStore{intents: []*Intent{intent}}
	times := []time.Time{decisionTime, decisionTime, failureTime}
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { return Observation{}, nil },
		func(context.Context, Intent) error { return errors.New("slow launch failed") },
		nil,
	)
	controller.now = func() time.Time {
		if len(times) == 0 {
			return failureTime
		}
		value := times[0]
		times = times[1:]
		return value
	}

	if err := controller.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce error = nil, want apply failure")
	}
	commits, _, _, _ := store.snapshot()
	if len(commits) != 2 {
		t.Fatalf("commits = %+v, want decision and failure", commits)
	}
	want := failureTime.Add(commits[1].Policy.Backoff(commits[1].AttemptCount))
	if !commits[1].NextAttemptAt.Equal(want) {
		t.Fatalf("next attempt = %s, want failure-time backoff %s", commits[1].NextAttemptAt, want)
	}
}

func TestControllerQuarantinesContainerBeforeDelete(t *testing.T) {
	now := time.Unix(16_000, 0).UTC()
	intent := controllerTestIntent("container", "docker", now)
	intent.RecoveryState = StateHealthy
	intent.NextAttemptAt = time.Time{}
	intent.AttemptCount = intent.Policy.MaxAttempts - 1
	intent.ObservedRestartCount = 7
	store := &controllerTestStore{intents: []*Intent{intent}}
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) {
			return Observation{Exists: true, Ready: true, Restarts: 8}, nil
		},
		nil,
		func(ctx context.Context, got Intent) error {
			store.mu.Lock()
			store.events = append(store.events, "delete")
			store.mu.Unlock()
			claim, ok := ReconcilerClaim(ctx)
			if !ok || claim.Revision != 5 || claim.RecoveryState != StateQuarantined || !reflect.DeepEqual(claim, got) {
				t.Fatalf("delete claim = %+v, ok=%v, intent=%+v", claim, ok, got)
			}
			return nil
		},
	)
	controller.now = func() time.Time { return now }

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	commits, _, events, _ := store.snapshot()
	if len(commits) != 2 || commits[0].RecoveryState != StateQuarantined || commits[0].DesiredState != DesiredRunning || !commits[0].NextAttemptAt.Equal(now) || !commits[1].NextAttemptAt.IsZero() {
		t.Fatalf("committed intents = %+v, want pending quarantine followed by enforced quarantine", commits)
	}
	if !reflect.DeepEqual(events, []string{"cas", "delete", "cas"}) {
		t.Fatalf("events = %v, want quarantine CAS, delete, then completion CAS", events)
	}
	if got, want := auditTypes(store.auditSnapshot()), []string{AuditQuarantined, AuditQuarantineEnforced}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audit types = %v, want %v", got, want)
	}
}

func TestControllerDoesNotQuarantineHealthyContainerHistoricalRestarts(t *testing.T) {
	now := time.Unix(16_500, 0).UTC()
	intent := controllerTestIntent("healthy-container", "docker", now)
	intent.RecoveryState = StateHealthy
	intent.NextAttemptAt = time.Time{}
	store := &controllerTestStore{intents: []*Intent{intent}}
	deleteCalls := 0
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) {
			return Observation{Exists: true, Ready: true, Restarts: 3}, nil
		},
		nil,
		func(context.Context, Intent) error {
			deleteCalls++
			return nil
		},
	)
	controller.now = func() time.Time { return now }

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	commits, _, _, _ := store.snapshot()
	if len(commits) != 1 {
		t.Fatalf("commits = %+v, want one baseline commit", commits)
	}
	if got := commits[0]; got.RecoveryState != StateHealthy || got.AttemptCount != 0 || got.ObservedRestartCount != 3 {
		t.Fatalf("committed baseline = %+v, want healthy with zero attempts and restart baseline 3", got)
	}
	if deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want zero", deleteCalls)
	}
}

func TestControllerDeleteFailurePreservesQuarantineAndSanitizesError(t *testing.T) {
	now := time.Unix(17_000, 0).UTC()
	intent := controllerTestIntent("delete-failure", "k3s", now)
	intent.RecoveryState = StateHealthy
	intent.NextAttemptAt = time.Time{}
	intent.AttemptCount = intent.Policy.MaxAttempts - 1
	intent.ObservedRestartCount = 2
	store := &controllerTestStore{intents: []*Intent{intent}}
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { return Observation{Exists: true, Restarts: 3}, nil },
		nil,
		func(context.Context, Intent) error { return errors.New("delete failed authorization=top-secret") },
	)
	controller.now = func() time.Time { return now }

	if err := controller.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce error = nil, want delete failure")
	}
	commits, expected, _, _ := store.snapshot()
	if len(commits) != 2 || !reflect.DeepEqual(expected, []int64{4, 5}) {
		t.Fatalf("commits = %+v, expected revisions = %v", commits, expected)
	}
	failure := commits[1]
	if failure.DesiredState != DesiredRunning || failure.RecoveryState != StateQuarantined {
		t.Fatalf("failure intent = %+v, want desired running and quarantined", failure)
	}
	wantRetry := now.Add(failure.Policy.Backoff(failure.AttemptCount))
	if !failure.NextAttemptAt.Equal(wantRetry) {
		t.Fatalf("delete retry = %s, want %s", failure.NextAttemptAt, wantRetry)
	}
	if strings.Contains(failure.LastError, "top-secret") || !strings.Contains(failure.LastError, "[REDACTED]") {
		t.Fatalf("persisted delete error = %q, want credential redaction", failure.LastError)
	}
	audits := store.auditSnapshot()
	if got, want := auditTypes(audits), []string{AuditQuarantined, AuditQuarantineEnforcementFailed}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audit types = %v, want %v", got, want)
	}
	if strings.Contains(audits[1].Reason, "top-secret") || !strings.Contains(audits[1].Reason, "[REDACTED]") {
		t.Fatalf("quarantine audit reason = %q, want redacted", audits[1].Reason)
	}
}

func TestControllerRetriesTransientQuarantineDeleteWithoutApplying(t *testing.T) {
	now := time.Unix(17_500, 0).UTC()
	currentTime := now
	intent := controllerTestIntent("delete-retry", "docker", now)
	intent.RecoveryState = StateHealthy
	intent.NextAttemptAt = time.Time{}
	intent.AttemptCount = intent.Policy.MaxAttempts - 1
	intent.ObservedRestartCount = 2
	store := &controllerTestStore{intents: []*Intent{intent}}
	observeCalls := 0
	applyCalls := 0
	deleteCalls := 0
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) {
			observeCalls++
			return Observation{Exists: true, Restarts: 3}, nil
		},
		func(context.Context, Intent) error {
			applyCalls++
			return nil
		},
		func(context.Context, Intent) error {
			deleteCalls++
			if deleteCalls == 1 {
				return errors.New("temporary delete failure")
			}
			return nil
		},
	)
	controller.now = func() time.Time { return currentTime }

	if err := controller.RunOnce(context.Background()); err == nil {
		t.Fatal("first RunOnce error = nil, want transient delete failure")
	}
	commits, _, _, _ := store.snapshot()
	if len(commits) != 2 || commits[1].RecoveryState != StateQuarantined || commits[1].NextAttemptAt.IsZero() {
		t.Fatalf("first pass commits = %+v, want scheduled quarantine deletion retry", commits)
	}

	currentTime = commits[1].NextAttemptAt.Add(-time.Nanosecond)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("pre-backoff RunOnce: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("pre-backoff delete calls = %d, want 1", deleteCalls)
	}

	currentTime = commits[1].NextAttemptAt
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("retry RunOnce: %v", err)
	}
	commits, expected, _, _ := store.snapshot()
	if len(commits) != 3 || !commits[2].NextAttemptAt.IsZero() || commits[2].RecoveryState != StateQuarantined {
		t.Fatalf("retry commits = %+v, want completed quarantine deletion", commits)
	}
	if !reflect.DeepEqual(expected, []int64{4, 5, 6}) {
		t.Fatalf("CAS revisions = %v, want [4 5 6]", expected)
	}
	if observeCalls != 1 || applyCalls != 0 || deleteCalls != 2 {
		t.Fatalf("side effects: observe=%d apply=%d delete=%d, want 1/0/2", observeCalls, applyCalls, deleteCalls)
	}
	if got, want := auditTypes(store.auditSnapshot()), []string{AuditQuarantined, AuditQuarantineEnforcementFailed, AuditQuarantineEnforced}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audit types = %v, want %v", got, want)
	}
}

func TestControllerContinuesAfterPerIntentError(t *testing.T) {
	now := time.Unix(18_000, 0).UTC()
	broken := controllerTestIntent("broken", "native", now)
	healthy := controllerTestIntent("recoverable", "native", now)
	store := &controllerTestStore{intents: []*Intent{broken, healthy}}
	var applyNames []string
	controller := NewController(
		store,
		func(_ context.Context, intent Intent) (Observation, error) {
			if intent.Name == broken.Name {
				return Observation{}, errors.New("status unavailable")
			}
			return Observation{}, nil
		},
		func(_ context.Context, intent Intent) error {
			applyNames = append(applyNames, intent.Name)
			return nil
		},
		nil,
	)
	controller.now = func() time.Time { return now }

	err := controller.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), broken.Name) {
		t.Fatalf("RunOnce error = %v, want isolated error for %s", err, broken.Name)
	}
	if !reflect.DeepEqual(applyNames, []string{healthy.Name}) {
		t.Fatalf("applied intents = %v, want later intent reconciled", applyNames)
	}
}

type controllerCancelStore struct {
	controllerTestStore
	cancel context.CancelFunc
}

func (s *controllerCancelStore) CompareAndSwapDeploymentIntent(ctx context.Context, intent *Intent, expectedRevision int64) (bool, error) {
	updated, err := s.controllerTestStore.CompareAndSwapDeploymentIntent(ctx, intent, expectedRevision)
	if updated && s.cancel != nil {
		s.cancel()
	}
	return updated, err
}

func TestControllerCancellationBeforeSideEffectStopsRecovery(t *testing.T) {
	now := time.Unix(18_500, 0).UTC()
	ctx, cancel := context.WithCancel(context.Background())
	store := &controllerCancelStore{
		controllerTestStore: controllerTestStore{intents: []*Intent{controllerTestIntent("cancelled", "native", now)}},
		cancel:              cancel,
	}
	var applyCalls int
	controller := NewController(
		store,
		func(context.Context, Intent) (Observation, error) { return Observation{}, nil },
		func(context.Context, Intent) error { applyCalls++; return nil },
		nil,
	)
	controller.now = func() time.Time { return now }

	err := controller.RunOnce(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce error = %v, want context canceled", err)
	}
	if applyCalls != 0 {
		t.Fatalf("apply calls = %d, want zero after cancellation at CAS boundary", applyCalls)
	}
}

func TestControllerStartRunsImmediatelyUsesOneSecondHeartbeatAndStops(t *testing.T) {
	store := &controllerTestStore{listCalled: make(chan struct{}, 2)}
	controller := NewController(store, func(context.Context, Intent) (Observation, error) {
		return Observation{}, nil
	}, nil, nil)
	timerDurations := make(chan time.Duration, 1)
	timer := make(chan time.Time)
	controller.after = func(duration time.Duration) <-chan time.Time {
		timerDurations <- duration
		return timer
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		controller.Start(ctx)
		close(done)
	}()

	select {
	case <-store.listCalled:
	case <-time.After(time.Second):
		t.Fatal("Start did not run an immediate recovery pass")
	}
	select {
	case duration := <-timerDurations:
		if duration != time.Second {
			t.Fatalf("timer duration = %s, want one-second heartbeat", duration)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not schedule the next pass")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start did not stop after context cancellation")
	}
	_, _, _, listCalls := store.snapshot()
	if listCalls != 1 {
		t.Fatalf("list calls after cancellation = %d, want one immediate pass", listCalls)
	}
}

func TestControllerHonorsEachIntentCheckInterval(t *testing.T) {
	start := time.Unix(19_000, 0).UTC()
	fast := controllerTestIntent("fast", "native", start)
	fast.Policy.CheckIntervalS = 2
	slow := controllerTestIntent("slow", "native", start)
	slow.Policy.CheckIntervalS = 10
	store := &controllerTestStore{intents: []*Intent{fast, slow}}
	checked := map[string]int{}
	current := start
	controller := NewController(store, func(_ context.Context, intent Intent) (Observation, error) {
		checked[intent.Name]++
		return Observation{Exists: true, Phase: "starting"}, nil
	}, nil, nil)
	controller.now = func() time.Time { return current }

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("initial RunOnce: %v", err)
	}
	current = start.Add(2 * time.Second)
	if err := controller.runOnce(context.Background(), true); err != nil {
		t.Fatalf("second run: %v", err)
	}
	current = start.Add(10 * time.Second)
	if err := controller.runOnce(context.Background(), true); err != nil {
		t.Fatalf("third run: %v", err)
	}
	if !reflect.DeepEqual(checked, map[string]int{"fast": 3, "slow": 2}) {
		t.Fatalf("observation counts = %v, want independent 2s and 10s schedules", checked)
	}
}

type controllerTestTick struct {
	duration time.Duration
	ch       chan time.Time
}

func awaitControllerTestValue[T any](t *testing.T, ch <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func TestControllerStartKeepsHeartbeatAfterFastestIntentRemoved(t *testing.T) {
	start := time.Unix(20_000, 0).UTC()
	fast := controllerTestIntent("fast", "native", start)
	fast.Policy.CheckIntervalS = 1
	slow := controllerTestIntent("slow", "native", start)
	slow.Policy.CheckIntervalS = 3
	store := &controllerTestStore{
		intents:    []*Intent{fast, slow},
		listCalled: make(chan struct{}, 4),
	}
	observed := make(chan string, 4)
	var nowUnix atomic.Int64
	nowUnix.Store(start.Unix())
	controller := NewController(store, func(_ context.Context, intent Intent) (Observation, error) {
		observed <- intent.Name
		return Observation{Exists: true, Phase: "starting"}, nil
	}, nil, nil)
	controller.now = func() time.Time { return time.Unix(nowUnix.Load(), 0).UTC() }
	ticks := make(chan controllerTestTick, 4)
	controller.after = func(duration time.Duration) <-chan time.Time {
		tick := controllerTestTick{duration: duration, ch: make(chan time.Time, 1)}
		ticks <- tick
		return tick.ch
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		controller.Start(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		awaitControllerTestValue(t, done, "controller shutdown")
	})

	awaitControllerTestValue(t, store.listCalled, "initial intent list")
	got := []string{
		awaitControllerTestValue(t, observed, "initial fast observation"),
		awaitControllerTestValue(t, observed, "initial slow observation"),
	}
	if !reflect.DeepEqual(got, []string{"fast", "slow"}) {
		t.Fatalf("initial observations = %v, want fast and slow", got)
	}
	firstTick := awaitControllerTestValue(t, ticks, "initial heartbeat")
	if firstTick.duration != time.Second {
		t.Fatalf("initial heartbeat = %s, want 1s", firstTick.duration)
	}

	store.mu.Lock()
	store.intents = []*Intent{slow}
	store.mu.Unlock()
	nowUnix.Store(start.Add(time.Second).Unix())
	firstTick.ch <- time.Time{}
	awaitControllerTestValue(t, store.listCalled, "intent list after fastest removal")
	secondTick := awaitControllerTestValue(t, ticks, "heartbeat after fastest removal")
	if secondTick.duration != time.Second {
		t.Fatalf("heartbeat after fastest removal = %s, want 1s", secondTick.duration)
	}
	select {
	case name := <-observed:
		t.Fatalf("observed %q before its 3s interval elapsed", name)
	default:
	}

	nowUnix.Store(start.Add(3 * time.Second).Unix())
	secondTick.ch <- time.Time{}
	awaitControllerTestValue(t, store.listCalled, "intent list at slow interval")
	if name := awaitControllerTestValue(t, observed, "slow observation at its interval"); name != "slow" {
		t.Fatalf("observation at slow interval = %q, want slow", name)
	}
	if tick := awaitControllerTestValue(t, ticks, "heartbeat after slow interval"); tick.duration != time.Second {
		t.Fatalf("heartbeat after slow interval = %s, want 1s", tick.duration)
	}
}

func TestControllerStartDiscoversNewShorterIntentOnNextHeartbeat(t *testing.T) {
	start := time.Unix(21_000, 0).UTC()
	slow := controllerTestIntent("slow", "native", start)
	slow.Policy.CheckIntervalS = 10
	store := &controllerTestStore{
		intents:    []*Intent{slow},
		listCalled: make(chan struct{}, 3),
	}
	observed := make(chan string, 3)
	var nowUnix atomic.Int64
	nowUnix.Store(start.Unix())
	controller := NewController(store, func(_ context.Context, intent Intent) (Observation, error) {
		observed <- intent.Name
		return Observation{Exists: true, Phase: "starting"}, nil
	}, nil, nil)
	controller.now = func() time.Time { return time.Unix(nowUnix.Load(), 0).UTC() }
	ticks := make(chan controllerTestTick, 3)
	controller.after = func(duration time.Duration) <-chan time.Time {
		tick := controllerTestTick{duration: duration, ch: make(chan time.Time, 1)}
		ticks <- tick
		return tick.ch
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		controller.Start(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		awaitControllerTestValue(t, done, "controller shutdown")
	})

	awaitControllerTestValue(t, store.listCalled, "initial intent list")
	if name := awaitControllerTestValue(t, observed, "initial slow observation"); name != "slow" {
		t.Fatalf("initial observation = %q, want slow", name)
	}
	firstTick := awaitControllerTestValue(t, ticks, "initial heartbeat")
	if firstTick.duration != time.Second {
		t.Fatalf("initial heartbeat = %s, want 1s", firstTick.duration)
	}

	fast := controllerTestIntent("new-fast", "native", start)
	fast.Policy.CheckIntervalS = 2
	store.mu.Lock()
	store.intents = []*Intent{slow, fast}
	store.mu.Unlock()
	nowUnix.Store(start.Add(time.Second).Unix())
	firstTick.ch <- time.Time{}
	awaitControllerTestValue(t, store.listCalled, "intent list after faster addition")
	if name := awaitControllerTestValue(t, observed, "new faster intent observation"); name != "new-fast" {
		t.Fatalf("next-heartbeat observation = %q, want newly added faster intent", name)
	}
	if tick := awaitControllerTestValue(t, ticks, "heartbeat after faster addition"); tick.duration != time.Second {
		t.Fatalf("heartbeat after faster addition = %s, want 1s", tick.duration)
	}
}
