package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/knowledge"
)

// Qwen2.5-VL-3B text backbone: 36 layers, 2 KV heads, head_dim 128 → 36864 B/token.
const qwen25vl3bKVPerTok = int64(2 * 36 * 2 * 128 * 2)

func TestMinimumLlamaMemoryFit(t *testing.T) {
	tests := []struct {
		name      string
		kvPerTok  int64
		usableMiB int
		nonKVMiB  int
		wantFit   bool
		wantCheck bool
	}{
		{
			name:      "rejects model whose weights alone exceed the safe budget",
			kvPerTok:  81920,
			usableMiB: 12288,
			nonKVMiB:  21109,
			wantFit:   false,
			wantCheck: true,
		},
		{
			name:      "allows model when weights compute reserve and minimum KV fit",
			kvPerTok:  qwen25vl3bKVPerTok,
			usableMiB: 6144,
			nonKVMiB:  4300,
			wantFit:   true,
			wantCheck: true,
		},
		{
			name:      "allows an exact safe-budget fit",
			kvPerTok:  512,
			usableMiB: 10000,
			nonKVMiB:  7975,
			wantFit:   true,
			wantCheck: true,
		},
		{
			name:      "skips preflight when memory is unknown",
			kvPerTok:  81920,
			usableMiB: 0,
			nonKVMiB:  21109,
			wantFit:   true,
			wantCheck: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := minimumLlamaMemoryFit(tt.kvPerTok, tt.usableMiB, tt.nonKVMiB)
			if got.Evaluated != tt.wantCheck {
				t.Fatalf("Evaluated = %v, want %v", got.Evaluated, tt.wantCheck)
			}
			if got.Fits != tt.wantFit {
				t.Fatalf("Fits = %v, want %v (required=%d MiB budget=%d MiB)", got.Fits, tt.wantFit, got.RequiredMiB, got.BudgetMiB)
			}
			if got.Evaluated && got.UsableMiB != tt.usableMiB {
				t.Fatalf("UsableMiB = %d, want %d", got.UsableMiB, tt.usableMiB)
			}
			if got.Evaluated && !got.Fits && got.RequiredMiB <= got.BudgetMiB {
				t.Fatalf("failed fit must require more than budget: required=%d MiB budget=%d MiB", got.RequiredMiB, got.BudgetMiB)
			}
		})
	}
}

func TestRequiresNativeLlamaMemoryPreflight(t *testing.T) {
	tests := []struct {
		name    string
		runtime string
		format  string
		engine  string
		want    bool
	}{
		{name: "native llama.cpp GGUF", runtime: "native", format: "gguf", engine: "llamacpp", want: true},
		{name: "native llama.cpp engine asset", runtime: "native", format: "gguf", engine: "llamacpp-hip-windows", want: true},
		{name: "docker llama.cpp GGUF", runtime: "docker", format: "gguf", engine: "llamacpp", want: false},
		{name: "k3s llama.cpp GGUF", runtime: "k3s", format: "gguf", engine: "llamacpp", want: false},
		{name: "native non GGUF", runtime: "native", format: "safetensors", engine: "llamacpp", want: false},
		{name: "native non llama engine", runtime: "native", format: "gguf", engine: "vllm", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiresNativeLlamaMemoryPreflight(tt.runtime, tt.format, tt.engine); got != tt.want {
				t.Fatalf("requiresNativeLlamaMemoryPreflight(%q, %q, %q) = %v, want %v", tt.runtime, tt.format, tt.engine, got, tt.want)
			}
		})
	}
}

func TestLlamaModelWeightMiBSumsSplitGGUFShards(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "demo-00001-of-00002.gguf")
	writeSizedFile(t, first, 2)
	writeSizedFile(t, filepath.Join(dir, "demo-00002-of-00002.gguf"), 3)
	writeSizedFile(t, filepath.Join(dir, "other-00001-of-00002.gguf"), 5)

	if got := llamaModelWeightMiB(first); got != 5 {
		t.Fatalf("llamaModelWeightMiB() = %d MiB, want 5 MiB", got)
	}
}

func TestLlamaNonKVMiBIncludesProjector(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "demo.gguf")
	projectorPath := filepath.Join(dir, "mmproj-demo.gguf")
	writeSizedFile(t, modelPath, 2)
	writeSizedFile(t, projectorPath, 1)

	if got := llamaNonKVMiB(modelPath, map[string]any{"mmproj": projectorPath}); got != 3 {
		t.Fatalf("llamaNonKVMiB() = %d MiB, want 3 MiB", got)
	}
}

