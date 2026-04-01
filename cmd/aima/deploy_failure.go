package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type deploymentFailureDetails struct {
	Message        string
	StartupMessage string
	ErrorLines     string
}

func summarizeDeploymentFailure(message, startupMessage, errorLines string) string {
	for _, candidate := range []string{message, startupMessage} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" && !isGenericDeploymentFailure(trimmed) {
			return trimmed
		}
	}
	if detail := summarizeErrorLines(errorLines); detail != "" {
		return detail
	}
	for _, candidate := range []string{message, startupMessage} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return "unknown startup failure"
}

func refineDeploymentFailure(
	ctx context.Context,
	deployName string,
	initial deploymentFailureDetails,
	statusFn func(context.Context, string) (json.RawMessage, error),
	logsFn func(context.Context, string, int) (string, error),
) string {
	best := summarizeDeploymentFailure(initial.Message, initial.StartupMessage, initial.ErrorLines)
	if !shouldRefineDeploymentFailure(best) {
		return best
	}

	current := initial
	tryRefine := func() bool {
		if statusFn != nil {
			if statusData, err := statusFn(ctx, deployName); err == nil {
				if refreshed, err := parseDeploymentFailureDetails(statusData); err == nil {
					current = mergeDeploymentFailureDetails(current, refreshed)
					for _, candidate := range []string{
						strings.TrimSpace(refreshed.Message),
						strings.TrimSpace(refreshed.StartupMessage),
						summarizeErrorLines(refreshed.ErrorLines),
						summarizeErrorLines(current.ErrorLines),
						summarizeDeploymentFailure(current.Message, current.StartupMessage, current.ErrorLines),
					} {
						if moreSpecificFailure(candidate, best) {
							best = candidate
						}
					}
				}
			}
		}
		if logsFn != nil {
			if logs, err := logsFn(ctx, deployName, 120); err == nil {
				if candidate := summarizeErrorLines(logs); moreSpecificFailure(candidate, best) {
					best = candidate
				}
			}
		}
		return !shouldRefineDeploymentFailure(best)
	}

	if tryRefine() {
		return best
	}

	select {
	case <-ctx.Done():
		return best
	case <-time.After(500 * time.Millisecond):
	}
	tryRefine()
	return best
}

func parseDeploymentFailureDetails(statusData json.RawMessage) (deploymentFailureDetails, error) {
	var details deploymentFailureDetails
	if err := json.Unmarshal(statusData, &struct {
		Message        *string `json:"message"`
		StartupMessage *string `json:"startup_message"`
		ErrorLines     *string `json:"error_lines"`
	}{
		Message:        &details.Message,
		StartupMessage: &details.StartupMessage,
		ErrorLines:     &details.ErrorLines,
	}); err != nil {
		return deploymentFailureDetails{}, err
	}
	return details, nil
}

func mergeDeploymentFailureDetails(base, overlay deploymentFailureDetails) deploymentFailureDetails {
	if moreSpecificFailure(overlay.Message, base.Message) || strings.TrimSpace(base.Message) == "" {
		base.Message = overlay.Message
	}
	if moreSpecificFailure(overlay.StartupMessage, base.StartupMessage) || strings.TrimSpace(base.StartupMessage) == "" {
		base.StartupMessage = overlay.StartupMessage
	}
	if moreSpecificFailure(summarizeErrorLines(overlay.ErrorLines), summarizeErrorLines(base.ErrorLines)) || strings.TrimSpace(base.ErrorLines) == "" {
		base.ErrorLines = overlay.ErrorLines
	}
	return base
}

func shouldRefineDeploymentFailure(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" || isGenericDeploymentFailure(msg) {
		return true
	}
	return strings.Contains(lower, "see root cause above") ||
		strings.Contains(lower, "failed core proc")
}

func moreSpecificFailure(candidate, best string) bool {
	return diagnosticLineScore(candidate) > diagnosticLineScore(best)
}

func isGenericDeploymentFailure(msg string) bool {
	switch strings.ToLower(strings.TrimSpace(msg)) {
	case "process exited before readiness", "unknown startup failure", "deployment metadata is stale; port is in use by another process":
		return true
	default:
		return false
	}
}

func summarizeErrorLines(errorLines string) string {
	lines := strings.Split(errorLines, "\n")
	bestLine := ""
	bestScore := 0
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		score := diagnosticLineScore(trimmed)
		if score > bestScore {
			bestLine = trimmed
			bestScore = score
		}
	}
	return bestLine
}

func diagnosticLineScore(line string) int {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case lower == "":
		return 0
	case isLowSignalErrorLine(line):
		return 0
	case strings.Contains(lower, "outofmemoryerror"), strings.Contains(lower, "out of memory"):
		return 130
	case strings.Contains(lower, "keyerror:"),
		strings.Contains(lower, "valueerror:"),
		strings.Contains(lower, "assertionerror:"),
		strings.Contains(lower, "typeerror:"),
		strings.Contains(lower, "indexerror:"),
		strings.Contains(lower, "filenotfounderror:"),
		strings.Contains(lower, "modulenotfounderror:"),
		strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "no such file"),
		strings.Contains(lower, "not found"):
		return 120
	case strings.Contains(lower, "see root cause above"),
		strings.Contains(lower, "failed core proc"),
		isGenericDeploymentFailure(line):
		return 20
	case strings.Contains(lower, "error:"),
		strings.Contains(lower, "exception"),
		strings.Contains(lower, "failed"),
		strings.Contains(lower, "cannot"),
		strings.Contains(lower, "panic"):
		return 80
	default:
		return 10
	}
}

func isLowSignalErrorLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case lower == "":
		return true
	case strings.HasPrefix(lower, "error in cpuinfo:"):
		return true
	default:
		return false
	}
}
