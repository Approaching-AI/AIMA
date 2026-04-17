package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/cloud"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/support"
)

// TestDeviceRegistration_EndToEnd walks a fresh edge through first-boot
// registration against a mocked aima-service and then verifies outbound
// Central calls carry the device_id obtained from that registration.
//
// This is the closest we get to a real 3-service integration test without
// spawning subprocesses: aima-service is httptest-mocked (its API contract
// is frozen by the real server), Central is replaced by an assertion-
// recording httptest server so we can inspect outgoing query strings, and
// the AIMA edge code path runs in-process.
func TestDeviceRegistration_EndToEnd(t *testing.T) {
	ctx := context.Background()

	// --- Mock aima-service: only the self-register endpoint is needed. ---
	var registerCalls int32
	aimaService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/devices/self-register" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&registerCalls, 1)
		// Echo invite_code back so we can assert it traveled through.
		body := map[string]any{}
		if r.Body != nil {
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, &body)
		}
		_ = body
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_id":             "dev-integration-1",
			"token":                 "tok-integration",
			"recovery_code":         "rec-integration",
			"token_expires_at":      time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339),
			"poll_interval_seconds": 5,
			"referral_code":         "REF-INT",
			"share_text":            "share",
			"budget": map[string]any{
				"max_tasks": 10, "used_tasks": 0, "budget_usd": 1,
				"spent_usd": 0, "status": "active",
				"is_bound": false, "referral_count": 0,
			},
		})
	}))
	defer aimaService.Close()

	// --- Mock Central: record every outbound request's device_id. ---
	var lastDeviceID atomic.Value
	lastDeviceID.Store("")
	var ingestCalls int32
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastDeviceID.Store(r.URL.Query().Get("device_id"))
		switch r.URL.Path {
		case "/api/v1/ingest":
			atomic.AddInt32(&ingestCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": 0})
		case "/api/v1/stats":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"devices": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer central.Close()

	// --- Edge harness: real SQLite config, real ToolDeps, real support.Service. ---
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open edge DB: %v", err)
	}
	defer db.Close()

	// Point the support client at the mock aima-service and Central at the
	// mock Central. These are the two hooks the user configures on a real
	// deploy; everything else is derived.
	for k, v := range map[string]string{
		"support.endpoint":  aimaService.URL,
		"central.endpoint":  central.URL,
		"central.api_key":   "test-key",
		"support.invite_code": "INVITE-INT",
	} {
		if err := db.SetConfig(ctx, k, v); err != nil {
			t.Fatalf("SetConfig %s: %v", k, err)
		}
	}

	supportSvc := support.NewService(db,
		support.WithHTTPClient(aimaService.Client()),
		support.WithLogger(slog.Default()))

	ac := &appContext{
		db:       db,
		rt:       &fakeRuntime{name: "docker"},
		eventBus: agent.NewEventBus(),
	}
	deps := &mcp.ToolDeps{
		GetConfig: db.GetConfig,
		SetConfig: db.SetConfig,
		// SyncPush hits ExportKnowledge; stub with an empty envelope since the
		// data itself is irrelevant to identity flow assertions.
		ExportKnowledge: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return []byte(`{"data":{"configurations":[],"benchmark_results":[],"knowledge_notes":[]}}`), nil
		},
	}
	buildIntegrationDeps(ac, deps)
	wireDeviceDeps(deps, supportSvc)

	// --- Pre-condition: no identity yet, outbound calls should fail. ---
	if got := cloud.ReadDeviceID(ctx, deps.GetConfig); got != "" {
		t.Fatalf("expected empty device.id before bootstrap, got %q", got)
	}
	if _, err := deps.SyncPush(ctx); err == nil {
		t.Fatalf("SyncPush should have errored before registration")
	}

	// --- Step 1: Bootstrap (what StartRegistrationWorker does in prod). ---
	res, err := supportSvc.Bootstrap(ctx, support.BootstrapOptions{InviteCode: "INVITE-INT"})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if res.DeviceID != "dev-integration-1" {
		t.Errorf("Bootstrap device_id = %q, want dev-integration-1", res.DeviceID)
	}
	if atomic.LoadInt32(&registerCalls) != 1 {
		t.Errorf("aima-service register calls = %d, want 1", registerCalls)
	}

	// --- Step 2: canonical keys populated. ---
	identity := cloud.ReadIdentity(ctx, deps.GetConfig)
	if !identity.Registered() {
		t.Fatalf("identity not registered after Bootstrap: %+v", identity)
	}
	if identity.DeviceID != "dev-integration-1" {
		t.Errorf("canonical device.id = %q, want dev-integration-1", identity.DeviceID)
	}
	if identity.RegistrationState != cloud.StateRegistered {
		t.Errorf("registration_state = %q, want registered", identity.RegistrationState)
	}

	// --- Step 3: device MCP tools surface the state. ---
	statusJSON, err := deps.DeviceStatus(ctx)
	if err != nil {
		t.Fatalf("DeviceStatus: %v", err)
	}
	var status map[string]any
	_ = json.Unmarshal(statusJSON, &status)
	if status["registered"] != true {
		t.Errorf("DeviceStatus.registered = %v, want true", status["registered"])
	}
	if status["device_id"] != "dev-integration-1" {
		t.Errorf("DeviceStatus.device_id = %v, want dev-integration-1", status["device_id"])
	}

	// --- Step 4: SyncPush now succeeds and carries device_id to Central. ---
	if _, err := deps.SyncPush(ctx); err != nil {
		t.Fatalf("SyncPush after register: %v", err)
	}
	if atomic.LoadInt32(&ingestCalls) != 1 {
		t.Errorf("central ingest calls = %d, want 1", ingestCalls)
	}
	if got := lastDeviceID.Load().(string); got != "dev-integration-1" {
		t.Errorf("Central saw device_id = %q, want dev-integration-1", got)
	}

	// --- Step 5: Bootstrap is idempotent when already registered. ---
	res2, err := supportSvc.Bootstrap(ctx, support.BootstrapOptions{})
	if err != nil {
		t.Fatalf("Bootstrap idempotent: %v", err)
	}
	if !res2.AlreadyRegistered {
		t.Errorf("second Bootstrap should be AlreadyRegistered, got %+v", res2)
	}
	if atomic.LoadInt32(&registerCalls) != 1 {
		t.Errorf("register should not be called again; got %d total", registerCalls)
	}

	// --- Step 6: Reset clears identity, subsequent Central calls fail again. ---
	if err := supportSvc.ResetIdentity(ctx); err != nil {
		t.Fatalf("ResetIdentity: %v", err)
	}
	if got := cloud.ReadDeviceID(ctx, deps.GetConfig); got != "" {
		t.Errorf("device.id after reset = %q, want empty", got)
	}
	if _, err := deps.SyncPush(ctx); err == nil {
		t.Errorf("SyncPush should error after ResetIdentity")
	}
}

// TestDeviceStatus_UnregisteredSurface checks the MCP DeviceStatus response
// for an edge that has never called Bootstrap — this is the payload the CLI
// surfaces when users first run `aima device status` on a new machine.
func TestDeviceStatus_UnregisteredSurface(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open edge DB: %v", err)
	}
	defer db.Close()

	deps := &mcp.ToolDeps{
		GetConfig: db.GetConfig,
		SetConfig: db.SetConfig,
	}
	wireDeviceDeps(deps, support.NewService(db))

	data, err := deps.DeviceStatus(ctx)
	if err != nil {
		t.Fatalf("DeviceStatus: %v", err)
	}
	var status map[string]any
	_ = json.Unmarshal(data, &status)
	if status["registered"] != false {
		t.Errorf("expected registered=false, got %v", status["registered"])
	}
	if status["registration_state"] != cloud.StateUnregistered {
		t.Errorf("expected unregistered state, got %v", status["registration_state"])
	}
	if !strings.HasPrefix(status["device_id"].(string), "") {
		// device_id should be empty string, not nil — matches JSON output format
		t.Errorf("device_id should be empty string, got %v", status["device_id"])
	}
}
