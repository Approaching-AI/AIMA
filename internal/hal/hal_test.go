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

func TestIsNA(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"[N/A]", true},
		{"N/A", true},
		{"[Not Supported]", true},
		{"Not Supported", true},
		{"", true},
		{" [N/A] ", true},
		{"24564", false},
		{"NVIDIA GeForce RTX 4090", false},
		{"8.9", false},
		{"notanumber", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.input), func(t *testing.T) {
			got := isNA(tt.input)
			if got != tt.want {
				t.Errorf("isNA(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseNvidiaGPU(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantNil     bool
		wantName    string
		wantVRAM    int
		wantArch    string
		wantCC      string
		wantDriver  string
		wantUnified bool
		wantCount   int
	}{
		{
			name:       "RTX 4090 single GPU",
			output:     "NVIDIA GeForce RTX 4090, 24564, 560.94, 8.9, 120.50, 450.00, 42.0\n",
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
			wantName:   "NVIDIA B200",
			wantVRAM:   196608,
			wantArch:   "Blackwell",
			wantCC:     "10.0",
			wantDriver: "570.00",
			wantCount:  1,
		},
		{
			name:        "GB10 with N/A fields",
			output:      "NVIDIA GB10, [N/A], 560.35.05, 10.0, [N/A], [N/A], 45.0\n",
			wantName:    "NVIDIA GB10",
			wantVRAM:    0,
			wantArch:    "Blackwell",
			wantCC:      "10.0",
			wantDriver:  "560.35.05",
			wantUnified: true,
			wantCount:   1,
		},
		{
			name:        "all N/A except name",
			output:      "NVIDIA Orin, [N/A], [N/A], [N/A], [N/A], [N/A], [N/A]\n",
			wantName:    "NVIDIA Orin",
			wantVRAM:    0,
			wantArch:    "unknown",
			wantCC:      "",
			wantDriver:  "",
			wantUnified: true,
			wantCount:   1,
		},
		{
			name:        "Not Supported variants",
			output:      "NVIDIA Jetson, [Not Supported], 535.00, [Not Supported], [Not Supported], [Not Supported], 50.0\n",
			wantName:    "NVIDIA Jetson",
			wantVRAM:    0,
			wantArch:    "unknown",
			wantCC:      "",
			wantDriver:  "535.00",
			wantUnified: true,
			wantCount:   1,
		},
		{
			name:    "name is N/A",
			output:  "[N/A], 24564, 560.94, 8.9, 120.50, 450.00, 42.0\n",
			wantNil: true,
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
			gpu := parseNvidiaGPU(tt.output)
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
			if gpu.ComputeID != tt.wantCC {
				t.Errorf("ComputeID = %q, want %q", gpu.ComputeID, tt.wantCC)
			}
			if gpu.DriverVersion != tt.wantDriver {
				t.Errorf("DriverVersion = %q, want %q", gpu.DriverVersion, tt.wantDriver)
			}
			if gpu.UnifiedMemory != tt.wantUnified {
				t.Errorf("UnifiedMemory = %v, want %v", gpu.UnifiedMemory, tt.wantUnified)
			}
			if gpu.Count != tt.wantCount {
				t.Errorf("Count = %d, want %d", gpu.Count, tt.wantCount)
			}
		})
	}
}

