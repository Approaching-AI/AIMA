package central

import "testing"

func TestPromptsNonEmpty(t *testing.T) {
	prompts := map[string]string{
		"promptRecommend":        promptRecommend,
		"promptOptimize":         promptOptimize,
		"promptGenerateScenario": promptGenerateScenario,
		"promptGapAnalysis":      promptGapAnalysis,
	}
	for name, prompt := range prompts {
		if len(prompt) < 100 {
			t.Errorf("%s is too short (%d chars)", name, len(prompt))
		}
		if prompt == "" {
			t.Errorf("%s is empty", name)
		}
	}
}
