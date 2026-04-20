package support

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServiceAskForHelpAndRun(t *testing.T) {
	t.Parallel()

	type serverState struct {
		mu             sync.Mutex
		taskActive     bool
		notified       bool
		progressCalls  int
		resultCalls    int
		lastResultBody map[string]any
	}

	state := &serverState{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/self-register", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"device_id":             "dev-1",
			"token":                 "tok-1",
			"recovery_code":         "rec-1",
			"token_expires_at":      time.Now().Add(48 * time.Hour).Format(time.RFC3339),
			"poll_interval_seconds": 1,
			"referral_code":         "ref-1",
			"share_text":            "Share AIMA-Service with your team",
			"budget": map[string]any{
				"max_tasks":      10,
				"used_tasks":     1,
				"budget_usd":     20,
				"spent_usd":      2,
				"status":         "active",
				"is_bound":       false,
				"referral_count": 3,
			},
		})
	})
	mux.HandleFunc("/api/v1/devices/dev-1/active-task", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		active := state.taskActive
		state.mu.Unlock()
		if active {
			writeJSON(t, w, map[string]any{
				"has_active_task": true,
				"task_id":         "task-1",
				"status":          "created",
				"target":          "diagnose and fix the issue",
			})
			return
		}
		writeJSON(t, w, map[string]any{"has_active_task": false})
	})
	mux.HandleFunc("/api/v1/devices/dev-1/tasks", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.taskActive = true
		state.mu.Unlock()
		writeJSON(t, w, map[string]any{"task_id": "task-1", "status": "created"})
	})
	mux.HandleFunc("/api/v1/devices/dev-1/poll", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.taskActive && state.resultCalls == 0 {
			writeJSON(t, w, map[string]any{
				"command_id":              "cmd-1",
				"command":                 base64.StdEncoding.EncodeToString([]byte("sleep 0.15; printf 'hello from support'")),
				"command_encoding":        "base64",
				"command_timeout_seconds": 30,
				"command_intent":          "Run diagnostics",
				"poll_interval_seconds":   1,
			})
			return
		}
		if !state.notified && state.resultCalls > 0 {
			state.notified = true
			writeJSON(t, w, map[string]any{
				"poll_interval_seconds":        1,
				"is_bound":                     true,
				"notif_task_id":                "task-1",
				"notif_task_status":            "succeeded",
				"notif_referral_code":          "ref-1",
				"notif_share_text":             "Share AIMA-Service with your team",
				"notif_budget_tasks_remaining": 8,
				"notif_budget_tasks_total":     10,
				"notif_budget_usd_remaining":   15,
				"notif_budget_usd_total":       20,
			})
			return
		}
		writeJSON(t, w, map[string]any{"poll_interval_seconds": 1})
	})
	mux.HandleFunc("/api/v1/devices/dev-1/commands/cmd-1/progress", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.progressCalls++
		state.mu.Unlock()
		writeJSON(t, w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/devices/dev-1/result", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode result body: %v", err)
		}
		state.mu.Lock()
		state.resultCalls++
		state.lastResultBody = body
		state.taskActive = false
		state.mu.Unlock()
		writeJSON(t, w, map[string]any{"ok": true})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	store := newMemoryStore()
	svc := NewService(store,
		WithHTTPClient(server.Client()),
		WithProgressInterval(20*time.Millisecond),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := svc.AskForHelp(ctx, AskRequest{
		Description: "diagnose and fix the issue",
		Endpoint:    server.URL,
		InviteCode:  "invite-123",
	})
	if err != nil {
		t.Fatalf("AskForHelp: %v", err)
	}
	if !result.Created {
		t.Fatalf("expected Created=true, got %+v", result)
	}
	if result.TaskID != "task-1" {
		t.Fatalf("TaskID = %q, want task-1", result.TaskID)
	}
	if result.ReferralCode != "ref-1" || result.ShareText == "" {
		t.Fatalf("expected referral summary in ask result, got %+v", result)
	}
	if result.MaxTasks != 10 || result.UsedTasks != 1 || result.BudgetUSD != 20 || result.SpentUSD != 2 {
		t.Fatalf("expected budget summary in ask result, got %+v", result)
	}
	if got := store.mustGet(ConfigEnabled); got != "true" {
		t.Fatalf("support.enabled = %q, want true", got)
	}
	if got := store.mustGet(configStateDeviceID); got != "dev-1" {
		t.Fatalf("device state not saved, got %q", got)
	}
	statusBeforeRun := svc.Status(ctx)
	if !statusBeforeRun.Enabled || !statusBeforeRun.Registered {
		t.Fatalf("expected enabled registered status, got %+v", statusBeforeRun)
	}
	if statusBeforeRun.ActiveTask == nil {
		t.Fatalf("expected active task snapshot before Run, got %+v", statusBeforeRun)
	}
	if statusBeforeRun.ActiveTask.TaskID != "task-1" || statusBeforeRun.ActiveTask.Status != "created" {
		t.Fatalf("unexpected active task snapshot before Run: %+v", statusBeforeRun.ActiveTask)
	}
	if statusBeforeRun.ActiveTask.Target != "diagnose and fix the issue" {
		t.Fatalf("unexpected active task target before Run: %+v", statusBeforeRun.ActiveTask)
	}
	if statusBeforeRun.ReferralCode != "ref-1" || statusBeforeRun.ShareText == "" {
		t.Fatalf("expected persisted support summary before Run, got %+v", statusBeforeRun)
	}
	if statusBeforeRun.MaxTasks != 10 || statusBeforeRun.UsedTasks != 1 || statusBeforeRun.BudgetUSD != 20 || statusBeforeRun.SpentUSD != 2 {
		t.Fatalf("unexpected budget summary before Run: %+v", statusBeforeRun)
	}
	if statusBeforeRun.IsBound {
		t.Fatalf("expected unbound status before Run, got %+v", statusBeforeRun)
	}

	if err := svc.Run(ctx, RunOptions{StopWhenIdle: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.progressCalls == 0 {
		t.Fatal("expected at least one progress update")
	}
	if state.resultCalls != 1 {
		t.Fatalf("resultCalls = %d, want 1", state.resultCalls)
	}
	if stdout, _ := state.lastResultBody["stdout"].(string); stdout == "" {
		t.Fatalf("stdout missing from result payload: %+v", state.lastResultBody)
	}
	statusAfterRun := svc.Status(ctx)
	if statusAfterRun.ActiveTask != nil {
		t.Fatalf("expected active task cleared after Run, got %+v", statusAfterRun.ActiveTask)
	}
	if statusAfterRun.LastTask == nil {
		t.Fatalf("expected last task snapshot after Run, got %+v", statusAfterRun)
	}
	if statusAfterRun.LastTask.TaskID != "task-1" || statusAfterRun.LastTask.Status != "succeeded" {
		t.Fatalf("unexpected last task snapshot after Run: %+v", statusAfterRun.LastTask)
	}
	if statusAfterRun.LastMessage == nil || !strings.Contains(statusAfterRun.LastMessage.Message, "Task task-1 finished with status succeeded") {
		t.Fatalf("unexpected last message snapshot after Run: %+v", statusAfterRun.LastMessage)
	}
	if !statusAfterRun.IsBound {
		t.Fatalf("expected bound status after Run, got %+v", statusAfterRun)
	}
	if statusAfterRun.MaxTasks != 10 || statusAfterRun.UsedTasks != 2 || statusAfterRun.BudgetUSD != 20 || statusAfterRun.SpentUSD != 5 {
		t.Fatalf("unexpected budget summary after Run: %+v", statusAfterRun)
	}
}

func TestServiceRunRetriesTransientPollFailure(t *testing.T) {
	t.Parallel()

	type serverState struct {
		mu            sync.Mutex
		taskActive    bool
		taskCounter   int
		progressCalls int
		resultCalls   int
		pollFailures  int
	}

	state := &serverState{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/self-register", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"device_id":             "dev-1",
			"token":                 "tok-1",
			"recovery_code":         "rec-1",
			"token_expires_at":      time.Now().Add(48 * time.Hour).Format(time.RFC3339),
			"poll_interval_seconds": 1,
		})
	})
	mux.HandleFunc("/api/v1/devices/dev-1/active-task", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		active := state.taskActive
		counter := state.taskCounter
		state.mu.Unlock()
		if active {
			writeJSON(t, w, map[string]any{
				"has_active_task": true,
				"task_id":         fmt.Sprintf("task-%d", counter),
				"status":          "created",
				"target":          "readonly",
			})
			return
		}
		writeJSON(t, w, map[string]any{"has_active_task": false})
	})
	mux.HandleFunc("/api/v1/devices/dev-1/tasks", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.taskActive = true
		state.taskCounter++
		counter := state.taskCounter
		state.mu.Unlock()
		writeJSON(t, w, map[string]any{"task_id": fmt.Sprintf("task-%d", counter), "status": "created"})
	})
	mux.HandleFunc("/api/v1/devices/dev-1/poll", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.pollFailures == 0 {
			state.pollFailures++
			http.Error(w, `{"detail":"temporary overload"}`, http.StatusServiceUnavailable)
			return
		}
		if state.taskActive && state.resultCalls == 0 {
			writeJSON(t, w, map[string]any{
				"command_id":              "cmd-1",
				"command":                 base64.StdEncoding.EncodeToString([]byte("printf 'ok'")),
				"command_encoding":        "base64",
				"command_timeout_seconds": 10,
				"command_intent":          "Run readonly check",
				"poll_interval_seconds":   1,
			})
			return
		}
		writeJSON(t, w, map[string]any{
			"poll_interval_seconds": 1,
			"notif_task_id":         "task-1",
			"notif_task_status":     "succeeded",
		})
	})
	mux.HandleFunc("/api/v1/devices/dev-1/result", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.resultCalls++
		state.taskActive = false
		state.mu.Unlock()
		writeJSON(t, w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/devices/dev-1/commands/cmd-1/progress", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.progressCalls++
		state.mu.Unlock()
		writeJSON(t, w, map[string]any{"ok": true})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	store := newMemoryStore()
	svc := NewService(store, WithHTTPClient(server.Client()))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := svc.AskForHelp(ctx, AskRequest{
		Description: "readonly",
		Endpoint:    server.URL,
		InviteCode:  "invite-123",
	}); err != nil {
		t.Fatalf("AskForHelp: %v", err)
	}

	if err := svc.Run(ctx, RunOptions{StopWhenIdle: true}); err != nil {
		t.Fatalf("Run should tolerate transient 503, got: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pollFailures != 1 {
		t.Fatalf("pollFailures = %d, want 1", state.pollFailures)
	}
	if state.resultCalls != 1 {
		t.Fatalf("resultCalls = %d, want 1", state.resultCalls)
	}
}

func TestServiceAskForHelpRegistrationPromptErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		detail string
		kind   RegistrationPromptKind
	}{
		{
			name:   "invite_or_worker",
			status: http.StatusUnprocessableEntity,
			detail: "invite_code or worker_enrollment_code is required for new device registration",
			kind:   RegistrationPromptInviteOrWorker,
		},
		{
			name:   "recovery",
			status: http.StatusForbidden,
			detail: "valid recovery_code required to refresh existing device credentials",
			kind:   RegistrationPromptRecovery,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/devices/self-register", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if err := json.NewEncoder(w).Encode(map[string]any{"detail": tc.detail}); err != nil {
					t.Fatalf("encode error response: %v", err)
				}
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			svc := NewService(newMemoryStore(), WithHTTPClient(server.Client()))
			// Invite code must be supplied explicitly; offline-first (INV-8)
			// blocks the HTTP call otherwise, so the test of server-driven
			// 422/403 → prompt-error translation needs a real invite.
			_, err := svc.AskForHelp(context.Background(), AskRequest{Endpoint: server.URL, InviteCode: "test-invite"})
			if err == nil {
				t.Fatal("expected registration prompt error")
			}

			var promptErr *RegistrationPromptError
			if !errors.As(err, &promptErr) {
				t.Fatalf("expected RegistrationPromptError, got %T (%v)", err, err)
			}
			if promptErr.Kind != tc.kind {
				t.Fatalf("prompt kind = %q, want %q", promptErr.Kind, tc.kind)
			}
			if promptErr.Detail != tc.detail {
				t.Fatalf("prompt detail = %q, want %q", promptErr.Detail, tc.detail)
			}
		})
	}
}