func TestParseNvidiaGPUMultiGPU(t *testing.T) {
	output := "NVIDIA GeForce RTX 4090, 24564, 560.94, 8.9, 120.50, 450.00, 42.0\n" +
		"NVIDIA GeForce RTX 4090, 24564, 560.94, 8.9, 115.00, 450.00, 40.0\n"

	gpu := parseNvidiaGPU(output)
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

func TestParseNvidiaGPUMalformedLine(t *testing.T) {
	t.Run("too few fields", func(t *testing.T) {
		gpu := parseNvidiaGPU("NVIDIA GeForce RTX 4090, 24564\n")
		if gpu != nil {
			t.Fatalf("expected nil GPU for too few fields, got %+v", gpu)
		}
	})

	t.Run("non-numeric VRAM tolerates as zero", func(t *testing.T) {
		gpu := parseNvidiaGPU("NVIDIA GeForce RTX 4090, notanumber, 560.94, 8.9, 120.50, 450.00, 42.0\n")
		if gpu == nil {
			t.Fatal("expected non-nil GPU with VRAM=0")
		}
		if gpu.VRAMMiB != 0 {
			t.Errorf("VRAMMiB = %d, want 0", gpu.VRAMMiB)
		}
		if gpu.Name != "NVIDIA GeForce RTX 4090" {
			t.Errorf("Name = %q, want %q", gpu.Name, "NVIDIA GeForce RTX 4090")
		}
	})
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

func TestDetectGPU_AllProbesFail(t *testing.T) {
	runner := newMockRunner(map[string]mockResult{})

	ctx := context.Background()
	gpu := detectGPU(ctx, runner)
	if gpu != nil {
		t.Fatalf("expected nil GPU when all probes fail, got %+v", gpu)
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
		},
	})

	ctx := context.Background()
	gpu := detectGPU(ctx, runner)
	if gpu == nil {
		t.Fatal("expected non-nil GPU")
	}
	if gpu.Vendor != "nvidia" {
		t.Errorf("Vendor = %q, want %q", gpu.Vendor, "nvidia")
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
		},
		"nvidia-smi --query-gpu=driver_version --format=csv,noheader": {
			output: []byte("560.94\n"),
		},
	})

	ctx := context.Background()
	gpu := detectGPU(ctx, runner)
	if gpu == nil {
		t.Fatal("expected non-nil GPU")
	}
	if gpu.Vendor != "nvidia" {
		t.Errorf("Vendor = %q, want %q", gpu.Vendor, "nvidia")
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
	if hw.GPU.Vendor != "nvidia" {
		t.Errorf("GPU Vendor = %q, want %q", hw.GPU.Vendor, "nvidia")
	}
	if hw.GPU.Arch != "Ampere" {
		t.Errorf("GPU Arch = %q, want %q", hw.GPU.Arch, "Ampere")
	}
}

func TestParseNvidiaGPUMetrics(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantNil  bool
		wantUtil int
		wantMem  int
		wantTemp float64
	}{
		{
			name:     "valid metrics",
			output:   "85, 18432, 24564, 72.0, 280.50\n",
			wantUtil: 85,
			wantMem:  18432,
			wantTemp: 72.0,
		},
		{
			name:     "idle GPU",
			output:   "0, 512, 24564, 35.0, 25.00\n",
			wantUtil: 0,
			wantMem:  512,
			wantTemp: 35.0,
		},
		{
			name:     "N/A utilization only",
			output:   "[N/A], 18432, 24564, 72.0, 280.50\n",
			wantUtil: 0,
			wantMem:  18432,
			wantTemp: 72.0,
		},
		{
			name:     "N/A power and temp",
			output:   "85, 18432, 24564, [N/A], [N/A]\n",
			wantUtil: 85,
			wantMem:  18432,
			wantTemp: 0,
		},
		{
			name:    "all critical N/A",
			output:  "[N/A], [N/A], [N/A], 45.0, [N/A]\n",
			wantNil: true,
		},
		{
			name:    "empty output",
			output:  "",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := parseNvidiaGPUMetrics(tt.output)
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
		t.Error("expected nil GPU metrics when no GPU tools found")
	}
	if m.RAM.TotalMiB <= 0 {
		t.Error("RAM total should be > 0")
	}
}

// --- Multi-vendor parse tests ---

