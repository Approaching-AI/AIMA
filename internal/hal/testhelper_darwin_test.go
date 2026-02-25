//go:build darwin

package hal

func platformMockOutputs() map[string]mockResult {
	return map[string]mockResult{
		"sysctl -n machdep.cpu.brand_string": {
			output: []byte("Apple M2 Max\n"),
		},
		"sysctl -n hw.physicalcpu": {
			output: []byte("12\n"),
		},
		"sysctl -n hw.logicalcpu": {
			output: []byte("12\n"),
		},
		"sysctl -n hw.cpufrequency": {
			output: []byte("3500000000\n"),
		},
		"sysctl -n hw.memsize": {
			output: []byte("34359738368\n"),
		},
		"vm_stat": {
			output: []byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:                              262144.\nPages active:                            524288.\nPages inactive:                          131072.\nPages speculative:                       65536.\nPages throttled:                         0.\n"),
		},
	}
}
