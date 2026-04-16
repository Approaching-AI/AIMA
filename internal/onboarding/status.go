package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jguan/aima/internal/buildinfo"
	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/mcp"
)

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

// Test hooks — overridable in unit tests, same pattern as the original
// cmd/aima implementation so existing tests keep working after migration.
// Exported so that cmd/aima HTTP handler tests can swap them out.
var FetchLatestRelease = fetchLatestGitHubRelease
var DetectOnboardingInitCapability = defaultOnboardingInitCapability

// BuildStatus aggregates hardware, stack, version, and onboarding-completion
// state into a single structured response consumed by the wizard UI, the
// onboarding MCP tool, and the `aima onboarding status` CLI command.
func BuildStatus(ctx context.Context, deps *Deps) (StatusResult, error) {
	var status StatusResult

	td := deps.ToolDeps

	// (a) Onboarding completed flag
	if td != nil && td.GetConfig != nil {
		val, err := td.GetConfig(ctx, "onboarding_completed")
		if err == nil && val == "true" {
			status.OnboardingCompleted = true
		}
	}

	// (b) Hardware info
	hw, hwErr := hal.Detect(ctx)
	if hwErr != nil {
		slog.Warn("onboarding status: hardware detection failed", "error", hwErr)
	}
	status.Hardware = buildHardware(ctx, deps, hw)

	// (c) Stack status
	stackStatus, stackErr := BuildStackStatus(ctx, deps)
	if stackErr != nil {
		slog.Warn("onboarding status: stack status failed", "error", stackErr)
	}
	status.StackStatus = stackStatus

	// (d) Version check
	status.Version = buildVersion(ctx, td)

	// (e) GPU occupancy — detect non-AIMA containers consuming GPU
	if hw != nil && hw.GPU != nil {
		occ := detectGPUOccupancy(ctx)
		if len(occ) > 0 {
			status.GPUOccupancy = occ
		}
	}

	return status, nil
}

// buildHardware extracts relevant hardware fields from hal.Detect output.
func buildHardware(ctx context.Context, deps *Deps, hw *hal.HardwareInfo) Hardware {
	result := Hardware{
		GPU:  []GPU{},
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}

	if hw == nil {
		return result
	}

	if hw.GPU != nil {
		result.GPU = []GPU{{
			Name:    hw.GPU.Name,
			VRAMMiB: hw.GPU.VRAMMiB,
			Count:   hw.GPU.Count,
			Arch:    hw.GPU.Arch,
		}}
	}

	result.CPU = CPU{
		Model: hw.CPU.Model,
		Cores: hw.CPU.Cores,
	}
	result.RAMMiB = hw.RAM.TotalMiB

	// Match hardware profile via catalog — delegated to caller-injected helper
	// so we don't need to import the catalog matcher directly (and to keep
	// parity with legacy cmd/aima behavior which only returns a match when a
	// catalog is loaded).
	if deps != nil && deps.DetectHWProfile != nil {
		result.ProfileMatch = deps.DetectHWProfile(ctx)
	}

	return result
}