func TestParseAMDGPU(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantNil   bool
		wantName  string
		wantArch  string
		wantVRAM  int
		wantPower float64
	}{
		{
			name:     "MI250X CDNA2",
			output:   `{"card0": {"Card Series": "AMD Instinct MI250X", "VRAM Total Memory (B)": "137438953472", "Temperature (Sensor edge) (C)": "42.0", "Average Graphics Package Power (W)": "150.0"}}`,
			wantName: "AMD Instinct MI250X",
			wantArch: "CDNA2",
			wantVRAM: 131072,
		},
		{
			name:     "MI300X CDNA3",
			output:   `{"card0": {"Card Series": "AMD Instinct MI300X", "VRAM Total Memory (B)": "206158430208"}}`,
			wantName: "AMD Instinct MI300X",
			wantArch: "CDNA3",
			wantVRAM: 196608,
		},
		{
			name:     "RX 7900 XTX RDNA3",
			output:   `{"card0": {"Card series": "Radeon RX 7900 XTX", "VRAM Total Memory (B)": "25769803776"}}`,
			wantName: "Radeon RX 7900 XTX",
			wantArch: "RDNA3",
			wantVRAM: 24576,
		},
		{
			name:     "multi-GPU count",
			output:   `{"card0": {"Card Series": "AMD Instinct MI250X", "VRAM Total Memory (B)": "137438953472"}, "card1": {"Card Series": "AMD Instinct MI250X", "VRAM Total Memory (B)": "137438953472"}}`,
			wantName: "AMD Instinct MI250X",
			wantArch: "CDNA2",
			wantVRAM: 131072,
		},
		{
			name:      "Radeon 8060S APU via GFX version",
			output:    `{"card0": {"Temperature (Sensor edge) (C)": "32.0", "Current Socket Graphics Package Power (W)": "6.03", "VRAM Total Memory (B)": "68719476736", "VRAM Total Used Memory (B)": "154820608", "Card Series": "AMD Radeon Graphics", "Card Model": "0x1586", "Card Vendor": "Advanced Micro Devices, Inc. [AMD/ATI]", "Card SKU": "STRXLGEN", "Subsystem ID": "-0x7fe3", "Device Rev": "0xc1", "Node ID": "1", "GUID": "11131", "GFX Version": "gfx1151"}}`,
			wantName:  "AMD Radeon Graphics",
			wantArch:  "RDNA3.5",
			wantVRAM:  65536,
			wantPower: 6.03,
		},
		{
			name:    "empty JSON",
			output:  `{}`,
			wantNil: true,
		},
		{
			name:    "invalid JSON",
			output:  `not json`,
			wantNil: true,
		},
		{
			name:    "no card name",
			output:  `{"card0": {"VRAM Total Memory (B)": "137438953472"}}`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gpu := parseAMDGPU(tt.output)
			if tt.wantNil {
				if gpu != nil {
					t.Fatalf("expected nil GPU, got %+v", gpu)
				}
				return
			}
			if gpu == nil {
				t.Fatal("expected non-nil GPU")
			}
			if gpu.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", gpu.Name, tt.wantName)
			}
			if gpu.Arch != tt.wantArch {
				t.Errorf("Arch = %q, want %q", gpu.Arch, tt.wantArch)
			}
			if gpu.VRAMMiB != tt.wantVRAM {
				t.Errorf("VRAMMiB = %d, want %d", gpu.VRAMMiB, tt.wantVRAM)
			}
			if tt.wantPower > 0 && gpu.PowerDrawWatts != tt.wantPower {
				t.Errorf("PowerDrawWatts = %f, want %f", gpu.PowerDrawWatts, tt.wantPower)
			}
		})
	}
}

func TestParseIntelGPU(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantNil  bool
		wantName string
		wantArch string
		wantVRAM int
	}{
		{
			name:     "Max 1550 Xe HPC",
			output:   `[{"device_id": 0, "device_name": "Intel(R) Data Center GPU Max 1550", "memory_physical_size_byte": 68719476736}]`,
			wantName: "Intel(R) Data Center GPU Max 1550",
			wantArch: "Xe HPC",
			wantVRAM: 65536,
		},
		{
			name:     "Arc A770 Xe HPG",
			output:   `[{"device_id": 0, "device_name": "Intel(R) Arc(TM) A770", "memory_physical_size_byte": 17179869184}]`,
			wantName: "Intel(R) Arc(TM) A770",
			wantArch: "Xe HPG",
			wantVRAM: 16384,
		},
		{
			name:     "multi-device",
			output:   `[{"device_id": 0, "device_name": "Intel(R) Data Center GPU Max 1550", "memory_physical_size_byte": 68719476736}, {"device_id": 1, "device_name": "Intel(R) Data Center GPU Max 1550", "memory_physical_size_byte": 68719476736}]`,
			wantName: "Intel(R) Data Center GPU Max 1550",
			wantArch: "Xe HPC",
			wantVRAM: 65536,
		},
		{
			name:    "empty array",
			output:  `[]`,
			wantNil: true,
		},
		{
			name:    "invalid JSON",
			output:  `not json`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gpu := parseIntelGPU(tt.output)
			if tt.wantNil {
				if gpu != nil {
					t.Fatalf("expected nil GPU, got %+v", gpu)
				}
				return
			}
			if gpu == nil {
				t.Fatal("expected non-nil GPU")
			}
			if gpu.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", gpu.Name, tt.wantName)
			}
			if gpu.Arch != tt.wantArch {
				t.Errorf("Arch = %q, want %q", gpu.Arch, tt.wantArch)
			}
			if gpu.VRAMMiB != tt.wantVRAM {
				t.Errorf("VRAMMiB = %d, want %d", gpu.VRAMMiB, tt.wantVRAM)
			}
		})
	}
}

