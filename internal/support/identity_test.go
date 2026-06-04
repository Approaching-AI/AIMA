package support

import "testing"

func TestDeriveHardwareIDsUsesHostnameInPrimaryIdentity(t *testing.T) {
	t.Parallel()

	hardwareA, candidatesA := deriveHardwareIDs("linux", "arm64", "mt-AIBOOK-ABA14104", "shared-machine-id", nil)
	hardwareB, candidatesB := deriveHardwareIDs("linux", "arm64", "devtech-AIBOOK-ABA14102", "shared-machine-id", nil)

	if hardwareA == "" || hardwareB == "" {
		t.Fatalf("hardware IDs must be present: %q %q", hardwareA, hardwareB)
	}
	if hardwareA == hardwareB {
		t.Fatalf("cloned machine-id with different hostnames produced the same primary hardware_id: %q", hardwareA)
	}

	legacy := hashString("shared-machine-id")
	if !containsString(candidatesA, legacy) || !containsString(candidatesB, legacy) {
		t.Fatalf("legacy machine-id hash must remain a recovery candidate: %v %v", candidatesA, candidatesB)
	}
}

func TestDeriveHardwareIDsIsDeterministicAndDeduplicatesCandidates(t *testing.T) {
	t.Parallel()

	hardwareA, candidatesA := deriveHardwareIDs(
		"linux",
		"arm64",
		"worker-host",
		"machine-1",
		[]string{"serial:abc", "serial:abc", "machine-1"},
	)
	hardwareB, candidatesB := deriveHardwareIDs(
		"linux",
		"arm64",
		"worker-host",
		"machine-1",
		[]string{"serial:abc", "serial:abc", "machine-1"},
	)

	if hardwareA != hardwareB {
		t.Fatalf("hardware ID changed between identical inputs: %q != %q", hardwareA, hardwareB)
	}
	if len(candidatesA) != len(candidatesB) {
		t.Fatalf("candidate count changed between identical inputs: %d != %d", len(candidatesA), len(candidatesB))
	}
	for i := range candidatesA {
		if candidatesA[i] != candidatesB[i] {
			t.Fatalf("candidate %d changed between identical inputs: %q != %q", i, candidatesA[i], candidatesB[i])
		}
	}

	if want := hashString("machine-1"); !containsString(candidatesA, want) {
		t.Fatalf("missing legacy machine candidate %q in %v", want, candidatesA)
	}
	if want := hashString("serial:abc"); !containsString(candidatesA, want) {
		t.Fatalf("missing serial candidate %q in %v", want, candidatesA)
	}
	if len(candidatesA) != 2 {
		t.Fatalf("expected deduplicated legacy+serial candidates, got %v", candidatesA)
	}
}

func TestDeriveHardwareIDsPrefersStableHardwareCandidateOverHostname(t *testing.T) {
	t.Parallel()

	hardwareA, _ := deriveHardwareIDs("linux", "arm64", "old-hostname", "machine-1", []string{"serial:abc"})
	hardwareB, _ := deriveHardwareIDs("linux", "arm64", "new-hostname", "machine-1", []string{"serial:abc"})
	hardwareC, _ := deriveHardwareIDs("linux", "arm64", "old-hostname", "machine-1", []string{"serial:def"})

	if hardwareA != hardwareB {
		t.Fatalf("same machine and stable hardware candidate should survive hostname changes: %q != %q", hardwareA, hardwareB)
	}
	if hardwareA == hardwareC {
		t.Fatalf("different stable hardware candidates should produce different primary hardware IDs: %q", hardwareA)
	}
}

func TestDeriveHardwareIDsIgnoresUnusableHardwareCandidates(t *testing.T) {
	t.Parallel()

	hardwareA, candidatesA := deriveHardwareIDs(
		"linux",
		"arm64",
		"host-a",
		"shared-machine-id",
		[]string{"serial:To be filled by O.E.M.", "mac:00:00:00:00:00:00"},
	)
	hardwareB, candidatesB := deriveHardwareIDs(
		"linux",
		"arm64",
		"host-b",
		"shared-machine-id",
		[]string{"serial:To be filled by O.E.M.", "mac:00:00:00:00:00:00"},
	)

	if hardwareA == hardwareB {
		t.Fatalf("unusable hardware candidates must not override hostname fallback: %q", hardwareA)
	}
	if containsString(candidatesA, hashString("serial:To be filled by O.E.M.")) ||
		containsString(candidatesB, hashString("serial:To be filled by O.E.M.")) {
		t.Fatalf("unusable serial should not be emitted as a candidate: %v %v", candidatesA, candidatesB)
	}
	if containsString(candidatesA, hashString("mac:00:00:00:00:00:00")) ||
		containsString(candidatesB, hashString("mac:00:00:00:00:00:00")) {
		t.Fatalf("unusable MAC should not be emitted as a candidate: %v %v", candidatesA, candidatesB)
	}
}

func TestNormalizeHardwareIDCandidateFiltersCommonDefaults(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"serial:None",
		"serial:Default string",
		"serial:To be filled by O.E.M.",
		"serial:Unknown",
		"serial:0000-0000",
		"mac:00:00:00:00:00:00",
		"mac:ff:ff:ff:ff:ff:ff",
		"mac:01:00:5e:00:00:fb",
	} {
		if got := normalizeHardwareIDCandidate(value); got != "" {
			t.Fatalf("normalizeHardwareIDCandidate(%q) = %q, want empty", value, got)
		}
	}

	if got := normalizeHardwareIDCandidate("serial:ABC-123"); got != "serial:ABC-123" {
		t.Fatalf("valid serial normalized to %q", got)
	}
	if got := normalizeHardwareIDCandidate("mac:AA:BB:CC:DD:EE:FF"); got != "mac:aa:bb:cc:dd:ee:ff" {
		t.Fatalf("valid MAC normalized to %q", got)
	}
}

func TestVirtualNetworkInterfaceName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"docker0", "veth1234", "br-abcd", "virbr0", "tailscale0", "utun3", "wg0"} {
		if !virtualNetworkInterfaceName(name) {
			t.Fatalf("virtualNetworkInterfaceName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"eth0", "en0", "wlan0"} {
		if virtualNetworkInterfaceName(name) {
			t.Fatalf("virtualNetworkInterfaceName(%q) = true, want false", name)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
