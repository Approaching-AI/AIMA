package runtime

import (
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jguan/aima/internal/k3s"
	"github.com/jguan/aima/internal/knowledge"
)

// StartupProgress holds the result of log-based progress detection.
type StartupProgress struct {
	Phase    string
	Message  string
	Progress int
}

// compiledPatterns caches compiled regexes keyed by the raw pattern string.
// Patterns come from static embedded YAML and are reused on every poll.
var (
	patternCache   = make(map[string]*regexp.Regexp)
	patternCacheMu sync.RWMutex
)

// getRegexp returns a compiled regexp for the given pattern, caching the result.
func getRegexp(pattern string) (*regexp.Regexp, error) {
	patternCacheMu.RLock()
	re, ok := patternCache[pattern]
	patternCacheMu.RUnlock()
	if ok {
		return re, nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	patternCacheMu.Lock()
	patternCache[pattern] = re
	patternCacheMu.Unlock()
	return re, nil
}

// DetectStartupProgress scans log text against engine-defined patterns
// and returns the highest-progress match found.
func DetectStartupProgress(logText string, patterns *knowledge.StartupLogPatterns) StartupProgress {
	if patterns == nil || len(patterns.Phases) == 0 {
		return StartupProgress{}
	}

	var best StartupProgress
	for _, phase := range patterns.Phases {
		re, err := getRegexp(phase.Pattern)
		if err != nil {
			slog.Debug("skip bad log pattern", "pattern", phase.Pattern, "error", err)
			continue
		}

		matches := re.FindAllStringSubmatch(logText, -1)
		if len(matches) == 0 {
			continue
		}

		progress := phase.Progress
		if phase.ProgressRegexGroup > 0 && phase.ProgressBase > 0 {
			// Use the last match for regex-based progress (e.g. CUDA graph capture %)
			lastMatch := matches[len(matches)-1]
			if phase.ProgressRegexGroup < len(lastMatch) {
				if pct, err := strconv.Atoi(lastMatch[phase.ProgressRegexGroup]); err == nil {
					rng := phase.ProgressRange
					if rng == 0 {
						rng = 100 - phase.ProgressBase
					}
					progress = phase.ProgressBase + (pct * rng / 100)
				}
			}
		}

		if progress > best.Progress {
			best = StartupProgress{
				Phase:    phase.Name,
				Progress: progress,
				Message:  formatPhaseName(phase.Name),
			}
		}
	}

	return best
}

// DetectStartupError checks log text against error patterns and returns
// the first matching error message, or empty string if no match.
func DetectStartupError(logText string, patterns *knowledge.StartupLogPatterns) string {
	if patterns == nil {
		return ""
	}
	for _, ep := range patterns.Errors {
		re, err := getRegexp(ep.Pattern)
		if err != nil {
			continue
		}
		if re.MatchString(logText) {
			return ep.Message
		}
	}
	return ""
}

// DetectK3SPhaseFromConditions maps pod conditions to pre-container startup phases.
func DetectK3SPhaseFromConditions(conditions []k3s.PodCondition, containerRunning bool) (phase string, progress int) {
	if containerRunning {
		return "initializing", 20
	}

	scheduled := false
	for _, c := range conditions {
		if c.Type == "PodScheduled" && c.Status == "True" {
			scheduled = true
		}
	}

	if !scheduled {
		return "scheduling", 2
	}

	// Scheduled but container not running → likely pulling image
	return "pulling_image", 10
}

// findEngineAsset looks up an engine asset by metadata.name.
func findEngineAsset(assets []knowledge.EngineAsset, name string) *knowledge.EngineAsset {
	if name == "" {
		return nil
	}
	for i := range assets {
		if strings.EqualFold(assets[i].Metadata.Name, name) {
			return &assets[i]
		}
	}
	return nil
}

// formatPhaseName converts "loading_weights" to "Loading weights..."
func formatPhaseName(name string) string {
	if name == "" {
		return ""
	}
	words := strings.Split(name, "_")
	words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	return strings.Join(words, " ") + "..."
}

// progressEntry tracks the last observed progress for a deployment.
type progressEntry struct {
	progress     int
	lastChangeAt time.Time
}

// ProgressTracker detects stalled deployments by monitoring progress changes.
// Each runtime embeds one instance. Thread-safe.
type ProgressTracker struct {
	mu      sync.Mutex
	entries map[string]*progressEntry
}

// NewProgressTracker creates a ready-to-use tracker.
func NewProgressTracker() *ProgressTracker {
	return &ProgressTracker{entries: make(map[string]*progressEntry)}
}

// Update checks whether a deployment's startup progress has stalled.
// Returns (stalled, lastProgressAt). stallThreshold = max(90s, min(EstimatedTotalS*0.4, 5min)).
func (t *ProgressTracker) Update(name string, currentProgress, estimatedTotalS int) (bool, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	entry, exists := t.entries[name]
	if !exists {
		t.entries[name] = &progressEntry{progress: currentProgress, lastChangeAt: now}
		return false, now
	}

	if currentProgress > entry.progress {
		entry.progress = currentProgress
		entry.lastChangeAt = now
		return false, now
	}

	// Calculate stall threshold
	threshold := 90 * time.Second
	if estimatedTotalS > 0 {
		dynamic := time.Duration(float64(estimatedTotalS)*0.4) * time.Second
		if dynamic > threshold {
			threshold = dynamic
		}
		if threshold > 5*time.Minute {
			threshold = 5 * time.Minute
		}
	}

	stalled := now.Sub(entry.lastChangeAt) > threshold
	return stalled, entry.lastChangeAt
}

// Remove cleans up tracking state for a deleted deployment.
func (t *ProgressTracker) Remove(name string) {
	t.mu.Lock()
	delete(t.entries, name)
	t.mu.Unlock()
}
