package onboarding

import (
	"context"
	"fmt"
	"strings"
)

// RunStart executes the read-only first-run guide. It intentionally composes
// existing onboarding primitives instead of duplicating their business logic.
func RunStart(ctx context.Context, deps *Deps, locale string, sink EventSink) (StartResult, error) {
	if strings.TrimSpace(locale) == "" {
		locale = "en"
	}

	status, err := BuildStatus(ctx, deps)
	if err != nil {
		return StartResult{}, fmt.Errorf("onboarding start status: %w", err)
	}

	scan, events, err := RunScan(ctx, deps, sink)
	if err != nil {
		return StartResult{}, fmt.Errorf("onboarding start scan: %w", err)
	}

	recommend, err := Recommend(ctx, deps, locale)
	if err != nil {
		return StartResult{}, fmt.Errorf("onboarding start recommend: %w", err)
	}

	nextModel := firstStartModel(recommend)
	result := StartResult{
		Status:    status,
		Scan:      scan,
		Events:    events,
		Recommend: recommend,
		NextModel: nextModel,
	}
	if nextModel != "" {
		result.NextCommand = "aima run " + nextModel
	}
	return result, nil
}

func firstStartModel(result RecommendResult) string {
	for _, r := range result.Recommendations {
		name := strings.TrimSpace(r.ModelName)
		if name == "" {
			continue
		}
		if r.HardwareFit || r.FitScore > 0 {
			return name
		}
	}
	return ""
}
