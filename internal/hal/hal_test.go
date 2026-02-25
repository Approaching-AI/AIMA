package hal

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
)

// mockRunner implements CommandRunner for testing.
type mockRunner struct {
	outputs map[string]mockResult
}

type mockResult struct {
	output []byte
	err    error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	if r, ok := m.outputs[key]; ok {
		return r.output, r.err
	}
	return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func newMockRunner(outputs map[string]mockResult) *mockRunner {
	return &mockRunner{outputs: outputs}
}

func TestParseGPUInfo(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantNil    bool
		wantName   string
		wantVRAM   int
		wantArch   string
		wantCC     string
		wantDriver string
		wantCount  int
	}{
		{
			name:       "RTX 4090 single GPU",
			output:     "NVIDIA GeForce RTX 4090, 24564, 560.94, 8.9, 120.50, 450.00, 42.0\n",
			wantNil:    false,
			wantName:   "NVIDIA GeForce RTX 4090",
			wantVRAM:   24564,
			wantArch:   "Ada",
			wantCC:     "8.9",
			wantDriver: "560.94",
			wantCount:  1,
		},
		{
			name:       "RTX 3090 Ampere",
			output:     "NVIDIA GeForce RTX 3090, 24576, 535.129.03, 8.6, 350.00, 350.00, 65.0\n",
			wantNil:    false,
			wantName:   "NVIDIA GeForce RTX 3090",
			wantVRAM:   24576,
			wantArch:   "Ampere",
			wantCC:     "8.6",
			wantDriver: "535.129.03",
			wantCount:  1,
		},
		{
			name:       "A100 Ampere 80GB",
			output:     "NVIDIA A100-SXM4-80GB, 81920, 525.85.12, 8.0, 275.00, 400.00, 35.0\n",
			wantNil:    false,
			wantName:   "NVIDIA A100-SXM4-80GB",
			wantVRAM:   81920,
			wantArch:   "Ampere",
			wantCC:     "8.0",
			wantDriver: "525.85.12",
			wantCount:  1,
		},
		{
			name:       "GTX 1080 Pascal",
			output:     "NVIDIA GeForce GTX 1080, 8192, 470.57.02, 6.1, 150.00, 180.00, 50.0\n",
			wantNil:    false,
			wantName:   "NVIDIA GeForce GTX 1080",
			wantVRAM:   8192,
			wantArch:   "Pascal",
			wantCC:     "6.1",
			wantDriver: "470.57.02",
			wantCount:  1,
		},
		{
			name:       "RTX 2080 Turing",
			output:     "NVIDIA GeForce RTX 2080, 8192, 535.54.03, 7.5, 180.00, 215.00, 55.0\n",
			wantNil:    false,
			wantName:   "NVIDIA GeForce RTX 2080",
			wantVRAM:   8192,
			wantArch:   "Turing",
			wantCC:     "7.5",
			wantDriver: "535.54.03",
			wantCount:  1,
		},
		{
			name:       "V100 Volta",
			output:     "Tesla V100-SXM2-16GB, 16384, 450.80.02, 7.0, 200.00, 300.00, 40.0\n",
			wantNil:    false,
			wantName:   "Tesla V100-SXM2-16GB",
			wantVRAM:   16384,
			wantArch:   "Volta",
			wantCC:     "7.0",
			wantDriver: "450.80.02",
			wantCount:  1,
		},
		{
			name:       "Blackwell B200",
			output:     "NVIDIA B200, 196608, 570.00, 10.0, 600.00, 1000.00, 38.0\n",
			wantNil:    false,
			wantName:   "NVIDIA B200",
			wantVRAM:   196608,
			wantArch:   "Blackwell",
			wantCC:     "10.0",
			wantDriver: "570.00",
			wantCount:  1,
		},
		{
			name:    "empty output",
			output:  "",
			wantNil: true,
		},
		{
			name:    "whitespace only",
			output:  "  \n  \n",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gpu := parseGPUInfo(tt.output)
			if tt.wantNil {
				if gpu != nil {
					t.Fatalf("expected nil GPU, got %+v", gpu)
				}
				return
			}
			if gpu == nil {
				t.Fatal("expected non-nil GPU, got nil")
			}
			if gpu.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", gpu.Name, tt.wantName)
			}
			if gpu.VRAMMiB != tt.wantVRAM {
				t.Errorf("VRAMMiB = %d, want %d", gpu.VRAMMiB, tt.wantVRAM)
			}
			if gpu.Arch != tt.wantArch {
				t.Errorf("Arch = %q, want %q", gpu.Arch, tt.wantArch)
			}
			if gpu.ComputeCapability != tt.wantCC {
				t.Errorf("ComputeCapability = %q, want %q", gpu.ComputeCapability, tt.wantCC)
			}
			if gpu.DriverVersion != tt.wantDriver {
				t.Errorf("DriverVersion = %q, want %q", gpu.DriverVersion, tt.wantDriver)
			}
			if gpu.Count != tt.wantCount {
				t.Errorf("Count = %d, want %d", gpu.Count, tt.wantCount)
			}
		})
	}
}

