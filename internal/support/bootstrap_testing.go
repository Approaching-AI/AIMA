package support

import "time"

// minRegistrationBackoffForTest returns the current starting backoff. Tests
// use the pair minRegistrationBackoffForTest / setMinRegistrationBackoffForTest
// to swap in a short backoff for fast retry coverage and restore it on
// teardown. Production code never calls these.
func minRegistrationBackoffForTest() time.Duration {
	return minRegistrationBackoff
}

func setMinRegistrationBackoffForTest(d time.Duration) {
	minRegistrationBackoff = d
}
