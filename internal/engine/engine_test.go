package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// mockRunner implements CommandRunner for tests
type mockRunner struct {
	responses map[string]mockResponse
}

type mockResponse struct {
	output []byte
	err    error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	if resp, ok := m.responses[key]; ok {
		return resp.output, resp.err
	}
	return nil, fmt.Errorf("command not mocked: %s", key)
}

func (m *mockRunner) Pipe(ctx context.Context, from, to []string) error {
	if _, err := m.Run(ctx, from[0], from[1:]...); err != nil {
		return err
	}
	_, err := m.Run(ctx, to[0], to[1:]...)
	return err
}

// --- crictl image list format for tests ---
type crictlImageList struct {
	Images []crictlImage `json:"images"`
}

type crictlImage struct {
	ID          string   `json:"id"`
	RepoTags    []string `json:"repoTags"`
	RepoDigests []string `json:"repoDigests"`
	Size        string   `json:"size"`
}

func TestScanWithCrictl(t *testing.T) {
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:abc123",
				RepoTags: []string{"vllm/vllm-openai:latest"},
				Size:     "8500000000",
			},
			{
				ID:       "sha256:def456",
				RepoTags: []string{"ghcr.io/ggerganov/llama.cpp:server"},
				Size:     "500000000",
			},
			{
				ID:       "sha256:ghi789",
				RepoTags: []string{"nginx:latest"},
				Size:     "100000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	engineAssets := map[string][]string{
		"vllm":     {"vllm/vllm-openai"},
		"llamacpp": {"ghcr.io/ggerganov/llama.cpp"},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: engineAssets,
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 matched engines, got %d", len(results))
	}

	// Check vllm
	var vllm, llamacpp *EngineImage
	for _, r := range results {
		switch r.Type {
		case "vllm":
			vllm = r
		case "llamacpp":
			llamacpp = r
		}
	}

	if vllm == nil {
		t.Fatal("vllm engine not found")
	}
	if vllm.Image != "vllm/vllm-openai" {
		t.Errorf("expected image vllm/vllm-openai, got %s", vllm.Image)
	}
	if vllm.Tag != "latest" {
		t.Errorf("expected tag latest, got %s", vllm.Tag)
	}
	if !vllm.Available {
		t.Error("expected vllm to be available")
	}

	if llamacpp == nil {
		t.Fatal("llamacpp engine not found")
	}
	if llamacpp.Tag != "server" {
		t.Errorf("expected tag server, got %s", llamacpp.Tag)
	}
}

func TestScanK3sCrictlFallback(t *testing.T) {
	// When standalone crictl is not available, scanner should try k3s crictl
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:abc123",
				RepoTags: []string{"vllm/vllm-openai:qwen3_5-cu130"},
				Size:     "8800000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json":     {err: fmt.Errorf("crictl not found")},
			"k3s crictl images -o json": {output: imageJSON},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{"vllm-nightly": {"vllm/vllm-openai:qwen3_5"}},
		Runner:        runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 engine, got %d", len(results))
	}
	if results[0].Type != "vllm-nightly" {
		t.Errorf("expected type vllm-nightly, got %s", results[0].Type)
	}
	if results[0].Tag != "qwen3_5-cu130" {
		t.Errorf("expected tag qwen3_5-cu130, got %s", results[0].Tag)
	}
}

