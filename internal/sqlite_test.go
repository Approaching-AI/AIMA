package state

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jguan/aima/internal/recovery"
)

func mustOpen(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenClose(t *testing.T) {
	db := mustOpen(t)
	if db == nil {
		t.Fatal("expected non-nil DB")
	}
}

func TestMigrateV20CreatesDeploymentIntents(t *testing.T) {
	db := mustOpen(t)

	var version int
	if err := db.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 20 {
		t.Fatalf("user_version=%d want at least 20", version)
	}
	for _, col := range []string{
		"name", "model", "engine_asset", "engine_version", "slot", "runtime", "revision",
		"config_json", "desired_state", "recovery_state", "recovery_policy_json",
		"attempt_count", "consecutive_failure_count", "observed_restart_count",
		"window_started_at", "next_attempt_at", "healthy_since", "last_exit_code", "last_error",
		"created_at", "updated_at",
	} {
		var count int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('deployment_intents') WHERE name = ?`, col).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("missing %s", col)
		}
	}
	for _, index := range []struct {
		name    string
		columns []string
	}{
		{"idx_deployment_intents_desired_recovery", []string{"desired_state", "recovery_state"}},
		{"idx_deployment_intents_next_attempt_at", []string{"next_attempt_at"}},
	} {
		rows, err := db.db.Query(`SELECT seqno, name FROM pragma_index_info(?) ORDER BY seqno`, index.name)
		if err != nil {
			t.Fatalf("index info for %s: %v", index.name, err)
		}
		var columns []string
		for rows.Next() {
			var seq int
			var name string
			if err := rows.Scan(&seq, &name); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			columns = append(columns, name)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		if !reflect.DeepEqual(columns, index.columns) {
			t.Errorf("index %s columns = %v, want %v", index.name, columns, index.columns)
		}
	}
}

func TestDeploymentIntentRoundTripsSanitizedConfig(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()
	exitCode := 17
	intent := &recovery.Intent{
		Name:                    "intent-1",
		Model:                   "model-1",
		EngineAsset:             "engine-1",
		EngineVersion:           "1.2.3",
		Slot:                    "slot-a",
		Runtime:                 "native",
		Revision:                4,
		Config:                  map[string]any{"ctx_size": 8192, "api_key": "secret", "nested": map[string]any{"token": "nested-secret"}},
		DesiredState:            recovery.DesiredRunning,
		RecoveryState:           recovery.StateWaiting,
		Policy:                  recovery.DefaultPolicy(),
		AttemptCount:            2,
		ConsecutiveFailureCount: 1,
		ObservedRestartCount:    3,
		WindowStartedAt:         time.Date(2026, 7, 30, 8, 9, 10, 123456789, time.FixedZone("UTC+8", 8*60*60)),
		NextAttemptAt:           time.Date(2026, 7, 30, 0, 10, 11, 0, time.UTC),
		HealthySince:            time.Date(2026, 7, 29, 23, 0, 0, 0, time.UTC),
		LastExitCode:            &exitCode,
		LastError:               "startup failed",
		CreatedAt:               time.Date(2026, 7, 29, 22, 0, 0, 0, time.UTC),
		UpdatedAt:               time.Date(2026, 7, 29, 22, 1, 0, 0, time.UTC),
	}
	if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("UpsertDeploymentIntent: %v", err)
	}

	got, err := db.GetDeploymentIntent(ctx, intent.Name)
	if err != nil {
		t.Fatalf("GetDeploymentIntent: %v", err)
	}
	if got.Name != intent.Name || got.Model != intent.Model || got.EngineAsset != intent.EngineAsset || got.EngineVersion != intent.EngineVersion || got.Slot != intent.Slot || got.Runtime != intent.Runtime || got.Revision != intent.Revision {
		t.Fatalf("identity fields = %#v", got)
	}
	if got.DesiredState != recovery.DesiredRunning || got.RecoveryState != recovery.StateWaiting || got.AttemptCount != 2 || got.ConsecutiveFailureCount != 1 || got.ObservedRestartCount != 3 || got.LastError != "startup failed" {
		t.Fatalf("recovery fields = %#v", got)
	}
	if got.LastExitCode == nil || *got.LastExitCode != exitCode {
		t.Fatalf("LastExitCode = %v, want %d", got.LastExitCode, exitCode)
	}
	if !reflect.DeepEqual(got.Policy, intent.Policy) {
		t.Fatalf("Policy = %#v, want %#v", got.Policy, intent.Policy)
	}
	if !got.WindowStartedAt.Equal(intent.WindowStartedAt.UTC()) || !got.NextAttemptAt.Equal(intent.NextAttemptAt.UTC()) || !got.HealthySince.Equal(intent.HealthySince.UTC()) || !got.CreatedAt.Equal(intent.CreatedAt.UTC()) || !got.UpdatedAt.Equal(intent.UpdatedAt.UTC()) {
		t.Fatalf("timestamps = %#v", got)
	}
	ctxSize, ok := got.Config["ctx_size"].(json.Number)
	if !ok || ctxSize.String() != "8192" || got.Config["api_key"] != "[REDACTED]" {
		t.Fatalf("Config = %#v", got.Config)
	}
	nested, ok := got.Config["nested"].(map[string]any)
	if !ok || nested["token"] != "[REDACTED]" {
		t.Fatalf("nested config = %#v", got.Config["nested"])
	}

	var configJSON string
	if err := db.RawDB().QueryRowContext(ctx, `SELECT config_json FROM deployment_intents WHERE name = ?`, intent.Name).Scan(&configJSON); err != nil {
		t.Fatalf("select stored config: %v", err)
	}
	if strings.Contains(configJSON, "secret") {
		t.Fatalf("stored config leaks a secret: %s", configJSON)
	}
}

func TestDeploymentIntentRoundTripPreservesLargeIntegerJSONToken(t *testing.T) {
	const want = "9007199254740993"
	db := mustOpen(t)
	ctx := context.Background()
	intent := &recovery.Intent{
		Name:          "intent-large-integer",
		Config:        map[string]any{"large_integer": uint64(9007199254740993)},
		DesiredState:  recovery.DesiredRunning,
		RecoveryState: recovery.StateHealthy,
		Policy:        recovery.DefaultPolicy(),
	}
	if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	var configJSON string
	if err := db.RawDB().QueryRowContext(ctx, `SELECT config_json FROM deployment_intents WHERE name = ?`, intent.Name).Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if configJSON != `{"large_integer":9007199254740993}` {
		t.Fatalf("stored config JSON = %s", configJSON)
	}
	got, err := db.GetDeploymentIntent(ctx, intent.Name)
	if err != nil {
		t.Fatal(err)
	}
	number, ok := got.Config["large_integer"].(json.Number)
	if !ok || number.String() != want {
		t.Fatalf("retrieved large integer = %#v", got.Config["large_integer"])
	}
}

func TestDeploymentIntentCASRejectsStaleRevision(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()
	intent := &recovery.Intent{Name: "intent-cas", Revision: 1, DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateHealthy, Policy: recovery.DefaultPolicy()}
	if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	intent.RecoveryState = recovery.StateWaiting
	ok, err := db.CompareAndSwapDeploymentIntent(ctx, intent, 99)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("stale revision updated row")
	}

	ok, err = db.CompareAndSwapDeploymentIntent(ctx, intent, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("current revision did not update row")
	}
	got, err := db.GetDeploymentIntent(ctx, intent.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || got.RecoveryState != recovery.StateWaiting {
		t.Fatalf("CAS result = %#v", got)
	}
}

func TestDeploymentIntentCASUsesFreshAuditTimeAndDoesNotMutateStaleCaller(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()
	initialUpdatedAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	intent := &recovery.Intent{
		Name:          "intent-cas-audit",
		Revision:      1,
		DesiredState:  recovery.DesiredRunning,
		RecoveryState: recovery.StateHealthy,
		Policy:        recovery.DefaultPolicy(),
		UpdatedAt:     initialUpdatedAt,
	}
	if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	caller, err := db.GetDeploymentIntent(ctx, intent.Name)
	if err != nil {
		t.Fatal(err)
	}
	caller.RecoveryState = recovery.StateWaiting
	staleBefore := *caller
	ok, err := db.CompareAndSwapDeploymentIntent(ctx, caller, 99)
	if err != nil || ok {
		t.Fatalf("stale CompareAndSwapDeploymentIntent = (%v, %v)", ok, err)
	}
	if !reflect.DeepEqual(*caller, staleBefore) {
		t.Fatalf("stale CAS mutated caller: %#v", caller)
	}

	ok, err = db.CompareAndSwapDeploymentIntent(ctx, caller, 1)
	if err != nil || !ok {
		t.Fatalf("current CompareAndSwapDeploymentIntent = (%v, %v)", ok, err)
	}
	if !caller.UpdatedAt.After(initialUpdatedAt) || caller.Revision != 2 {
		t.Fatalf("successful CAS caller = %#v", caller)
	}
	stored, err := db.GetDeploymentIntent(ctx, intent.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.UpdatedAt.After(initialUpdatedAt) || !stored.UpdatedAt.Equal(caller.UpdatedAt) {
		t.Fatalf("stored updated_at = %v, caller = %v", stored.UpdatedAt, caller.UpdatedAt)
	}
}

func TestDeploymentIntentPersistenceRedactsConfigAndLastErrorWithoutMutatingCaller(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()
	typedMap := map[string]string{"openai_api_key": "upsert-key-secret"}
	typedSlice := []map[string]string{{"github_token": "upsert-token-secret"}}
	intent := &recovery.Intent{
		Name:          "intent-redaction",
		Revision:      1,
		Config:        map[string]any{"typed_map": typedMap, "typed_slice": typedSlice},
		DesiredState:  recovery.DesiredRunning,
		RecoveryState: recovery.StateHealthy,
		Policy:        recovery.DefaultPolicy(),
		LastError:     "api_key=upsert-error-secret; Authorization: Bearer upsert-bearer-secret",
	}
	if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if typedMap["openai_api_key"] != "upsert-key-secret" || typedSlice[0]["github_token"] != "upsert-token-secret" || strings.Contains(intent.LastError, "[REDACTED]") {
		t.Fatalf("persistence mutated caller: %#v", intent)
	}

	assertStoredIntentSecretsRedacted(t, db, ctx, intent.Name, []string{"upsert-key-secret", "upsert-token-secret", "upsert-error-secret", "upsert-bearer-secret"})
	intent, err := db.GetDeploymentIntent(ctx, intent.Name)
	if err != nil {
		t.Fatal(err)
	}
	intent.Config = map[string]any{"service_token": "cas-token-secret"}
	intent.LastError = "password: cas-password-secret; bearer cas-bearer-secret"
	ok, err := db.CompareAndSwapDeploymentIntent(ctx, intent, 1)
	if err != nil || !ok {
		t.Fatalf("CompareAndSwapDeploymentIntent = (%v, %v)", ok, err)
	}
	assertStoredIntentSecretsRedacted(t, db, ctx, intent.Name, []string{"cas-token-secret", "cas-password-secret", "cas-bearer-secret"})
}

func TestDeploymentIntentReadReportsMalformedJSONAndTimestamps(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()
	intent := &recovery.Intent{Name: "intent-invalid-read", DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateHealthy, Policy: recovery.DefaultPolicy()}
	if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RawDB().ExecContext(ctx, `UPDATE deployment_intents SET config_json = '{' WHERE name = ?`, intent.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetDeploymentIntent(ctx, intent.Name); err == nil || !strings.Contains(err.Error(), "decode config JSON") {
		t.Fatalf("GetDeploymentIntent malformed config error = %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx, `UPDATE deployment_intents SET config_json = '{}', recovery_policy_json = '{' WHERE name = ?`, intent.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetDeploymentIntent(ctx, intent.Name); err == nil || !strings.Contains(err.Error(), "decode recovery policy JSON") {
		t.Fatalf("GetDeploymentIntent malformed recovery policy error = %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx, `UPDATE deployment_intents SET config_json = '{} {}', recovery_policy_json = '{"enabled":true}' WHERE name = ?`, intent.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetDeploymentIntent(ctx, intent.Name); err == nil || !strings.Contains(err.Error(), "decode config JSON") {
		t.Fatalf("GetDeploymentIntent trailing config error = %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx, `UPDATE deployment_intents SET config_json = '{}', updated_at = 'not-a-timestamp' WHERE name = ?`, intent.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetDeploymentIntent(ctx, intent.Name); err == nil || !strings.Contains(err.Error(), "decode updated_at") {
		t.Fatalf("GetDeploymentIntent malformed timestamp error = %v", err)
	}
}

func TestDeploymentIntentPersistenceRejectsUnsupportedConfigValues(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()
	unsupported := make(chan int)
	intent := &recovery.Intent{Name: "intent-invalid-write", Config: map[string]any{"unsupported": unsupported}, DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateHealthy, Policy: recovery.DefaultPolicy()}
	if err := db.UpsertDeploymentIntent(ctx, intent); err == nil || !strings.Contains(err.Error(), "encode deployment intent config") {
		t.Fatalf("UpsertDeploymentIntent unsupported config error = %v", err)
	}
	intent.Config = nil
	if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	intent.Config = map[string]any{"unsupported": unsupported}
	if ok, err := db.CompareAndSwapDeploymentIntent(ctx, intent, 0); ok || err == nil || !strings.Contains(err.Error(), "encode deployment intent config") {
		t.Fatalf("CompareAndSwapDeploymentIntent unsupported config = (%v, %v)", ok, err)
	}
}

func TestDeploymentIntentPersistenceRejectsCyclicConfigWithoutPartialRow(t *testing.T) {
	type recursiveConfig struct {
		Next *recursiveConfig `json:"next"`
	}
	mapCycle := map[string]any{}
	mapCycle["self"] = mapCycle
	pointerCycle := &recursiveConfig{}
	pointerCycle.Next = pointerCycle

	db := mustOpen(t)
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		config map[string]any
	}{
		{"map", map[string]any{"cycle": mapCycle}},
		{"pointer", map[string]any{"cycle": pointerCycle}},
	} {
		t.Run(test.name, func(t *testing.T) {
			intent := &recovery.Intent{Name: "intent-cycle-" + test.name, Config: test.config, DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateHealthy, Policy: recovery.DefaultPolicy()}
			if err := db.UpsertDeploymentIntent(ctx, intent); err == nil || !strings.Contains(err.Error(), "encode deployment intent config") {
				t.Fatalf("UpsertDeploymentIntent cycle error = %v", err)
			}
			var count int
			if err := db.RawDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_intents WHERE name = ?`, intent.Name).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("partial row count = %d", count)
			}
		})
	}
}