func TestParseHuaweiNPU(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantNil  bool
		wantName string
		wantArch string
		wantVRAM int
	}{
		{
			name:     "Ascend 910B",
			output:   `{"NPU": [{"Name": "Ascend 910B", "HBM Capacity(MB)": 65536, "Temperature(C)": 42}]}`,
			wantName: "Ascend 910B",
			wantArch: "Ascend910B",
			wantVRAM: 65536,
		},
		{
			name:     "Ascend 310P",
			output:   `{"NPU": [{"Name": "Ascend 310P", "HBM Capacity(MB)": 16384, "Temperature(C)": 35}]}`,
			wantName: "Ascend 310P",
			wantArch: "Ascend310P",
			wantVRAM: 16384,
		},
		{
			name:     "multi-NPU",
			output:   `{"NPU": [{"Name": "Ascend 910B", "HBM Capacity(MB)": 65536}, {"Name": "Ascend 910B", "HBM Capacity(MB)": 65536}]}`,
			wantName: "Ascend 910B",
			wantArch: "Ascend910B",
			wantVRAM: 65536,
		},
		{
			name:    "empty NPU array",
			output:  `{"NPU": []}`,
			wantNil: true,
		},
		{
			name:    "invalid JSON",
			output:  `not json`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gpu := parseHuaweiNPU(tt.output)
			if tt.wantNil {
				if gpu != nil {
					t.Fatalf("expected nil GPU, got %+v", gpu)
				}
				return
			}
			if gpu == nil {
				t.Fatal("expected non-nil GPU")
			}
			if gpu.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", gpu.Name, tt.wantName)
			}
			if gpu.Arch != tt.wantArch {
				t.Errorf("Arch = %q, want %q", gpu.Arch, tt.wantArch)
			}
			if gpu.VRAMMiB != tt.wantVRAM {
				t.Errorf("VRAMMiB = %d, want %d", gpu.VRAMMiB, tt.wantVRAM)
			}
		})
	}
}

func TestParseMThreadsGPU(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantNil  bool
		wantName string
		wantArch string
		wantVRAM int
	}{
		{
			name:     "MTT S4000",
			output:   `{"gpus": [{"product_name": "MTT S4000", "memory_total": "32768 MiB", "temperature": "45 C", "power_draw": "150.0 W"}]}`,
			wantName: "MTT S4000",
			wantArch: "MTT S4000",
			wantVRAM: 32768,
		},
		{
			name:     "MTT S80",
			output:   `{"gpus": [{"product_name": "MTT S80", "memory_total": "16384 MiB"}]}`,
			wantName: "MTT S80",
			wantArch: "MTT S80",
			wantVRAM: 16384,
		},
		{
			name:    "empty gpus array",
			output:  `{"gpus": []}`,
			wantNil: true,
		},
		{
			name:    "invalid JSON",
			output:  `not json`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gpu := parseMThreadsGPU(tt.output)
			if tt.wantNil {
				if gpu != nil {
					t.Fatalf("expected nil GPU, got %+v", gpu)
				}
				return
			}
			if gpu == nil {
				t.Fatal("expected non-nil GPU")
			}
			if gpu.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", gpu.Name, tt.wantName)
			}
			if gpu.Arch != tt.wantArch {
				t.Errorf("Arch = %q, want %q", gpu.Arch, tt.wantArch)
			}
			if gpu.VRAMMiB != tt.wantVRAM {
				t.Errorf("VRAMMiB = %d, want %d", gpu.VRAMMiB, tt.wantVRAM)
			}
		})
	}
}

// --- Probe chain integration tests ---

func TestProbeChain_NvidiaFailsAMDSucceeds(t *testing.T) {
	runner := newMockRunner(map[string]mockResult{
		"nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap,power.draw,power.limit,temperature.gpu --format=csv,noheader,nounits": {
			err: fmt.Errorf("nvidia-smi not found"),
		},
		"rocm-smi --json --showproductname --showmeminfo vram --showtemp --showpower": {
			output: []byte(`{"card0": {"Card Series": "AMD Instinct MI250X", "VRAM Total Memory (B)": "137438953472", "Temperature (Sensor edge) (C)": "42.0"}}`),
		},
	})

	ctx := context.Background()
	gpu := detectGPU(ctx, runner)
	if gpu == nil {
		t.Fatal("expected non-nil GPU")
	}
	if gpu.Vendor != "amd" {
		t.Errorf("Vendor = %q, want %q", gpu.Vendor, "amd")
	}
	if gpu.Name != "AMD Instinct MI250X" {
		t.Errorf("Name = %q, want %q", gpu.Name, "AMD Instinct MI250X")
	}
	if gpu.Arch != "CDNA2" {
		t.Errorf("Arch = %q, want %q", gpu.Arch, "CDNA2")
	}
}

func TestProbeChain_FallsToHuawei(t *testing.T) {
	runner := newMockRunner(map[string]mockResult{
		"npu-smi info -t common -j": {
			output: []byte(`{"NPU": [{"Name": "Ascend 910B", "HBM Capacity(MB)": 65536, "Temperature(C)": 42}]}`),
		},
	})

	ctx := context.Background()
	gpu := detectGPU(ctx, runner)
	if gpu == nil {
		t.Fatal("expected non-nil GPU")
	}
	if gpu.Vendor != "huawei" {
		t.Errorf("Vendor = %q, want %q", gpu.Vendor, "huawei")
	}
	if gpu.Name != "Ascend 910B" {
		t.Errorf("Name = %q, want %q", gpu.Name, "Ascend 910B")
	}
}

