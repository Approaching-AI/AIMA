package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedVersionDir(t *testing.T) {
	root := t.TempDir()
	got, err := ManagedVersionDir(root, "windows-amd64", "engine-a", "1.2.3")
	if err != nil {
		t.Fatalf("ManagedVersionDir: %v", err)
	}
	want := filepath.Join(root, "dist", "windows-amd64", "engine-a", "1.2.3")
	if got != want {
		t.Fatalf("ManagedVersionDir = %q, want %q", got, want)
	}
}

func TestManagedVersionDirRejectsTraversal(t *testing.T) {
	for _, tc := range []struct {
		platform string
		asset    string
		version  string
	}{
		{platform: "../windows-amd64", asset: "engine", version: "1.0"},
		{platform: "windows-amd64", asset: "../bad", version: "1.0"},
		{platform: "windows-amd64", asset: "engine", version: "../bad"},
		{platform: "windows-amd64", asset: "engine", version: ".."},
	} {
		if _, err := ManagedVersionDir("/data", tc.platform, tc.asset, tc.version); err == nil {
			t.Fatalf("expected traversal rejection for %#v", tc)
		}
	}
}

func TestNewStagingDirUsesEngineStagingRoot(t *testing.T) {
	root := t.TempDir()
	got, err := NewStagingDir(root, "engine-a")
	if err != nil {
		t.Fatalf("NewStagingDir: %v", err)
	}
	parent := filepath.Join(root, "staging", "engines")
	if filepath.Dir(got) != parent || !strings.HasPrefix(filepath.Base(got), "engine-a-") {
		t.Fatalf("staging directory = %q, want %s/engine-a-*", got, parent)
	}
	if info, err := os.Stat(got); err != nil || !info.IsDir() {
		t.Fatalf("staging directory not created: %v", err)
	}
}

func TestPromoteStagedDir(t *testing.T) {
	root := t.TempDir()
	staged := filepath.Join(root, "staged")
	dest := filepath.Join(root, "managed", "engine", "1.0")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "engine"), []byte("verified"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PromoteStagedDir(staged, dest); err != nil {
		t.Fatalf("PromoteStagedDir: %v", err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staging directory should have moved, stat error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "engine"))
	if err != nil || string(got) != "verified" {
		t.Fatalf("promoted engine = %q, %v", got, err)
	}
}

