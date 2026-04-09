package agent

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlBlockRe matches a fenced yaml code block.
var yamlBlockRe = regexp.MustCompile("(?s)```ya?ml\n(.*?)```")

// parsePlanTasks extracts TaskSpec list from plan.md markdown.
// Looks for the yaml code block under "## Tasks".
func parsePlanTasks(md string) ([]TaskSpec, error) {
	section := extractSection(md, "## Tasks")
	if section == "" {
		return nil, fmt.Errorf("no ## Tasks section found")
	}
	matches := yamlBlockRe.FindStringSubmatch(section)
	if len(matches) < 2 {
		return nil, fmt.Errorf("no yaml code block in ## Tasks section")
	}
	var tasks []TaskSpec
	if err := yaml.Unmarshal([]byte(matches[1]), &tasks); err != nil {
		return nil, fmt.Errorf("parse tasks yaml: %w", err)
	}
	return tasks, nil
}

// parseRecommendedConfigs extracts RecommendedConfig list from summary.md.
// Looks for the yaml code block under "## Recommended Configurations".
func parseRecommendedConfigs(md string) ([]RecommendedConfig, error) {
	section := extractSection(md, "## Recommended Configurations")
	if section == "" {
		return nil, nil // no recommendations yet is normal
	}
	matches := yamlBlockRe.FindStringSubmatch(section)
	if len(matches) < 2 {
		return nil, nil
	}
	var configs []RecommendedConfig
	if err := yaml.Unmarshal([]byte(matches[1]), &configs); err != nil {
		return nil, fmt.Errorf("parse recommendations yaml: %w", err)
	}
	return configs, nil
}

// extractSection returns the content from a markdown heading until the next
// heading of equal or higher level (or end of document).
func extractSection(md, heading string) string {
	level := len(heading) - len(strings.TrimLeft(heading, "#"))
	idx := strings.Index(md, heading)
	if idx == -1 {
		return ""
	}
	rest := md[idx+len(heading):]
	// Find next heading of same or higher level
	prefix := strings.Repeat("#", level) + " "
	for i := 0; i < len(rest); i++ {
		if i == 0 || rest[i-1] == '\n' {
			remaining := rest[i:]
			if strings.HasPrefix(remaining, prefix) || (level > 1 && strings.HasPrefix(remaining, strings.Repeat("#", level-1)+" ")) {
				return rest[:i]
			}
		}
	}
	return rest
}
