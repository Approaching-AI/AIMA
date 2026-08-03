package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/recovery"
)

type fakeEngineLifecycleInventory struct {
	engines       []*state.Engine
	intents       []*recovery.Intent
	events        []string
	upsertErr     error
	activationErr error
	rollbackErr   error
}

func (f *fakeEngineLifecycleInventory) ListEngines(context.Context) ([]*state.Engine, error) {
	out := make([]*state.Engine, 0, len(f.engines))
	for _, entry := range f.engines {
		clone := *entry
		out = append(out, &clone)
	}
	return out, nil
}

func (f *fakeEngineLifecycleInventory) UpsertScannedEngine(_ context.Context, entry *state.Engine) error {
	f.events = append(f.events, "upsert")
	if f.upsertErr != nil {
		return f.upsertErr
	}
	clone := *entry
	for i := range f.engines {
		if f.engines[i].ID == entry.ID {
			f.engines[i] = &clone
			return nil
		}
	}
	f.engines = append(f.engines, &clone)
	return nil
}

func (f *fakeEngineLifecycleInventory) ActivateEngineVersion(_ context.Context, id string) (string, error) {
	f.events = append(f.events, "activate")
	if f.activationErr != nil {
		return "", f.activationErr
	}
	var candidate *state.Engine
	for _, entry := range f.engines {
		if entry.ID == id {
			candidate = entry
			break
		}
	}
	if candidate == nil {
		return "", errors.New("candidate not found")
	}
	previous := ""
	for _, entry := range f.engines {
		if entry.ID != candidate.ID && entry.Active && entry.AssetName == candidate.AssetName &&
			entry.Platform == candidate.Platform && entry.RuntimeType == candidate.RuntimeType {
			previous = entry.ID
			entry.Active = false
			entry.LifecycleStatus = "verified"
		}
	}
	candidate.Active = true
	candidate.LifecycleStatus = "active"
	candidate.PreviousEngineID = previous
	return previous, nil
}

func (f *fakeEngineLifecycleInventory) ListDeploymentIntents(context.Context) ([]*recovery.Intent, error) {
	return f.intents, nil
}

func (f *fakeEngineLifecycleInventory) RollbackEngineVersion(_ context.Context, activeID string) (string, error) {
	f.events = append(f.events, "rollback")
	if f.rollbackErr != nil {
		return "", f.rollbackErr
	}
	var current *state.Engine
	for _, entry := range f.engines {
		if entry.ID == activeID {
			current = entry
			break
		}
	}
	if current == nil || !current.Active {
		return "", errors.New("active engine not found")
	}
	var previous *state.Engine
	for _, entry := range f.engines {
		if entry.ID == current.PreviousEngineID {
			previous = entry
			break
		}
	}
	if previous == nil {
		return "", errors.New("previous engine not found")
	}
	current.Active = false
	current.LifecycleStatus = "verified"
	previous.Active = true
	previous.LifecycleStatus = "active"
	previous.PreviousEngineID = current.ID
	return previous.ID, nil
}

func testNativeLifecycleAsset(version, catalogPlatform string) *knowledge.EngineAsset {
	return &knowledge.EngineAsset{
		Metadata: knowledge.EngineMetadata{Name: "engine-a", Type: "native-engine", Version: version},
		Runtime:  knowledge.EngineRuntime{Default: "native"},
		Source: &knowledge.EngineSource{
			Binary:    "engine-server",
			Platforms: []string{catalogPlatform},
			Download:  map[string]string{catalogPlatform: "https://catalog.example/engine.zip"},
			SHA256:    map[string]string{catalogPlatform: "catalog-sha256"},
		},
	}
}

func newTestEngineLifecycleService(t *testing.T, inventory *fakeEngineLifecycleInventory, asset *knowledge.EngineAsset) *engineLifecycleService {
	t.Helper()
	return &engineLifecycleService{
		inventory:         inventory,
		dataDir:           t.TempDir(),
		inventoryPlatform: "windows-amd64",
		catalogPlatform:   "windows/amd64",
		resolveAsset: func(context.Context, string) (*knowledge.EngineAsset, error) {
			return asset, nil
		},
	}
}

