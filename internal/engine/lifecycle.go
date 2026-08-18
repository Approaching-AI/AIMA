package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var lifecyclePathComponent = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// EnsureRequest is the caller's desired Engine inventory state.
type EnsureRequest struct {
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Platform string `json:"platform"`
	Apply    bool   `json:"apply"`
}

// InstalledEngine is the read-only inventory projection used by the planner.
type InstalledEngine struct {
	ID                 string
	AssetName          string
	Version            string
	CatalogVersion     string
	DetectedVersion    string
	VersionMatch       string
	Platform           string
	RuntimeType        string
	Origin             string
	Source             string
	Available          bool
	Active             bool
	VerificationStatus string
}

// EnsureCandidate describes the concrete Catalog artifact selected for an
// Engine ensure operation.
type EnsureCandidate struct {
	ID                   string
	AssetName            string
	Version              string
	CompatibleVersions   []string
	RuntimeType          string
	Origin               string
	Source               string
	NetworkRequired      bool
	VerificationEvidence string
	BlockReason          string
}

// EnsurePlan is a stable, side-effect-free description of an Engine ensure.
type EnsurePlan struct {
	AssetName            string   `json:"asset_name"`
	RequestedVersion     string   `json:"requested_version"`
	CurrentEngineID      string   `json:"current_engine_id,omitempty"`
	CandidateEngineID    string   `json:"candidate_engine_id,omitempty"`
	Action               string   `json:"action"` // reuse, install, upgrade, activate, noop
	Origin               string   `json:"origin,omitempty"`
	Source               string   `json:"source,omitempty"`
	NetworkRequired      bool     `json:"network_required"`
	VerificationEvidence string   `json:"verification_evidence,omitempty"`
	Blocked              bool     `json:"blocked"`
	BlockReason          string   `json:"block_reason,omitempty"`
	AffectedDeployments  []string `json:"affected_deployments"`
}

// BuildEnsurePlan deterministically chooses local reuse or a verified Catalog
// candidate from immutable inputs. It performs no filesystem, network, or
// inventory operation.
func BuildEnsurePlan(req EnsureRequest, candidate EnsureCandidate, installed []InstalledEngine, affectedDeployments []string) EnsurePlan {
	assetName := strings.TrimSpace(candidate.AssetName)
	if assetName == "" {
		assetName = strings.TrimSpace(req.Name)
	}
	requestedVersion := strings.TrimSpace(req.Version)
	if requestedVersion == "" {
		requestedVersion = strings.TrimSpace(candidate.Version)
	}
	plan := EnsurePlan{
		AssetName:            assetName,
		RequestedVersion:     requestedVersion,
		CandidateEngineID:    strings.TrimSpace(candidate.ID),
		Origin:               strings.TrimSpace(candidate.Origin),
		Source:               strings.TrimSpace(candidate.Source),
		NetworkRequired:      candidate.NetworkRequired,
		VerificationEvidence: strings.TrimSpace(candidate.VerificationEvidence),
		AffectedDeployments:  stableUniqueStrings(affectedDeployments),
	}

	if assetName == "" {
		return blockEnsurePlan(plan, "engine asset name is required")
	}
	if requestedVersion == "" {
		return blockEnsurePlan(plan, "requested engine version is required")
	}

	versions := append([]InstalledEngine(nil), installed...)
	sort.SliceStable(versions, func(i, j int) bool {
		left, right := installedPreference(versions[i], candidate.RuntimeType), installedPreference(versions[j], candidate.RuntimeType)
		if left != right {
			return left > right
		}
		return versions[i].ID < versions[j].ID
	})

	var current *InstalledEngine
	var exact *InstalledEngine
	var compatible *InstalledEngine
	for i := range versions {
		entry := &versions[i]
		if !entry.Available || !sameEnsureGroup(*entry, assetName, req.Platform) {
			continue
		}
		if entry.Active && current == nil {
			current = entry
		}
		if exact == nil && strings.TrimSpace(entry.Version) == requestedVersion {
			exact = entry
			continue
		}
		if compatible == nil && installedVersionMatches(*entry, requestedVersion, candidate.CompatibleVersions) {
			compatible = entry
		}
	}
	if exact == nil {
		exact = compatible
	}
	if current != nil {
		plan.CurrentEngineID = current.ID
	}
	if exact != nil {
		plan.Action = "reuse"
		plan.CandidateEngineID = exact.ID
		plan.Origin = exact.Origin
		plan.Source = exact.Source
		plan.NetworkRequired = false
		plan.VerificationEvidence = ""
		if !exact.Active && exact.VerificationStatus != "verified" {
			return blockEnsurePlan(plan, fmt.Sprintf("installed engine %s is not verified", exact.ID))
		}
		return plan
	}
	if current == nil {
		plan.Action = "install"
	} else {
		plan.Action = "upgrade"
	}

	if reason := strings.TrimSpace(candidate.BlockReason); reason != "" {
		return blockEnsurePlan(plan, reason)
	}
	if candidate.Version != "" && strings.TrimSpace(candidate.Version) != requestedVersion {
		return blockEnsurePlan(plan, fmt.Sprintf("requested version %s is not available from the selected Catalog asset", requestedVersion))
	}
	if plan.Source == "" {
		return blockEnsurePlan(plan, "no install source is available")
	}
	if plan.VerificationEvidence == "" {
		return blockEnsurePlan(plan, "managed install requires checksum or digest evidence")
	}
	return plan
}

