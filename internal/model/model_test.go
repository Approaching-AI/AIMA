package model

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultScanPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home dir: %v", err)
	}

	paths := DefaultScanPaths()

	// Should always include ~/.aima/models/
	aimaPath := filepath.Join(home, ".aima", "models")
	found := false
	for _, p := range paths {
		if p == aimaPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %s in default scan paths, got %v", aimaPath, paths)
	}

	// Should include ~/.cache/huggingface/hub/
	hfPath := filepath.Join(home, ".cache", "huggingface", "hub")
	found = false
	for _, p := range paths {
		if p == hfPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %s in default scan paths, got %v", hfPath, paths)
	}

	// Linux-only path
	if runtime.GOOS == "linux" {
		found = false
		for _, p := range paths {
			if p == "/mnt/data/models" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected /mnt/data/models in default scan paths on Linux")
		}
	}
}

func TestDefaultScanPathsWithEnvVar(t *testing.T) {
	t.Setenv("AIMA_MODEL_DIR", "/custom/model/dir")
	paths := DefaultScanPaths()

	if paths[0] != "/custom/model/dir" {
		t.Errorf("expected first path to be env var value, got %s", paths[0])
	}
}

func TestScanHuggingFaceModel(t *testing.T) {
	dir := t.TempDir()

	// Create a mock HuggingFace model directory
	modelDir := filepath.Join(dir, "my-llama-model")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}

	config := map[string]any{
		"model_type":          "llama",
		"hidden_size":         4096,
		"num_hidden_layers":   32,
		"num_attention_heads": 32,
	}
	configBytes, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(modelDir, "config.json"), configBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a fake safetensors file (1KB)
	safetensorsData := make([]byte, 1024)
	if err := os.WriteFile(filepath.Join(modelDir, "model-00001.safetensors"), safetensorsData, 0o644); err != nil {
		t.Fatal(err)
	}

	models, err := Scan(context.Background(), ScanOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}

	m := models[0]
	if m.Name != "my-llama-model" {
		t.Errorf("expected name my-llama-model, got %s", m.Name)
	}
	if m.Format != "safetensors" {
		t.Errorf("expected format safetensors, got %s", m.Format)
	}
	if m.DetectedArch != "llama" {
		t.Errorf("expected arch llama, got %s", m.DetectedArch)
	}
	if m.SizeBytes != 1024 {
		t.Errorf("expected size 1024, got %d", m.SizeBytes)
	}

	expectedID := fmt.Sprintf("%x", sha256.Sum256([]byte(modelDir)))
	if m.ID != expectedID {
		t.Errorf("expected ID %s, got %s", expectedID, m.ID)
	}
}

func TestScanGGUFModel(t *testing.T) {
	dir := t.TempDir()

	modelDir := filepath.Join(dir, "my-gguf-model")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a mock GGUF file with correct magic bytes
	ggufData := make([]byte, 64)
	// GGUF magic: 0x47475546 ("GGUF" in little-endian)
	binary.LittleEndian.PutUint32(ggufData[0:4], 0x46554747)
	// Version 3
	binary.LittleEndian.PutUint32(ggufData[4:8], 3)
	if err := os.WriteFile(filepath.Join(modelDir, "model.gguf"), ggufData, 0o644); err != nil {
		t.Fatal(err)
	}

	models, err := Scan(context.Background(), ScanOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}

	m := models[0]
	if m.Format != "gguf" {
		t.Errorf("expected format gguf, got %s", m.Format)
	}
	if m.SizeBytes != 64 {
		t.Errorf("expected size 64, got %d", m.SizeBytes)
	}
}

func TestScanGGUFInvalidMagic(t *testing.T) {
	dir := t.TempDir()

	modelDir := filepath.Join(dir, "bad-gguf")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a fake GGUF file with wrong magic bytes
	ggufData := make([]byte, 64)
	binary.LittleEndian.PutUint32(ggufData[0:4], 0xDEADBEEF)
	if err := os.WriteFile(filepath.Join(modelDir, "model.gguf"), ggufData, 0o644); err != nil {
		t.Fatal(err)
	}

	models, err := Scan(context.Background(), ScanOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(models) != 0 {
		t.Errorf("expected 0 models for invalid GGUF, got %d", len(models))
	}
}

func TestScanEmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	models, err := Scan(context.Background(), ScanOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected 0 models, got %d", len(models))
	}
}