func TestEngineEnsureDryRunHasNoSideEffects(t *testing.T) {
	inv := &fakeEngineLifecycleInventory{}
	service := newTestEngineLifecycleService(t, inv, testNativeLifecycleAsset("2.0.0", "windows/amd64"))
	service.stageNative = func(context.Context, *engine.BinarySource, string, bool) (stagedNativeEngine, error) {
		t.Fatal("dry-run called native stager")
		return stagedNativeEngine{}, nil
	}
	service.pullContainer = func(context.Context, *knowledge.EngineAsset, string) (*state.Engine, error) {
		t.Fatal("dry-run called container puller")
		return nil, nil
	}

	result, err := service.Ensure(context.Background(), engine.EnsureRequest{Name: "engine-a", Apply: false})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if result.Applied || result.Plan.Action != "install" {
		t.Fatalf("result = %+v", result)
	}
	if len(inv.events) != 0 {
		t.Fatalf("dry-run inventory events = %#v", inv.events)
	}
}

func TestEngineEnsureReusesExactPreinstalledWithoutDownloadOrMove(t *testing.T) {
	inv := &fakeEngineLifecycleInventory{engines: []*state.Engine{{
		ID:                 "preinstalled-v2",
		Type:               "native-engine",
		AssetName:          "engine-a",
		Version:            "2.0.0",
		CatalogVersion:     "2.0.0",
		Platform:           "windows-amd64",
		RuntimeType:        "native",
		BinaryPath:         `D:\engines\engine-server.exe`,
		Location:           `D:\engines\engine-server.exe`,
		Origin:             "preinstalled",
		ContentDigest:      "sha256:preinstalled",
		Available:          true,
		Active:             true,
		LifecycleStatus:    "active",
		VerificationStatus: "unverified",
	}}}
	service := newTestEngineLifecycleService(t, inv, testNativeLifecycleAsset("2.0.0", "windows/amd64"))
	service.stageNative = func(context.Context, *engine.BinarySource, string, bool) (stagedNativeEngine, error) {
		t.Fatal("preinstalled reuse called native stager")
		return stagedNativeEngine{}, nil
	}

	result, err := service.Ensure(context.Background(), engine.EnsureRequest{Name: "engine-a", Apply: true})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !result.Applied || result.ActiveEngineID != "preinstalled-v2" || result.Plan.Action != "reuse" {
		t.Fatalf("result = %+v", result)
	}
	if len(inv.events) != 0 {
		t.Fatalf("preinstalled reuse inventory events = %#v", inv.events)
	}
}

func TestEngineEnsureNativeStagesPromotesUpsertsThenActivates(t *testing.T) {
	inv := &fakeEngineLifecycleInventory{engines: []*state.Engine{{
		ID:                 "active-v1",
		AssetName:          "engine-a",
		Version:            "1.0.0",
		Platform:           "windows-amd64",
		RuntimeType:        "native",
		Origin:             "preinstalled",
		Available:          true,
		Active:             true,
		LifecycleStatus:    "active",
		VerificationStatus: "unverified",
	}}}
	service := newTestEngineLifecycleService(t, inv, testNativeLifecycleAsset("2.0.0", "windows/amd64"))
	var stagedDir string
	service.stageNative = func(_ context.Context, _ *engine.BinarySource, dir string, _ bool) (stagedNativeEngine, error) {
		inv.events = append(inv.events, "download")
		stagedDir = dir
		path := filepath.Join(dir, "engine-server.exe")
		if err := os.WriteFile(path, []byte("verified-binary"), 0o755); err != nil {
			return stagedNativeEngine{}, err
		}
		return stagedNativeEngine{RelativeBinaryPath: "engine-server.exe", ContentDigest: "sha256:binary", SizeBytes: 15}, nil
	}

	result, err := service.Ensure(context.Background(), engine.EnsureRequest{Name: "engine-a", Version: "2.0.0", Apply: true})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := inv.events; len(got) != 3 || got[0] != "download" || got[1] != "upsert" || got[2] != "activate" {
		t.Fatalf("events = %#v, want download, upsert, activate", got)
	}
	if _, err := os.Stat(stagedDir); !os.IsNotExist(err) {
		t.Fatalf("staging directory should be promoted, stat error = %v", err)
	}
	if result.ActiveEngineID == "" || result.PreviousEngineID != "active-v1" {
		t.Fatalf("result = %+v", result)
	}
	active := inv.engines[len(inv.engines)-1]
	if !active.Active || active.VerificationStatus != "verified" || active.Origin != "managed" {
		t.Fatalf("managed candidate = %+v", active)
	}
}

