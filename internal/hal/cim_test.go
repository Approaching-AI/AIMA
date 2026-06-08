package hal

import "testing"

// Fixtures captured from a real AMD Ryzen AI Max+ 395 "Strix Halo" Windows 11
// box via `Get-CimInstance ... | ConvertTo-Json -Compress`.

func TestDecodeCIMObjects(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{"single object", `{"A":1}`, 1},
		{"array", `[{"A":1},{"A":2}]`, 2},
		{"leading junk then object", "\uFEFF\n{\"A\":1}", 1},
		{"empty", "", 0},
		{"whitespace", "  \n ", 0},
		{"invalid json", "not json", 0},
		{"empty array", `[]`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(decodeCIMObjects(tt.output)); got != tt.want {
				t.Errorf("len = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseCIMCPU(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantModel   string
		wantCores   int
		wantThreads int
		wantFreq    float64
	}{
		{
			name:        "Strix Halo (real fixture, trailing spaces)",
			output:      `{"Name":"AMD RYZEN AI MAX+ 395 w/ Radeon 8060S          ","NumberOfCores":16,"NumberOfLogicalProcessors":32,"MaxClockSpeed":3000}`,
			wantModel:   "AMD RYZEN AI MAX+ 395 w/ Radeon 8060S",
			wantCores:   16,
			wantThreads: 32,
			wantFreq:    3.0,
		},
		{
			name:        "Intel single socket",
			output:      `{"Name":"Intel(R) Core(TM) i9-13900K","NumberOfCores":24,"NumberOfLogicalProcessors":32,"MaxClockSpeed":3600}`,
			wantModel:   "Intel(R) Core(TM) i9-13900K",
			wantCores:   24,
			wantThreads: 32,
			wantFreq:    3.6,
		},
		{
			name:        "dual socket sums cores/threads",
			output:      `[{"Name":"Intel Xeon Gold","NumberOfCores":32,"NumberOfLogicalProcessors":64,"MaxClockSpeed":2800},{"Name":"Intel Xeon Gold","NumberOfCores":32,"NumberOfLogicalProcessors":64,"MaxClockSpeed":2800}]`,
			wantModel:   "Intel Xeon Gold",
			wantCores:   64,
			wantThreads: 128,
			wantFreq:    2.8,
		},
		{"empty", "", "", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := CPUInfo{}
			parseCIMCPU(tt.output, &info)
			if info.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", info.Model, tt.wantModel)
			}
			if info.Cores != tt.wantCores {
				t.Errorf("Cores = %d, want %d", info.Cores, tt.wantCores)
			}
			if info.Threads != tt.wantThreads {
				t.Errorf("Threads = %d, want %d", info.Threads, tt.wantThreads)
			}
			if info.FreqGHz != tt.wantFreq {
				t.Errorf("FreqGHz = %v, want %v", info.FreqGHz, tt.wantFreq)
			}
		})
	}
}

func TestParseCIMRAM(t *testing.T) {
	tests := []struct {
		name          string
		output        string
		wantTotal     int
		wantAvailable int
	}{
		{
			name:          "Strix Halo (real fixture, KiB)",
			output:        `{"TotalVisibleMemorySize":33184580,"FreePhysicalMemory":25201448}`,
			wantTotal:     32406,
			wantAvailable: 24610,
		},
		{"empty", "", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := RAMInfo{}
			parseCIMRAM(tt.output, &info)
			if info.TotalMiB != tt.wantTotal {
				t.Errorf("TotalMiB = %d, want %d", info.TotalMiB, tt.wantTotal)
			}
			if info.AvailableMiB != tt.wantAvailable {
				t.Errorf("AvailableMiB = %d, want %d", info.AvailableMiB, tt.wantAvailable)
			}
		})
	}
}

func TestParseCIMSwap(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{"two pagefiles sum (real fixture)", `[{"AllocatedBaseSize":20480},{"AllocatedBaseSize":96000}]`, 116480},
		{"single pagefile", `{"AllocatedBaseSize":20480}`, 20480},
		{"system managed (empty)", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := RAMInfo{}
			parseCIMSwap(tt.output, &info)
			if info.SwapTotalMiB != tt.want {
				t.Errorf("SwapTotalMiB = %d, want %d", info.SwapTotalMiB, tt.want)
			}
		})
	}
}