func TestScanTagAwarePatternPriority(t *testing.T) {
	// Tag-aware patterns should take priority over repo-only patterns.
	// vllm/vllm-openai:qwen3_5-cu130 should match vllm-nightly (tag pattern)
	// not vllm (repo pattern "vllm/"), even though both could match.
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:abc123",
				RepoTags: []string{"vllm/vllm-openai:qwen3_5-cu130"},
				Size:     "8800000000",
			},
			{
				ID:       "sha256:def456",
				RepoTags: []string{"vllm/vllm-openai:v0.8.5"},
				Size:     "9000000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{
			"vllm":         {"vllm/vllm-openai"},
			"vllm-nightly": {"vllm/vllm-openai:qwen3_5"},
		},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 engines, got %d", len(results))
	}

	var nightly, stable *EngineImage
	for _, r := range results {
		switch r.Type {
		case "vllm-nightly":
			nightly = r
		case "vllm":
			stable = r
		}
	}

	if nightly == nil {
		t.Fatal("vllm-nightly engine not found")
	}
	if nightly.Tag != "qwen3_5-cu130" {
		t.Errorf("expected nightly tag qwen3_5-cu130, got %s", nightly.Tag)
	}

	if stable == nil {
		t.Fatal("vllm engine not found")
	}
	if stable.Tag != "v0.8.5" {
		t.Errorf("expected stable tag v0.8.5, got %s", stable.Tag)
	}
}

func TestPatternMatchExactAnchors(t *testing.T) {
	// ^pattern$ should match exactly
	patterns := []patternEntry{
		{pattern: "^vllm-nightly$", engineType: "vllm-nightly"},
	}

	if got := patternMatch("vllm-nightly", patterns); got != "vllm-nightly" {
		t.Errorf("^vllm-nightly$ should match 'vllm-nightly', got %q", got)
	}
	if got := patternMatch("vllm-nightly-extra", patterns); got != "" {
		t.Errorf("^vllm-nightly$ should NOT match 'vllm-nightly-extra', got %q", got)
	}
	if got := patternMatch("pre-vllm-nightly", patterns); got != "" {
		t.Errorf("^vllm-nightly$ should NOT match 'pre-vllm-nightly', got %q", got)
	}
}

func TestPatternMatchDeterministicPriority(t *testing.T) {
	patterns := []patternEntry{
		{pattern: "vllm", engineType: "contains"},
		{pattern: "^vllm$", engineType: "exact"},
		{pattern: "^vllm-nightly$", engineType: "nightly"},
	}
	for i := 0; i < 100; i++ {
		if got := patternMatch("vllm", patterns); got != "exact" {
			t.Fatalf("iteration %d: expected exact, got %q", i, got)
		}
	}
}


func TestScanFallbackToDocker(t *testing.T) {
	// Docker image list JSON format (one JSON object per line)
	dockerImages := []string{
		`{"Repository":"vllm/vllm-openai","Tag":"v0.8","ID":"abc123","Size":"8.5GB"}`,
	}
	dockerOutput := ""
	for _, img := range dockerImages {
		dockerOutput += img + "\n"
	}

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json":                                                    {err: fmt.Errorf("crictl not found")},
			"docker images --format {{json .}} --no-trunc": {output: []byte(dockerOutput)},
		},
	}

	engineAssets := map[string][]string{
		"vllm": {"vllm/vllm-openai"},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: engineAssets,
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 engine, got %d", len(results))
	}
	if results[0].Type != "vllm" {
		t.Errorf("expected type vllm, got %s", results[0].Type)
	}
	if results[0].Tag != "v0.8" {
		t.Errorf("expected tag v0.8, got %s", results[0].Tag)
	}
}

func TestScanBothFail(t *testing.T) {
	// ScanUnified gracefully degrades: if no container runtime is available
	// it returns an empty list without error (native scan still runs).
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json":                         {err: fmt.Errorf("crictl not found")},
			"docker images --format {{json .}} --no-trunc": {err: fmt.Errorf("docker not found")},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{"vllm": {"vllm/vllm-openai"}},
		Runner:        runner,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results when no runtime available, got %d", len(results))
	}
}

func TestScanNoMatchingImages(t *testing.T) {
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:xyz",
				RepoTags: []string{"nginx:latest"},
				Size:     "100000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{"vllm": {"vllm/vllm-openai"}},
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 matched engines, got %d", len(results))
	}
}

func TestScanEmptyAssetPatterns(t *testing.T) {
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:abc",
				RepoTags: []string{"vllm/vllm-openai:latest"},
				Size:     "8500000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{},
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 engines when no assets configured, got %d", len(results))
	}
}

