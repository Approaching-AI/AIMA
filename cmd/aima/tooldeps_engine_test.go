package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
)

type fakeEngineRemovalStore struct {
	engine       *state.Engine
	referenced   bool
	events       []string
	snapshot     *state.RollbackSnapshot
	referenceErr error
	snapshotErr  error
	deleteErr    error
}

func (f *fakeEngineRemovalStore) GetEngine(context.Context, string) (*state.Engine, error) {
	if f.engine == nil {
		return nil, errors.New("engine not found")
	}
	clone := *f.engine
	return &clone, nil
}

func (f *fakeEngineRemovalStore) EngineHasReferences(context.Context, string) (bool, error) {
	f.events = append(f.events, "references")
	return f.referenced, f.referenceErr
}

func (f *fakeEngineRemovalStore) SaveSnapshot(_ context.Context, snapshot *state.RollbackSnapshot) error {
	f.events = append(f.events, "snapshot")
	if f.snapshotErr != nil {
		return f.snapshotErr
	}
	clone := *snapshot
	f.snapshot = &clone
	return nil
}

func (f *fakeEngineRemovalStore) DeleteEngine(context.Context, string) error {
	f.events = append(f.events, "delete")
	return f.deleteErr
}

func TestEngineRemoveProtectsPreinstalledAndLegacyFiles(t *testing.T) {
	for _, origin := range []string{"preinstalled", "legacy"} {
		t.Run(origin, func(t *testing.T) {
			dataDir := t.TempDir()
			binary := filepath.Join(t.TempDir(), "engine-server")
			if err := os.WriteFile(binary, []byte("external"), 0o755); err != nil {
				t.Fatal(err)
			}
			store := &fakeEngineRemovalStore{engine: &state.Engine{
				ID: "engine-v1", Origin: origin, RuntimeType: "native", BinaryPath: binary, Location: binary,
			}}

			if err := removeEngine(context.Background(), store, dataDir, "engine-v1", true); err == nil {
				t.Fatal("expected physical-delete rejection")
			}
			if got, err := os.ReadFile(binary); err != nil || string(got) != "external" {
				t.Fatalf("protected file changed: %q, %v", got, err)
			}
			if len(store.events) != 1 || store.events[0] != "references" {
				t.Fatalf("events = %#v", store.events)
			}
		})
	}
}