func TestServiceGoUXManifestJSON(t *testing.T) {
	t.Parallel()

	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ux-manifests/device-go", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		writeJSON(t, w, map[string]any{
			"manifest_version": "2026-03-24.1",
			"flow_id":          "device-go",
			"blocks": map[string]any{
				"task_menu": map[string]any{
					"title": map[string]any{
						"text": "What would you like me to help you do?",
					},
				},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	store := newMemoryStore()
	if err := store.SetConfig(context.Background(), ConfigEndpoint, server.URL); err != nil {
		t.Fatalf("set endpoint: %v", err)
	}
	if err := store.SetConfig(context.Background(), ConfigWorkerCode, "worker-42"); err != nil {
		t.Fatalf("set worker code: %v", err)
	}
	if err := store.SetConfig(context.Background(), configStateReferralCode, "ref-99"); err != nil {
		t.Fatalf("set referral code: %v", err)
	}

	svc := NewService(store, WithHTTPClient(server.Client()))
	raw, err := svc.GoUXManifestJSON(context.Background())
	if err != nil {
		t.Fatalf("GoUXManifestJSON: %v", err)
	}
	if !strings.Contains(string(raw), `"flow_id":"device-go"`) {
		t.Fatalf("unexpected manifest payload: %s", string(raw))
	}
	if gotPath != "/api/v1/ux-manifests/device-go?ref=ref-99&schema_version=v1&worker_code=worker-42" {
		t.Fatalf("manifest path = %q", gotPath)
	}
}

func TestServiceDefaultEndpointUsesRootAPIBase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newMemoryStore()
	svc := NewService(store)

	if err := svc.persistOverrides(ctx, AskRequest{}); err != nil {
		t.Fatalf("persistOverrides: %v", err)
	}
	if got := store.mustGet(ConfigEndpoint); got != DefaultEndpoint {
		t.Fatalf("stored default endpoint = %q, want %q", got, DefaultEndpoint)
	}
	if got := svc.endpointFromConfig(ctx); got != "https://aimaserver.com/api/v1" {
		t.Fatalf("normalized endpoint = %q, want %q", got, "https://aimaserver.com/api/v1")
	}
}

type memoryStore struct {
	mu     sync.Mutex
	values map[string]string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{values: make(map[string]string)}
}

func (s *memoryStore) GetConfig(ctx context.Context, key string) (string, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	if !ok {
		return "", fmt.Errorf("key not found: %s", key)
	}
	return value, nil
}

func (s *memoryStore) SetConfig(ctx context.Context, key, value string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *memoryStore) mustGet(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

func writeJSON(t *testing.T, w http.ResponseWriter, body map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode json response: %v", err)
	}
}