// --- Pull tests ---

func TestPullFirstRegistrySucceeds(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl pull docker.io/vllm/vllm-openai:latest": {output: []byte("pulled")},
		},
	}

	err := Pull(context.Background(), PullOptions{
		Image:      "vllm/vllm-openai",
		Tag:        "latest",
		Registries: []string{"docker.io"},
		Runner:     runner,
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
}

func TestPullFirstFailsSecondSucceeds(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl pull docker.io/vllm/vllm-openai:latest":                                       {err: fmt.Errorf("timeout")},
			"crictl pull registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai:latest": {output: []byte("pulled")},
		},
	}

	err := Pull(context.Background(), PullOptions{
		Image:      "vllm/vllm-openai",
		Tag:        "latest",
		Registries: []string{"docker.io", "registry.cn-hangzhou.aliyuncs.com/aima"},
		Runner:     runner,
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
}

func TestPullAllRegistriesFail(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl pull docker.io/vllm/vllm-openai:latest":                                       {err: fmt.Errorf("timeout")},
			"crictl pull registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai:latest": {err: fmt.Errorf("auth fail")},
		},
	}

	err := Pull(context.Background(), PullOptions{
		Image:      "vllm/vllm-openai",
		Tag:        "latest",
		Registries: []string{"docker.io", "registry.cn-hangzhou.aliyuncs.com/aima"},
		Runner:     runner,
	})
	if err == nil {
		t.Error("expected error when all registries fail")
	}
}

func TestPullFallbackToDocker(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl pull docker.io/vllm/vllm-openai:latest": {err: fmt.Errorf("crictl not found")},
			"docker pull docker.io/vllm/vllm-openai:latest": {output: []byte("pulled")},
		},
	}

	err := Pull(context.Background(), PullOptions{
		Image:      "vllm/vllm-openai",
		Tag:        "latest",
		Registries: []string{"docker.io"},
		Runner:     runner,
	})
	if err != nil {
		t.Fatalf("pull with docker fallback: %v", err)
	}
}

func TestPullContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := &mockRunner{
		responses: map[string]mockResponse{},
	}

	err := Pull(ctx, PullOptions{
		Image:      "vllm/vllm-openai",
		Tag:        "latest",
		Registries: []string{"docker.io"},
		Runner:     runner,
	})
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

// --- Import tests ---

func TestImportWithCtr(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "image.tar")
	os.WriteFile(tarPath, []byte("fake tar"), 0o644)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			fmt.Sprintf("ctr -n k8s.io images import %s", tarPath): {output: []byte("imported")},
		},
	}

	err := Import(context.Background(), tarPath, runner)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
}

func TestImportFallbackToDocker(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "image.tar")
	os.WriteFile(tarPath, []byte("fake tar"), 0o644)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			fmt.Sprintf("ctr -n k8s.io images import %s", tarPath): {err: fmt.Errorf("ctr not found")},
			fmt.Sprintf("docker load -i %s", tarPath):              {output: []byte("loaded")},
		},
	}

	err := Import(context.Background(), tarPath, runner)
	if err != nil {
		t.Fatalf("import with docker fallback: %v", err)
	}
}

func TestImportBothFail(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "image.tar")
	os.WriteFile(tarPath, []byte("fake tar"), 0o644)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			fmt.Sprintf("ctr -n k8s.io images import %s", tarPath): {err: fmt.Errorf("ctr not found")},
			fmt.Sprintf("docker load -i %s", tarPath):              {err: fmt.Errorf("docker not found")},
		},
	}

	err := Import(context.Background(), tarPath, runner)
	if err == nil {
		t.Error("expected error when both ctr and docker fail")
	}
}

