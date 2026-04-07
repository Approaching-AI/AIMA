package main

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jguan/aima/catalog"
	"github.com/jguan/aima/internal/cli"
	"github.com/jguan/aima/internal/knowledge"
)

const defaultOnboardingSampleModel = "qwen3-8b"

type onboardingManifest struct {
	Version       string                       `json:"version"`
	DefaultLocale string                       `json:"default_locale"`
	Locales       map[string]*onboardingLocale `json:"locales"`
}

type onboardingLocale struct {
	Title           string                    `json:"title"`
	Tabs            map[string]string         `json:"tabs"`
	QuickStart      onboardingQuickStart      `json:"quick_start"`
	FullCommands    onboardingFullCommands    `json:"full_commands"`
	Troubleshooting onboardingTroubleshooting `json:"troubleshooting"`
}

type onboardingQuickStart struct {
	Intro    string              `json:"intro"`
	Steps    []onboardingStep    `json:"steps"`
	Commands []onboardingCommand `json:"commands"`
}

type onboardingFullCommands struct {
	Groups []onboardingGroup `json:"groups"`
}

type onboardingTroubleshooting struct {
	Items []onboardingStep `json:"items"`
}

type onboardingGroup struct {
	ID          string              `json:"id,omitempty"`
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Items       []onboardingCommand `json:"items"`
}

type onboardingStep struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

