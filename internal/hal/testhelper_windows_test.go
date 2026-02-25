//go:build windows

package hal

func platformMockOutputs() map[string]mockResult {
	return map[string]mockResult{
		"wmic cpu get Name,NumberOfCores,NumberOfLogicalProcessors,MaxClockSpeed /format:csv": {
			output: []byte("Node,MaxClockSpeed,Name,NumberOfCores,NumberOfLogicalProcessors\nWORKSTATION,3600,Intel(R) Core(TM) i9-13900K,24,32\n"),
		},
		"wmic os get TotalVisibleMemorySize,FreePhysicalMemory /format:csv": {
			output: []byte("Node,FreePhysicalMemory,TotalVisibleMemorySize\nWORKSTATION,16777216,33554432\n"),
		},
	}
}
