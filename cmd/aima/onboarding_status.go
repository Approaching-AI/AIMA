package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/jguan/aima/internal/buildinfo"
	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/mcp"
)

// onboardingGPU is a single GPU entry in the onboarding status response.
type onboardingGPU struct {
	Name    string `json:"name"`
	VRAMMiB int    `json:"vram_mib"`
	Count   int    `json:"count"`
	Arch    string `json:"arch"`
}

// onboardingCPU describes the host CPU in the onboarding status response.
type onboardingCPU struct {
	Model string `json:"model"`
	Cores int    `json:"cores"`
}

// onboardingHardware aggregates hardware info for the onboarding status response.
type onboardingHardware struct {
	GPU          []onboardingGPU `json:"gpu"`
	CPU          onboardingCPU   `json:"cpu"`
	RAMMiB       int             `json:"ram_mib"`
	OS           string          `json:"os"`
	Arch         string          `json:"arch"`
	ProfileMatch string          `json:"profile_match"`
}

// onboardingStackStatus describes the stack readiness for onboarding.
type onboardingStackStatus struct {
	Docker                 string `json:"docker"`
	K3S                    string `json:"k3s"`
	NeedsInit              bool   `json:"needs_init"`
	InitTierRecommendation string `json:"init_tier_recommendation"`
	CanAutoInit            bool   `json:"can_auto_init"`
	InitBlockedReason      string `json:"init_blocked_reason,omitempty"`
}

// onboardingVersion holds version check results.
type onboardingVersion struct {
	Current             string `json:"current"`
	Latest              string `json:"latest,omitempty"`
	UpgradeAvailable    bool   `json:"upgrade_available"`
	ReleaseURL          string `json:"release_url,omitempty"`
	ReleaseNotesSummary string `json:"release_notes_summary,omitempty"`
}

// onboardingStatus is the full onboarding status response.
type onboardingStatus struct {
	OnboardingCompleted bool                  `json:"onboarding_completed"`
	Hardware            onboardingHardware    `json:"hardware"`
	StackStatus         onboardingStackStatus `json:"stack_status"`
	Version             onboardingVersion     `json:"version"`
}

// versionCheckCache is the JSON structure cached in SQLite for version check results.
type versionCheckCache struct {
	Timestamp           time.Time `json:"timestamp"`
	Latest              string    `json:"latest"`
	ReleaseURL          string    `json:"release_url"`
	ReleaseNotesSummary string    `json:"release_notes_summary"`
}

// githubRelease is the subset of fields we parse from the GitHub releases API.
type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
}

const (
	versionCheckCacheTTL   = 24 * time.Hour
	versionCheckTimeout    = 2 * time.Second
	githubReleasesEndpoint = "https://api.github.com/repos/Approaching-AI/aima/releases/latest"
)

var fetchLatestRelease = fetchLatestGitHubRelease
var detectOnboardingInitCapability = defaultOnboardingInitCapability

// buildOnboardingStatusJSON aggregates hardware, stack, version, and onboarding
// completion state into a single JSON response for the Web UI onboarding wizard.
func buildOnboardingStatusJSON(ctx context.Context, ac *appContext, deps *mcp.ToolDeps) (json.RawMessage, error) {
	var status onboardingStatus

	// (a) Onboarding completed flag
	if deps.GetConfig != nil {
		val, err := deps.GetConfig(ctx, "onboarding_completed")
		if err == nil && val == "true" {
			status.OnboardingCompleted = true
		}
	}

	// (b) Hardware info
	hw, hwErr := hal.Detect(ctx)
	if hwErr != nil {
		slog.Warn("onboarding status: hardware detection failed", "error", hwErr)
	}
	status.Hardware = buildOnboardingHardware(ctx, ac, hw)

	// (c) Stack status
	stackStatus, stackErr := buildOnboardingStackStatus(ctx, deps)
	if stackErr != nil {
		slog.Warn("onboarding status: stack status failed", "error", stackErr)
	}
	status.StackStatus = stackStatus

	// (d) Version check
	status.Version = buildOnboardingVersion(ctx, deps)

	return json.Marshal(status)
}