func TestScanNonExistentDirectory(t *testing.T) {
	models, err := Scan(context.Background(), ScanOptions{Paths: []string{"/nonexistent/path/xyz"}})
	if err != nil {
		t.Fatalf("scan should not error on missing dirs: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected 0 models, got %d", len(models))
	}
}

func TestScanMultipleModels(t *testing.T) {
	dir := t.TempDir()

	// Model 1: HuggingFace
	m1Dir := filepath.Join(dir, "model-hf")
	os.MkdirAll(m1Dir, 0o755)
	config := map[string]any{"model_type": "qwen2", "hidden_size": 3584, "num_hidden_layers": 28, "num_attention_heads": 28}
	configBytes, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(m1Dir, "config.json"), configBytes, 0o644)
	os.WriteFile(filepath.Join(m1Dir, "model.safetensors"), make([]byte, 2048), 0o644)

	// Model 2: GGUF
	m2Dir := filepath.Join(dir, "model-gguf")
	os.MkdirAll(m2Dir, 0o755)
	ggufData := make([]byte, 32)
	binary.LittleEndian.PutUint32(ggufData[0:4], 0x46554747)
	binary.LittleEndian.PutUint32(ggufData[4:8], 3)
	os.WriteFile(filepath.Join(m2Dir, "model.gguf"), ggufData, 0o644)

	models, err := Scan(context.Background(), ScanOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
}

func TestScanArchitectureDetection(t *testing.T) {
	tests := []struct {
		modelType    string
		expectedArch string
	}{
		{"llama", "llama"},
		{"LlamaForCausalLM", "llama"},
		{"chatglm", "glm"},
		{"glm-4", "glm"},
		{"qwen2", "qwen"},
		{"Qwen2ForCausalLM", "qwen"},
		{"whisper", "whisper"},
		{"WhisperForConditionalGeneration", "whisper"},
		{"mistral", "mistral"},
		{"MistralForCausalLM", "mistral"},
		{"baichuan", "baichuan"},
		{"internlm2", "internlm"},
		{"deepseek", "deepseek"},
		{"unknown_type", "unknown_type"},
	}

	for _, tt := range tests {
		t.Run(tt.modelType, func(t *testing.T) {
			dir := t.TempDir()
			modelDir := filepath.Join(dir, "test-model")
			os.MkdirAll(modelDir, 0o755)

			config := map[string]any{
				"model_type":          tt.modelType,
				"hidden_size":         1024,
				"num_hidden_layers":   12,
				"num_attention_heads": 12,
			}
			configBytes, _ := json.Marshal(config)
			os.WriteFile(filepath.Join(modelDir, "config.json"), configBytes, 0o644)
			os.WriteFile(filepath.Join(modelDir, "model.safetensors"), make([]byte, 100), 0o644)

			models, err := Scan(context.Background(), ScanOptions{Paths: []string{dir}})
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if len(models) != 1 {
				t.Fatalf("expected 1 model, got %d", len(models))
			}
			if models[0].DetectedArch != tt.expectedArch {
				t.Errorf("expected arch %s, got %s", tt.expectedArch, models[0].DetectedArch)
			}
		})
	}
}

func TestScanParameterEstimation(t *testing.T) {
	tests := []struct {
		name           string
		hiddenSize     int
		numLayers      int
		expectedParams string
	}{
		{"tiny ~0.1B", 512, 6, "<1B"},
		{"small ~1B", 2048, 16, "1B"},
		{"7B-class", 4096, 32, "7B"},
		{"13B-class", 5120, 40, "13B"},
		{"70B-class", 8192, 80, "70B"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			modelDir := filepath.Join(dir, "test-model")
			os.MkdirAll(modelDir, 0o755)

			config := map[string]any{
				"model_type":          "llama",
				"hidden_size":         tt.hiddenSize,
				"num_hidden_layers":   tt.numLayers,
				"num_attention_heads": 32,
			}
			configBytes, _ := json.Marshal(config)
			os.WriteFile(filepath.Join(modelDir, "config.json"), configBytes, 0o644)
			os.WriteFile(filepath.Join(modelDir, "model.safetensors"), make([]byte, 100), 0o644)

			models, err := Scan(context.Background(), ScanOptions{Paths: []string{dir}})
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if len(models) != 1 {
				t.Fatalf("expected 1 model, got %d", len(models))
			}
			if models[0].DetectedParams != tt.expectedParams {
				t.Errorf("expected params %s, got %s", tt.expectedParams, models[0].DetectedParams)
			}
		})
	}
}

