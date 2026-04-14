package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jguan/aima/internal/mcp"
)

func TestBuildOnboardingStatusJSON_NoConfig(t *testing.T) {
	origFetchLatestRelease := fetchLatestRelease
	fetchLatestRelease = func(ctx context.Context) (*githubRelease, error) {
		return nil, fmt.Errorf("offline")
	}
	defer func() {
		fetchLatestRelease = origFetchLatestRelease
	}()

	// When GetConfig returns an error (no config), onboarding_completed should be false.
	deps := &mcp.ToolDeps{
		GetConfig: func(ctx context.Context, key string) (string, error) {
			return "", nil // empty value, not "true"
		},
		// StackStatus is nil — will be handled gracefully
	}
	ac := &appContext{} // no catalog, no db

	data, err := buildOnboardingStatusJSON(context.Background(), ac, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var status onboardingStatus
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("failed to unmarshal status: %v", err)
	}

	if status.OnboardingCompleted {
		t.Error("expected onboarding_completed to be false when config is empty")
	}

	// Hardware should have empty GPU list, not nil
	if status.Hardware.GPU == nil {
		t.Error("expected hardware.gpu to be non-nil empty slice")
	}
}

func TestBuildOnboardingVersion_CachesFailedLookup(t *testing.T) {
	origFetchLatestRelease := fetchLatestRelease
	defer func() {
		fetchLatestRelease = origFetchLatestRelease
	}()

	fetchCalls := 0
	fetchLatestRelease = func(ctx context.Context) (*githubRelease, error) {
		fetchCalls++
		return nil, fmt.Errorf("offline")
	}

	var cached string
	deps := &mcp.ToolDeps{
		GetConfig: func(ctx context.Context, key string) (string, error) {
			if key == "version_check_cache" {
				return cached, nil
			}
			return "", nil
		},
		SetConfig: func(ctx context.Context, key, value string) error {
			if key == "version_check_cache" {
				cached = value
			}
			return nil
		},
	}

	first := buildOnboardingVersion(context.Background(), deps)
	second := buildOnboardingVersion(context.Background(), deps)

	if first.Latest != "" || second.Latest != "" {
		t.Fatalf("expected empty latest version when cached failure is used, got %q / %q", first.Latest, second.Latest)
	}
	if cached == "" {
		t.Fatal("expected failed version lookup to populate cache")
	}
	if fetchCalls != 1 {
		t.Fatalf("fetchLatestRelease call count = %d, want 1", fetchCalls)
	}
}

func TestBuildOnboardingStackStatus_ExposesAutoInitCapability(t *testing.T) {
	origDetect := detectOnboardingInitCapability
	detectOnboardingInitCapability = func(deps *mcp.ToolDeps) (bool, string) {
		return true, ""
	}
	defer func() {
		detectOnboardingInitCapability = origDetect
	}()

	deps := &mcp.ToolDeps{
		StackInit: func(ctx context.Context, tier string, allowDownload bool) (json.RawMessage, error) {
			return nil, nil
		},
		StackStatus: func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"components":[{"name":"docker","ready":false},{"name":"k3s","ready":false}],"all_ready":false}`), nil
		},
	}

	status, err := buildOnboardingStackStatus(context.Background(), deps)
	if err != nil {
		t.Fatalf("buildOnboardingStackStatus: %v", err)
	}
	if !status.NeedsInit {
		t.Fatal("expected NeedsInit=true")
	}
	if !status.CanAutoInit {
		t.Fatal("expected CanAutoInit=true")
	}
}

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"newer patch", "v0.3.1", "v0.3.2", true},
		{"older patch", "v0.3.2", "v0.3.1", false},
		{"equal version", "v0.3.1", "v0.3.1", false},
		{"newer major", "v0.3.1", "v1.0.0", true},
		{"empty current", "", "v0.3.2", false},
		{"empty latest", "v0.3.1", "", false},
		{"two-part version current", "v0.3", "v0.3.1", true},
		{"newer minor", "v0.2.9", "v0.3.0", true},
		{"older major", "v1.0.0", "v0.9.9", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNewerVersion(tt.current, tt.latest)
			if got != tt.want {
				t.Errorf("isNewerVersion(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestParseVersionParts(t *testing.T) {
	tests := []struct {
		name string
		v    string
		want []int
	}{
		{"full semver", "v0.3.1", []int{0, 3, 1}},
		{"no v prefix", "0.3.1", []int{0, 3, 1}},
		{"two parts", "v0.3", []int{0, 3, 0}},
		{"single part", "v3", nil},
		{"with prerelease suffix", "v1.2.3-rc1", []int{1, 2, 3}},
		{"with build metadata", "v1.2.3+build42", []int{1, 2, 3}},
		{"empty string", "", nil},
		{"major only", "v", nil},
		{"zero version", "v0.0.0", []int{0, 0, 0}},
		{"large numbers", "v10.20.30", []int{10, 20, 30}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVersionParts(tt.v)
			if tt.want == nil {
				if got != nil {
					t.Errorf("parseVersionParts(%q) = %v, want nil", tt.v, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("parseVersionParts(%q) = nil, want %v", tt.v, tt.want)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseVersionParts(%q) length = %d, want %d", tt.v, len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("parseVersionParts(%q)[%d] = %d, want %d", tt.v, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestTruncateReleaseNotes(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		maxLen int
		want   string
	}{
		{"short body", "hello", 10, "hello"},
		{"exact length", "12345", 5, "12345"},
		{"over limit", "hello world", 5, "hello..."},
		{"empty", "", 10, ""},
		{"whitespace trimmed", "  hello  ", 10, "hello"},
		{"truncate with whitespace", "  hello world  ", 5, "hello..."},
		{"zero maxLen", "hello", 0, "..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateReleaseNotes(tt.body, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateReleaseNotes(%q, %d) = %q, want %q", tt.body, tt.maxLen, got, tt.want)
			}
		})
	}
}
