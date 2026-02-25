//go:build windows

package hal

import "testing"

func TestParseWMICCPU(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantModel   string
		wantCores   int
		wantThreads int
		wantFreq    float64
	}{
		{
			name:        "standard Intel",
			output:      "Node,MaxClockSpeed,Name,NumberOfCores,NumberOfLogicalProcessors\nWORKSTATION,3600,Intel(R) Core(TM) i9-13900K,24,32\n",
			wantModel:   "Intel(R) Core(TM) i9-13900K",
			wantCores:   24,
			wantThreads: 32,
			wantFreq:    3.6,
		},
		{
			name:        "AMD Ryzen",
			output:      "Node,MaxClockSpeed,Name,NumberOfCores,NumberOfLogicalProcessors\nDESKTOP,4500,AMD Ryzen 9 7950X,16,32\n",
			wantModel:   "AMD Ryzen 9 7950X",
			wantCores:   16,
			wantThreads: 32,
			wantFreq:    4.5,
		},
		{
			name:        "empty output",
			output:      "",
			wantModel:   "",
			wantCores:   0,
			wantThreads: 0,
			wantFreq:    0,
		},
		{
			name:        "header only",
			output:      "Node,MaxClockSpeed,Name,NumberOfCores,NumberOfLogicalProcessors\n",
			wantModel:   "",
			wantCores:   0,
			wantThreads: 0,
			wantFreq:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := CPUInfo{}
			parseWMICCPU(tt.output, &info)
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
				t.Errorf("FreqGHz = %f, want %f", info.FreqGHz, tt.wantFreq)
			}
		})
	}
}

func TestParseWMICRAM(t *testing.T) {
	tests := []struct {
		name          string
		output        string
		wantTotal     int
		wantAvailable int
	}{
		{
			name:          "32GB system",
			output:        "Node,FreePhysicalMemory,TotalVisibleMemorySize\nWORKSTATION,16777216,33554432\n",
			wantTotal:     32768,
			wantAvailable: 16384,
		},
		{
			name:          "16GB system low memory",
			output:        "Node,FreePhysicalMemory,TotalVisibleMemorySize\nLAPTOP,2097152,16777216\n",
			wantTotal:     16384,
			wantAvailable: 2048,
		},
		{
			name:          "empty output",
			output:        "",
			wantTotal:     0,
			wantAvailable: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := RAMInfo{}
			parseWMICRAM(tt.output, &info)
			if info.TotalMiB != tt.wantTotal {
				t.Errorf("TotalMiB = %d, want %d", info.TotalMiB, tt.wantTotal)
			}
			if info.AvailableMiB != tt.wantAvailable {
				t.Errorf("AvailableMiB = %d, want %d", info.AvailableMiB, tt.wantAvailable)
			}
		})
	}
}