func installedPreference(entry InstalledEngine, candidateRuntime string) int {
	score := 0
	if entry.Active {
		score += 100
	}
	if entry.VerificationStatus == "verified" {
		score += 20
	}
	if entry.Origin == "preinstalled" {
		score += 10
	}
	if entry.RuntimeType == candidateRuntime {
		score += 5
	}
	return score
}

func sameEnsureGroup(entry InstalledEngine, assetName, platform string) bool {
	if !strings.EqualFold(strings.TrimSpace(entry.AssetName), strings.TrimSpace(assetName)) {
		return false
	}
	platform = strings.TrimSpace(platform)
	return platform == "" || strings.EqualFold(strings.TrimSpace(entry.Platform), platform)
}

func installedVersionMatches(entry InstalledEngine, requestedVersion string, compatibleVersions []string) bool {
	installedVersion := strings.TrimSpace(entry.Version)
	if strings.TrimSpace(entry.VersionMatch) != "compatible" ||
		CompareDetectedVersion(entry.CatalogVersion, requestedVersion, nil) != "exact" {
		return false
	}
	detectedVersion := strings.TrimSpace(entry.DetectedVersion)
	if detectedVersion == "" {
		detectedVersion = installedVersion
	}
	return CompareDetectedVersion(detectedVersion, requestedVersion, compatibleVersions) == "compatible"
}

func blockEnsurePlan(plan EnsurePlan, reason string) EnsurePlan {
	plan.Blocked = true
	plan.BlockReason = reason
	if plan.Action == "" {
		plan.Action = "install"
	}
	return plan
}

func stableUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// ManagedNativeEngineID returns the same stable identity used by managed
// native discovery for a binary relative to the platform dist root.
func ManagedNativeEngineID(asset, version, relativePath string) string {
	return binaryHash("managed-" + strings.TrimSpace(asset) + "-" + strings.TrimSpace(version) + "-" + filepath.ToSlash(filepath.Clean(relativePath)))
}

// ManagedVersionDir returns the immutable directory for one managed native
// engine version under the AIMA data directory.
func ManagedVersionDir(dataDir, platform, asset, version string) (string, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return "", fmt.Errorf("data directory is required")
	}
	platform, err := safeLifecycleComponent("platform", platform)
	if err != nil {
		return "", err
	}
	asset, err = safeLifecycleComponent("asset", asset)
	if err != nil {
		return "", err
	}
	version, err = safeLifecycleComponent("version", version)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(dataDir), "dist", platform, asset, version), nil
}

// NewStagingDir creates a private native-engine staging directory on the same
// data filesystem used for final managed versions.
func NewStagingDir(dataDir, asset string) (string, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return "", fmt.Errorf("data directory is required")
	}
	asset, err := safeLifecycleComponent("asset", asset)
	if err != nil {
		return "", err
	}
	root := filepath.Join(filepath.Clean(dataDir), "staging", "engines")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create engine staging root %s: %w", root, err)
	}
	dir, err := os.MkdirTemp(root, asset+"-*")
	if err != nil {
		return "", fmt.Errorf("create engine staging directory: %w", err)
	}
	return dir, nil
}

