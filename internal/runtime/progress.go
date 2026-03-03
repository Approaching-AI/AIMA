package runtime

import (
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"

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
		if assets[i].Metadata.Name == name {
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