func TestEngineEnsureContainerPassesCatalogDigest(t *testing.T) {
	asset := &knowledge.EngineAsset{
		Metadata: knowledge.EngineMetadata{Name: "engine-a", Type: "container-engine", Version: "3.0.0"},
		Runtime:  knowledge.EngineRuntime{Default: "container"},
		Image: knowledge.EngineImage{
			Name:       "registry.example/engine-a",
			Tag:        "3.0.0",
			Platforms:  []string{"windows/amd64"},
			Registries: []string{"registry.example"},
			Digest:     "sha256:catalog-digest",
		},
	}
	inv := &fakeEngineLifecycleInventory{}
	service := newTestEngineLifecycleService(t, inv, asset)
	service.pullContainer = func(_ context.Context, _ *knowledge.EngineAsset, digest string) (*state.Engine, error) {
		if digest != "sha256:catalog-digest" {
			t.Fatalf("digest = %q", digest)
		}
		return &state.Engine{
			ID: "runtime-image-id", Type: "container-engine", AssetName: "engine-a",
			Image: "registry.example/engine-a", Tag: "3.0.0", Platform: "windows-amd64",
			RuntimeType: "container", Available: true,
		}, nil
	}

	result, err := service.Ensure(context.Background(), engine.EnsureRequest{Name: "engine-a", Apply: true})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if result.ActiveEngineID != "runtime-image-id" || len(inv.events) != 2 {
		t.Fatalf("result = %+v, events = %#v", result, inv.events)
	}
}

func TestEngineEnsureFailureKeepsOldActiveAndRemovesStaging(t *testing.T) {
	old := &state.Engine{
		ID: "active-v1", AssetName: "engine-a", Version: "1.0.0", Platform: "windows-amd64",
		RuntimeType: "native", Origin: "preinstalled", Available: true, Active: true,
		LifecycleStatus: "active", VerificationStatus: "unverified",
	}
	inv := &fakeEngineLifecycleInventory{engines: []*state.Engine{old}}
	service := newTestEngineLifecycleService(t, inv, testNativeLifecycleAsset("2.0.0", "windows/amd64"))
	var stagedDir string
	service.stageNative = func(_ context.Context, _ *engine.BinarySource, dir string, _ bool) (stagedNativeEngine, error) {
		stagedDir = dir
		if err := os.WriteFile(filepath.Join(dir, "partial"), []byte("partial"), 0o644); err != nil {
			return stagedNativeEngine{}, err
		}
		return stagedNativeEngine{}, errors.New("checksum mismatch")
	}

	if _, err := service.Ensure(context.Background(), engine.EnsureRequest{Name: "engine-a", Apply: true}); err == nil {
		t.Fatal("expected staging failure")
	}
	if _, err := os.Stat(stagedDir); !os.IsNotExist(err) {
		t.Fatalf("failed staging directory remains: %v", err)
	}
	if !old.Active || len(inv.engines) != 1 || len(inv.events) != 0 {
		t.Fatalf("old active changed: engines=%+v events=%#v", inv.engines, inv.events)
	}
}