func assertStoredIntentSecretsRedacted(t *testing.T, db *DB, ctx context.Context, name string, secrets []string) {
	t.Helper()
	var configJSON, lastError string
	if err := db.RawDB().QueryRowContext(ctx, `SELECT config_json, last_error FROM deployment_intents WHERE name = ?`, name).Scan(&configJSON, &lastError); err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(configJSON, secret) || strings.Contains(lastError, secret) {
			t.Fatalf("stored intent leaks %q: config=%q last_error=%q", secret, configJSON, lastError)
		}
	}
}

func TestListRunnableDeploymentIntentsIncludesPendingQuarantineEnforcement(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()
	for _, intent := range []*recovery.Intent{
		{Name: "healthy", DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateHealthy, Policy: recovery.DefaultPolicy()},
		{Name: "waiting", DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateWaiting, Policy: recovery.DefaultPolicy()},
		{Name: "quarantine-enforced", Runtime: "docker", DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateQuarantined, Policy: recovery.DefaultPolicy()},
		{Name: "quarantine-pending", Runtime: "docker", DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateQuarantined, Policy: recovery.DefaultPolicy(), NextAttemptAt: time.Now().UTC()},
		{Name: "native-quarantine", Runtime: "native", DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateQuarantined, Policy: recovery.DefaultPolicy(), NextAttemptAt: time.Now().UTC()},
		{Name: "stopped", DesiredState: recovery.DesiredStopped, RecoveryState: recovery.StateWaiting, Policy: recovery.DefaultPolicy()},
	} {
		if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
			t.Fatalf("UpsertDeploymentIntent(%s): %v", intent.Name, err)
		}
	}

	got, err := db.ListRunnableDeploymentIntents(ctx)
	if err != nil {
		t.Fatalf("ListRunnableDeploymentIntents: %v", err)
	}
	names := make([]string, 0, len(got))
	for _, intent := range got {
		names = append(names, intent.Name)
	}
	if !reflect.DeepEqual(names, []string{"healthy", "quarantine-pending", "waiting"}) {
		t.Fatalf("runnable intent names = %v", names)
	}
}

