package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jguan/aima/internal/runtime"

	state "github.com/jguan/aima/internal"
)

// selectDefaultRuntime picks the best available runtime: K3S > Docker > Native.
func selectDefaultRuntime(k3sRt, dockerRt, nativeRt runtime.Runtime) runtime.Runtime {
	if k3sRt != nil {
		return k3sRt
	}
	if dockerRt != nil {
		return dockerRt
	}
	return nativeRt
}

// pickRuntimeForDeployment selects the runtime for a specific deployment based on
// the engine's runtime recommendation and available runtimes.
//
//	"native"    -> nativeRt
//	"docker"    -> dockerRt > nativeRt
//	"k3s"       -> k3sRt > error
//	"container" -> k3sRt > dockerRt (needs partition? k3s required)
//	"auto" / "" -> defaultRt
func pickRuntimeForDeployment(recommendation string, k3sRt, dockerRt, nativeRt, defaultRt runtime.Runtime, hasPartition bool) (runtime.Runtime, error) {
	switch recommendation {
	case "native":
		return nativeRt, nil
	case "docker":
		if dockerRt != nil {
			return dockerRt, nil
		}
		return nativeRt, nil
	case "k3s":
		if k3sRt != nil {
			return k3sRt, nil
		}
		return nil, fmt.Errorf("K3S runtime required but not available. Run 'aima init --k3s' to install")
	case "container":
		if hasPartition {
			if k3sRt != nil {
				return k3sRt, nil
			}
			return nil, fmt.Errorf("GPU partitioning requires K3S. Run 'aima init --k3s' to install")
		}
		if k3sRt != nil {
			return k3sRt, nil
		}
		if dockerRt != nil {
			return dockerRt, nil
		}
		return nativeRt, nil
	default: // "auto" or ""
		return defaultRt, nil
	}
}

const deletedDeploymentReuseGracePeriod = 15 * time.Second

type deletedDeploymentSnapshot map[string]time.Time

type matchedDeployment struct {
	Runtime runtime.Runtime
	Status  *runtime.DeploymentStatus
}

func normalizeDeletedDeploymentKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func deploymentModelKey(d *runtime.DeploymentStatus) string {
	if d == nil {
		return ""
	}
	if model := strings.TrimSpace(d.Model); model != "" {
		return model
	}
	return strings.TrimSpace(d.Labels["aima.dev/model"])
}

func loadDeletedDeploymentSnapshot(ctx context.Context, db *state.DB) (deletedDeploymentSnapshot, error) {
	if db == nil {
		return nil, nil
	}
	cutoff := time.Now().Add(-deletedDeploymentReuseGracePeriod)
	if err := db.PruneDeletedDeploymentsBefore(ctx, cutoff); err != nil {
		return nil, err
	}
	marks, err := db.ListDeletedDeploymentsSince(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	snapshot := make(deletedDeploymentSnapshot, len(marks))
	for key, ts := range marks {
		if norm := normalizeDeletedDeploymentKey(key); norm != "" {
			snapshot[norm] = ts
		}
	}
	return snapshot, nil
}

func markDeletedDeployments(ctx context.Context, db *state.DB, deletedAt time.Time, keys ...string) error {
	if db == nil {
		return nil
	}
	normalized := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		norm := normalizeDeletedDeploymentKey(key)
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		normalized = append(normalized, norm)
	}
	if len(normalized) == 0 {
		return nil
	}
	if err := db.PruneDeletedDeploymentsBefore(ctx, deletedAt.Add(-deletedDeploymentReuseGracePeriod)); err != nil {
		return err
	}
	return db.MarkDeletedDeployments(ctx, deletedAt, normalized...)
}

func (s deletedDeploymentSnapshot) suppress(d *runtime.DeploymentStatus) bool {
	if len(s) == 0 || d == nil {
		return false
	}
	deletedAt, ok := s.deletedAt(d.Name, deploymentModelKey(d))
	if !ok {
		return false
	}
	if d.StartTime != "" {
		if startedAt, err := time.Parse(time.RFC3339Nano, d.StartTime); err == nil {
			return !startedAt.After(deletedAt)
		}
		if startedAt, err := time.Parse(time.RFC3339, d.StartTime); err == nil {
			return !startedAt.After(deletedAt)
		}
	}
	if d.StartedAtUnix == 0 {
		return true
	}
	return d.StartedAtUnix <= deletedAt.Unix()
}

func (s deletedDeploymentSnapshot) deletedAt(keys ...string) (time.Time, bool) {
	var newest time.Time
	found := false
	for _, key := range keys {
		if ts, ok := s[normalizeDeletedDeploymentKey(key)]; ok {
			if !found || ts.After(newest) {
				newest = ts
				found = true
			}
		}
	}
	return newest, found
}