func TestEngineEnsureNativePersistsAtomicActivation(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.InsertEngine(ctx, &state.Engine{
		ID: "active-v1", Type: "native-engine", AssetName: "engine-a", Version: "1.0.0",
		Platform: "windows-amd64", RuntimeType: "native", Origin: "preinstalled",
		BinaryPath: `D:\engines\engine-server.exe`, Available: true, Active: true,
		LifecycleStatus: "active", VerificationStatus: "unverified",
	}); err != nil {
		t.Fatalf("InsertEngine: %v", err)
	}
	asset := testNativeLifecycleAsset("2.0.0", "windows/amd64")
	service := &engineLifecycleService{
		inventory: db, dataDir: t.TempDir(), inventoryPlatform: "windows-amd64", catalogPlatform: "windows/amd64",
		resolveAsset: func(context.Context, string) (*knowledge.EngineAsset, error) { return asset, nil },
		stageNative: func(_ context.Context, _ *engine.BinarySource, dir string, _ bool) (stagedNativeEngine, error) {
			if err := os.WriteFile(filepath.Join(dir, "engine-server.exe"), []byte("verified"), 0o755); err != nil {
				return stagedNativeEngine{}, err
			}
			return stagedNativeEngine{RelativeBinaryPath: "engine-server.exe", ContentDigest: "sha256:verified", SizeBytes: 8}, nil
		},
	}

	result, err := service.Ensure(ctx, engine.EnsureRequest{Name: "engine-a", Apply: true})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if result.PreviousEngineID != "active-v1" || result.ActiveEngineID == "" {
		t.Fatalf("result = %+v", result)
	}
	old, err := db.GetEngine(ctx, "active-v1")
	if err != nil {
		t.Fatalf("GetEngine(active-v1): %v", err)
	}
	active, err := db.GetEngine(ctx, result.ActiveEngineID)
	if err != nil {
		t.Fatalf("GetEngine(candidate): %v", err)
	}
	if old.Active || old.LifecycleStatus != "verified" {
		t.Fatalf("old engine = %+v", old)
	}
	if !active.Active || active.VerificationStatus != "verified" || active.PreviousEngineID != "active-v1" {
		t.Fatalf("active engine = %+v", active)
	}
}

func TestSafeStagedRelativePathRejectsCrossPlatformTraversal(t *testing.T) {
	for _, candidate := range []string{"../escape", `..\escape`, "/absolute", `C:\escape\engine.exe`, `\\server\share\engine.exe`} {
		if _, err := safeStagedRelativePath(candidate); err == nil {
			t.Fatalf("safeStagedRelativePath(%q) accepted unsafe path", candidate)
		}
	}
	if got, err := safeStagedRelativePath(`bin\engine.exe`); err != nil || got != filepath.Join("bin", "engine.exe") {
		t.Fatalf("safe relative path = %q, %v", got, err)
	}
}

func TestEngineRollbackActivatesVerifiedPrevious(t *testing.T) {
	previous := &state.Engine{
		ID: "engine-v1", AssetName: "engine-a", Version: "1.0.0", Platform: "windows-amd64",
		RuntimeType: "native", Available: true, LifecycleStatus: "verified", VerificationStatus: "verified",
	}
	current := &state.Engine{
		ID: "engine-v2", AssetName: "engine-a", Version: "2.0.0", Platform: "windows-amd64",
		RuntimeType: "native", Available: true, Active: true, LifecycleStatus: "active",
		VerificationStatus: "verified", PreviousEngineID: previous.ID,
	}
	inv := &fakeEngineLifecycleInventory{engines: []*state.Engine{previous, current}}
	service := newTestEngineLifecycleService(t, inv, testNativeLifecycleAsset("2.0.0", "windows/amd64"))

	result, err := service.Rollback(context.Background(), "engine-a", true)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if !result.Applied || result.Refused || result.OldActiveEngineID != current.ID || result.ActiveEngineID != previous.ID {
		t.Fatalf("result = %+v", result)
	}
	if current.Active || !previous.Active {
		t.Fatalf("rollback state: previous=%+v current=%+v", previous, current)
	}
	if len(inv.events) != 1 || inv.events[0] != "rollback" {
		t.Fatalf("events = %#v, want only SQLite rollback", inv.events)
	}
}

func TestEngineRollbackDoesNotRestartDeployments(t *testing.T) {
	previous := &state.Engine{
		ID: "engine-v1", AssetName: "engine-a", Platform: "windows-amd64", RuntimeType: "native",
		Available: true, LifecycleStatus: "verified", VerificationStatus: "verified",
	}
	current := &state.Engine{
		ID: "engine-v2", AssetName: "engine-a", Platform: "windows-amd64", RuntimeType: "native",
		Available: true, Active: true, LifecycleStatus: "active", VerificationStatus: "verified",
		PreviousEngineID: previous.ID,
	}
	inv := &fakeEngineLifecycleInventory{engines: []*state.Engine{previous, current}}
	service := newTestEngineLifecycleService(t, inv, testNativeLifecycleAsset("2.0.0", "windows/amd64"))

	if _, err := service.Rollback(context.Background(), "engine-a", true); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(inv.events) != 1 || inv.events[0] != "rollback" {
		t.Fatalf("rollback invoked a non-inventory callback: %#v", inv.events)
	}
}