func TestListDeploymentIntentsIncludesStoppedAndQuarantined(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()
	for _, intent := range []*recovery.Intent{
		{Name: "healthy", DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateHealthy, Policy: recovery.DefaultPolicy()},
		{Name: "quarantined", DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateQuarantined, Policy: recovery.DefaultPolicy()},
		{Name: "stopped", DesiredState: recovery.DesiredStopped, RecoveryState: recovery.StateHealthy, Policy: recovery.DefaultPolicy()},
	} {
		if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
			t.Fatalf("UpsertDeploymentIntent(%s): %v", intent.Name, err)
		}
	}

	got, err := db.ListDeploymentIntents(ctx)
	if err != nil {
		t.Fatalf("ListDeploymentIntents: %v", err)
	}
	names := make([]string, 0, len(got))
	for _, intent := range got {
		names = append(names, intent.Name)
	}
	if !reflect.DeepEqual(names, []string{"healthy", "quarantined", "stopped"}) {
		t.Fatalf("intent names = %v", names)
	}
}

func TestStopDeploymentIntentStopsAndClearsScheduledAttempt(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()
	intent := &recovery.Intent{
		Name:          "intent-stop",
		Revision:      5,
		DesiredState:  recovery.DesiredRunning,
		RecoveryState: recovery.StateWaiting,
		Policy:        recovery.DefaultPolicy(),
		NextAttemptAt: time.Now().UTC().Add(time.Minute),
	}
	if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err := db.StopDeploymentIntent(ctx, intent.Name); err != nil {
		t.Fatalf("StopDeploymentIntent: %v", err)
	}
	got, err := db.GetDeploymentIntent(ctx, intent.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != recovery.DesiredStopped || !got.NextAttemptAt.IsZero() || got.Revision != 6 {
		t.Fatalf("stopped intent = %#v", got)
	}
}

func TestIntentConfigRedactsSensitiveKeys(t *testing.T) {
	got := recovery.SanitizeConfig(map[string]any{
		"ctx_size": 8192,
		"api_key":  "secret",
		"nested":   map[string]any{"token": "x"},
	})
	if got["ctx_size"] != 8192 || got["api_key"] != "[REDACTED]" {
		t.Fatalf("got %#v", got)
	}
	nested, ok := got["nested"].(map[string]any)
	if !ok || nested["token"] != "[REDACTED]" {
		t.Fatalf("nested config = %#v", got["nested"])
	}
}

func TestExternalServiceUpsertAndList(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	first := &ExternalService{
		ID:           "external-1",
		BaseURL:      "http://127.0.0.1:8009",
		Kind:         "healthz",
		Status:       "reachable",
		Source:       "scan",
		ModelsJSON:   `["SenseVoiceSmall"]`,
		MetadataJSON: `{"status":"ok"}`,
	}
	if err := db.UpsertExternalService(ctx, first); err != nil {
		t.Fatalf("UpsertExternalService(first): %v", err)
	}

	second := &ExternalService{
		ID:           "external-1",
		BaseURL:      "http://127.0.0.1:8009",
		Kind:         "healthz",
		Status:       "reachable",
		Source:       "scan",
		ModelsJSON:   `["SenseVoiceSmall","pyannote"]`,
		MetadataJSON: `{"status":"ok","version":"2"}`,
	}
	if err := db.UpsertExternalService(ctx, second); err != nil {
		t.Fatalf("UpsertExternalService(second): %v", err)
	}

	services, err := db.ListExternalServices(ctx)
	if err != nil {
		t.Fatalf("ListExternalServices: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("len = %d, want 1", len(services))
	}
	got := services[0]
	if got.BaseURL != "http://127.0.0.1:8009" {
		t.Fatalf("BaseURL = %q, want http://127.0.0.1:8009", got.BaseURL)
	}
	if got.ModelsJSON != `["SenseVoiceSmall","pyannote"]` {
		t.Fatalf("ModelsJSON = %s, want updated models", got.ModelsJSON)
	}
	if got.FirstSeenAt.IsZero() || got.LastSeenAt.IsZero() {
		t.Fatalf("timestamps should be populated: %+v", got)
	}
	if got.LastSeenAt.Before(got.FirstSeenAt) {
		t.Fatalf("LastSeenAt %s before FirstSeenAt %s", got.LastSeenAt, got.FirstSeenAt)
	}
}

func TestExternalServiceImportedFlagPersistsAcrossScanUpserts(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	svc := &ExternalService{
		ID:         "external-2",
		BaseURL:    "http://127.0.0.1:8004",
		Kind:       "openai",
		Status:     "reachable",
		Source:     "scan",
		ModelsJSON: `["whisper-large-v3-hf"]`,
	}
	if err := db.UpsertExternalService(ctx, svc); err != nil {
		t.Fatalf("UpsertExternalService: %v", err)
	}
	if err := db.SetExternalServiceImported(ctx, "http://127.0.0.1:8004", true); err != nil {
		t.Fatalf("SetExternalServiceImported: %v", err)
	}
	if err := db.UpsertExternalService(ctx, svc); err != nil {
		t.Fatalf("second UpsertExternalService: %v", err)
	}

	services, err := db.ListExternalServices(ctx)
	if err != nil {
		t.Fatalf("ListExternalServices: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("len = %d, want 1", len(services))
	}
	if !services[0].Imported {
		t.Fatal("Imported = false, want true after scan refresh")
	}
}

func TestExternalServiceImportedModelsPersistAcrossScanUpserts(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	svc := &ExternalService{
		ID:         "external-3",
		BaseURL:    "http://127.0.0.1:8005",
		Kind:       "openai",
		Status:     "reachable",
		Source:     "scan",
		ModelsJSON: `["old-model","new-model"]`,
	}
	if err := db.UpsertExternalService(ctx, svc); err != nil {
		t.Fatalf("UpsertExternalService: %v", err)
	}
	if err := db.SetExternalServiceImportedModels(ctx, svc.BaseURL, true, []string{"new-model"}); err != nil {
		t.Fatalf("SetExternalServiceImportedModels: %v", err)
	}
	svc.ModelsJSON = `["old-model","new-model","later-model"]`
	if err := db.UpsertExternalService(ctx, svc); err != nil {
		t.Fatalf("second UpsertExternalService: %v", err)
	}

	services, err := db.ListExternalServices(ctx)
	if err != nil {
		t.Fatalf("ListExternalServices: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("len = %d, want 1", len(services))
	}
	if !services[0].Imported {
		t.Fatal("Imported = false, want true after selected import")
	}
	if services[0].ImportedModelsJSON != `["new-model"]` {
		t.Fatalf("ImportedModelsJSON = %s, want selected model subset", services[0].ImportedModelsJSON)
	}
}

func TestSchemaIncludesModelVariantGPUCountMin(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	rows, err := db.RawDB().QueryContext(ctx, "PRAGMA table_info(model_variants)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(model_variants): %v", err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var (
			cid        int
			name       string
			typ        string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &primaryKey); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if name == "gpu_count_min" {
			found = true
			if !strings.EqualFold(typ, "INTEGER") {
				t.Errorf("gpu_count_min type = %q, want INTEGER", typ)
			}
			break
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	if !found {
		t.Fatal("expected model_variants.gpu_count_min column")
	}
}

func TestDeletedDeploymentTombstones(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	deleteAt := time.Now().UTC().Truncate(time.Second)
	if err := db.MarkDeletedDeployments(ctx, deleteAt, "qwen3-8b-vllm", "qwen3-8b"); err != nil {
		t.Fatalf("MarkDeletedDeployments: %v", err)
	}

	marks, err := db.ListDeletedDeploymentsSince(ctx, deleteAt.Add(-1*time.Second))
	if err != nil {
		t.Fatalf("ListDeletedDeploymentsSince: %v", err)
	}
	if len(marks) != 2 {
		t.Fatalf("len(marks) = %d, want 2", len(marks))
	}
	if got := marks["qwen3-8b-vllm"]; !got.Equal(deleteAt) {
		t.Fatalf("marks[qwen3-8b-vllm] = %v, want %v", got, deleteAt)
	}

	if err := db.PruneDeletedDeploymentsBefore(ctx, deleteAt.Add(1*time.Second)); err != nil {
		t.Fatalf("PruneDeletedDeploymentsBefore: %v", err)
	}
	marks, err = db.ListDeletedDeploymentsSince(ctx, deleteAt.Add(-1*time.Second))
	if err != nil {
		t.Fatalf("ListDeletedDeploymentsSince(after prune): %v", err)
	}
	if len(marks) != 0 {
		t.Fatalf("len(marks after prune) = %d, want 0", len(marks))
	}
}

func TestOpenConcurrent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	for i := 0; i < 8; i++ {
		dbPath := filepath.Join(dir, fmt.Sprintf("aima-%d.db", i))
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make(chan *DB, 2)
		errs := make(chan error, 2)

		openFn := func() {
			defer wg.Done()
			<-start
			db, err := Open(ctx, dbPath)
			if err != nil {
				errs <- err
				return
			}
			results <- db
		}

		wg.Add(2)
		go openFn()
		go openFn()
		close(start)
		wg.Wait()
		close(errs)
		close(results)

		var gotErr error
		for err := range errs {
			if gotErr == nil {
				gotErr = err
			}
		}
		for db := range results {
			_ = db.Close()
		}
		if gotErr != nil {
			t.Fatalf("concurrent Open(%s): %v", dbPath, gotErr)
		}
	}
}

func TestLookupEngineExecutionHintsResolvesTypeToHardwareCompat(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO hardware_profiles (id, name, gpu_arch) VALUES ('nvidia-rtx4090-x86', 'RTX 4090', 'Ada')`); err != nil {
		t.Fatalf("insert hardware profile: %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO engine_assets (id, type) VALUES ('sglang-kt-ada', 'sglang-kt')`); err != nil {
		t.Fatalf("insert engine asset: %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO engine_hardware_compat (engine_id, hardware_id, cpu_offload, ssd_offload, npu_offload)
		 VALUES ('sglang-kt-ada', 'nvidia-rtx4090-x86', 1, 0, 0)`); err != nil {
		t.Fatalf("insert engine compat: %v", err)
	}

	hints, err := db.LookupEngineExecutionHints(ctx, "sglang-kt", "nvidia-rtx4090-x86")
	if err != nil {
		t.Fatalf("LookupEngineExecutionHints: %v", err)
	}
	if !hints.CPUOffload || hints.SSDOffload || hints.NPUOffload {
		t.Fatalf("hints = %#v, want CPU offload only", hints)
	}
}

func TestBuildHeterogeneousObservationUsesHintsNotEngineName(t *testing.T) {
	got := BuildHeterogeneousObservation(EngineExecutionHints{CPUOffload: true}, map[string]any{
		"n_gpu_layers":        40,
		"threadpool_count":    2,
		"mem_fraction_static": 0.85,
	}, map[string]any{
		"ram_usage_mib":       32768,
		"cpu_usage_pct":       61.5,
		"vram_usage_mib":      84424,
		"gpu_utilization_pct": 58.0,
	})
	if got["path"] != "gpu+cpu" {
		t.Fatalf("path = %#v, want gpu+cpu", got["path"])
	}
	if got["cpu_offload"] != true {
		t.Fatalf("cpu_offload = %#v, want true", got["cpu_offload"])
	}
	if got["n_gpu_layers"] != 40 {
		t.Fatalf("n_gpu_layers = %#v, want 40", got["n_gpu_layers"])
	}
	if got["threadpool_count"] != 2 {
		t.Fatalf("threadpool_count = %#v, want 2", got["threadpool_count"])
	}
	if _, ok := got["mem_fraction_static"]; ok {
		t.Fatalf("unexpected generic config key in observation: %#v", got)
	}
}

func TestModelCRUD(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	m := &Model{
		ID:             "m-001",
		Name:           "qwen3-8b",
		Type:           "llm",
		Path:           "/data/models/qwen3-8b",
		Format:         "safetensors",
		SizeBytes:      16_000_000_000,
		DetectedArch:   "qwen",
		DetectedParams: "8B",
		Status:         "registered",
	}

	t.Run("insert and get", func(t *testing.T) {
		if err := db.InsertModel(ctx, m); err != nil {
			t.Fatalf("InsertModel: %v", err)
		}
		got, err := db.GetModel(ctx, "m-001")
		if err != nil {
			t.Fatalf("GetModel: %v", err)
		}
		if got.Name != "qwen3-8b" {
			t.Errorf("Name = %q, want %q", got.Name, "qwen3-8b")
		}
		if got.SizeBytes != 16_000_000_000 {
			t.Errorf("SizeBytes = %d, want %d", got.SizeBytes, 16_000_000_000)
		}
		if got.Status != "registered" {
			t.Errorf("Status = %q, want %q", got.Status, "registered")
		}
		if got.CreatedAt.IsZero() {
			t.Error("CreatedAt should be set")
		}
	})

	t.Run("list", func(t *testing.T) {
		models, err := db.ListModels(ctx)
		if err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if len(models) != 1 {
			t.Fatalf("len = %d, want 1", len(models))
		}
	})

	t.Run("update status", func(t *testing.T) {
		if err := db.UpdateModelStatus(ctx, "m-001", "downloading"); err != nil {
			t.Fatalf("UpdateModelStatus: %v", err)
		}
		got, _ := db.GetModel(ctx, "m-001")
		if got.Status != "downloading" {
			t.Errorf("Status = %q, want %q", got.Status, "downloading")
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := db.DeleteModel(ctx, "m-001"); err != nil {
			t.Fatalf("DeleteModel: %v", err)
		}
		_, err := db.GetModel(ctx, "m-001")
		if err == nil {
			t.Fatal("expected error after delete")
		}
	})

	t.Run("get nonexistent", func(t *testing.T) {
		_, err := db.GetModel(ctx, "does-not-exist")
		if err == nil {
			t.Fatal("expected error for nonexistent model")
		}
	})
}

func TestEngineCRUD(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	e := &Engine{
		ID:        "e-001",
		Type:      "vllm",
		Image:     "vllm/vllm-openai",
		Tag:       "latest",
		SizeBytes: 8_500_000_000,
		Platform:  "linux/arm64",
		Available: true,
	}

	t.Run("insert and get", func(t *testing.T) {
		if err := db.InsertEngine(ctx, e); err != nil {
			t.Fatalf("InsertEngine: %v", err)
		}
		got, err := db.GetEngine(ctx, "e-001")
		if err != nil {
			t.Fatalf("GetEngine: %v", err)
		}
		if got.Type != "vllm" {
			t.Errorf("Type = %q, want %q", got.Type, "vllm")
		}
		if got.Image != "vllm/vllm-openai" {
			t.Errorf("Image = %q, want %q", got.Image, "vllm/vllm-openai")
		}
		if !got.Available {
			t.Error("Available should be true")
		}
	})

	t.Run("list", func(t *testing.T) {
		engines, err := db.ListEngines(ctx)
		if err != nil {
			t.Fatalf("ListEngines: %v", err)
		}
		if len(engines) != 1 {
			t.Fatalf("len = %d, want 1", len(engines))
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := db.DeleteEngine(ctx, "e-001"); err != nil {
			t.Fatalf("DeleteEngine: %v", err)
		}
		_, err := db.GetEngine(ctx, "e-001")
		if err == nil {
			t.Fatal("expected error after delete")
		}
	})
}

func TestKnowledgeNoteCRUD(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	n := &KnowledgeNote{
		ID:              "n-001",
		Title:           "vLLM on GB10 tuning",
		Tags:            []string{"vllm", "gb10", "tuning"},
		HardwareProfile: "nvidia-gb10-arm64",
		Model:           "qwen3-8b",
		Engine:          "vllm",
		Content:         "kind: knowledge_note\nrecommendation:\n  config:\n    gpu_memory_utilization: 0.85",
		Confidence:      "high",
	}

	t.Run("insert", func(t *testing.T) {
		if err := db.InsertNote(ctx, n); err != nil {
			t.Fatalf("InsertNote: %v", err)
		}
	})

	t.Run("search by hardware", func(t *testing.T) {
		notes, err := db.SearchNotes(ctx, NoteFilter{HardwareProfile: "nvidia-gb10-arm64"})
		if err != nil {
			t.Fatalf("SearchNotes: %v", err)
		}
		if len(notes) != 1 {
			t.Fatalf("len = %d, want 1", len(notes))
		}
		if notes[0].Title != "vLLM on GB10 tuning" {
			t.Errorf("Title = %q, want %q", notes[0].Title, "vLLM on GB10 tuning")
		}
		if len(notes[0].Tags) != 3 {
			t.Errorf("Tags len = %d, want 3", len(notes[0].Tags))
		}
	})

	t.Run("search by model and engine", func(t *testing.T) {
		notes, err := db.SearchNotes(ctx, NoteFilter{Model: "qwen3-8b", Engine: "vllm"})
		if err != nil {
			t.Fatalf("SearchNotes: %v", err)
		}
		if len(notes) != 1 {
			t.Fatalf("len = %d, want 1", len(notes))
		}
	})

	t.Run("search no match", func(t *testing.T) {
		notes, err := db.SearchNotes(ctx, NoteFilter{Model: "nonexistent"})
		if err != nil {
			t.Fatalf("SearchNotes: %v", err)
		}
		if len(notes) != 0 {
			t.Fatalf("len = %d, want 0", len(notes))
		}
	})

	t.Run("search empty filter returns all", func(t *testing.T) {
		notes, err := db.SearchNotes(ctx, NoteFilter{})
		if err != nil {
			t.Fatalf("SearchNotes: %v", err)
		}
		if len(notes) != 1 {
			t.Fatalf("len = %d, want 1", len(notes))
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := db.DeleteNote(ctx, "n-001"); err != nil {
			t.Fatalf("DeleteNote: %v", err)
		}
		notes, _ := db.SearchNotes(ctx, NoteFilter{})
		if len(notes) != 0 {
			t.Fatalf("len = %d, want 0 after delete", len(notes))
		}
	})
}

func TestUpsertOpenQuestion_SeedsCatalogStatusAndPreservesRuntimeResolution(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	if err := db.UpsertOpenQuestion(ctx, "oq-001", "stack:hami", "question", "test", "hypothesis", "untested", ""); err != nil {
		t.Fatalf("UpsertOpenQuestion initial: %v", err)
	}
	if err := db.UpsertOpenQuestion(ctx, "oq-001", "stack:hami", "question", "test", "hypothesis", "confirmed_incompatible", "known finding"); err != nil {
		t.Fatalf("UpsertOpenQuestion catalog update: %v", err)
	}

	q, err := db.GetOpenQuestion(ctx, "oq-001")
	if err != nil {
		t.Fatalf("GetOpenQuestion: %v", err)
	}
	if q.Status != "confirmed_incompatible" {
		t.Fatalf("status = %q, want confirmed_incompatible", q.Status)
	}
	if q.ActualResult != "known finding" {
		t.Fatalf("actual_result = %q, want known finding", q.ActualResult)
	}

	if err := db.ResolveOpenQuestion(ctx, "oq-001", "tested", "runtime result", "apple-m4-arm64"); err != nil {
		t.Fatalf("ResolveOpenQuestion: %v", err)
	}
	if err := db.UpsertOpenQuestion(ctx, "oq-001", "stack:hami", "question", "test", "hypothesis", "untested", ""); err != nil {
		t.Fatalf("UpsertOpenQuestion preserve runtime: %v", err)
	}

	q, err = db.GetOpenQuestion(ctx, "oq-001")
	if err != nil {
		t.Fatalf("GetOpenQuestion after resolve: %v", err)
	}
	if q.Status != "tested" {
		t.Fatalf("status after resolve = %q, want tested", q.Status)
	}
	if q.ActualResult != "runtime result" {
		t.Fatalf("actual_result after resolve = %q, want runtime result", q.ActualResult)
	}
	if q.Hardware != "apple-m4-arm64" {
		t.Fatalf("hardware = %q, want apple-m4-arm64", q.Hardware)
	}
}

func TestConfig(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	t.Run("set and get", func(t *testing.T) {
		if err := db.SetConfig(ctx, "data_dir", "/opt/aima/data"); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		val, err := db.GetConfig(ctx, "data_dir")
		if err != nil {
			t.Fatalf("GetConfig: %v", err)
		}
		if val != "/opt/aima/data" {
			t.Errorf("value = %q, want %q", val, "/opt/aima/data")
		}
	})

	t.Run("upsert", func(t *testing.T) {
		if err := db.SetConfig(ctx, "data_dir", "/new/path"); err != nil {
			t.Fatalf("SetConfig upsert: %v", err)
		}
		val, _ := db.GetConfig(ctx, "data_dir")
		if val != "/new/path" {
			t.Errorf("value = %q, want %q", val, "/new/path")
		}
	})

	t.Run("get nonexistent", func(t *testing.T) {
		_, err := db.GetConfig(ctx, "nonexistent")
		if err == nil {
			t.Fatal("expected error for nonexistent config key")
		}
	})
}

func TestAuditLog(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	entry := &AuditEntry{
		AgentType:     "go_agent",
		ToolName:      "deploy.apply",
		Arguments:     `{"engine":"vllm","model":"qwen3-8b"}`,
		ResultSummary: "deployed successfully",
	}

	if err := db.LogAction(ctx, entry); err != nil {
		t.Fatalf("LogAction: %v", err)
	}
}

func TestRecoveryAuditLog(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()
	event := recovery.AuditEvent{
		Type:          recovery.AuditRecoveryFailed,
		Source:        recovery.AuditSourceReconciler,
		Deployment:    "generic-deployment",
		Model:         "generic-model",
		Runtime:       "native",
		EngineAsset:   "generic-engine",
		EngineVersion: "1.2.3",
		Result:        recovery.AuditResultFailed,
		Reason:        "launch failed token=secret-value",
		RecoveryState: recovery.StateWaiting,
		AttemptCount:  2,
		Revision:      7,
		NextAttemptAt: time.Unix(20_000, 0).UTC().Format(time.RFC3339),
	}

	if err := db.LogRecoveryEvent(ctx, event); err != nil {
		t.Fatalf("LogRecoveryEvent: %v", err)
	}
	var agentType, toolName, arguments, resultSummary string
	if err := db.db.QueryRowContext(ctx, `
SELECT agent_type, tool_name, arguments, result_summary
FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&agentType, &toolName, &arguments, &resultSummary); err != nil {
		t.Fatalf("query recovery audit: %v", err)
	}
	if agentType != recovery.AuditSourceReconciler || toolName != "deployment.recovery."+recovery.AuditRecoveryFailed {
		t.Fatalf("audit identity = %q/%q", agentType, toolName)
	}
	if strings.Contains(arguments, "secret-value") || strings.Contains(resultSummary, "secret-value") {
		t.Fatalf("audit leaked credential: arguments=%q result=%q", arguments, resultSummary)
	}
	var stored recovery.AuditEvent
	if err := json.Unmarshal([]byte(arguments), &stored); err != nil {
		t.Fatalf("decode recovery audit arguments: %v", err)
	}
	if stored.Deployment != event.Deployment || stored.EngineVersion != event.EngineVersion || stored.Reason != "launch failed token=[REDACTED]" {
		t.Fatalf("stored recovery audit = %+v", stored)
	}
	if !strings.Contains(resultSummary, recovery.AuditResultFailed) || !strings.Contains(resultSummary, "[REDACTED]") {
		t.Fatalf("result summary = %q, want failed and redacted reason", resultSummary)
	}
}

func TestUpdateConfigStatus(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	cfg := &Configuration{
		ID:         "cfg-001",
		HardwareID: "nvidia-gb10-arm64",
		EngineID:   "vllm-nightly",
		ModelID:    "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.8}`,
		ConfigHash: "abc123",
		Status:     "experiment",
		Source:     "benchmark",
	}
	if err := db.InsertConfiguration(ctx, cfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}

	t.Run("promote to golden", func(t *testing.T) {
		if err := db.UpdateConfigStatus(ctx, "cfg-001", "golden"); err != nil {
			t.Fatalf("UpdateConfigStatus: %v", err)
		}
		got, err := db.GetConfiguration(ctx, "cfg-001")
		if err != nil {
			t.Fatalf("GetConfiguration: %v", err)
		}
		if got.Status != "golden" {
			t.Errorf("Status = %q, want %q", got.Status, "golden")
		}
	})

	t.Run("archive", func(t *testing.T) {
		if err := db.UpdateConfigStatus(ctx, "cfg-001", "archived"); err != nil {
			t.Fatalf("UpdateConfigStatus: %v", err)
		}
		got, _ := db.GetConfiguration(ctx, "cfg-001")
		if got.Status != "archived" {
			t.Errorf("Status = %q, want %q", got.Status, "archived")
		}
	})

	t.Run("nonexistent config", func(t *testing.T) {
		err := db.UpdateConfigStatus(ctx, "does-not-exist", "golden")
		if err == nil {
			t.Fatal("expected error for nonexistent config")
		}
	})
}

func TestDuplicateInsert(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	m := &Model{
		ID:   "m-dup",
		Name: "test",
		Type: "llm",
		Path: "/tmp/test",
	}
	if err := db.InsertModel(ctx, m); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := db.InsertModel(ctx, m); err == nil {
		t.Fatal("expected error on duplicate insert")
	}
}

func TestFindGoldenBenchmark(t *testing.T) {
	db := mustOpen(t)
	ctx := context.Background()

	// Insert a golden config
	cfg := &Configuration{
		ID: "cfg-golden-1", HardwareID: "hw1", EngineID: "eng1", ModelID: "model1",
		Config: `{"concurrency":4}`, ConfigHash: "hash-golden-1",
		Status: "golden", Source: "benchmark",
	}
	if err := db.InsertConfiguration(ctx, cfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}

	// Insert a benchmark for it
	br := &BenchmarkResult{
		ID: "bench-1", ConfigID: "cfg-golden-1", Concurrency: 4,
		ThroughputTPS: 100.0, Modality: "text",
	}
	if err := db.InsertBenchmarkResult(ctx, br); err != nil {
		t.Fatalf("InsertBenchmarkResult: %v", err)
	}

	t.Run("finds golden with benchmark", func(t *testing.T) {
		c, b, err := db.FindGoldenBenchmark(ctx, "hw1", "eng1", "model1", "text")
		if err != nil {
			t.Fatalf("FindGoldenBenchmark: %v", err)
		}
		if c == nil {
			t.Fatal("expected non-nil config")
		}
		if c.ID != "cfg-golden-1" {
			t.Errorf("config ID = %q, want cfg-golden-1", c.ID)
		}
		if b == nil {
			t.Fatal("expected non-nil benchmark")
		}
		if b.ThroughputTPS != 100.0 {
			t.Errorf("ThroughputTPS = %f, want 100.0", b.ThroughputTPS)
		}
	})

	t.Run("no golden for different triple", func(t *testing.T) {
		c, b, err := db.FindGoldenBenchmark(ctx, "hw2", "eng1", "model1", "text")
		if err != nil {
			t.Fatalf("FindGoldenBenchmark: %v", err)
		}
		if c != nil || b != nil {
			t.Error("expected nil config and benchmark for non-matching triple")
		}
	})

	t.Run("golden without benchmark", func(t *testing.T) {
		cfg2 := &Configuration{
			ID: "cfg-golden-2", HardwareID: "hw2", EngineID: "eng2", ModelID: "model2",
			Config: `{}`, ConfigHash: "hash-golden-2",
			Status: "golden", Source: "benchmark",
		}
		if err := db.InsertConfiguration(ctx, cfg2); err != nil {
			t.Fatalf("InsertConfiguration: %v", err)
		}
		c, b, err := db.FindGoldenBenchmark(ctx, "hw2", "eng2", "model2", "text")
		if err != nil {
			t.Fatalf("FindGoldenBenchmark: %v", err)
		}
		if c == nil {
			t.Fatal("expected non-nil config")
		}
		if b != nil {
			t.Error("expected nil benchmark for golden config without benchmarks")
		}
	})

	t.Run("filters by modality", func(t *testing.T) {
		if err := db.InsertBenchmarkResult(ctx, &BenchmarkResult{
			ID: "bench-vlm-1", ConfigID: "cfg-golden-1", Concurrency: 1,
			ThroughputTPS: 12.0, Modality: "vlm",
		}); err != nil {
			t.Fatalf("InsertBenchmarkResult(vlm): %v", err)
		}
		c, b, err := db.FindGoldenBenchmark(ctx, "hw1", "eng1", "model1", "vlm")
		if err != nil {
			t.Fatalf("FindGoldenBenchmark(vlm): %v", err)
		}
		if c == nil || b == nil {
			t.Fatal("expected golden config with vlm benchmark")
		}
		if b.Modality != "vlm" {
			t.Fatalf("benchmark modality = %q, want vlm", b.Modality)
		}
		if b.ID != "bench-vlm-1" {
			t.Fatalf("benchmark id = %q, want bench-vlm-1", b.ID)
		}
	})
}