// PromoteStagedDir atomically renames a verified staging directory into its
// immutable destination. An existing destination is reused only when every
// entry has the same type, permissions, link target, and file content.
func PromoteStagedDir(stagedDir, destDir string) error {
	stagedDir = filepath.Clean(strings.TrimSpace(stagedDir))
	destDir = filepath.Clean(strings.TrimSpace(destDir))
	if stagedDir == "." || destDir == "." {
		return fmt.Errorf("staging and destination directories are required")
	}
	if stagedDir == destDir {
		return fmt.Errorf("staging and destination directories must differ")
	}

	stagedInfo, err := os.Lstat(stagedDir)
	if err != nil {
		return fmt.Errorf("inspect staged engine directory %s: %w", stagedDir, err)
	}
	if !stagedInfo.IsDir() {
		return fmt.Errorf("staged engine path %s is not a directory", stagedDir)
	}

	if destInfo, err := os.Lstat(destDir); err == nil {
		if !destInfo.IsDir() {
			return fmt.Errorf("managed engine destination %s already exists and is not a directory", destDir)
		}
		equal, compareErr := equalLifecycleDirs(stagedDir, destDir)
		if compareErr != nil {
			return fmt.Errorf("compare staged and existing engine directories: %w", compareErr)
		}
		if equal {
			return nil
		}
		return fmt.Errorf("managed engine destination %s already exists with different contents", destDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect managed engine destination %s: %w", destDir, err)
	}

	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return fmt.Errorf("create managed engine parent %s: %w", filepath.Dir(destDir), err)
	}
	if err := os.Rename(stagedDir, destDir); err != nil {
		// A concurrent promoter may have won after the existence check. Reuse
		// its result only when it carries exactly the same evidence.
		if destInfo, statErr := os.Lstat(destDir); statErr == nil && destInfo.IsDir() {
			equal, compareErr := equalLifecycleDirs(stagedDir, destDir)
			if compareErr == nil && equal {
				return nil
			}
			if compareErr != nil {
				return fmt.Errorf("promote staged engine to %s: %w (compare concurrent destination: %v)", destDir, err, compareErr)
			}
			return fmt.Errorf("promote staged engine to %s: destination appeared with different contents", destDir)
		}
		return fmt.Errorf("promote staged engine from %s to %s: %w", stagedDir, destDir, err)
	}
	return nil
}

func safeLifecycleComponent(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || !lifecyclePathComponent.MatchString(value) {
		return "", fmt.Errorf("invalid %s path component %q", label, value)
	}
	return value, nil
}

func equalLifecycleDirs(left, right string) (bool, error) {
	leftEntries, err := os.ReadDir(left)
	if err != nil {
		return false, err
	}
	rightEntries, err := os.ReadDir(right)
	if err != nil {
		return false, err
	}
	if len(leftEntries) != len(rightEntries) {
		return false, nil
	}
	for i := range leftEntries {
		if leftEntries[i].Name() != rightEntries[i].Name() {
			return false, nil
		}
		leftPath := filepath.Join(left, leftEntries[i].Name())
		rightPath := filepath.Join(right, rightEntries[i].Name())
		leftInfo, err := os.Lstat(leftPath)
		if err != nil {
			return false, err
		}
		rightInfo, err := os.Lstat(rightPath)
		if err != nil {
			return false, err
		}
		if leftInfo.Mode().Type() != rightInfo.Mode().Type() || leftInfo.Mode().Perm() != rightInfo.Mode().Perm() {
			return false, nil
		}
		switch {
		case leftInfo.IsDir():
			equal, err := equalLifecycleDirs(leftPath, rightPath)
			if err != nil || !equal {
				return equal, err
			}
		case leftInfo.Mode().IsRegular():
			if leftInfo.Size() != rightInfo.Size() {
				return false, nil
			}
			leftDigest, err := fileSHA256(leftPath)
			if err != nil {
				return false, err
			}
			rightDigest, err := fileSHA256(rightPath)
			if err != nil {
				return false, err
			}
			if leftDigest != rightDigest {
				return false, nil
			}
		case leftInfo.Mode()&os.ModeSymlink != 0:
			leftTarget, err := os.Readlink(leftPath)
			if err != nil {
				return false, err
			}
			rightTarget, err := os.Readlink(rightPath)
			if err != nil {
				return false, err
			}
			if leftTarget != rightTarget {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	return true, nil
}