// buildOnboardingHardware extracts relevant hardware fields from hal.Detect output.
func buildOnboardingHardware(ctx context.Context, ac *appContext, hw *hal.HardwareInfo) onboardingHardware {
	result := onboardingHardware{
		GPU:  []onboardingGPU{},
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}

	if hw == nil {
		return result
	}

	if hw.GPU != nil {
		result.GPU = []onboardingGPU{{
			Name:    hw.GPU.Name,
			VRAMMiB: hw.GPU.VRAMMiB,
			Count:   hw.GPU.Count,
			Arch:    hw.GPU.Arch,
		}}
	}

	result.CPU = onboardingCPU{
		Model: hw.CPU.Model,
		Cores: hw.CPU.Cores,
	}
	result.RAMMiB = hw.RAM.TotalMiB

	// Match hardware profile via catalog
	if ac != nil && ac.cat != nil {
		result.ProfileMatch = detectHWProfile(ctx, ac.cat)
	}

	return result
}

// buildOnboardingStackStatus calls StackStatus and interprets component readiness.
func buildOnboardingStackStatus(ctx context.Context, deps *mcp.ToolDeps) (onboardingStackStatus, error) {
	result := onboardingStackStatus{
		Docker:                 "not_installed",
		K3S:                    "not_installed",
		NeedsInit:              false,
		InitTierRecommendation: "docker",
	}

	if deps.StackStatus == nil {
		return result, nil
	}

	raw, err := deps.StackStatus(ctx)
	if err != nil {
		return result, fmt.Errorf("stack status: %w", err)
	}

	// Parse the InitResult from stack status
	var initResult struct {
		Components []struct {
			Name    string `json:"name"`
			Ready   bool   `json:"ready"`
			Skipped bool   `json:"skipped"`
		} `json:"components"`
		AllReady bool `json:"all_ready"`
	}
	if err := json.Unmarshal(raw, &initResult); err != nil {
		return result, fmt.Errorf("parse stack status: %w", err)
	}

	for _, comp := range initResult.Components {
		name := strings.ToLower(comp.Name)
		switch {
		case strings.Contains(name, "docker"):
			if comp.Ready {
				result.Docker = "ready"
			} else if comp.Skipped {
				result.Docker = "skipped"
			}
		case strings.Contains(name, "k3s"):
			if comp.Ready {
				result.K3S = "ready"
			} else if comp.Skipped {
				result.K3S = "skipped"
			}
		}
	}

	// Determine needs_init: true if neither docker nor k3s is ready
	if result.Docker != "ready" && result.K3S != "ready" {
		result.NeedsInit = true
	}

	// Recommend k3s tier if K3S is partially installed (not "not_installed" but not "ready")
	if result.K3S != "not_installed" && result.K3S != "ready" {
		result.InitTierRecommendation = "k3s"
	}
	if result.NeedsInit {
		result.CanAutoInit, result.InitBlockedReason = detectOnboardingInitCapability(deps)
	}

	return result, nil
}

func defaultOnboardingInitCapability(deps *mcp.ToolDeps) (bool, string) {
	if deps == nil || deps.StackInit == nil {
		return false, "stack init is not available"
	}
	if runtime.GOOS != "linux" {
		return false, ""
	}
	if os.Geteuid() != 0 {
		return false, "automatic init requires AIMA to run with root privileges or a privileged helper"
	}
	return true, ""
}

