package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
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

func TestEngineImportNativeRejectsNonRunnableBundleWithoutCatalogSHA256(t *testing.T) {
	inv := &fakeEngineLifecycleInventory{}
	asset := testNativeLifecycleAsset("2.0.0", "windows/amd64")
	asset.Source.SHA256 = nil
	bundlePath := filepath.Join(t.TempDir(), "engine-a-2.0.0.tar.gz")
	writeNativeEngineBundle(t, bundlePath, "engine-a", "2.0.0", "engine-server", []byte("untrusted"))
	service := newTestEngineLifecycleService(t, inv, asset)
	service.nativeImportAssets = func() []knowledge.EngineAsset { return []knowledge.EngineAsset{*asset} }

	if _, err := service.ImportNative(context.Background(), bundlePath); err == nil || !strings.Contains(err.Error(), "smoke") {
		t.Fatalf("ImportNative error = %v, want smoke rejection", err)
	}
	if len(inv.events) != 0 || len(inv.engines) != 0 {
		t.Fatalf("untrusted import reached inventory: events=%#v engines=%+v", inv.events, inv.engines)
	}
}

func TestEngineImportNativeAcceptsRunnableNestedBundle(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX launcher")
	}
	root := t.TempDir()
	for _, dir := range []string{"bin", "lib", "libexec", "amdgcn"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "bin", "aima-engine")
	launcher := "#!/bin/sh\necho 'aima-engine-native 1.5.0-native'\n"
	if err := os.WriteFile(binary, []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"lib/libengine.so", "libexec/loader", "amdgcn/oclc.bc"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	asset := &knowledge.EngineAsset{
		Metadata: knowledge.EngineMetadata{Name: "aima-engine-native-amd395", Type: "aima-engine-native", Version: "1.5.0"},
		Runtime:  knowledge.EngineRuntime{Default: "native"},
		Source: &knowledge.EngineSource{
			Binary: "aima-engine", Platforms: []string{goruntime.GOOS + "/" + goruntime.GOARCH}, InstallType: "preinstalled",
			Probe: &knowledge.EngineSourceProbe{
				VersionCommand:  []string{"./aima-engine", "--version"},
				VersionPattern:  `aima-engine-native[[:space:]]+v?([0-9]+\.[0-9]+\.[0-9]+)-native`,
				FallbackVersion: "unknown",
			},
		},
	}
	inv := &fakeEngineLifecycleInventory{}
	dataDir := t.TempDir()
	service := &engineLifecycleService{
		inventory: inv, dataDir: dataDir,
		inventoryPlatform:  goruntime.GOOS + "-" + goruntime.GOARCH,
		catalogPlatform:    goruntime.GOOS + "/" + goruntime.GOARCH,
		nativeImportAssets: func() []knowledge.EngineAsset { return []knowledge.EngineAsset{*asset} },
	}

	imported, err := service.ImportNative(context.Background(), root)
	if err != nil {
		t.Fatalf("ImportNative: %v", err)
	}
	if imported.DetectedVersion != "1.5.0" || imported.VersionMatch != "exact" || !imported.Available {
		t.Fatalf("imported evidence = %+v", imported)
	}
	versionRoot := filepath.Dir(filepath.Dir(imported.BinaryPath))
	for _, path := range []string{"bin/aima-engine", "lib/libengine.so", "libexec/loader", "amdgcn/oclc.bc"} {
		if _, err := os.Stat(filepath.Join(versionRoot, filepath.FromSlash(path))); err != nil {
			t.Fatalf("missing imported bundle path %s: %v", path, err)
		}
	}
}

func TestEngineImportNativeStandalonePathPreservesRealBundleTopology(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX launcher")
	}
	root := t.TempDir()
	for _, dir := range []string{"bin", "lib", "libexec", "amdgcn"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "bin", "aima-engine")
	launcher := "#!/bin/sh\nroot=$(CDPATH= cd -- \"$(dirname \"$0\")/..\" && pwd)\n" +
		"if [ ! -f \"$root/libexec/loader\" ]; then echo 'bundled ELF loader is missing' >&2; exit 127; fi\n" +
		"echo 'aima-engine-native 1.5.0-native'\n"
	if err := os.WriteFile(binary, []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"lib/libengine.so", "libexec/loader", "amdgcn/oclc.bc"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(file)), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	asset := knowledge.EngineAsset{
		Metadata: knowledge.EngineMetadata{Name: "aima-engine-native-amd395", Type: "aima-engine-native", Version: "1.5.0"},
		Runtime:  knowledge.EngineRuntime{Default: "native"},
		Source: &knowledge.EngineSource{
			Binary: "aima-engine", Platforms: []string{goruntime.GOOS + "/" + goruntime.GOARCH}, InstallType: "preinstalled",
			Probe: &knowledge.EngineSourceProbe{
				VersionCommand: []string{"./aima-engine", "--version"},
				VersionPattern: `aima-engine-native[[:space:]]+v?([0-9]+\.[0-9]+\.[0-9]+)-native`,
			},
		},
	}
	inv := &fakeEngineLifecycleInventory{}
	service := &engineLifecycleService{
		inventory: inv, dataDir: t.TempDir(), inventoryPlatform: goruntime.GOOS + "-" + goruntime.GOARCH,
		catalogPlatform:    goruntime.GOOS + "/" + goruntime.GOARCH,
		nativeImportAssets: func() []knowledge.EngineAsset { return []knowledge.EngineAsset{asset} },
	}
	imported, err := service.ImportNative(context.Background(), binary)
	if err != nil {
		t.Fatalf("ImportNative standalone nested binary: %v", err)
	}
	versionRoot := filepath.Dir(filepath.Dir(imported.BinaryPath))
	for _, path := range []string{"bin/aima-engine", "lib/libengine.so", "libexec/loader", "amdgcn/oclc.bc"} {
		if _, err := os.Stat(filepath.Join(versionRoot, filepath.FromSlash(path))); err != nil {
			t.Fatalf("missing imported standalone bundle path %s: %v", path, err)
		}
	}
}