func TestScanContextCancellation(t *testing.T) {
	dir := t.TempDir()

	// Create a model dir so scanning has work to do
	modelDir := filepath.Join(dir, "model")
	os.MkdirAll(modelDir, 0o755)
	config := map[string]any{"model_type": "llama", "hidden_size": 4096, "num_hidden_layers": 32, "num_attention_heads": 32}
	configBytes, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(modelDir, "config.json"), configBytes, 0o644)
	os.WriteFile(filepath.Join(modelDir, "model.safetensors"), make([]byte, 100), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := Scan(ctx, ScanOptions{Paths: []string{dir}})
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

// --- Download tests ---

func TestDownloadFull(t *testing.T) {
	content := []byte("hello world model data 1234567890")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.Write(content)
	}))
	defer server.Close()

	dir := t.TempDir()
	destPath := filepath.Join(dir, "model.bin")

	var lastDownloaded, lastTotal int64
	err := Download(context.Background(), DownloadOptions{
		URL:      server.URL + "/model.bin",
		DestPath: destPath,
		OnProgress: func(downloaded, total int64) {
			lastDownloaded = downloaded
			lastTotal = total
		},
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("content mismatch: got %q", data)
	}

	if lastTotal != int64(len(content)) {
		t.Errorf("expected total %d, got %d", len(content), lastTotal)
	}
	if lastDownloaded != int64(len(content)) {
		t.Errorf("expected downloaded %d, got %d", len(content), lastDownloaded)
	}
}

func TestDownloadResume(t *testing.T) {
	content := []byte("abcdefghijklmnopqrstuvwxyz")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader != "" {
			// Parse "bytes=N-"
			var start int64
			fmt.Sscanf(rangeHeader, "bytes=%d-", &start)
			if start >= int64(len(content)) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
			w.Header().Set("Content-Length", fmt.Sprintf("%d", int64(len(content))-start))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[start:])
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.Write(content)
	}))
	defer server.Close()

	dir := t.TempDir()
	destPath := filepath.Join(dir, "model.bin")

	// Write first 10 bytes as a partial file
	partialPath := destPath + ".partial"
	if err := os.WriteFile(partialPath, content[:10], 0o644); err != nil {
		t.Fatal(err)
	}

	err := Download(context.Background(), DownloadOptions{
		URL:      server.URL + "/model.bin",
		DestPath: destPath,
	})
	if err != nil {
		t.Fatalf("download resume: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("content mismatch: got %q, want %q", data, content)
	}

	// Partial file should be cleaned up
	if _, err := os.Stat(partialPath); !os.IsNotExist(err) {
		t.Error("partial file should be removed after successful download")
	}
}

func TestDownloadContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		// Write slowly - just write a small chunk then hang
		w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		// Block until context is cancelled
		<-r.Context().Done()
	}))
	defer server.Close()

	dir := t.TempDir()
	destPath := filepath.Join(dir, "model.bin")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- Download(ctx, DownloadOptions{
			URL:      server.URL + "/model.bin",
			DestPath: destPath,
		})
	}()

	cancel()
	err := <-done
	if err == nil {
		t.Error("expected error from cancelled download")
	}
}

func TestDownloadServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	dir := t.TempDir()
	err := Download(context.Background(), DownloadOptions{
		URL:      server.URL + "/model.bin",
		DestPath: filepath.Join(dir, "model.bin"),
	})
	if err == nil {
		t.Error("expected error for server error")
	}
}

// --- Import tests ---