func TestParseGPUInfoMultiGPU(t *testing.T) {
	output := "NVIDIA GeForce RTX 4090, 24564, 560.94, 8.9, 120.50, 450.00, 42.0\n" +
		"NVIDIA GeForce RTX 4090, 24564, 560.94, 8.9, 115.00, 450.00, 40.0\n"

	gpu := parseGPUInfo(output)
	if gpu == nil {
		t.Fatal("expected non-nil GPU, got nil")
	}
	if gpu.Count != 2 {
		t.Errorf("Count = %d, want 2", gpu.Count)
	}
	if gpu.Name != "NVIDIA GeForce RTX 4090" {
		t.Errorf("Name = %q, want %q", gpu.Name, "NVIDIA GeForce RTX 4090")
	}
}

func TestParseGPUInfoMalformedLine(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"too few fields", "NVIDIA GeForce RTX 4090, 24564\n"},
		{"non-numeric VRAM", "NVIDIA GeForce RTX 4090, notanumber, 560.94, 8.9, 120.50, 450.00, 42.0\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gpu := parseGPUInfo(tt.output)
			if gpu != nil {
				t.Fatalf("expected nil GPU for malformed input, got %+v", gpu)
			}
		})
	}
}

func TestComputeCapToArch(t *testing.T) {
	tests := []struct {
		cc   string
		arch string
	}{
		{"10.0", "Blackwell"},
		{"10.2", "Blackwell"},
		{"9.0", "Hopper"},
		{"9.1", "Hopper"},
		{"8.9", "Ada"},
		{"8.0", "Ampere"},
		{"8.6", "Ampere"},
		{"8.7", "Ampere"},
		{"7.5", "Turing"},
		{"7.0", "Volta"},
		{"6.1", "Pascal"},
		{"6.0", "Pascal"},
		{"5.0", "unknown"},
		{"", "unknown"},
		{"invalid", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.cc, func(t *testing.T) {
			got := computeCapToArch(tt.cc)
			if got != tt.arch {
				t.Errorf("computeCapToArch(%q) = %q, want %q", tt.cc, got, tt.arch)
			}
		})
	}
}

func TestDetectGPU_NvidiaSmiNotFound(t *testing.T) {
	runner := newMockRunner(map[string]mockResult{})

	ctx := context.Background()
	gpu := detectGPU(ctx, runner)
	if gpu != nil {
		t.Fatalf("expected nil GPU when nvidia-smi not found, got %+v", gpu)
	}
}

func TestDetectGPU_NvidiaSmiError(t *testing.T) {
	runner := newMockRunner(map[string]mockResult{
		"nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap,power.draw,power.limit,temperature.gpu --format=csv,noheader,nounits": {
			output: []byte(""),
			err:    fmt.Errorf("nvidia-smi failed"),
		},
	})

	ctx := context.Background()
	gpu := detectGPU(ctx, runner)
	if gpu != nil {
		t.Fatalf("expected nil GPU when nvidia-smi errors, got %+v", gpu)
	}
}

func TestDetectGPU_ValidOutput(t *testing.T) {
	runner := newMockRunner(map[string]mockResult{
		"nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap,power.draw,power.limit,temperature.gpu --format=csv,noheader,nounits": {
			output: []byte("NVIDIA GeForce RTX 4090, 24564, 560.94, 8.9, 120.50, 450.00, 42.0\n"),
			err:    nil,
		},
	})

	ctx := context.Background()
	gpu := detectGPU(ctx, runner)
	if gpu == nil {
		t.Fatal("expected non-nil GPU")
	}
	if gpu.Name != "NVIDIA GeForce RTX 4090" {
		t.Errorf("Name = %q, want %q", gpu.Name, "NVIDIA GeForce RTX 4090")
	}
	if gpu.VRAMMiB != 24564 {
		t.Errorf("VRAMMiB = %d, want 24564", gpu.VRAMMiB)
	}
}

func TestDetectGPU_CUDAVersionQuery(t *testing.T) {
	runner := newMockRunner(map[string]mockResult{
		"nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap,power.draw,power.limit,temperature.gpu --format=csv,noheader,nounits": {
			output: []byte("NVIDIA GeForce RTX 4090, 24564, 560.94, 8.9, 120.50, 450.00, 42.0\n"),
			err:    nil,
		},
		"nvidia-smi --query-gpu=driver_version --format=csv,noheader": {
			output: []byte("560.94\n"),
			err:    nil,
		},
	})

	ctx := context.Background()
	gpu := detectGPU(ctx, runner)
	if gpu == nil {
		t.Fatal("expected non-nil GPU")
	}
	if gpu.DriverVersion != "560.94" {
		t.Errorf("DriverVersion = %q, want %q", gpu.DriverVersion, "560.94")
	}
}