func TestImportNonExistentFile(t *testing.T) {
	runner := &mockRunner{responses: map[string]mockResponse{}}

	err := Import(context.Background(), "/nonexistent/image.tar", runner)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestImportDockerToContainerdPipe(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"docker save vllm/vllm-openai:latest":   {output: []byte("ok")},
			"k3s ctr -n k8s.io images import -":     {output: []byte("ok")},
		},
	}

	if err := ImportDockerToContainerd(context.Background(), "vllm/vllm-openai:latest", runner); err != nil {
		t.Fatalf("ImportDockerToContainerd: %v", err)
	}
}

func TestImportDockerToContainerdPipeError(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"docker save vllm/vllm-openai:latest": {err: fmt.Errorf("save failed")},
		},
	}

	if err := ImportDockerToContainerd(context.Background(), "vllm/vllm-openai:latest", runner); err == nil {
		t.Fatal("expected error when docker save fails")
	}
}

func TestScanContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: []byte(`{"images":[]}`)},
		},
	}

	_, err := ScanUnified(ctx, ScanOptions{
		AssetPatterns: map[string][]string{"vllm": {"vllm/vllm-openai"}},
		Runner:       runner,
	})
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestScanImageWithRegistry(t *testing.T) {
	// Test that images with full registry prefix are matched
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:abc123",
				RepoTags: []string{"registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai:v0.8"},
				Size:     "8500000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	engineAssets := map[string][]string{
		"vllm": {"vllm/vllm-openai", "registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai"},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: engineAssets,
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 engine, got %d", len(results))
	}
	if results[0].Type != "vllm" {
		t.Errorf("expected type vllm, got %s", results[0].Type)
	}
}

func TestScanCustomFastAPIContainers(t *testing.T) {
	// Verify pattern matching for custom FastAPI TTS/ASR containers on GB10.
	// qwen-tts-fastapi patterns must match "qwen3-tts" (with digit 3) in image names.
	// glm-asr-fastapi patterns must match "glm-asr" and "asr-nano" in image names.
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:tts1",
				RepoTags: []string{"qujing-qwen3-tts-real:latest"},
				Size:     "2500000000",
			},
			{
				ID:       "sha256:tts2",
				RepoTags: []string{"qujing-qwen3-tts:latest"},
				Size:     "2400000000",
			},
			{
				ID:       "sha256:asr1",
				RepoTags: []string{"qujing-glm-asr-nano:latest"},
				Size:     "3000000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{
			"qwen-tts-fastapi": {"^qwen-tts-fastapi$", "qwen3-tts", "qwen-tts", "tts-fastapi"},
			"glm-asr-fastapi":  {"^glm-asr-fastapi$", "glm-asr", "asr-nano"},
		},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 matched engines, got %d", len(results))
	}

	typeCount := map[string]int{}
	for _, r := range results {
		typeCount[r.Type]++
	}
	if typeCount["qwen-tts-fastapi"] != 2 {
		t.Errorf("expected 2 qwen-tts-fastapi matches, got %d", typeCount["qwen-tts-fastapi"])
	}
	if typeCount["glm-asr-fastapi"] != 1 {
		t.Errorf("expected 1 glm-asr-fastapi match, got %d", typeCount["glm-asr-fastapi"])
	}
}

func TestPullImageNameConstruction(t *testing.T) {
	// Verify the image name is constructed properly from registry + image basename
	tests := []struct {
		image    string
		registry string
		tag      string
		wantRef  string
	}{
		{"vllm/vllm-openai", "docker.io", "latest", "docker.io/vllm/vllm-openai:latest"},
		{"vllm/vllm-openai", "registry.cn-hangzhou.aliyuncs.com/aima", "v0.8", "registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai:v0.8"},
	}

	for _, tt := range tests {
		t.Run(tt.wantRef, func(t *testing.T) {
			ref := buildImageRef(tt.registry, tt.image, tt.tag)
			if ref != tt.wantRef {
				t.Errorf("buildImageRef(%q, %q, %q) = %q, want %q", tt.registry, tt.image, tt.tag, ref, tt.wantRef)
			}
		})
	}
}