// BuildStackStatus calls StackStatus and interprets component readiness.
func BuildStackStatus(ctx context.Context, deps *Deps) (StackStatusInfo, error) {
	result := StackStatusInfo{
		Docker:                 "not_installed",
		K3S:                    "not_installed",
		NeedsInit:              false,
		InitTierRecommendation: "docker",
	}

	if deps == nil || deps.ToolDeps == nil || deps.ToolDeps.StackStatus == nil {
		return result, nil
	}

	raw, err := deps.ToolDeps.StackStatus(ctx)
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
		result.CanAutoInit, result.InitBlockedReason = DetectOnboardingInitCapability(deps.ToolDeps)
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

// buildVersion checks the current version against the latest GitHub release.
// Failures are silent; the response will contain only the current version.
//
// INV-8 (Offline-first): outbound HTTP to GitHub is OFF by default. Callers
// must opt in via SQLite config key `version.check_upstream=true`. Local
// SQLite cache reads always work (no network), so a previously-cached result
// is still surfaced even when outbound fetch is disabled.
func buildVersion(ctx context.Context, deps *mcp.ToolDeps) VersionInfo {
	result := VersionInfo{
		Current: buildinfo.Version,
	}

	// Try to load cached version check (always allowed — reading local SQLite
	// is not network traffic).
	if deps != nil && deps.GetConfig != nil {
		cached, ok := loadVersionCheckCache(ctx, deps)
		if ok {
			result.Latest = cached.Latest
			result.ReleaseURL = cached.ReleaseURL
			result.ReleaseNotesSummary = cached.ReleaseNotesSummary
			result.UpgradeAvailable = isNewerVersion(result.Current, result.Latest)
			return result
		}
	}

	// INV-8 gate: only fetch from GitHub when explicitly opted-in.
	if !versionCheckUpstreamEnabled(ctx, deps) {
		return result
	}

	// Fetch from GitHub
	release, err := FetchLatestRelease(ctx)
	if err != nil {
		slog.Debug("onboarding status: version check failed", "error", err)
		if deps != nil && deps.SetConfig != nil {
			saveVersionCheckCache(ctx, deps, versionCheckCache{Timestamp: time.Now()})
		}
		return result
	}

	result.Latest = release.TagName
	result.ReleaseURL = release.HTMLURL
	result.ReleaseNotesSummary = truncateReleaseNotes(release.Body, 200)
	result.UpgradeAvailable = isNewerVersion(result.Current, result.Latest)

	// Cache the result
	if deps != nil && deps.SetConfig != nil {
		saveVersionCheckCache(ctx, deps, versionCheckCache{
			Timestamp:           time.Now(),
			Latest:              result.Latest,
			ReleaseURL:          result.ReleaseURL,
			ReleaseNotesSummary: result.ReleaseNotesSummary,
		})
	}

	return result
}

// BuildVersion is the exported wrapper around buildVersion for callers that
// want to query the version-check pipeline without fetching the full status.
func BuildVersion(ctx context.Context, deps *Deps) VersionInfo {
	if deps == nil {
		return buildVersion(ctx, nil)
	}
	return buildVersion(ctx, deps.ToolDeps)
}

// versionCheckUpstreamEnabled returns true only if the caller explicitly
// opted in to outbound version checks via SQLite config. Default: false
// (INV-8 offline-first).
func versionCheckUpstreamEnabled(ctx context.Context, deps *mcp.ToolDeps) bool {
	if deps == nil || deps.GetConfig == nil {
		return false
	}
	val, err := deps.GetConfig(ctx, "version.check_upstream")
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
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

// detectGPUOccupancy finds running Docker containers that use GPU resources but
// were not deployed by AIMA (i.e. missing the aima.dev/engine label). This lets
// the onboarding wizard warn the user about pre-existing GPU consumers and offer
// to stop them before attempting a deployment.
func detectGPUOccupancy(ctx context.Context) []GPUProcess {
	detectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// List all running containers that request GPU (--gpus or NVIDIA runtime).
	// We exclude containers that have the aima.dev/engine label (managed by AIMA).
	out, err := exec.CommandContext(detectCtx, "docker", "ps",
		"--format", "{{json .}}",
		"--filter", "status=running",
	).CombinedOutput()
	if err != nil {
		return nil
	}

	type dockerEntry struct {
		Names  string `json:"Names"`
		Image  string `json:"Image"`
		Labels string `json:"Labels"`
	}

	var procs []GPUProcess
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var e dockerEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		// Skip AIMA-managed containers
		if strings.Contains(e.Labels, "aima.dev/engine") {
			continue
		}
		// Check if the container has GPU access via docker inspect
		if !containerUsesGPU(detectCtx, e.Names) {
			continue
		}
		gpuMem := containerGPUMemMiB(detectCtx, e.Names)
		procs = append(procs, GPUProcess{
			Name:      e.Names,
			Type:      "container",
			Image:     e.Image,
			GPUMemMiB: gpuMem,
		})
	}
	return procs
}

// containerUsesGPU checks whether a container has GPU device requests or uses
// the nvidia runtime.
func containerUsesGPU(ctx context.Context, name string) bool {
	out, err := exec.CommandContext(ctx, "docker", "inspect", name,
		"--format", "{{json .HostConfig.DeviceRequests}} {{.HostConfig.Runtime}}",
	).CombinedOutput()
	if err != nil {
		return false
	}
	s := string(out)
	return strings.Contains(s, "nvidia") || strings.Contains(s, "gpu")
}

// containerGPUMemMiB tries to estimate GPU memory used by a container by
// looking at nvidia-smi process entries. Returns 0 if unavailable.
func containerGPUMemMiB(ctx context.Context, name string) int {
	// Get the container's main PID
	pidOut, err := exec.CommandContext(ctx, "docker", "inspect", name,
		"--format", "{{.State.Pid}}",
	).CombinedOutput()
	if err != nil {
		return 0
	}
	containerPID := strings.TrimSpace(string(pidOut))
	if containerPID == "" || containerPID == "0" {
		return 0
	}

	// Query nvidia-smi for GPU memory by PID. The container's processes are
	// children of the container PID, so we search all nvidia-smi pids and
	// match by checking if they belong to this container's cgroup.
	smiOut, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-compute-apps=pid,used_gpu_memory",
		"--format=csv,noheader,nounits",
	).CombinedOutput()
	if err != nil {
		return 0
	}

	totalMiB := 0
	for _, line := range strings.Split(strings.TrimSpace(string(smiOut)), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ",", 2)
		if len(parts) < 2 {
			continue
		}
		pid := strings.TrimSpace(parts[0])
		memStr := strings.TrimSpace(parts[1])

		// Check if this PID belongs to the container
		cgroupPath := fmt.Sprintf("/proc/%s/cgroup", pid)
		cgroupData, err := os.ReadFile(cgroupPath)
		if err != nil {
			// Fallback: check if PID is a child of the container PID
			if isChildOfPID(ctx, pid, containerPID) {
				mem, _ := strconv.Atoi(memStr)
				totalMiB += mem
			}
			continue
		}
		if strings.Contains(string(cgroupData), name) {
			mem, _ := strconv.Atoi(memStr)
			totalMiB += mem
		}
	}
	return totalMiB
}

// isChildOfPID checks if childPID is a descendant of parentPID by walking /proc.
func isChildOfPID(ctx context.Context, childPID, parentPID string) bool {
	current := childPID
	for i := 0; i < 10; i++ { // max depth
		statPath := fmt.Sprintf("/proc/%s/stat", current)
		data, err := os.ReadFile(statPath)
		if err != nil {
			return false
		}
		// /proc/PID/stat format: pid (comm) state ppid ...
		fields := strings.Fields(string(data))
		if len(fields) < 4 {
			return false
		}
		// Find ppid — it's the first numeric field after the closing paren of comm
		ppidIdx := -1
		for j, f := range fields {
			if strings.HasSuffix(f, ")") {
				ppidIdx = j + 2 // state is j+1, ppid is j+2
				break
			}
		}
		if ppidIdx < 0 || ppidIdx >= len(fields) {
			return false
		}
		ppid := fields[ppidIdx]
		if ppid == parentPID {
			return true
		}
		if ppid == "1" || ppid == "0" {
			return false
		}
		current = ppid
	}
	return false
}