func TestDetectOS(t *testing.T) {
	info := detectOS()
	if info.OS == "" {
		t.Error("OS should not be empty")
	}
	if info.Arch == "" {
		t.Error("Arch should not be empty")
	}
}

func TestDetectWithMockRunner(t *testing.T) {
	runner := newMockRunner(platformMockOutputs())

	ctx := context.Background()
	hw, err := detectWithRunner(ctx, runner)
	if err != nil {
		t.Fatalf("Detect returned error: %v", err)
	}
	if hw == nil {
		t.Fatal("Detect returned nil HardwareInfo")
	}
	if hw.GPU != nil {
		t.Log("GPU detected (mock should have returned nil)")
	}
	if hw.OS.OS == "" {
		t.Error("OS should not be empty")
	}
	if hw.OS.Arch == "" {
		t.Error("Arch should not be empty")
	}
	if hw.CPU.Cores <= 0 {
		t.Error("CPU cores should be > 0")
	}
	if hw.RAM.TotalMiB <= 0 {
		t.Error("RAM total should be > 0")
	}
}

func TestDetectWithMockRunner_WithGPU(t *testing.T) {
	mocks := platformMockOutputs()
	mocks["nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap,power.draw,power.limit,temperature.gpu --format=csv,noheader,nounits"] = mockResult{
		output: []byte("NVIDIA GeForce RTX 3090, 24576, 535.129.03, 8.6, 300.00, 350.00, 55.0\n"),
		err:    nil,
	}
	runner := newMockRunner(mocks)

	ctx := context.Background()
	hw, err := detectWithRunner(ctx, runner)
	if err != nil {
		t.Fatalf("Detect returned error: %v", err)
	}
	if hw.GPU == nil {
		t.Fatal("expected GPU info")
	}
	if hw.GPU.Arch != "Ampere" {
		t.Errorf("GPU Arch = %q, want %q", hw.GPU.Arch, "Ampere")
	}
}

func TestParseGPUMetrics(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantNil bool
		wantUtil int
		wantMem  int
		wantTemp float64
	}{
		{
			name:     "valid metrics",
			output:   "85, 18432, 24564, 72.0, 280.50\n",
			wantNil:  false,
			wantUtil: 85,
			wantMem:  18432,
			wantTemp: 72.0,
		},
		{
			name:     "idle GPU",
			output:   "0, 512, 24564, 35.0, 25.00\n",
			wantNil:  false,
			wantUtil: 0,
			wantMem:  512,
			wantTemp: 35.0,
		},
		{
			name:    "empty output",
			output:  "",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := parseGPUMetrics(tt.output)
			if tt.wantNil {
				if m != nil {
					t.Fatalf("expected nil, got %+v", m)
				}
				return
			}
			if m == nil {
				t.Fatal("expected non-nil GPUMetrics")
			}
			if m.UtilizationPercent != tt.wantUtil {
				t.Errorf("UtilizationPercent = %d, want %d", m.UtilizationPercent, tt.wantUtil)
			}
			if m.MemoryUsedMiB != tt.wantMem {
				t.Errorf("MemoryUsedMiB = %d, want %d", m.MemoryUsedMiB, tt.wantMem)
			}
			if m.TemperatureCelsius != tt.wantTemp {
				t.Errorf("TemperatureCelsius = %f, want %f", m.TemperatureCelsius, tt.wantTemp)
			}
		})
	}
}

func TestCollectMetricsWithMockRunner(t *testing.T) {
	mocks := platformMockOutputs()
	mocks["nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw --format=csv,noheader,nounits"] = mockResult{
		output: []byte("75, 20000, 24564, 68.0, 250.00\n"),
		err:    nil,
	}
	runner := newMockRunner(mocks)

	ctx := context.Background()
	m, err := collectMetricsWithRunner(ctx, runner)
	if err != nil {
		t.Fatalf("CollectMetrics returned error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil Metrics")
	}
	if m.GPU == nil {
		t.Fatal("expected non-nil GPU metrics")
	}
	if m.GPU.UtilizationPercent != 75 {
		t.Errorf("GPU utilization = %d, want 75", m.GPU.UtilizationPercent)
	}
	if m.RAM.TotalMiB <= 0 {
		t.Error("RAM total should be > 0")
	}
}

func TestCollectMetrics_NoGPU(t *testing.T) {
	runner := newMockRunner(platformMockOutputs())

	ctx := context.Background()
	m, err := collectMetricsWithRunner(ctx, runner)
	if err != nil {
		t.Fatalf("CollectMetrics returned error: %v", err)
	}
	if m.GPU != nil {
		t.Error("expected nil GPU metrics when nvidia-smi not found")
	}
	if m.RAM.TotalMiB <= 0 {
		t.Error("RAM total should be > 0")
	}
}