func TestEnsureLlamaMinimumMemoryFitReportsActionableDetails(t *testing.T) {
	modelPath := filepath.Join(t.TempDir(), "oversized.gguf")
	writeTestGGUF(t, modelPath, 21109)

	err := ensureLlamaMinimumMemoryFit(modelPath, nil, knowledge.HardwareInfo{RAMTotalMiB: 16384})
	if err == nil {
		t.Fatal("ensureLlamaMinimumMemoryFit() returned nil, want an out-of-memory detail")
	}
	for _, want := range []string{
		"weights+projector 21109 MiB",
		"safe memory budget is 11059 MiB from 12288 MiB detected",
		"more-quantized model",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestEnsureLlamaMinimumMemoryFitSkipsUnknownArchitecture(t *testing.T) {
	modelPath := filepath.Join(t.TempDir(), "unparseable.gguf")
	writeSizedFile(t, modelPath, 21109)

	if err := ensureLlamaMinimumMemoryFit(modelPath, nil, knowledge.HardwareInfo{RAMTotalMiB: 16384}); err != nil {
		t.Fatalf("ensureLlamaMinimumMemoryFit() = %v, want nil when GGUF architecture is unknown", err)
	}
}

func writeSizedFile(t *testing.T, path string, sizeMiB int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := f.Truncate(int64(sizeMiB) * bytesPerMiB); err != nil {
		t.Fatalf("truncate %s: %v", path, err)
	}
}

func writeTestGGUF(t *testing.T, path string, sizeMiB int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	write := func(value any) {
		t.Helper()
		if err := binary.Write(f, binary.LittleEndian, value); err != nil {
			t.Fatalf("write GGUF: %v", err)
		}
	}
	writeString := func(value string) {
		write(uint64(len(value)))
		if _, err := f.WriteString(value); err != nil {
			t.Fatalf("write GGUF string: %v", err)
		}
	}
	writeUint32 := func(key string, value uint32) {
		writeString(key)
		write(uint32(4)) // GGUF UINT32
		write(value)
	}

	write(uint32(0x46554747)) // GGUF
	write(uint32(3))
	write(uint64(0)) // tensors
	write(uint64(4)) // metadata entries
	writeString("general.architecture")
	write(uint32(8)) // GGUF STRING
	writeString("llama")
	writeUint32("llama.block_count", 20)
	writeUint32("llama.attention.head_count_kv", 8)
	writeUint32("llama.attention.key_length", 128)
	if err := f.Truncate(int64(sizeMiB) * bytesPerMiB); err != nil {
		t.Fatalf("truncate %s: %v", path, err)
	}
}

func TestClampContextForMemory(t *testing.T) {
	tests := []struct {
		name      string
		reqCtx    int
		nCtxTrain int
		kvPerTok  int64
		usableMiB int
		nonKVMiB  int
		want      int
		clamped   bool // want < reqCtx
	}{
		{
			name:      "huge memory, capped only by trained context",
			reqCtx:    128000, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 112640, nonKVMiB: 4300, // ~110GB Strix Halo
			want: 128000, clamped: false,
		},
		{
			name:      "request above trained context is capped down",
			reqCtx:    200000, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 112640, nonKVMiB: 4300,
			want: 128000, clamped: true,
		},
		{
			name:      "16GB unified clamps below 128k",
			reqCtx:    128000, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 12288, nonKVMiB: 4300, // 16GB - 4GB reserve
			// budget = 12288*0.9 - 4300 - 1024 = 5734 MiB → /36864 B = ~163k → but capped at train 128000? 163k>128000 so 128000
			want: 128000, clamped: false,
		},
		{
			name:      "8GB discrete GPU clamps below 128k",
			reqCtx:    128000, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 8192, nonKVMiB: 4300,
			// budget = int(8192*0.9) - 4300 - 1024 = 7372 - 5324 = 2048 MiB
			// maxCtx = 2048*1024*1024/36864 = 58254 → round down to 58112
			want: 58112, clamped: true,
		},
		{
			name:      "tiny memory floors at minCtx",
			reqCtx:    128000, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 6144, nonKVMiB: 4300,
			// budget = 6144*0.9 - 4300 - 1024 = ~205 MiB → ~5832 → round 5632
			want: 5632, clamped: true,
		},
		{
			name:      "unknown memory: only trained-context cap applies",
			reqCtx:    65536, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 0, nonKVMiB: 0,
			want: 65536, clamped: false,
		},
		{
			name:      "unknown arch (kvPerTok 0): no memory clamp",
			reqCtx:    65536, nCtxTrain: 0, kvPerTok: 0,
			usableMiB: 8192, nonKVMiB: 4300,
			want: 65536, clamped: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := clampContextForMemory(tt.reqCtx, tt.nCtxTrain, tt.kvPerTok, tt.usableMiB, tt.nonKVMiB)
			if got != tt.want {
				t.Errorf("clampContextForMemory = %d, want %d", got, tt.want)
			}
			if (got < tt.reqCtx) != tt.clamped {
				t.Errorf("clamped = %v, want %v (got=%d req=%d)", got < tt.reqCtx, tt.clamped, got, tt.reqCtx)
			}
		})
	}
}

func TestUsableMemoryMiB(t *testing.T) {
	tests := []struct {
		name string
		hw   knowledge.HardwareInfo
		want int
	}{
		{"unified 128GB reserves 16GB cap", knowledge.HardwareInfo{UnifiedMemory: true, RAMTotalMiB: 131072}, 131072 - 16384},
		{"unified 16GB reserves 1/4", knowledge.HardwareInfo{UnifiedMemory: true, RAMTotalMiB: 16384}, 16384 - 4096},
		{"unified 8GB reserve floored at 2GB", knowledge.HardwareInfo{UnifiedMemory: true, RAMTotalMiB: 8192}, 8192 - 2048},
		{"unified APU prefers iGPU pool over under-detected OS RAM", knowledge.HardwareInfo{UnifiedMemory: true, RAMTotalMiB: 32768, GPUVRAMMiB: 110456}, 110456},
		{"discrete prefers free VRAM", knowledge.HardwareInfo{GPUVRAMMiB: 8192, GPUMemFreeMiB: 7000}, 7000},
		{"discrete falls back to total VRAM", knowledge.HardwareInfo{GPUVRAMMiB: 8192}, 8192},
		{"cpu-only uses system RAM", knowledge.HardwareInfo{RAMTotalMiB: 32768}, 32768 - 8192},
		{"unknown memory returns 0", knowledge.HardwareInfo{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usableMemoryMiB(tt.hw); got != tt.want {
				t.Errorf("usableMemoryMiB = %d, want %d", got, tt.want)
			}
		})
	}
}