func TestImportHuggingFaceModel(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create source model
	modelDir := filepath.Join(srcDir, "my-model")
	os.MkdirAll(modelDir, 0o755)
	config := map[string]any{"model_type": "llama", "hidden_size": 4096, "num_hidden_layers": 32, "num_attention_heads": 32}
	configBytes, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(modelDir, "config.json"), configBytes, 0o644)
	os.WriteFile(filepath.Join(modelDir, "model.safetensors"), make([]byte, 512), 0o644)

	info, err := Import(context.Background(), modelDir, destDir)
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if info.Name != "my-model" {
		t.Errorf("expected name my-model, got %s", info.Name)
	}
	if info.Format != "safetensors" {
		t.Errorf("expected format safetensors, got %s", info.Format)
	}
	if info.DetectedArch != "llama" {
		t.Errorf("expected arch llama, got %s", info.DetectedArch)
	}

	// Verify files were copied to destDir
	destModelDir := filepath.Join(destDir, "my-model")
	if _, err := os.Stat(filepath.Join(destModelDir, "config.json")); os.IsNotExist(err) {
		t.Error("config.json not copied to dest dir")
	}
}

func TestImportInvalidPath(t *testing.T) {
	_, err := Import(context.Background(), "/nonexistent/path", t.TempDir())
	if err == nil {
		t.Error("expected error for nonexistent source path")
	}
}

func TestImportNotAModel(t *testing.T) {
	srcDir := t.TempDir()
	// Just an empty directory - no model files
	_, err := Import(context.Background(), srcDir, t.TempDir())
	if err == nil {
		t.Error("expected error for non-model directory")
	}
}

func TestImportAlreadyInScanDir(t *testing.T) {
	dir := t.TempDir()

	// Create model inside the dest dir itself
	modelDir := filepath.Join(dir, "my-model")
	os.MkdirAll(modelDir, 0o755)
	config := map[string]any{"model_type": "llama", "hidden_size": 4096, "num_hidden_layers": 32, "num_attention_heads": 32}
	configBytes, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(modelDir, "config.json"), configBytes, 0o644)
	os.WriteFile(filepath.Join(modelDir, "model.safetensors"), make([]byte, 256), 0o644)

	// Import where srcPath is already under destDir
	info, err := Import(context.Background(), modelDir, dir)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if info.Format != "safetensors" {
		t.Errorf("expected format safetensors, got %s", info.Format)
	}
}

func TestScanModelTypeDetection(t *testing.T) {
	dir := t.TempDir()

	modelDir := filepath.Join(dir, "whisper-model")
	os.MkdirAll(modelDir, 0o755)

	config := map[string]any{"model_type": "whisper", "hidden_size": 1280, "num_hidden_layers": 32, "num_attention_heads": 20}
	configBytes, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(modelDir, "config.json"), configBytes, 0o644)
	os.WriteFile(filepath.Join(modelDir, "model.safetensors"), make([]byte, 100), 0o644)

	// Also add tokenizer_config.json for type detection
	tokConfig := map[string]any{}
	tokBytes, _ := json.Marshal(tokConfig)
	os.WriteFile(filepath.Join(modelDir, "tokenizer_config.json"), tokBytes, 0o644)

	models, err := Scan(context.Background(), ScanOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].Type != "asr" {
		t.Errorf("expected type asr for whisper, got %s", models[0].Type)
	}
}

// estimateParamCount is tested indirectly via TestScanParameterEstimation,
// but we also verify the formula here to document it.
func TestEstimateParamCountFormula(t *testing.T) {
	// The standard transformer formula: params ~= 12 * L * d^2
	// where L = num_hidden_layers, d = hidden_size
	// We round to standard sizes: <1B, 1B, 3B, 7B, 8B, 13B, 14B, 32B, 34B, 70B, 72B, 110B, etc.

	tests := []struct {
		h, l int
		want string
	}{
		{4096, 32, "7B"},    // 12 * 32 * 4096^2 = ~6.4B
		{3584, 28, "3B"},    // 12 * 28 * 3584^2 = ~4.3B -> closest is 3B? Let's see
		{8192, 80, "70B"},   // 12 * 80 * 8192^2 = ~64B
		{2048, 16, "1B"},    // 12 * 16 * 2048^2 = ~0.8B
		{512, 6, "<1B"},     // 12 * 6 * 512^2 = ~0.019B
		{5120, 40, "13B"},   // 12 * 40 * 5120^2 = ~12.6B
		{6144, 48, "22B"},   // 12 * 48 * 6144^2 = ~21.7B
		{14336, 80, "200B"}, // huge
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("h%d_l%d", tt.h, tt.l), func(t *testing.T) {
			got := estimateParams(tt.h, tt.l)
			if got != tt.want {
				rawParams := 12.0 * float64(tt.l) * float64(tt.h) * float64(tt.h) / 1e9
				t.Errorf("h=%d l=%d: raw=%.1fB, want %s, got %s", tt.h, tt.l, rawParams, tt.want, got)
			}
		})
	}
}