func TestPromoteStagedDirDoesNotReplaceExisting(t *testing.T) {
	root := t.TempDir()
	staged := filepath.Join(root, "staged")
	dest := filepath.Join(root, "dest")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "engine"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "engine"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := PromoteStagedDir(staged, dest); err == nil {
		t.Fatal("expected conflicting destination error")
	}
	got, err := os.ReadFile(filepath.Join(dest, "engine"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("existing destination was replaced: %q", got)
	}
}

func TestPromoteStagedDirReusesIdenticalExisting(t *testing.T) {
	root := t.TempDir()
	staged := filepath.Join(root, "staged")
	dest := filepath.Join(root, "dest")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{staged, dest} {
		if err := os.WriteFile(filepath.Join(dir, "engine"), []byte("same"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := PromoteStagedDir(staged, dest); err != nil {
		t.Fatalf("identical destination should be reused: %v", err)
	}
}

func TestBuildEnsurePlanPrefersExactPreinstalled(t *testing.T) {
	plan := BuildEnsurePlan(
		EnsureRequest{Name: "engine-a", Version: "1.2.3", Platform: "windows-amd64"},
		EnsureCandidate{
			AssetName:            "engine-a",
			Version:              "1.2.3",
			RuntimeType:          "native",
			Origin:               "managed",
			Source:               "https://catalog.example/engine.zip",
			NetworkRequired:      true,
			VerificationEvidence: "sha256:catalog",
		},
		[]InstalledEngine{{
			ID:                 "preinstalled-id",
			AssetName:          "engine-a",
			Version:            "1.2.3",
			Platform:           "windows-amd64",
			RuntimeType:        "native",
			Origin:             "preinstalled",
			Source:             `D:\engines\engine.exe`,
			Available:          true,
			VerificationStatus: "verified",
		}},
		nil,
	)
	if plan.Action != "reuse" || plan.CandidateEngineID != "preinstalled-id" {
		t.Fatalf("plan = %+v, want exact preinstalled reuse", plan)
	}
	if plan.NetworkRequired {
		t.Fatal("exact preinstalled reuse must not require network")
	}
	if plan.Origin != "preinstalled" {
		t.Fatalf("origin = %q, want preinstalled", plan.Origin)
	}
}

func TestBuildEnsurePlanReusesCatalogCompatibleInstalledVersion(t *testing.T) {
	plan := BuildEnsurePlan(
		EnsureRequest{Name: "llamacpp-hip-linux", Platform: "linux-amd64"},
		EnsureCandidate{
			AssetName:            "llamacpp-hip-linux",
			Version:              "b9330",
			CompatibleVersions:   []string{"b9637"},
			RuntimeType:          "native",
			Origin:               "managed",
			Source:               "https://catalog.example/llama.tar.gz",
			NetworkRequired:      true,
			VerificationEvidence: "sha256:catalog",
		},
		[]InstalledEngine{{
			ID:                 "imported-b9637",
			AssetName:          "llamacpp-hip-linux",
			Version:            "9637",
			CatalogVersion:     "b9330",
			DetectedVersion:    "9637",
			VersionMatch:       "compatible",
			Platform:           "linux-amd64",
			RuntimeType:        "native",
			Origin:             "imported",
			Available:          true,
			VerificationStatus: "verified",
		}},
		nil,
	)
	if plan.Blocked || plan.Action != "reuse" || plan.CandidateEngineID != "imported-b9637" || plan.NetworkRequired {
		t.Fatalf("plan = %+v, want compatible local reuse", plan)
	}
}

func TestBuildEnsurePlanPrefersExactVersionOverActiveCompatibleVersion(t *testing.T) {
	plan := BuildEnsurePlan(
		EnsureRequest{Name: "engine-a", Version: "2.0.0", Platform: "linux-amd64"},
		EnsureCandidate{
			AssetName: "engine-a", Version: "2.0.0", CompatibleVersions: []string{"1.0.0"}, RuntimeType: "native",
		},
		[]InstalledEngine{
			{
				ID: "active-compatible", AssetName: "engine-a", Version: "1.0.0", CatalogVersion: "2.0.0",
				DetectedVersion: "1.0.0", VersionMatch: "compatible", Platform: "linux-amd64", RuntimeType: "native",
				Available: true, Active: true, VerificationStatus: "verified",
			},
			{
				ID: "inactive-exact", AssetName: "engine-a", Version: "2.0.0", CatalogVersion: "2.0.0",
				DetectedVersion: "2.0.0", VersionMatch: "exact", Platform: "linux-amd64", RuntimeType: "native",
				Available: true, VerificationStatus: "verified",
			},
		},
		nil,
	)
	if plan.Blocked || plan.Action != "reuse" || plan.CandidateEngineID != "inactive-exact" {
		t.Fatalf("plan = %+v, want exact version reuse", plan)
	}
}

func TestBuildEnsurePlanDoesNotReuseCompatibilityRemovedFromCatalog(t *testing.T) {
	plan := BuildEnsurePlan(
		EnsureRequest{Name: "llamacpp-hip-linux", Platform: "linux-amd64"},
		EnsureCandidate{
			AssetName:            "llamacpp-hip-linux",
			Version:              "b9330",
			RuntimeType:          "native",
			Origin:               "managed",
			Source:               "https://catalog.example/llama.tar.gz",
			NetworkRequired:      true,
			VerificationEvidence: "sha256:catalog",
		},
		[]InstalledEngine{{
			ID:                 "formerly-compatible-b9637",
			AssetName:          "llamacpp-hip-linux",
			Version:            "9637",
			CatalogVersion:     "b9330",
			DetectedVersion:    "9637",
			VersionMatch:       "compatible",
			Platform:           "linux-amd64",
			RuntimeType:        "native",
			Origin:             "imported",
			Available:          true,
			VerificationStatus: "verified",
		}},
		nil,
	)
	if plan.Action != "install" || plan.CandidateEngineID == "formerly-compatible-b9637" {
		t.Fatalf("plan = %+v, removed compatibility must not be reused", plan)
	}
}

func TestBuildEnsurePlanBlocksNetworkInstallWithoutDigest(t *testing.T) {
	plan := BuildEnsurePlan(
		EnsureRequest{Name: "engine-a", Version: "2.0.0", Platform: "linux-amd64"},
		EnsureCandidate{
			AssetName:       "engine-a",
			Version:         "2.0.0",
			RuntimeType:     "native",
			Origin:          "managed",
			Source:          "https://catalog.example/engine.tar.gz",
			NetworkRequired: true,
		},
		nil,
		nil,
	)
	if !plan.Blocked || !strings.Contains(strings.ToLower(plan.BlockReason), "checksum") {
		t.Fatalf("plan = %+v, want checksum block", plan)
	}
}

func TestBuildEnsurePlanDoesNotSilentlyReplaceUnverifiedActive(t *testing.T) {
	plan := BuildEnsurePlan(
		EnsureRequest{Name: "engine-a", Version: "2.0.0", Platform: "linux-amd64", Apply: true},
		EnsureCandidate{
			AssetName:            "engine-a",
			Version:              "2.0.0",
			RuntimeType:          "native",
			Origin:               "managed",
			Source:               "https://catalog.example/engine.tar.gz",
			NetworkRequired:      true,
			VerificationEvidence: "sha256:catalog",
		},
		[]InstalledEngine{{
			ID:                 "active-v1",
			AssetName:          "engine-a",
			Version:            "1.0.0",
			Platform:           "linux-amd64",
			RuntimeType:        "native",
			Origin:             "preinstalled",
			Available:          true,
			Active:             true,
			VerificationStatus: "unverified",
		}},
		[]string{"deployment-a"},
	)
	if plan.Blocked || plan.Action != "upgrade" {
		t.Fatalf("plan = %+v, want verified staged upgrade", plan)
	}
	if plan.CurrentEngineID != "active-v1" || plan.CandidateEngineID != "" {
		t.Fatalf("plan replaced active before apply: %+v", plan)
	}
	if len(plan.AffectedDeployments) != 1 || plan.AffectedDeployments[0] != "deployment-a" {
		t.Fatalf("affected deployments = %#v", plan.AffectedDeployments)
	}
}

func TestBuildEnsurePlanDoesNotActivateUnverifiedInactivePreinstalled(t *testing.T) {
	plan := BuildEnsurePlan(
		EnsureRequest{Name: "engine-a", Version: "2.0.0", Platform: "windows-amd64", Apply: true},
		EnsureCandidate{
			AssetName:            "engine-a",
			Version:              "2.0.0",
			RuntimeType:          "native",
			Origin:               "managed",
			Source:               "https://catalog.example/engine.zip",
			NetworkRequired:      true,
			VerificationEvidence: "sha256:catalog",
		},
		[]InstalledEngine{{
			ID: "preinstalled-unverified", AssetName: "engine-a", Version: "2.0.0",
			Platform: "windows-amd64", RuntimeType: "native", Origin: "preinstalled",
			Available: true, VerificationStatus: "unverified",
		}},
		nil,
	)
	if !plan.Blocked || plan.Action != "reuse" {
		t.Fatalf("plan = %+v, want blocked unverified reuse", plan)
	}
}

func TestBuildEnsurePlanDoesNotTreatCatalogVersionAsDetectedVersion(t *testing.T) {
	plan := BuildEnsurePlan(
		EnsureRequest{Name: "engine-a", Version: "2.0.0", Platform: "windows-amd64"},
		EnsureCandidate{
			AssetName: "engine-a", Version: "2.0.0", RuntimeType: "container", Origin: "managed",
			Source: "registry.example/engine-a:2.0.0", VerificationEvidence: "sha256:catalog",
		},
		[]InstalledEngine{{
			ID: "unversioned-local", AssetName: "engine-a", CatalogVersion: "2.0.0",
			Platform: "windows-amd64", RuntimeType: "container", Origin: "preinstalled", Available: true,
		}},
		nil,
	)
	if plan.Action != "install" || plan.CandidateEngineID == "unversioned-local" {
		t.Fatalf("plan = %+v, Catalog version must not impersonate detected version", plan)
	}
}

func TestManagedNativeEngineIDMatchesScannerIdentity(t *testing.T) {
	root := t.TempDir()
	relative := filepath.Join("engine-a", "2.0.0", "engine-server")
	binaryPath := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	images, err := ScanNative(t.Context(), ScanOptions{
		DistDir: root, Platform: "linux-amd64",
		Assets:       []AssetDescriptor{{AssetName: "engine-a", Type: "native-engine", CatalogVersion: "2.0.0"}},
		BinaryAssets: map[string]string{"engine-server": "engine-a"},
	})
	if err != nil {
		t.Fatalf("ScanNative: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("images = %+v", images)
	}
	want := ManagedNativeEngineID("engine-a", "2.0.0", relative)
	if images[0].ID != want {
		t.Fatalf("managed ID = %q, want %q", images[0].ID, want)
	}
	if images[0].VersionMatch != "exact" || images[0].ContentDigest == "" {
		t.Fatalf("managed evidence = %+v", images[0])
	}
}
