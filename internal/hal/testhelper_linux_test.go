//go:build linux

package hal

// platformMockOutputs returns an empty map for Linux because Linux detection
// reads /proc files directly instead of running commands via the runner.
func platformMockOutputs() map[string]mockResult {
	return map[string]mockResult{}
}