func TestProbeChain_NvidiaParseFailsFallsThrough(t *testing.T) {
	runner := newMockRunner(map[string]mockResult{
		"nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap,power.draw,power.limit,temperature.gpu --format=csv,noheader,nounits": {
			output: []byte(""),
		},
		"xpu-smi discovery --json": {
			output: []byte(`[{"device_id": 0, "device_name": "Intel(R) Data Center GPU Max 1550", "memory_physical_size_byte": 68719476736}]`),
		},
	})

	ctx := context.Background()
	gpu := detectGPU(ctx, runner)
	if gpu == nil {
		t.Fatal("expected non-nil GPU")
	}
	if gpu.Vendor != "intel" {
		t.Errorf("Vendor = %q, want %q", gpu.Vendor, "intel")
	}
	if gpu.Name != "Intel(R) Data Center GPU Max 1550" {
		t.Errorf("Name = %q, want %q", gpu.Name, "Intel(R) Data Center GPU Max 1550")
	}
}

func TestAMDGPUToArch(t *testing.T) {
	tests := []struct {
		name string
		arch string
	}{
		{"AMD Instinct MI300X", "CDNA3"},
		{"AMD Instinct MI250X", "CDNA2"},
		{"AMD Instinct MI210", "CDNA2"},
		{"AMD Instinct MI100", "CDNA"},
		{"Radeon RX 7900 XTX", "RDNA3"},
		{"Radeon RX 6900 XT", "RDNA2"},
		{"Radeon PRO W7900", "RDNA3"},
		{"Unknown GPU", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := amdGPUToArch(tt.name)
			if got != tt.arch {
				t.Errorf("amdGPUToArch(%q) = %q, want %q", tt.name, got, tt.arch)
			}
		})
	}
}

func TestGfxVersionToArch(t *testing.T) {
	tests := []struct {
		gfxVer string
		want   string
	}{
		{"gfx1151", "RDNA3.5"},
		{"gfx1150", "RDNA3.5"},
		{"gfx1100", "RDNA3"},
		{"gfx1103", "RDNA3"},
		{"gfx1030", "RDNA2"},
		{"gfx1036", "RDNA2"},
		{"gfx1010", "RDNA"},
		{"gfx1012", "RDNA"},
		{"gfx942", "CDNA3"},
		{"gfx940", "CDNA3"},
		{"gfx90a", "CDNA2"},
		{"gfx908", "CDNA"},
		{"gfx900", "GCN5"},
		{"gfx906", "GCN5"},
		{"gfx1200", "RDNA4"},
		{"", ""},
		{"notgfx", ""},
		{"gfx", ""},
	}

	for _, tt := range tests {
		t.Run(tt.gfxVer, func(t *testing.T) {
			got := gfxVersionToArch(tt.gfxVer)
			if got != tt.want {
				t.Errorf("gfxVersionToArch(%q) = %q, want %q", tt.gfxVer, got, tt.want)
			}
		})
	}
}

func TestHuaweiNPUToArch(t *testing.T) {
	tests := []struct {
		name string
		arch string
	}{
		{"Ascend 910B", "Ascend910B"},
		{"Ascend 910C", "Ascend910C"},
		{"Ascend 910", "Ascend910"},
		{"Ascend 310P", "Ascend310P"},
		{"Ascend 310", "Ascend310"},
		{"Unknown NPU", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := huaweiNPUToArch(tt.name)
			if got != tt.arch {
				t.Errorf("huaweiNPUToArch(%q) = %q, want %q", tt.name, got, tt.arch)
			}
		})
	}
}

// --- Enrichment tests ---