// buildOnboardingVersion checks the current version against the latest GitHub release.
// Failures are silent; the response will contain only the current version.
func buildOnboardingVersion(ctx context.Context, deps *mcp.ToolDeps) onboardingVersion {
	result := onboardingVersion{
		Current: buildinfo.Version,
	}

	// Try to load cached version check
	if deps.GetConfig != nil {
		cached, ok := loadVersionCheckCache(ctx, deps)
		if ok {
			result.Latest = cached.Latest
			result.ReleaseURL = cached.ReleaseURL
			result.ReleaseNotesSummary = cached.ReleaseNotesSummary
			result.UpgradeAvailable = isNewerVersion(result.Current, result.Latest)
			return result
		}
	}

	// Fetch from GitHub
	release, err := fetchLatestRelease(ctx)
	if err != nil {
		slog.Debug("onboarding status: version check failed", "error", err)
		if deps.SetConfig != nil {
			saveVersionCheckCache(ctx, deps, versionCheckCache{Timestamp: time.Now()})
		}
		return result
	}

	result.Latest = release.TagName
	result.ReleaseURL = release.HTMLURL
	result.ReleaseNotesSummary = truncateReleaseNotes(release.Body, 200)
	result.UpgradeAvailable = isNewerVersion(result.Current, result.Latest)

	// Cache the result
	if deps.SetConfig != nil {
		saveVersionCheckCache(ctx, deps, versionCheckCache{
			Timestamp:           time.Now(),
			Latest:              result.Latest,
			ReleaseURL:          result.ReleaseURL,
			ReleaseNotesSummary: result.ReleaseNotesSummary,
		})
	}

	return result
}

// loadVersionCheckCache returns the cached version check if it exists and is still valid.
func loadVersionCheckCache(ctx context.Context, deps *mcp.ToolDeps) (versionCheckCache, bool) {
	raw, err := deps.GetConfig(ctx, "version_check_cache")
	if err != nil || raw == "" {
		return versionCheckCache{}, false
	}

	var cached versionCheckCache
	if err := json.Unmarshal([]byte(raw), &cached); err != nil {
		return versionCheckCache{}, false
	}

	if time.Since(cached.Timestamp) > versionCheckCacheTTL {
		return versionCheckCache{}, false
	}

	return cached, true
}

// saveVersionCheckCache stores the version check result in SQLite config.
func saveVersionCheckCache(ctx context.Context, deps *mcp.ToolDeps, cache versionCheckCache) {
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	if err := deps.SetConfig(ctx, "version_check_cache", string(data)); err != nil {
		slog.Debug("onboarding status: failed to cache version check", "error", err)
	}
}

// fetchLatestGitHubRelease makes an HTTP GET to the GitHub releases API.
func fetchLatestGitHubRelease(ctx context.Context) (*githubRelease, error) {
	ctx, cancel := context.WithTimeout(ctx, versionCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubReleasesEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "aima/"+buildinfo.Version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API returned %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1*1024*1024)).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}

	return &release, nil
}

// isNewerVersion returns true if latest is a newer semver than current.
// Both are expected to start with "v" (e.g., "v0.3.1").
// Returns false if either is unparseable or if versions are equal.
func isNewerVersion(current, latest string) bool {
	if latest == "" || current == "" {
		return false
	}
	cur := parseVersionParts(current)
	lat := parseVersionParts(latest)
	if cur == nil || lat == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if lat[i] > cur[i] {
			return true
		}
		if lat[i] < cur[i] {
			return false
		}
	}
	return false
}

// parseVersionParts extracts [major, minor, patch] from a version string.
// Returns nil if the string is not parseable as semver.
func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	// Strip any suffix like "-dev", "-rc1", etc.
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return nil
	}
	result := make([]int, 3)
	for i := 0; i < 3 && i < len(parts); i++ {
		n := 0
		for _, c := range parts[i] {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		result[i] = n
	}
	return result
}

// truncateReleaseNotes trims release notes to maxLen characters, adding "..." if truncated.
func truncateReleaseNotes(body string, maxLen int) string {
	body = strings.TrimSpace(body)
	if len(body) <= maxLen {
		return body
	}
	return body[:maxLen] + "..."
}