func TestEngineImportNativeRejectsBrokenStandaloneLauncher(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX launcher")
	}
	launcher := filepath.Join(t.TempDir(), "aima-engine")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\necho 'bundled ELF loader is missing' >&2\nexit 127\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	asset := knowledge.EngineAsset{
		Metadata: knowledge.EngineMetadata{Name: "aima-engine-native-amd395", Type: "aima-engine-native", Version: "1.5.0"},
		Source: &knowledge.EngineSource{
			Binary: "aima-engine", Platforms: []string{goruntime.GOOS + "/" + goruntime.GOARCH}, InstallType: "preinstalled",
			Probe: &knowledge.EngineSourceProbe{VersionCommand: []string{"./aima-engine", "--version"}, VersionPattern: `([0-9]+\.[0-9]+\.[0-9]+)`},
		},
	}
	inv := &fakeEngineLifecycleInventory{}
	service := &engineLifecycleService{
		inventory: inv, dataDir: t.TempDir(), inventoryPlatform: goruntime.GOOS + "-" + goruntime.GOARCH,
		catalogPlatform:    goruntime.GOOS + "/" + goruntime.GOARCH,
		nativeImportAssets: func() []knowledge.EngineAsset { return []knowledge.EngineAsset{asset} },
	}
	if _, err := service.ImportNative(context.Background(), launcher); err == nil || !strings.Contains(err.Error(), "bundled ELF loader is missing") {
		t.Fatalf("ImportNative error = %v, want explicit loader failure", err)
	}
	if len(inv.engines) != 0 {
		t.Fatalf("broken launcher reached inventory: %+v", inv.engines)
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

	result, err := service.Rollback(context.Background(), "engine-a", "native", true)
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

func TestEngineRollbackSelectsRuntimeGroup(t *testing.T) {
	nativePrevious := &state.Engine{
		ID: "native-v1", AssetName: "engine-a", Platform: "windows-amd64", RuntimeType: "native",
		Available: true, LifecycleStatus: "verified", VerificationStatus: "verified",
	}
	nativeCurrent := &state.Engine{
		ID: "native-v2", AssetName: "engine-a", Platform: "windows-amd64", RuntimeType: "native",
		Available: true, Active: true, LifecycleStatus: "active", VerificationStatus: "verified",
		PreviousEngineID: nativePrevious.ID,
	}
	containerPrevious := &state.Engine{
		ID: "container-v1", AssetName: "engine-a", Platform: "windows-amd64", RuntimeType: "container",
		Available: true, LifecycleStatus: "verified", VerificationStatus: "verified",
	}
	containerCurrent := &state.Engine{
		ID: "container-v2", AssetName: "engine-a", Platform: "windows-amd64", RuntimeType: "container",
		Available: true, Active: true, LifecycleStatus: "active", VerificationStatus: "verified",
		PreviousEngineID: containerPrevious.ID,
	}
	inv := &fakeEngineLifecycleInventory{engines: []*state.Engine{
		nativePrevious, nativeCurrent, containerPrevious, containerCurrent,
	}}
	service := newTestEngineLifecycleService(t, inv, testNativeLifecycleAsset("2.0.0", "windows/amd64"))

	result, err := service.Rollback(context.Background(), "engine-a", "native", true)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.RuntimeType != "native" || result.ActiveEngineID != nativePrevious.ID {
		t.Fatalf("result = %+v", result)
	}
	if nativeCurrent.Active || !nativePrevious.Active || !containerCurrent.Active || containerPrevious.Active {
		t.Fatalf("runtime-scoped rollback changed wrong group: native=%+v/%+v container=%+v/%+v",
			nativePrevious, nativeCurrent, containerPrevious, containerCurrent)
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

	if _, err := service.Rollback(context.Background(), "engine-a", "native", true); err != nil {
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

	result, err := service.Rollback(context.Background(), "engine-a", "native", false)
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

			if _, err := service.Rollback(context.Background(), "engine-a", "native", true); err == nil {
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

	result, err := service.Rollback(ctx, "engine-a", "native", true)
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