func TestEngineRemoveProtectsManagedPathOutsideDataDir(t *testing.T) {
	dataDir := t.TempDir()
	binary := filepath.Join(t.TempDir(), "engine-server")
	if err := os.WriteFile(binary, []byte("external"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := &fakeEngineRemovalStore{engine: &state.Engine{
		ID: "engine-v1", Origin: "managed", RuntimeType: "native", BinaryPath: binary, Location: binary,
	}}

	err := removeEngine(context.Background(), store, dataDir, "engine-v1", true)
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("error = %v, want outside-data-dir rejection", err)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("outside file changed: %v", err)
	}
}

func TestEngineRemoveProtectsReferencedVersions(t *testing.T) {
	for _, reference := range []string{"active version", "previous rollback link", "deployment intent"} {
		t.Run(reference, func(t *testing.T) {
			dataDir := t.TempDir()
			binary := filepath.Join(dataDir, "dist", "linux-amd64", "engine-a", "1.0.0", "engine-server")
			if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(binary, []byte("managed"), 0o755); err != nil {
				t.Fatal(err)
			}
			store := &fakeEngineRemovalStore{referenced: true, engine: &state.Engine{
				ID: "engine-v1", Origin: "managed", RuntimeType: "native", BinaryPath: binary, Location: binary,
			}}

			if err := removeEngine(context.Background(), store, dataDir, "engine-v1", true); err == nil {
				t.Fatal("expected referenced-engine rejection")
			}
			if _, err := os.Stat(binary); err != nil {
				t.Fatalf("referenced file changed: %v", err)
			}
			if len(store.events) != 1 || store.events[0] != "references" {
				t.Fatalf("events = %#v", store.events)
			}
		})
	}
}

func TestEngineRemoveManagedFileAfterAuthorization(t *testing.T) {
	dataDir := t.TempDir()
	binary := filepath.Join(dataDir, "dist", "linux-amd64", "engine-a", "1.0.0", "engine-server")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("managed"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := &fakeEngineRemovalStore{engine: &state.Engine{
		ID: "engine-v1", AssetName: "engine-a", Version: "1.0.0", Origin: "managed",
		RuntimeType: "native", BinaryPath: binary, Location: binary,
	}}

	if err := removeEngine(context.Background(), store, dataDir, "engine-v1", true); err != nil {
		t.Fatalf("removeEngine: %v", err)
	}
	if _, err := os.Stat(binary); !os.IsNotExist(err) {
		t.Fatalf("managed file remains: %v", err)
	}
	if store.snapshot == nil || store.snapshot.ResourceName != "engine-v1" {
		t.Fatalf("snapshot = %+v", store.snapshot)
	}
	wantEvents := []string{"references", "snapshot", "delete"}
	if len(store.events) != len(wantEvents) {
		t.Fatalf("events = %#v", store.events)
	}
	for i := range wantEvents {
		if store.events[i] != wantEvents[i] {
			t.Fatalf("events = %#v", store.events)
		}
	}
}

func TestEngineRemoveRejectsSymlinkEscape(t *testing.T) {
	dataDir := t.TempDir()
	external := filepath.Join(t.TempDir(), "engine-server")
	if err := os.WriteFile(external, []byte("external"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dataDir, "dist", "engine-link")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store := &fakeEngineRemovalStore{engine: &state.Engine{
		ID: "engine-v1", Origin: "managed", RuntimeType: "native", BinaryPath: link, Location: link,
	}}

	if err := removeEngine(context.Background(), store, dataDir, "engine-v1", true); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
	if got, err := os.ReadFile(external); err != nil || string(got) != "external" {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
}

func TestEngineRemoveRejectsContainerPhysicalDelete(t *testing.T) {
	store := &fakeEngineRemovalStore{engine: &state.Engine{
		ID: "engine-v1", Origin: "managed", RuntimeType: "container",
		Image: "registry.example/engine-a", Tag: "1.0.0", Location: "registry.example/engine-a:1.0.0",
	}}

	err := removeEngine(context.Background(), store, t.TempDir(), "engine-v1", true)
	if err == nil || !strings.Contains(err.Error(), "AIMA_DATA_DIR") {
		t.Fatalf("error = %v, want data-directory ownership rejection", err)
	}
	if len(store.events) != 1 || store.events[0] != "references" {
		t.Fatalf("events = %#v", store.events)
	}
}

func TestEngineOfflineLifecycleIntegration(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := state.Open(ctx, filepath.Join(dataDir, "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	externalDir := t.TempDir()
	externalBinary := filepath.Join(externalDir, "engine-server")
	if err := os.WriteFile(externalBinary, []byte("engine-v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "engine-a-2.0.0.tar.gz")
	writeNativeEngineBundle(t, bundlePath, "engine-a", "2.0.0", "engine-server", []byte("engine-v2"))
	bundleDigest, err := sha256FileDigest(bundlePath)
	if err != nil {
		t.Fatalf("bundle digest: %v", err)
	}

	asset := knowledge.EngineAsset{
		Metadata: knowledge.EngineMetadata{
			Name: "engine-a", Type: "engine-type", Version: "2.0.0", CompatibleVersions: []string{"1.0.0"},
		},
		Runtime: knowledge.EngineRuntime{Default: "native"},
		Source: &knowledge.EngineSource{
			Binary: "engine-server", Platforms: []string{"linux/amd64"},
			SHA256: map[string]string{"linux/amd64": bundleDigest},
			Probe: &knowledge.EngineSourceProbe{
				Paths: []string{externalBinary}, FallbackVersion: "1.0.0",
			},
		},
	}
	descriptor := engine.AssetDescriptor{
		AssetName: "engine-a", Type: "engine-type", CatalogVersion: "2.0.0",
		CompatibleVersions: []string{"1.0.0"}, Probe: asset.Source.Probe,
	}
	t.Setenv("PATH", "")
	scanned, err := engine.ScanUnified(ctx, engine.ScanOptions{
		Assets: []engine.AssetDescriptor{descriptor}, Runner: &mockCommandRunner{},
		DistDir: filepath.Join(dataDir, "dist", "linux-amd64"), ExtraDirs: []string{externalDir},
		Platform: "linux-amd64", BinaryAssets: map[string]string{"engine-server": "engine-a"},
	})
	if err != nil {
		t.Fatalf("ScanUnified: %v", err)
	}
	var preinstalled *state.Engine
	for _, image := range scanned {
		if image.AssetName == "engine-a" && image.DetectedVersion == "1.0.0" {
			preinstalled = stateEngineFromScan(image)
			break
		}
	}
	if preinstalled == nil || preinstalled.Origin != "preinstalled" || preinstalled.BinaryPath != externalBinary {
		t.Fatalf("preinstalled scan evidence = %+v", preinstalled)
	}
	preinstalled.Active = true
	preinstalled.LifecycleStatus = "active"
	preinstalled.VerificationStatus = "verified"
	if err := db.InsertEngine(ctx, preinstalled); err != nil {
		t.Fatalf("InsertEngine(preinstalled): %v", err)
	}

	service := &engineLifecycleService{
		inventory: db, dataDir: dataDir, inventoryPlatform: "linux-amd64", catalogPlatform: "linux/amd64",
		resolveAsset:       func(context.Context, string) (*knowledge.EngineAsset, error) { return &asset, nil },
		nativeImportAssets: func() []knowledge.EngineAsset { return []knowledge.EngineAsset{asset} },
	}
	plan, err := service.Ensure(ctx, engine.EnsureRequest{Name: "engine-a", Version: "1.0.0", Apply: false})
	if err != nil {
		t.Fatalf("Ensure(v1 plan): %v", err)
	}
	if plan.Plan.Action != "reuse" || plan.Plan.Blocked || plan.Plan.NetworkRequired || plan.Plan.CandidateEngineID != preinstalled.ID {
		t.Fatalf("preinstalled ensure plan = %+v", plan)
	}

	asset.Source.SHA256["linux/amd64"] = "sha256:" + strings.Repeat("0", 64)
	if _, err := service.ImportNative(ctx, bundlePath); err == nil {
		t.Fatal("expected checksum mismatch")
	}
	oldAfterFailure, err := db.GetEngine(ctx, preinstalled.ID)
	if err != nil || !oldAfterFailure.Active {
		t.Fatalf("failed import changed active engine: %+v, %v", oldAfterFailure, err)
	}
	destination, err := engine.ManagedVersionDir(dataDir, "linux-amd64", "engine-a", "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("failed import left destination: %v", err)
	}

	asset.Source.SHA256["linux/amd64"] = bundleDigest
	imported, err := service.ImportNative(ctx, bundlePath)
	if err != nil {
		t.Fatalf("ImportNative: %v", err)
	}
	if imported.Origin != "imported" || imported.Version != "2.0.0" || imported.VerificationStatus != "verified" || imported.Active {
		t.Fatalf("imported inventory = %+v", imported)
	}
	if !strings.HasPrefix(imported.BinaryPath, destination+string(filepath.Separator)) {
		t.Fatalf("imported path %q is not versioned below %q", imported.BinaryPath, destination)
	}
	if _, err := os.Stat(externalBinary); err != nil {
		t.Fatalf("import moved external preinstall: %v", err)
	}

	activated, err := service.Ensure(ctx, engine.EnsureRequest{Name: "engine-a", Version: "2.0.0", Apply: true})
	if err != nil {
		t.Fatalf("Ensure(v2 apply): %v", err)
	}
	if !activated.Applied || activated.ActiveEngineID != imported.ID || activated.PreviousEngineID != preinstalled.ID {
		t.Fatalf("activation result = %+v", activated)
	}
	rolledBack, err := service.Rollback(ctx, "engine-a", "native", true)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolledBack.ActiveEngineID != preinstalled.ID || rolledBack.OldActiveEngineID != imported.ID {
		t.Fatalf("rollback result = %+v", rolledBack)
	}

	if err := removeEngine(ctx, db, dataDir, preinstalled.ID, true); err == nil {
		t.Fatal("expected external preinstall delete rejection")
	}
	if got, err := os.ReadFile(externalBinary); err != nil || string(got) != "engine-v1" {
		t.Fatalf("external preinstall changed: %q, %v", got, err)
	}
}

func writeNativeEngineBundle(t *testing.T, path, asset, version, binary string, content []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	header := &tar.Header{
		Name: filepath.ToSlash(filepath.Join(asset, version, binary)), Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLooksLikeNativeEngineBundleLeavesCompressedTarAmbiguous(t *testing.T) {
	for _, path := range []string{"engine.tar.gz", "engine.tgz"} {
		if looksLikeNativeEngineBundle(path) {
			t.Fatalf("%s was classified as Native before container import", path)
		}
	}
	for _, path := range []string{"engine.zip", "engine.exe", "engine.appimage"} {
		if !looksLikeNativeEngineBundle(path) {
			t.Fatalf("%s was not classified as an unambiguous Native bundle", path)
		}
	}
}
