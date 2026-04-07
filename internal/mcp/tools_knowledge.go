package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerKnowledgeTools(s *Server, deps *ToolDeps) {
	// knowledge.resolve
	s.RegisterTool(&Tool{
		Name:        "knowledge.resolve",
		Description: "Find the optimal engine and configuration for deploying a model on this hardware. Merges YAML defaults, golden configs, and user overrides into a final resolved config.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name to resolve, e.g. 'qwen3-0.6b'. Call model.list or knowledge.list_models to see available names."},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'vllm', 'llamacpp'. Omit to auto-select the best engine."},`+
				`"overrides":{"type":"object","description":"Config overrides to apply on top of resolved defaults, e.g. {\"gpu_memory_utilization\": 0.85}"}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ResolveConfig == nil {
				return ErrorResult("knowledge.resolve not implemented"), nil
			}
			var p struct {
				Model     string         `json:"model"`
				Engine    string         `json:"engine"`
				Overrides map[string]any `json:"overrides"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" {
				return ErrorResult("model is required"), nil
			}
			data, err := deps.ResolveConfig(ctx, p.Model, p.Engine, p.Overrides)
			if err != nil {
				return nil, fmt.Errorf("resolve config for %s: %w", p.Model, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.search
	s.RegisterTool(&Tool{
		Name:        "knowledge.search",
		Description: "Search knowledge notes (agent exploration records) by hardware, model, or engine filter. Returns matching notes with titles, tags, and content.",
		InputSchema: schema(
			`"hardware":{"type":"string","description":"Filter by hardware profile, e.g. 'nvidia-rtx4060'"},`+
				`"model":{"type":"string","description":"Filter by model name, e.g. 'qwen3-0.6b'"},`+
				`"engine":{"type":"string","description":"Filter by engine type, e.g. 'vllm'"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SearchKnowledge == nil {
				return ErrorResult("knowledge.search not implemented"), nil
			}
			var p struct {
				Hardware string `json:"hardware"`
				Model    string `json:"model"`
				Engine   string `json:"engine"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			filter := make(map[string]string)
			if p.Hardware != "" {
				filter["hardware"] = p.Hardware
			}
			if p.Model != "" {
				filter["model"] = p.Model
			}
			if p.Engine != "" {
				filter["engine"] = p.Engine
			}
			data, err := deps.SearchKnowledge(ctx, filter)
			if err != nil {
				return nil, fmt.Errorf("search knowledge: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.save
	s.RegisterTool(&Tool{
		Name:        "knowledge.save",
		Description: "Save a knowledge note recording exploration results, experiment findings, or recommendations.",
		InputSchema: schema(
			`"note":{"type":"object","description":"Knowledge note to save","properties":{`+
				`"title":{"type":"string","description":"Short descriptive title for the note"},`+
				`"content":{"type":"string","description":"Full text content of the note (findings, observations, recommendations)"},`+
				`"hardware_profile":{"type":"string","description":"Hardware profile name, e.g. 'nvidia-rtx4090-x86'"},`+
				`"model":{"type":"string","description":"Model name, e.g. 'glm-4.7-flash'"},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'sglang-kt'"},`+
				`"tags":{"type":"array","items":{"type":"string"},"description":"Tags for categorization"},`+
				`"confidence":{"type":"string","description":"Confidence level: high, medium, low"}`+
				`},"required":["title","content"]}`, "note"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SaveKnowledge == nil {
				return ErrorResult("knowledge.save not implemented"), nil
			}
			var p struct {
				Note json.RawMessage `json:"note"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if len(p.Note) == 0 {
				return ErrorResult("note is required"), nil
			}
			if err := deps.SaveKnowledge(ctx, p.Note); err != nil {
				return nil, fmt.Errorf("save knowledge: %w", err)
			}
			return TextResult("knowledge note saved"), nil
		},
	})

	// knowledge.generate_pod
	s.RegisterTool(&Tool{
		Name:        "knowledge.generate_pod",
		Description: "Generate K3S Pod YAML manifest for a model/engine deployment without applying it.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name, e.g. 'qwen3-0.6b'"},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'vllm'"},`+
				`"slot":{"type":"string","description":"Partition slot, e.g. 'slot-0'. Omit for default."}`,
			"model", "engine"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.GeneratePod == nil {
				return ErrorResult("knowledge.generate_pod not implemented"), nil
			}
			var p struct {
				Model  string `json:"model"`
				Engine string `json:"engine"`
				Slot   string `json:"slot"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" || p.Engine == "" {
				return ErrorResult("model and engine are required"), nil
			}
			data, err := deps.GeneratePod(ctx, p.Model, p.Engine, p.Slot)
			if err != nil {
				return nil, fmt.Errorf("generate pod for %s/%s: %w", p.Model, p.Engine, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.list_profiles
	s.RegisterTool(&Tool{
		Name:        "knowledge.list_profiles",
		Description: "List all hardware profiles defined in the YAML knowledge base with GPU/CPU/RAM capability vectors and resource names.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListProfiles == nil {
				return ErrorResult("knowledge.list_profiles not implemented"), nil
			}
			data, err := deps.ListProfiles(ctx)
			if err != nil {
				return nil, fmt.Errorf("list profiles: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.list_engines
	s.RegisterTool(&Tool{
		Name:        "knowledge.list_engines",
		Description: "List all engine assets in the YAML catalog with hardware requirements, image sources, and features.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListEngineAssets == nil {
				return ErrorResult("knowledge.list_engines not implemented"), nil
			}
			data, err := deps.ListEngineAssets(ctx)
			if err != nil {
				return nil, fmt.Errorf("list engine assets: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.list_models
	s.RegisterTool(&Tool{
		Name:        "knowledge.list_models",
		Description: "List all model assets in the YAML catalog with variants, download sources, and compatible engines.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListModelAssets == nil {
				return ErrorResult("knowledge.list_models not implemented"), nil
			}
			data, err := deps.ListModelAssets(ctx)
			if err != nil {
				return nil, fmt.Errorf("list model assets: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.search_configs (enhanced — multi-dimensional search with SQL preprocessing)
	s.RegisterTool(&Tool{
		Name:        "knowledge.search_configs",
		Description: "Search tested Configuration records (Hardware x Engine x Model) with filtering, sorting, and benchmark performance metrics.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"hardware":{"type":"string","description":"Hardware profile ID or GPU architecture"},"model":{"type":"string","description":"Model ID or model family"},"engine":{"type":"string","description":"Engine type"},"engine_features":{"type":"array","items":{"type":"string"},"description":"Required engine features"},"constraints":{"type":"object","properties":{"ttft_ms_p95_max":{"type":"number"},"throughput_tps_min":{"type":"number"},"vram_mib_max":{"type":"integer"},"power_watts_max":{"type":"number"}}},"concurrency":{"type":"integer"},"status":{"type":"string","enum":["golden","experiment","archived"]},"sort_by":{"type":"string","enum":["throughput","latency","vram","power","created"]},"sort_order":{"type":"string","enum":["asc","desc"]},"limit":{"type":"integer"}}}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SearchConfigs == nil {
				return ErrorResult("knowledge.search_configs not implemented"), nil
			}
			data, err := deps.SearchConfigs(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("search configs: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.compare
	s.RegisterTool(&Tool{
		Name:        "knowledge.compare",
		Description: "Compare multiple Configuration records side-by-side on throughput, latency, and VRAM. Requires config_ids.",
		InputSchema: schema(
			`"config_ids":{"type":"array","items":{"type":"string"},"minItems":2,"maxItems":10,"description":"Configuration IDs to compare"},`+
				`"metrics":{"type":"array","items":{"type":"string"},"description":"Metrics to compare (default: throughput, latency, vram)"},`+
				`"concurrency":{"type":"integer","description":"Fixed concurrency for comparison"}`,
			"config_ids"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CompareConfigs == nil {
				return ErrorResult("knowledge.compare not implemented"), nil
			}
			data, err := deps.CompareConfigs(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("compare configs: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.similar
	s.RegisterTool(&Tool{
		Name:        "knowledge.similar",
		Description: "Find configurations with similar performance profiles using 6D vector distance. Useful for cross-hardware migration.",
		InputSchema: schema(
			`"config_id":{"type":"string","description":"Reference configuration ID"},`+
				`"weights":{"type":"object","description":"Custom metric weights (throughput, latency, vram, power, qps)"},`+
				`"filter_hardware":{"type":"string","description":"Limit search to specific hardware"},`+
				`"exclude_same_config":{"type":"boolean","default":true},`+
				`"limit":{"type":"integer","default":5}`,
			"config_id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SimilarConfigs == nil {
				return ErrorResult("knowledge.similar not implemented"), nil
			}
			data, err := deps.SimilarConfigs(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("similar configs: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.lineage
	s.RegisterTool(&Tool{
		Name:        "knowledge.lineage",
		Description: "Trace the derivation chain of a Configuration — all ancestor and descendant configs with performance progression.",
		InputSchema: schema(`"config_id":{"type":"string","description":"Configuration ID to trace"}`, "config_id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.LineageConfigs == nil {
				return ErrorResult("knowledge.lineage not implemented"), nil
			}
			var p struct {
				ConfigID string `json:"config_id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.ConfigID == "" {
				return ErrorResult("config_id is required"), nil
			}
			data, err := deps.LineageConfigs(ctx, p.ConfigID)
			if err != nil {
				return nil, fmt.Errorf("lineage %s: %w", p.ConfigID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.gaps
	s.RegisterTool(&Tool{
		Name:        "knowledge.gaps",
		Description: "Identify untested Hardware x Engine x Model combinations that lack benchmark data.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"hardware":{"type":"string","description":"Limit to specific hardware"},"min_benchmarks":{"type":"integer","default":3,"description":"Threshold below which a combination is considered a gap"}}}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.GapsKnowledge == nil {
				return ErrorResult("knowledge.gaps not implemented"), nil
			}
			data, err := deps.GapsKnowledge(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("knowledge gaps: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.aggregate
	s.RegisterTool(&Tool{
		Name:        "knowledge.aggregate",
		Description: "Aggregate benchmark statistics grouped by engine, hardware, or model with averages, min/max, and sample counts.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"hardware":{"type":"string","description":"Filter by hardware"},"model":{"type":"string","description":"Filter by model"},"group_by":{"type":"string","enum":["engine","hardware","model"],"default":"engine","description":"Dimension to group by"}}}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.AggregateKnowledge == nil {
				return ErrorResult("knowledge.aggregate not implemented"), nil
			}
			data, err := deps.AggregateKnowledge(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("knowledge aggregate: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.promote
	s.RegisterTool(&Tool{
		Name:        "knowledge.promote",
		Description: "Change a Configuration's status to 'experiment', 'golden' (auto-injected as L2 defaults), or 'archived'.",
		InputSchema: schema(
			`"config_id":{"type":"string","description":"Configuration ID to promote"},`+
				`"status":{"type":"string","enum":["golden","experiment","archived"],"description":"Target status"}`,
			"config_id", "status"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PromoteConfig == nil {
				return ErrorResult("knowledge.promote not implemented"), nil
			}
			var p struct {
				ConfigID string `json:"config_id"`
				Status   string `json:"status"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.ConfigID == "" || p.Status == "" {
				return ErrorResult("config_id and status are required"), nil
			}
			data, err := deps.PromoteConfig(ctx, p.ConfigID, p.Status)
			if err != nil {
				return nil, fmt.Errorf("promote config: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.export
	s.RegisterTool(&Tool{
		Name:        "knowledge.export",
		Description: "Export knowledge data (configurations, benchmarks, notes) to JSON. Filter by hardware, model, or engine.",
		InputSchema: schema(
			`"hardware":{"type":"string","description":"Filter by hardware profile ID"},`+
				`"model":{"type":"string","description":"Filter by model name"},`+
				`"engine":{"type":"string","description":"Filter by engine type"},`+
				`"output_path":{"type":"string","description":"File path to write JSON. If omitted, returns JSON in response."}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ExportKnowledge == nil {
				return ErrorResult("knowledge.export not implemented"), nil
			}
			data, err := deps.ExportKnowledge(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("export knowledge: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.import
	s.RegisterTool(&Tool{
		Name:        "knowledge.import",
		Description: "Import knowledge data from a JSON file. Conflict resolution: 'skip' (default) or 'overwrite'. Supports dry-run. Atomic transaction.",
		InputSchema: schema(
			`"input_path":{"type":"string","description":"Path to JSON file to import"},`+
				`"conflict":{"type":"string","enum":["skip","overwrite"],"description":"Conflict resolution (default: skip)"},`+
				`"dry_run":{"type":"boolean","description":"Preview import without writing (default: false)"}`,
			"input_path"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ImportKnowledge == nil {
				return ErrorResult("knowledge.import not implemented"), nil
			}
			data, err := deps.ImportKnowledge(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("import knowledge: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.list
	s.RegisterTool(&Tool{
		Name:        "knowledge.list",
		Description: "List a summary of all YAML knowledge base assets: counts and names of hardware profiles, engines, models, and partitions.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListKnowledgeSummary == nil {
				return ErrorResult("knowledge.list not implemented"), nil
			}
			data, err := deps.ListKnowledgeSummary(ctx)
			if err != nil {
				return nil, fmt.Errorf("knowledge list: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.validate
	s.RegisterTool(&Tool{
		Name:        "knowledge.validate",
		Description: "Compare predicted vs actual performance. Flags divergent predictions (>20% deviation).",
		InputSchema: schema(
			`"hardware":{"type":"string","description":"GPU architecture"},`+
				`"engine":{"type":"string","description":"Engine type"},`+
				`"model":{"type":"string","description":"Model name"}`,
		),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ValidateKnowledge == nil {
				return ErrorResult("validation not available"), nil
			}
			data, err := deps.ValidateKnowledge(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("knowledge.validate: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.engine_switch_cost
	s.RegisterTool(&Tool{
		Name:        "knowledge.engine_switch_cost",
		Description: "Quantify cost vs benefit of switching engines. Returns throughput gain, switch time, and recommendation.",
		InputSchema: schema(
			`"current_engine":{"type":"string","description":"Currently deployed engine type"},`+
				`"target_engine":{"type":"string","description":"Engine to evaluate switching to"},`+
				`"hardware":{"type":"string","description":"GPU architecture"},`+
				`"model":{"type":"string","description":"Model name"}`,
			"current_engine", "target_engine"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.EngineSwitchCost == nil {
				return ErrorResult("engine switch cost not available"), nil
			}
			data, err := deps.EngineSwitchCost(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("knowledge.engine_switch_cost: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.open_questions
	s.RegisterTool(&Tool{
		Name:        "knowledge.open_questions",
		Description: "List, resolve, or launch exploration runs for open questions from knowledge assets.",
		InputSchema: schema(
			`"action":{"type":"string","description":"Action: list (default), resolve, run/validate to create an exploration run","enum":["list","resolve","run","validate"]},`+
				`"status":{"type":"string","description":"Filter by status: untested, tested, confirmed, confirmed_incompatible, rejected"},`+
				`"id":{"type":"string","description":"Question ID (for resolve action)"},`+
				`"result":{"type":"string","description":"Actual test result (for resolve action)"},`+
				`"hardware":{"type":"string","description":"Hardware that tested (for resolve/run action)"},`+
				`"model":{"type":"string","description":"Model used for automated validation runs"},`+
				`"engine":{"type":"string","description":"Engine used for automated validation runs"},`+
				`"endpoint":{"type":"string","description":"Inference endpoint override for automated validation runs"},`+
				`"requested_by":{"type":"string","description":"Who requested the run"},`+
				`"concurrency":{"type":"integer","description":"Benchmark concurrency for automated validation runs"},`+
				`"rounds":{"type":"integer","description":"Benchmark rounds for automated validation runs"}`,
		),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.OpenQuestions == nil {
				return ErrorResult("open questions not available"), nil
			}
			data, err := deps.OpenQuestions(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("knowledge.open_questions: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.sync_push
	s.RegisterTool(&Tool{
		Name:        "knowledge.sync_push",
		Description: "Push local knowledge (configurations + benchmarks) to the central knowledge server.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SyncPush == nil {
				return ErrorResult("sync not configured — set central.endpoint and central.api_key first"), nil
			}
			data, err := deps.SyncPush(ctx)
			if err != nil {
				return nil, fmt.Errorf("knowledge.sync_push: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.sync_pull
	s.RegisterTool(&Tool{
		Name:        "knowledge.sync_pull",
		Description: "Pull new knowledge from the central server (configs/benchmarks newer than last pull), including advisories and scenarios from the v2 sync protocol.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SyncPull == nil {
				return ErrorResult("sync not configured — set central.endpoint and central.api_key first"), nil
			}
			data, err := deps.SyncPull(ctx)
			if err != nil {
				return nil, fmt.Errorf("knowledge.sync_pull: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.advise — request recommendation from central
	s.RegisterTool(&Tool{
		Name:        "knowledge.advise",
		Description: "Request an AI-powered engine/config recommendation from the central knowledge server for a model on this hardware.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name to get advice for, e.g. 'qwen3-8b'"},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'vllm'. Omit to get a recommendation."},`+
				`"intent":{"type":"string","description":"Optimization intent: 'low-latency', 'high-throughput', 'balanced'"}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RequestAdvise == nil {
				return ErrorResult("central advise not configured — set central.endpoint first"), nil
			}
			var p struct {
				Model  string `json:"model"`
				Engine string `json:"engine"`
				Intent string `json:"intent"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" {
				return ErrorResult("model is required"), nil
			}
			data, err := deps.RequestAdvise(ctx, p.Model, p.Engine, p.Intent)
			if err != nil {
				return nil, fmt.Errorf("knowledge.advise for %s: %w", p.Model, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.advisory_feedback — send feedback on a central advisory
	s.RegisterTool(&Tool{
		Name:        "knowledge.advisory_feedback",
		Description: "Send feedback on a central advisory after local validation (accepted/rejected with reason).",
		InputSchema: schema(
			`"advisory_id":{"type":"string","description":"Advisory ID from central server"},`+
				`"status":{"type":"string","enum":["accepted","rejected","partial"],"description":"Feedback status"},`+
				`"reason":{"type":"string","description":"Explanation of why advisory was accepted or rejected"}`,
			"advisory_id", "status"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.AdvisoryFeedback == nil {
				return ErrorResult("advisory feedback not configured — set central.endpoint first"), nil
			}
			var p struct {
				AdvisoryID string `json:"advisory_id"`
				Status     string `json:"status"`
				Reason     string `json:"reason"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.AdvisoryID == "" || p.Status == "" {
				return ErrorResult("advisory_id and status are required"), nil
			}
			data, err := deps.AdvisoryFeedback(ctx, p.AdvisoryID, p.Status, p.Reason)
			if err != nil {
				return nil, fmt.Errorf("knowledge.advisory_feedback for %s: %w", p.AdvisoryID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.sync_status
	s.RegisterTool(&Tool{
		Name:        "knowledge.sync_status",
		Description: "Show knowledge sync status: central server URL, connectivity, last push/pull timestamps.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SyncStatus == nil {
				return ErrorResult("sync not configured — set central.endpoint and central.api_key first"), nil
			}
			data, err := deps.SyncStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("knowledge.sync_status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