func TestParseNvidiaCUDAVersion(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "dev-win header",
			output: "| NVIDIA-SMI 566.36                 Driver Version: 566.36         CUDA Version: 12.7     |\n",
			want:   "12.7",
		},
		{
			name:   "GB10 header",
			output: "| NVIDIA-SMI 580.126.09             Driver Version: 580.126.09     CUDA Version: 13.0     |\n",
			want:   "13.0",
		},
		{
			name:   "linux-1 header",
			output: "| NVIDIA-SMI 550.135                Driver Version: 550.135        CUDA Version: 12.4     |\n",
			want:   "12.4",
		},
		{
			name: "full output",
			output: "Thu Feb 26 11:34:43 2026\n" +
				"+-----------------------------------------------------------------------------------------+\n" +
				"| NVIDIA-SMI 566.36                 Driver Version: 566.36         CUDA Version: 12.7     |\n" +
				"|-----------------------------------------+------------------------+----------------------+\n",
			want: "12.7",
		},
		{
			name:   "no CUDA version",
			output: "| NVIDIA-SMI 566.36                 Driver Version: 566.36         |\n",
			want:   "",
		},
		{
			name:   "empty",
			output: "",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNvidiaCUDAVersion(tt.output)
			if got != tt.want {
				t.Errorf("parseNvidiaCUDAVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseNvidiaPowerCap(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   float64
	}{
		{
			name:   "desktop RTX 4090",
			output: "|  0%   30C    P8             19W /  450W |   41141MiB /  49140MiB |      0%      Default |\n",
			want:   450,
		},
		{
			name:   "laptop RTX 4060",
			output: "| N/A   51C    P0             15W /   75W |       0MiB /   8188MiB |      0%      Default |\n",
			want:   75,
		},
		{
			name:   "N/A power cap (GB10)",
			output: "| N/A   38C    P8              4W /  N/A  | Not Supported          |      0%      Default |\n",
			want:   0,
		},
		{
			name:   "no power line",
			output: "some other output\n",
			want:   0,
		},
		{
			name:   "empty",
			output: "",
			want:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNvidiaPowerCap(tt.output)
			if got != tt.want {
				t.Errorf("parseNvidiaPowerCap() = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestEnrichNvidiaGPU(t *testing.T) {
	smiOutput := "Thu Feb 26 11:34:43 2026\n" +
		"+-----------------------------------------------------------------------------------------+\n" +
		"| NVIDIA-SMI 566.36                 Driver Version: 566.36         CUDA Version: 12.7     |\n" +
		"|-----------------------------------------+------------------------+----------------------+\n" +
		"|   0  NVIDIA GeForce RTX 4060 ...  WDDM  |   00000000:01:00.0 Off |                  N/A |\n" +
		"| N/A   51C    P0             15W /   75W |       0MiB /   8188MiB |      0%      Default |\n"

	t.Run("fills CUDA version and power limit", func(t *testing.T) {
		runner := newMockRunner(map[string]mockResult{
			"nvidia-smi": {output: []byte(smiOutput)},
		})
		gpu := &GPUInfo{Vendor: "nvidia", Name: "NVIDIA GeForce RTX 4060 Laptop GPU"}
		enrichNvidiaGPU(context.Background(), runner, gpu)

		if gpu.SDKVersion != "CUDA 12.7" {
			t.Errorf("SDKVersion = %q, want %q", gpu.SDKVersion, "CUDA 12.7")
		}
		if gpu.PowerLimitWatts != 75 {
			t.Errorf("PowerLimitWatts = %f, want 75", gpu.PowerLimitWatts)
		}
	})

	t.Run("does not overwrite existing values", func(t *testing.T) {
		runner := newMockRunner(map[string]mockResult{
			"nvidia-smi": {output: []byte(smiOutput)},
		})
		gpu := &GPUInfo{Vendor: "nvidia", SDKVersion: "CUDA 11.0", PowerLimitWatts: 300}
		enrichNvidiaGPU(context.Background(), runner, gpu)

		if gpu.SDKVersion != "CUDA 11.0" {
			t.Errorf("SDKVersion = %q, want %q (should not overwrite)", gpu.SDKVersion, "CUDA 11.0")
		}
		if gpu.PowerLimitWatts != 300 {
			t.Errorf("PowerLimitWatts = %f, want 300 (should not overwrite)", gpu.PowerLimitWatts)
		}
	})

	t.Run("graceful degradation when nvidia-smi fails", func(t *testing.T) {
		runner := newMockRunner(map[string]mockResult{})
		gpu := &GPUInfo{Vendor: "nvidia", Name: "NVIDIA GeForce RTX 4090"}
		enrichNvidiaGPU(context.Background(), runner, gpu)

		if gpu.SDKVersion != "" {
			t.Errorf("SDKVersion = %q, want empty on failure", gpu.SDKVersion)
		}
	})
}

func TestEnrichAMDGPU(t *testing.T) {
	t.Run("fills SDK and driver version", func(t *testing.T) {
		runner := newMockRunner(map[string]mockResult{
			"cat /opt/rocm/.info/version":       {output: []byte("6.4.0\n")},
			"modinfo -F version amdgpu":          {output: []byte("6.11.8\n")},
		})
		gpu := &GPUInfo{Vendor: "amd", Name: "AMD Radeon Graphics"}
		enrichAMDGPU(context.Background(), runner, gpu)

		if gpu.SDKVersion != "ROCm 6.4.0" {
			t.Errorf("SDKVersion = %q, want %q", gpu.SDKVersion, "ROCm 6.4.0")
		}
		if gpu.DriverVersion != "6.11.8" {
			t.Errorf("DriverVersion = %q, want %q", gpu.DriverVersion, "6.11.8")
		}
	})

	t.Run("does not overwrite existing values", func(t *testing.T) {
		runner := newMockRunner(map[string]mockResult{
			"cat /opt/rocm/.info/version":       {output: []byte("6.4.0\n")},
			"modinfo -F version amdgpu":          {output: []byte("6.11.8\n")},
		})
		gpu := &GPUInfo{Vendor: "amd", SDKVersion: "ROCm 5.0", DriverVersion: "5.0.0"}
		enrichAMDGPU(context.Background(), runner, gpu)

		if gpu.SDKVersion != "ROCm 5.0" {
			t.Errorf("SDKVersion = %q, want %q (should not overwrite)", gpu.SDKVersion, "ROCm 5.0")
		}
		if gpu.DriverVersion != "5.0.0" {
			t.Errorf("DriverVersion = %q, want %q (should not overwrite)", gpu.DriverVersion, "5.0.0")
		}
	})

	t.Run("graceful degradation when tools absent", func(t *testing.T) {
		runner := newMockRunner(map[string]mockResult{})
		gpu := &GPUInfo{Vendor: "amd", Name: "AMD Radeon Graphics"}
		enrichAMDGPU(context.Background(), runner, gpu)

		if gpu.SDKVersion != "" {
			t.Errorf("SDKVersion = %q, want empty on failure", gpu.SDKVersion)
		}
		if gpu.DriverVersion != "" {
			t.Errorf("DriverVersion = %q, want empty on failure", gpu.DriverVersion)
		}
	})
}

func TestDetectWithRunner_UnifiedMemoryBackfill(t *testing.T) {
	mocks := platformMockOutputs()
	mocks["nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap,power.draw,power.limit,temperature.gpu --format=csv,noheader,nounits"] = mockResult{
		output: []byte("NVIDIA GB10, [N/A], 580.126.09, 12.1, 4.72, [N/A], 38\n"),
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
	if !hw.GPU.UnifiedMemory {
		t.Error("expected UnifiedMemory = true for GB10")
	}
	if hw.GPU.VRAMMiB != hw.RAM.TotalMiB {
		t.Errorf("VRAMMiB = %d, want %d (RAM total)", hw.GPU.VRAMMiB, hw.RAM.TotalMiB)
	}
	if hw.GPU.VRAMMiB <= 0 {
		t.Error("VRAMMiB should be > 0 after backfill")
	}
}

func TestDetectWithRunner_AMDUnifiedMemory(t *testing.T) {
	mocks := platformMockOutputs()
	// nvidia-smi fails (no NVIDIA GPU), rocm-smi returns AMD APU data
	mocks["nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap,power.draw,power.limit,temperature.gpu --format=csv,noheader,nounits"] = mockResult{
		err: fmt.Errorf("nvidia-smi failed"),
	}
	mocks["rocm-smi --json --showproductname --showmeminfo vram --showtemp --showpower"] = mockResult{
		output: []byte(`{"card0": {"Card Series": "AMD Radeon Graphics", "VRAM Total Memory (B)": "68719476736", "Temperature (Sensor edge) (C)": "32.0", "Current Socket Graphics Package Power (W)": "6.03", "GFX Version": "gfx1151"}}`),
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
	if hw.GPU.Vendor != "amd" {
		t.Errorf("Vendor = %q, want %q", hw.GPU.Vendor, "amd")
	}
	if hw.GPU.Arch != "RDNA3.5" {
		t.Errorf("Arch = %q, want %q", hw.GPU.Arch, "RDNA3.5")
	}
	// VRAM (65536 MiB) ≈ system RAM → unified memory should be detected.
	// Note: RAM.TotalMiB comes from platform mock outputs which report real system RAM.
	// The test validates the heuristic triggers when VRAM/RAM ratio is within [0.9, 1.1].
	if hw.RAM.TotalMiB > 0 {
		ratio := float64(hw.GPU.VRAMMiB) / float64(hw.RAM.TotalMiB)
		if ratio >= 0.9 && ratio <= 1.1 {
			if !hw.GPU.UnifiedMemory {
				t.Error("expected UnifiedMemory = true for AMD APU where VRAM ≈ RAM")
			}
		}
	}
	if hw.GPU.PowerDrawWatts != 6.03 {
		t.Errorf("PowerDrawWatts = %f, want 6.03", hw.GPU.PowerDrawWatts)
	}
}

func TestDetectWithRunner_AMDDiscreteNotUnified(t *testing.T) {
	mocks := platformMockOutputs()
	mocks["nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap,power.draw,power.limit,temperature.gpu --format=csv,noheader,nounits"] = mockResult{
		err: fmt.Errorf("nvidia-smi failed"),
	}
	mocks["rocm-smi --json --showproductname --showmeminfo vram --showtemp --showpower"] = mockResult{
		output: []byte(`{"card0": {"Card Series": "Radeon RX 7900 XTX", "VRAM Total Memory (B)": "25769803776", "Temperature (Sensor edge) (C)": "45.0", "Average Graphics Package Power (W)": "200.0"}}`),
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
	// Discrete GPU: VRAM (24576 MiB) << system RAM → NOT unified memory.
	if hw.GPU.UnifiedMemory {
		t.Error("expected UnifiedMemory = false for discrete AMD GPU")
	}
}

// --- NPU tests ---

func TestParseAccelUevent(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantDriver string
		wantPCIID  string
	}{
		{
			name: "AMD XDNA (amd395)",
			content: "DRIVER=amdxdna\n" +
				"PCI_CLASS=118000\n" +
				"PCI_ID=1022:17F0\n" +
				"PCI_SUBSYS_ID=1022:17F0\n" +
				"PCI_SLOT_NAME=0000:c7:00.1\n" +
				"MODALIAS=pci:v00001022d000017F0sv00001022sd000017F0bc11sc80i00\n",
			wantDriver: "amdxdna",
			wantPCIID:  "1022:17F0",
		},
		{
			name: "Intel NPU",
			content: "DRIVER=intel_vpu\n" +
				"PCI_CLASS=118000\n" +
				"PCI_ID=8086:7D1D\n",
			wantDriver: "intel_vpu",
			wantPCIID:  "8086:7D1D",
		},
		{
			name:       "empty",
			content:    "",
			wantDriver: "",
			wantPCIID:  "",
		},
		{
			name:       "no driver",
			content:    "PCI_ID=1022:17F0\n",
			wantDriver: "",
			wantPCIID:  "1022:17F0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver, pciID := parseAccelUevent(tt.content)
			if driver != tt.wantDriver {
				t.Errorf("driver = %q, want %q", driver, tt.wantDriver)
			}
			if pciID != tt.wantPCIID {
				t.Errorf("pciID = %q, want %q", pciID, tt.wantPCIID)
			}
		})
	}
}

func TestNpuVendorFromDriver(t *testing.T) {
	tests := []struct {
		driver string
		want   string
	}{
		{"amdxdna", "amd"},
		{"intel_vpu", "intel"},
		{"qcom_npu", "qualcomm"},
		{"unknown_driver", "unknown_driver"},
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			got := npuVendorFromDriver(tt.driver)
			if got != tt.want {
				t.Errorf("npuVendorFromDriver(%q) = %q, want %q", tt.driver, got, tt.want)
			}
		})
	}
}

func TestNpuName(t *testing.T) {
	tests := []struct {
		name    string
		vbnv    string
		pciID   string
		driver  string
		want    string
	}{
		{"prefer vbnv", "RyzenAI-npu5", "1022:17F0", "amdxdna", "RyzenAI-npu5"},
		{"fallback to pciID", "", "1022:17F0", "amdxdna", "1022:17F0"},
		{"fallback to driver", "", "", "amdxdna", "amdxdna"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := npuName(tt.vbnv, tt.pciID, tt.driver)
			if got != tt.want {
				t.Errorf("npuName(%q, %q, %q) = %q, want %q", tt.vbnv, tt.pciID, tt.driver, got, tt.want)
			}
		})
	}
}

func TestCollectMetrics_UnifiedMemoryBackfill(t *testing.T) {
	mocks := platformMockOutputs()
	mocks["nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw --format=csv,noheader,nounits"] = mockResult{
		output: []byte("0, [N/A], [N/A], 38, 4.72\n"),
	}
	runner := newMockRunner(mocks)

	ctx := context.Background()
	m, err := collectMetricsWithRunner(ctx, runner)
	if err != nil {
		t.Fatalf("CollectMetrics returned error: %v", err)
	}
	if m.GPU == nil {
		t.Fatal("expected GPU metrics")
	}
	if m.GPU.MemoryTotalMiB != m.RAM.TotalMiB {
		t.Errorf("GPU MemoryTotalMiB = %d, want %d (RAM total)", m.GPU.MemoryTotalMiB, m.RAM.TotalMiB)
	}
	if m.GPU.MemoryUsedMiB != m.RAM.UsedMiB {
		t.Errorf("GPU MemoryUsedMiB = %d, want %d (RAM used)", m.GPU.MemoryUsedMiB, m.RAM.UsedMiB)
	}
}
