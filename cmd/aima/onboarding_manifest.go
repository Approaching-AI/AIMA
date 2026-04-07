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

var onboardingTopLevelCommandDescriptions = map[string]map[string]string{
	"zh": {
		"agent":      "管理 AI Agent 子系统。需要子命令，例如 /cli agent status。",
		"app":        "管理应用依赖声明。需要子命令，例如 /cli app list；注册应用可用 /cli app register --name demo --needs '[]'。",
		"ask":        "向 AI Agent 提问。需要补问题内容，例如 /cli ask 这台机器适合跑什么模型？",
		"askforhelp": "连接支持服务。可直接写求助内容，例如 /cli askforhelp 模型部署失败。",
		"benchmark":  "记录并查询基准测试结果。需要子命令，例如 /cli benchmark list。",
		"catalog":    "管理 YAML 知识目录。需要子命令，例如 /cli catalog status 或 /cli catalog validate。",
		"completion": "为指定 shell 生成自动补全脚本。需要补 shell 名称，例如 /cli completion zsh。",
		"config":     "获取或设置持久化配置。需要子命令，例如 /cli config get agent.endpoint 或 /cli config set agent.model {sample_model}。",
		"deploy":     "部署推理服务。通常需要模型名，例如 /cli deploy {sample_model} --dry-run；查看部署列表可用 /cli deploy list。",
		"discover":   "发现局域网上的 LLM 推理服务。可直接执行，例如 /cli discover。",
		"engine":     "管理推理引擎。需要子命令，例如 /cli engine list 或 /cli engine pull。",
		"explore":    "持久化探索任务。需要子命令，例如 /cli explore start --model {sample_model} 或 /cli explore status --id <run-id>。",
		"fleet":      "管理局域网中的 AIMA 设备集群。需要子命令，例如 /cli fleet devices 或 /cli fleet info <device-id>。",
		"hal":        "硬件抽象层：检测能力并采集指标。需要子命令，例如 /cli hal detect 或 /cli hal metrics。",
		"help":       "查看任意命令帮助。可直接执行，例如 /cli help；查看 model 的帮助可用 /cli help model。",
		"init":       "安装基础设施栈。可直接执行，例如 /cli init；完整安装可用 /cli init --k3s。",
		"knowledge":  "管理知识库。需要子命令，例如 /cli knowledge list 或 /cli knowledge resolve {sample_model}。",
		"mcp":        "通过 stdio 提供 MCP 服务。可直接执行，例如 /cli mcp。",
		"model":      "管理模型。需要子命令，例如 /cli model list 或 /cli model pull {sample_model}。",
		"openclaw":   "OpenClaw 集成：将 AIMA 模型同步为 providers。需要子命令，例如 /cli openclaw status 或 /cli openclaw sync。",
		"run":        "下载、部署并提供模型服务。需要模型名，例如 /cli run {sample_model}。",
		"scenario":   "管理部署场景。需要子命令，例如 /cli scenario list 或 /cli scenario apply <scenario-name>。",
		"serve":      "启动 AIMA 服务。可直接执行，例如 /cli serve。",
		"status":     "显示系统状态。可直接执行，例如 /cli status。",
		"tui":        "交互式终端仪表盘。可直接执行，例如 /cli tui。",
		"tuning":     "自动调优：参数搜索 + 基准测试 + 应用最佳结果。需要子命令，例如 /cli tuning start --model {sample_model} 或 /cli tuning results。",
		"undeploy":   "移除已部署的推理服务。需要部署名，例如 /cli undeploy <deployment-name>。",
		"version":    "显示 AIMA 版本和构建信息。可直接执行，例如 /cli version。",
	},
	"en": {
		"agent":      "Manage the AI agent subsystem. It needs a subcommand, for example /cli agent status.",
		"app":        "Manage application dependency declarations. It needs a subcommand, for example /cli app list; to register an app use /cli app register --name demo --needs '[]'.",
		"ask":        "Ask the AI agent a question. You need to add the question text, for example /cli ask What model fits this machine?",
		"askforhelp": "Connect to the support service. You can write the help request directly, for example /cli askforhelp Model deployment failed.",
		"benchmark":  "Record and query benchmark results. It needs a subcommand, for example /cli benchmark list.",
		"catalog":    "Manage the YAML knowledge catalog. It needs a subcommand, for example /cli catalog status or /cli catalog validate.",
		"completion": "Generate shell autocompletion. You need to add the shell name, for example /cli completion zsh.",
		"config":     "Get or set persistent configuration. It needs a subcommand, for example /cli config get agent.endpoint or /cli config set agent.model {sample_model}.",
		"deploy":     "Deploy an inference service. It usually needs a model name, for example /cli deploy {sample_model} --dry-run; to inspect deployments use /cli deploy list.",
		"discover":   "Discover LLM inference services on the local network. You can run it directly, for example /cli discover.",
		"engine":     "Manage inference engines. It needs a subcommand, for example /cli engine list or /cli engine pull.",
		"explore":    "Run persistent exploration jobs. It needs a subcommand, for example /cli explore start --model {sample_model} or /cli explore status --id <run-id>.",
		"fleet":      "Manage AIMA devices on the LAN. It needs a subcommand, for example /cli fleet devices or /cli fleet info <device-id>.",
		"hal":        "Inspect hardware capabilities and metrics. It needs a subcommand, for example /cli hal detect or /cli hal metrics.",
		"help":       "View help for any command. You can run it directly, for example /cli help; for model help use /cli help model.",
		"init":       "Install the infrastructure stack. You can run it directly, for example /cli init; for the full install use /cli init --k3s.",
		"knowledge":  "Manage the knowledge base. It needs a subcommand, for example /cli knowledge list or /cli knowledge resolve {sample_model}.",
		"mcp":        "Serve MCP over stdio. You can run it directly, for example /cli mcp.",
		"model":      "Manage models. It needs a subcommand, for example /cli model list or /cli model pull {sample_model}.",
		"openclaw":   "Manage the OpenClaw integration. It needs a subcommand, for example /cli openclaw status or /cli openclaw sync.",
		"run":        "Download, deploy, and serve a model. You need to add a model name, for example /cli run {sample_model}.",
		"scenario":   "Manage deployment scenarios. It needs a subcommand, for example /cli scenario list or /cli scenario apply <scenario-name>.",
		"serve":      "Start the AIMA server. You can run it directly, for example /cli serve.",
		"status":     "Show system status. You can run it directly, for example /cli status.",
		"tui":        "Open the terminal dashboard. You can run it directly, for example /cli tui.",
		"tuning":     "Auto-tune model parameters. It needs a subcommand, for example /cli tuning start --model {sample_model} or /cli tuning results.",
		"undeploy":   "Remove a deployed inference service. You need to add the deployment name, for example /cli undeploy <deployment-name>.",
		"version":    "Show version and build information. You can run it directly, for example /cli version.",
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
	for localeKey, locale := range manifest.Locales {
		if locale == nil {
			continue
		}
		locale.QuickStart.Commands = rewriteOnboardingCommands(locale.QuickStart.Commands, root, sampleModel)
		locale.FullCommands.Groups = rewriteOnboardingGroups(localeKey, locale.FullCommands.Groups, root, sampleModel)
	}

	out, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func rewriteOnboardingGroups(localeKey string, groups []onboardingGroup, root *cobra.Command, sampleModel string) []onboardingGroup {
	out := make([]onboardingGroup, 0, len(groups))
	for _, group := range groups {
		if group.ID == "top_level_commands" {
			group.Items = buildTopLevelOnboardingCommands(localeKey, root, sampleModel)
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

func buildTopLevelOnboardingCommands(localeKey string, root *cobra.Command, sampleModel string) []onboardingCommand {
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
			Description: onboardingCommandDescription(localeKey, cmd, sampleModel),
		})
	}
	return items
}

func onboardingCommandDescription(localeKey string, cmd *cobra.Command, sampleModel string) string {
	if cmd == nil {
		return ""
	}
	if translations := onboardingTopLevelCommandDescriptions[localeKey]; translations != nil {
		if translated := strings.TrimSpace(translations[cmd.Name()]); translated != "" {
			return replaceSampleModelPlaceholder(translated, sampleModel)
		}
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
