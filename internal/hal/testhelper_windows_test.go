//go:build windows

package hal

// Mock outputs keyed by the exact CIM PowerShell commands detect_windows.go runs.
func platformMockOutputs() map[string]mockResult {
	return map[string]mockResult{
		"powershell -NoProfile -NonInteractive -Command " + cimCPUScript: {
			output: []byte(`{"Name":"Intel(R) Core(TM) i9-13900K","NumberOfCores":24,"NumberOfLogicalProcessors":32,"MaxClockSpeed":3600}`),
		},
		"powershell -NoProfile -NonInteractive -Command " + cimRAMScript: {
			output: []byte(`{"TotalVisibleMemorySize":33554432,"FreePhysicalMemory":16777216}`),
		},
	}
}