func loadDeletedDeploymentSuppressor(ctx context.Context, db *state.DB) func(*runtime.DeploymentStatus) bool {
	snapshot, err := loadDeletedDeploymentSnapshot(ctx, db)
	if err != nil {
		slog.Warn("load deleted deployment tombstones", "error", err)
		return nil
	}
	if len(snapshot) == 0 {
		return nil
	}
	return snapshot.suppress
}

// listAllRuntimes aggregates deployment lists from all available runtimes.
func listAllRuntimes(ctx context.Context, rts ...runtime.Runtime) []*runtime.DeploymentStatus {
	var all []*runtime.DeploymentStatus
	for _, r := range uniqueRuntimes(rts...) {
		if deps, err := r.List(ctx); err == nil {
			all = append(all, deps...)
		}
	}
	return all
}

func uniqueRuntimes(rts ...runtime.Runtime) []runtime.Runtime {
	seen := make(map[string]struct{}, len(rts))
	unique := make([]runtime.Runtime, 0, len(rts))
	for _, r := range rts {
		if r == nil {
			continue
		}
		key := fmt.Sprintf("%p", r)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, r)
	}
	return unique
}

func findMatchingDeployments(ctx context.Context, query string, suppress func(*runtime.DeploymentStatus) bool, rts ...runtime.Runtime) []matchedDeployment {
	matches := make([]matchedDeployment, 0)
	seen := make(map[string]struct{})
	for _, rt := range uniqueRuntimes(rts...) {
		if status, err := rt.Status(ctx, query); err == nil && status != nil && deploymentMatchesQuery(status, query) {
			if suppress == nil || !suppress(status) {
				key := fmt.Sprintf("%p|%s", rt, status.Name)
				if _, ok := seen[key]; !ok {
					seen[key] = struct{}{}
					matches = append(matches, matchedDeployment{Runtime: rt, Status: status})
				}
			}
		}
		statuses, err := rt.List(ctx)
		if err != nil {
			continue
		}
		for _, status := range statuses {
			if status == nil || !deploymentMatchesQuery(status, query) {
				continue
			}
			if suppress != nil && suppress(status) {
				continue
			}
			key := fmt.Sprintf("%p|%s", rt, status.Name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			matches = append(matches, matchedDeployment{Runtime: rt, Status: status})
		}
	}
	return matches
}

func summarizeMatchedDeployments(matches []matchedDeployment) string {
	if len(matches) == 0 {
		return ""
	}
	parts := make([]string, 0, len(matches))
	for _, match := range matches {
		if match.Runtime == nil || match.Status == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s/%s", match.Runtime.Name(), match.Status.Name))
	}
	return strings.Join(parts, ", ")
}

func findDeploymentStatus(ctx context.Context, query string, suppress func(*runtime.DeploymentStatus) bool, rts ...runtime.Runtime) (*runtime.DeploymentStatus, error) {
	seen := make(map[string]bool)
	for _, r := range rts {
		if r == nil {
			continue
		}
		name := fmt.Sprintf("%p", r)
		if seen[name] {
			continue
		}
		seen[name] = true
		if status, err := r.Status(ctx, query); err == nil {
			if suppress != nil && suppress(status) {
				continue
			}
			return status, nil
		}
	}
	for _, d := range listAllRuntimes(ctx, rts...) {
		if deploymentMatchesQuery(d, query) {
			if suppress != nil && suppress(d) {
				continue
			}
			return d, nil
		}
	}
	return nil, fmt.Errorf("deployment %q not found", query)
}

func findExistingDeployment(ctx context.Context, query string, rts ...runtime.Runtime) *runtime.DeploymentStatus {
	status, err := findDeploymentStatus(ctx, query, nil, rts...)
	if err != nil {
		return nil
	}
	return status
}

func filterDeploymentStatuses(statuses []*runtime.DeploymentStatus, suppress func(*runtime.DeploymentStatus) bool) []*runtime.DeploymentStatus {
	if suppress == nil || len(statuses) == 0 {
		return statuses
	}
	filtered := make([]*runtime.DeploymentStatus, 0, len(statuses))
	for _, status := range statuses {
		if suppress(status) {
			continue
		}
		filtered = append(filtered, status)
	}
	return filtered
}

func shouldReuseExistingDeployment(existing *runtime.DeploymentStatus, engineType, slot string, configOverrides map[string]any) bool {
	if existing == nil {
		return false
	}
	if !(existing.Ready || existing.Phase == "running" || existing.Phase == "starting") {
		return false
	}
	// Explicit deployment intents should reconcile the runtime instead of
	// short-circuiting on a same-name deployment that may have drifted.
	if strings.TrimSpace(engineType) != "" {
		return false
	}
	if strings.TrimSpace(slot) != "" {
		return false
	}
	return len(configOverrides) == 0
}