func TestParseCIMCPULoad(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   float64
	}{
		{"idle (real fixture)", `{"LoadPercentage":0}`, 0},
		{"single load", `{"LoadPercentage":42}`, 42},
		{"dual socket averaged", `[{"LoadPercentage":25},{"LoadPercentage":75}]`, 50},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseCIMCPULoad(tt.output); got != tt.want {
				t.Errorf("parseCIMCPULoad() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWindowsGPUVendor(t *testing.T) {
	tests := []struct {
		pnp    string
		compat string
		want   string
	}{
		{`PCI\VEN_1002&DEV_1586&SUBSYS_801D2014`, "Advanced Micro Devices, Inc.", "amd"},
		{`PCI\VEN_10DE&DEV_2782`, "NVIDIA", "nvidia"},
		{`PCI\VEN_8086&DEV_56A0`, "Intel Corporation", "intel"},
		{`ROOT\BasicDisplay`, "(Standard display types)", ""},
		{"", "Advanced Micro Devices, Inc.", "amd"},
		{"", "NVIDIA", "nvidia"},
		{"", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.pnp+"|"+tt.compat, func(t *testing.T) {
			if got := windowsGPUVendor(tt.pnp, tt.compat); got != tt.want {
				t.Errorf("windowsGPUVendor(%q,%q) = %q, want %q", tt.pnp, tt.compat, got, tt.want)
			}
		})
	}
}

func TestWindowsPCIDeviceID(t *testing.T) {
	tests := []struct {
		pnp  string
		want string
	}{
		{`PCI\VEN_1002&DEV_1586&SUBSYS_801D2014&REV_C1\4&35FE04F8&0&0041`, "1586"},
		{`PCI\VEN_10DE&DEV_2782&SUBSYS_...`, "2782"},
		{`ROOT\BasicDisplay`, ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.pnp, func(t *testing.T) {
			if got := windowsPCIDeviceID(tt.pnp); got != tt.want {
				t.Errorf("windowsPCIDeviceID(%q) = %q, want %q", tt.pnp, got, tt.want)
			}
		})
	}
}

func TestParseWindowsGPUs(t *testing.T) {
	t.Run("Strix Halo Radeon 8060S (real fixture)", func(t *testing.T) {
		out := `{"Name":"AMD Radeon(TM) 8060S Graphics","DriverVersion":"32.0.31007.1017","AdapterCompatibility":"Advanced Micro Devices, Inc.","PNPDeviceID":"PCI\\VEN_1002&DEV_1586&SUBSYS_801D2014&REV_C1\\4&35FE04F8&0&0041","AdapterRAM":4293918720}`
		gpu := parseWindowsGPUs(out)
		if gpu == nil {
			t.Fatal("expected non-nil GPU")
		}
		if gpu.Vendor != "amd" {
			t.Errorf("Vendor = %q, want amd", gpu.Vendor)
		}
		if gpu.Name != "AMD Radeon 8060S Graphics" {
			t.Errorf("Name = %q, want AMD Radeon 8060S Graphics", gpu.Name)
		}
		if gpu.Arch != "RDNA3.5" {
			t.Errorf("Arch = %q, want RDNA3.5", gpu.Arch)
		}
		if gpu.ComputeID != "gfx1151" {
			t.Errorf("ComputeID = %q, want gfx1151", gpu.ComputeID)
		}
		if gpu.DriverVersion != "32.0.31007.1017" {
			t.Errorf("DriverVersion = %q, want 32.0.31007.1017", gpu.DriverVersion)
		}
		if !gpu.UnifiedMemory {
			t.Error("UnifiedMemory = false, want true for Strix Halo APU")
		}
		if gpu.Count != 1 {
			t.Errorf("Count = %d, want 1", gpu.Count)
		}
	})

	t.Run("skips Microsoft Basic Display, picks NVIDIA", func(t *testing.T) {
		out := `[{"Name":"Microsoft Basic Display Adapter","DriverVersion":"10.0.0","AdapterCompatibility":"(Standard display types)","PNPDeviceID":"ROOT\\BasicDisplay\\0000"},{"Name":"NVIDIA GeForce RTX 4060 Laptop GPU","DriverVersion":"32.0.15.6636","AdapterCompatibility":"NVIDIA","PNPDeviceID":"PCI\\VEN_10DE&DEV_28E0&SUBSYS_..."}]`
		gpu := parseWindowsGPUs(out)
		if gpu == nil {
			t.Fatal("expected non-nil GPU")
		}
		if gpu.Vendor != "nvidia" {
			t.Errorf("Vendor = %q, want nvidia", gpu.Vendor)
		}
		if gpu.Name != "NVIDIA GeForce RTX 4060 Laptop GPU" {
			t.Errorf("Name = %q", gpu.Name)
		}
		if gpu.DriverVersion != "32.0.15.6636" {
			t.Errorf("DriverVersion = %q", gpu.DriverVersion)
		}
		if gpu.Count != 1 {
			t.Errorf("Count = %d, want 1", gpu.Count)
		}
	})

	t.Run("no recognizable GPU returns nil", func(t *testing.T) {
		out := `{"Name":"Microsoft Basic Display Adapter","AdapterCompatibility":"(Standard display types)","PNPDeviceID":"ROOT\\BasicDisplay\\0000"}`
		if gpu := parseWindowsGPUs(out); gpu != nil {
			t.Fatalf("expected nil, got %+v", gpu)
		}
	})

	t.Run("empty output returns nil", func(t *testing.T) {
		if gpu := parseWindowsGPUs(""); gpu != nil {
			t.Fatalf("expected nil, got %+v", gpu)
		}
	})
}