// --- Type detection from model_type field ---
func TestDetectModelType(t *testing.T) {
	tests := []struct {
		arch     string
		wantType string
	}{
		{"whisper", "asr"},
		{"bark", "tts"},
		{"speecht5", "tts"},
		{"stable_diffusion", "diffusion"},
		{"llama", "llm"},
		{"qwen", "llm"},
		{"glm", "llm"},
		{"llava", "vlm"},
		{"internvl", "vlm"},
	}

	for _, tt := range tests {
		t.Run(tt.arch, func(t *testing.T) {
			got := detectModelType(tt.arch)
			if got != tt.wantType {
				t.Errorf("detectModelType(%q) = %q, want %q", tt.arch, got, tt.wantType)
			}
		})
	}
}

// Helpers so tests compile even if we haven't exported everything.
// The actual estimateParams and detectModelType are package-level funcs.
// This test just validates expectations.

func TestDownloadNoResumeFallback(t *testing.T) {
	// Server that doesn't support range requests - returns 200 with full content
	content := []byte("full content here")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ignore Range header, always return full content
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer server.Close()

	dir := t.TempDir()
	destPath := filepath.Join(dir, "model.bin")

	// Write partial file
	partialPath := destPath + ".partial"
	os.WriteFile(partialPath, []byte("partial"), 0o644)

	err := Download(context.Background(), DownloadOptions{
		URL:      server.URL + "/model.bin",
		DestPath: destPath,
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("content mismatch: got %q", data)
	}
}

func TestScanGGUFTopLevel(t *testing.T) {
	// Test that a GGUF file directly in a scan directory is found
	dir := t.TempDir()

	ggufData := make([]byte, 32)
	binary.LittleEndian.PutUint32(ggufData[0:4], 0x46554747)
	binary.LittleEndian.PutUint32(ggufData[4:8], 3)
	os.WriteFile(filepath.Join(dir, "model.gguf"), ggufData, 0o644)

	models, err := Scan(context.Background(), ScanOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// A GGUF file directly in scan root: it should be detected.
	// The "model" here is the parent dir (the scan dir itself)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].Format != "gguf" {
		t.Errorf("expected format gguf, got %s", models[0].Format)
	}
}

// Verify detectArch handles various model_type values
func TestDetectArch(t *testing.T) {
	tests := []struct {
		modelType string
		want      string
	}{
		{"LlamaForCausalLM", "llama"},
		{"chatglm", "glm"},
		{"glm-4", "glm"},
		{"ChatGLMModel", "glm"},
		{"qwen2", "qwen"},
		{"Qwen2ForCausalLM", "qwen"},
		{"WhisperForConditionalGeneration", "whisper"},
		{"MistralForCausalLM", "mistral"},
		{"BaichuanForCausalLM", "baichuan"},
		{"InternLM2ForCausalLM", "internlm"},
		{"DeepseekV2ForCausalLM", "deepseek"},
		{"totally_novel_arch", "totally_novel_arch"},
	}
	for _, tt := range tests {
		t.Run(tt.modelType, func(t *testing.T) {
			got := detectArch(tt.modelType)
			if got != tt.want {
				t.Errorf("detectArch(%q) = %q, want %q", tt.modelType, got, tt.want)
			}
		})
	}
}

// Suppress unused import warnings by using the imports
var _ = strings.Contains
var _ = math.Round