type onboardingCommand struct {
	ID          string `json:"id,omitempty"`
	Command     string `json:"command,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

type onboardingCommandSpec struct {
	parts      []string
	render     func(sampleModel string) string
	needsModel bool
}

var onboardingCommandSpecs = map[string]onboardingCommandSpec{
	"status": {
		parts:  []string{"status"},
		render: func(_ string) string { return "status" },
	},
	"help": {
		parts:  []string{"help"},
		render: func(_ string) string { return "help" },
	},
	"hardware": {
		parts:  []string{"hal", "detect"},
		render: func(_ string) string { return "hal detect" },
	},
	"metrics": {
		parts:  []string{"hal", "metrics"},
		render: func(_ string) string { return "hal metrics" },
	},
	"models": {
		parts:  []string{"model", "list"},
		render: func(_ string) string { return "model list" },
	},
	"engines": {
		parts:  []string{"engine", "list"},
		render: func(_ string) string { return "engine list" },
	},
	"deployments": {
		parts:  []string{"deploy", "list"},
		render: func(_ string) string { return "deploy list" },
	},
	"fleet_devices": {
		parts:  []string{"fleet", "devices"},
		render: func(_ string) string { return "fleet devices" },
	},
	"engine_plan": {
		parts:  []string{"engine", "plan"},
		render: func(_ string) string { return "engine plan" },
	},
	"engine_pull": {
		parts:  []string{"engine", "pull"},
		render: func(_ string) string { return "engine pull" },
	},
	"model_pull": {
		parts:      []string{"model", "pull"},
		needsModel: true,
		render: func(sampleModel string) string {
			return "model pull " + sampleModel
		},
	},
	"deploy_dry_run": {
		parts:      []string{"deploy"},
		needsModel: true,
		render: func(sampleModel string) string {
			return "deploy " + sampleModel + " --dry-run"
		},
	},
	"run": {
		parts:      []string{"run"},
		needsModel: true,
		render: func(sampleModel string) string {
			return "run " + sampleModel
		},
	},
}

func buildOnboardingManifestJSON(cat *knowledge.Catalog) (json.RawMessage, error) {
	raw, err := catalog.FS.ReadFile("ui-onboarding.json")
	if err != nil {
		return nil, err
	}

	var manifest onboardingManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}

	root := cli.NewRootCmd(&cli.App{})
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()

	sampleModel := pickOnboardingSampleModel(cat)
	for _, locale := range manifest.Locales {
		if locale == nil {
			continue
		}
		locale.QuickStart.Commands = rewriteOnboardingCommands(locale.QuickStart.Commands, root, sampleModel)
		locale.FullCommands.Groups = rewriteOnboardingGroups(locale.FullCommands.Groups, root, sampleModel)
	}

	out, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func rewriteOnboardingGroups(groups []onboardingGroup, root *cobra.Command, sampleModel string) []onboardingGroup {
	out := make([]onboardingGroup, 0, len(groups))
	for _, group := range groups {
		if group.ID == "top_level_commands" {
			group.Items = buildTopLevelOnboardingCommands(root)
		} else {
			group.Items = rewriteOnboardingCommands(group.Items, root, sampleModel)
		}
		if len(group.Items) == 0 {
			continue
		}
		out = append(out, group)
	}
	return out
}

func rewriteOnboardingCommands(items []onboardingCommand, root *cobra.Command, sampleModel string) []onboardingCommand {
	out := make([]onboardingCommand, 0, len(items))
	for _, item := range items {
		item.Command = replaceSampleModelPlaceholder(item.Command, sampleModel)
		item.Description = replaceSampleModelPlaceholder(item.Description, sampleModel)
		item.Label = replaceSampleModelPlaceholder(item.Label, sampleModel)
		if command, ok := resolveOnboardingCLICommand(item.ID, root, sampleModel); ok {
			item.Command = command
			out = append(out, item)
			continue
		}
		if _, ok := onboardingCommandSpecs[item.ID]; ok {
			continue
		}
		if strings.TrimSpace(item.Command) == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func buildTopLevelOnboardingCommands(root *cobra.Command) []onboardingCommand {
	if root == nil {
		return nil
	}

	items := make([]onboardingCommand, 0, len(root.Commands()))
	for _, cmd := range root.Commands() {
		if cmd == nil || cmd.Hidden {
			continue
		}
		items = append(items, onboardingCommand{
			ID:          cmd.Name(),
			Command:     "/cli " + cmd.Name(),
			Description: onboardingCommandDescription(cmd),
		})
	}
	return items
}

func onboardingCommandDescription(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	if short := strings.TrimSpace(cmd.Short); short != "" {
		return short
	}
	return strings.TrimSpace(cmd.Long)
}

func replaceSampleModelPlaceholder(value, sampleModel string) string {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(sampleModel) == "" {
		return value
	}
	replacer := strings.NewReplacer(
		"`{sample_model}`", sampleModel,
		"{sample_model}", sampleModel,
		" is an example value and can be replaced with a real model name.", " is an example model name; replace it with your own model name.",
		" 是示例值，可替换为实际模型名。", " 是示例模型名，可替换成你自己的模型名。",
	)
	return replacer.Replace(value)
}

func resolveOnboardingCLICommand(id string, root *cobra.Command, sampleModel string) (string, bool) {
	spec, ok := onboardingCommandSpecs[id]
	if !ok {
		return "", false
	}
	if spec.needsModel && strings.TrimSpace(sampleModel) == "" {
		return "", false
	}
	if !cliCommandExists(root, spec.parts...) {
		return "", false
	}
	return "/cli " + spec.render(sampleModel), true
}

func cliCommandExists(root *cobra.Command, parts ...string) bool {
	if root == nil {
		return false
	}
	cmd := root
	for _, part := range parts {
		var next *cobra.Command
		for _, child := range cmd.Commands() {
			if child.Name() == part {
				next = child
				break
			}
		}
		if next == nil {
			return false
		}
		cmd = next
	}
	return true
}

func pickOnboardingSampleModel(cat *knowledge.Catalog) string {
	if cat == nil {
		return defaultOnboardingSampleModel
	}

	bestName := ""
	bestScore := math.MaxFloat64
	for _, asset := range cat.ModelAssets {
		if !strings.EqualFold(strings.TrimSpace(asset.Metadata.Type), "llm") {
			continue
		}
		score := parseModelParameterCount(asset.Metadata.ParameterCount)
		if bestName == "" || score < bestScore {
			bestName = asset.Metadata.Name
			bestScore = score
		}
	}
	if bestName != "" {
		return bestName
	}
	for _, asset := range cat.ModelAssets {
		if strings.TrimSpace(asset.Metadata.Name) != "" {
			return asset.Metadata.Name
		}
	}
	return defaultOnboardingSampleModel
}

func parseModelParameterCount(raw string) float64 {
	value := strings.TrimSpace(strings.ToUpper(raw))
	if value == "" {
		return math.MaxFloat64
	}

	multiplier := 1.0
	switch {
	case strings.HasSuffix(value, "B"):
		value = strings.TrimSuffix(value, "B")
	case strings.HasSuffix(value, "M"):
		value = strings.TrimSuffix(value, "M")
		multiplier = 0.001
	case strings.HasSuffix(value, "K"):
		value = strings.TrimSuffix(value, "K")
		multiplier = 0.000001
	}

	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return math.MaxFloat64
	}
	return number * multiplier
}