func TestEngineRollbackRequiresConfirm(t *testing.T) {
	current := &state.Engine{
		ID: "engine-v2", AssetName: "engine-a", Platform: "windows-amd64", RuntimeType: "native",
		Available: true, Active: true, LifecycleStatus: "active", VerificationStatus: "verified",
		PreviousEngineID: "engine-v1",
	}
	inv := &fakeEngineLifecycleInventory{engines: []*state.Engine{current}}
	service := newTestEngineLifecycleService(t, inv, testNativeLifecycleAsset("2.0.0", "windows/amd64"))

	result, err := service.Rollback(context.Background(), "engine-a", false)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if !result.Refused || result.Applied || result.Confirmed || result.Reason == "" {
		t.Fatalf("result = %+v", result)
	}
	if !current.Active || len(inv.events) != 0 {
		t.Fatalf("unconfirmed rollback mutated state: current=%+v events=%#v", current, inv.events)
	}
}

func TestEngineRollbackRejectsUnavailableOrUnverifiedPrevious(t *testing.T) {
	for _, tc := range []struct {
		name         string
		available    bool
		verification string
	}{
		{name: "unavailable", available: false, verification: "verified"},
		{name: "unverified", available: true, verification: "unverified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := &state.Engine{
				ID: "engine-v1", AssetName: "engine-a", Platform: "windows-amd64", RuntimeType: "native",
				Available: tc.available, LifecycleStatus: "verified", VerificationStatus: tc.verification,
			}
			current := &state.Engine{
				ID: "engine-v2", AssetName: "engine-a", Platform: "windows-amd64", RuntimeType: "native",
				Available: true, Active: true, LifecycleStatus: "active", VerificationStatus: "verified",
				PreviousEngineID: previous.ID,
			}
			inv := &fakeEngineLifecycleInventory{engines: []*state.Engine{previous, current}}
			service := newTestEngineLifecycleService(t, inv, testNativeLifecycleAsset("2.0.0", "windows/amd64"))

			if _, err := service.Rollback(context.Background(), "engine-a", true); err == nil {
				t.Fatal("expected rollback rejection")
			}
			if !current.Active || previous.Active || len(inv.events) != 0 {
				t.Fatalf("rejected rollback mutated state: previous=%+v current=%+v events=%#v", previous, current, inv.events)
			}
		})
	}
}

func TestEngineRollbackPersistsAtomicActivation(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	previous := &state.Engine{
		ID: "engine-v1", AssetName: "engine-a", Version: "1.0.0", Platform: "windows-amd64",
		RuntimeType: "native", Origin: "managed", Available: true,
		LifecycleStatus: "verified", VerificationStatus: "verified",
	}
	current := &state.Engine{
		ID: "engine-v2", AssetName: "engine-a", Version: "2.0.0", Platform: "windows-amd64",
		RuntimeType: "native", Origin: "managed", Available: true, Active: true,
		LifecycleStatus: "active", VerificationStatus: "verified", PreviousEngineID: previous.ID,
	}
	if err := db.InsertEngine(ctx, previous); err != nil {
		t.Fatalf("InsertEngine(previous): %v", err)
	}
	if err := db.InsertEngine(ctx, current); err != nil {
		t.Fatalf("InsertEngine(current): %v", err)
	}
	service := &engineLifecycleService{inventory: db, inventoryPlatform: "windows-amd64"}

	result, err := service.Rollback(ctx, "engine-a", true)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	gotPrevious, err := db.GetEngine(ctx, previous.ID)
	if err != nil {
		t.Fatalf("GetEngine(previous): %v", err)
	}
	gotCurrent, err := db.GetEngine(ctx, current.ID)
	if err != nil {
		t.Fatalf("GetEngine(current): %v", err)
	}
	if result.ActiveEngineID != previous.ID || !gotPrevious.Active || gotCurrent.Active || gotPrevious.PreviousEngineID != current.ID {
		t.Fatalf("result=%+v previous=%+v current=%+v", result, gotPrevious, gotCurrent)
	}
}
