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
			`"model":{"type":"string","description":"Model name to resolve, e.g. 'qwen3-0.6b'. Call model.list or catalog.list to see available names."},`+
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

	// knowledge.search — merge search (notes) + search_configs via scope param
	s.RegisterTool(&Tool{
		Name:        "knowledge.search",
		Description: "Search knowledge records. scope=configs (default): search tested Configuration records with filtering and benchmark metrics. scope=notes: search knowledge notes by hardware/model/engine filter. scope=all: returns both.",
		InputSchema: schema(
			`"scope":{"type":"string","enum":["configs","notes","all"],"description":"What to search: configs (default), notes, or all"},`+
				`"hardware":{"type":"string","description":"Filter by hardware profile, e.g. 'nvidia-rtx4060'"},`+
				`"model":{"type":"string","description":"Filter by model name, e.g. 'qwen3-0.6b'"},`+
				`"engine":{"type":"string","description":"Filter by engine type, e.g. 'vllm'"},`+
				`"engine_features":{"type":"array","items":{"type":"string"},"description":"Required engine features (for configs scope)"},`+
				`"constraints":{"type":"object","description":"Performance constraints (for configs scope)"},`+
				`"status":{"type":"string","enum":["golden","experiment","archived"],"description":"Config status filter (for configs scope)"},`+
				`"sort_by":{"type":"string","enum":["throughput","latency","vram","power","created"],"description":"Sort field (for configs scope)"},`+
				`"sort_order":{"type":"string","enum":["asc","desc"],"description":"Sort direction (for configs scope)"},`+
				`"limit":{"type":"integer","description":"Max results"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Scope string `json:"scope"`
			}
			if len(params) > 0 {
				_ = json.Unmarshal(params, &p)
			}
			if p.Scope == "" {
				p.Scope = "configs"
			}

			switch p.Scope {
			case "notes":
				if deps.SearchKnowledge == nil {
					return ErrorResult("knowledge search not implemented"), nil
				}
				var pn struct {
					Hardware string `json:"hardware"`
					Model    string `json:"model"`
					Engine   string `json:"engine"`
				}
				if err := json.Unmarshal(params, &pn); err != nil {
					return nil, fmt.Errorf("parse params: %w", err)
				}
				filter := make(map[string]string)
				if pn.Hardware != "" {
					filter["hardware"] = pn.Hardware
				}
				if pn.Model != "" {
					filter["model"] = pn.Model
				}
				if pn.Engine != "" {
					filter["engine"] = pn.Engine
				}
				data, err := deps.SearchKnowledge(ctx, filter)
				if err != nil {
					return nil, fmt.Errorf("search knowledge notes: %w", err)
				}
				return TextResult(string(data)), nil

			case "all":
				// Combine notes and configs results
				results := map[string]json.RawMessage{}
				if deps.SearchKnowledge != nil {
					var pn struct {
						Hardware string `json:"hardware"`
						Model    string `json:"model"`
						Engine   string `json:"engine"`
					}
					_ = json.Unmarshal(params, &pn)
					filter := make(map[string]string)
					if pn.Hardware != "" {
						filter["hardware"] = pn.Hardware
					}
					if pn.Model != "" {
						filter["model"] = pn.Model
					}
					if pn.Engine != "" {
						filter["engine"] = pn.Engine
					}
					notes, err := deps.SearchKnowledge(ctx, filter)
					if err == nil {
						results["notes"] = notes
					}
				}
				if deps.SearchConfigs != nil {
					configs, err := deps.SearchConfigs(ctx, params)
					if err == nil {
						results["configs"] = configs
					}
				}
				out, _ := json.Marshal(results)
				return TextResult(string(out)), nil

			default: // "configs"
				if deps.SearchConfigs == nil {
					return ErrorResult("knowledge search_configs not implemented"), nil
				}
				data, err := deps.SearchConfigs(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("search configs: %w", err)
				}
				return TextResult(string(data)), nil
			}
		},
	})

	// knowledge.analytics — merge compare + similar + lineage + gaps + aggregate via query param
	s.RegisterTool(&Tool{
		Name:        "knowledge.analytics",
		Description: "Advanced knowledge analytics. query=compare: compare configs side-by-side. query=similar: find configs with similar performance profiles. query=lineage: trace derivation chain. query=gaps: identify untested combinations. query=aggregate: aggregate benchmark stats grouped by dimension.",
		InputSchema: schema(
			`"query":{"type":"string","enum":["compare","similar","lineage","gaps","aggregate"],"description":"Analytics query type"},`+
				`"config_ids":{"type":"array","items":{"type":"string"},"description":"Config IDs (required for compare)"},`+
				`"config_id":{"type":"string","description":"Reference config ID (required for similar, lineage)"},`+
				`"metrics":{"type":"array","items":{"type":"string"},"description":"Metrics to compare (for compare)"},`+
				`"concurrency":{"type":"integer","description":"Concurrency for comparison (for compare)"},`+
				`"weights":{"type":"object","description":"Metric weights (for similar)"},`+
				`"filter_hardware":{"type":"string","description":"Limit search to hardware (for similar)"},`+
				`"exclude_same_config":{"type":"boolean","description":"Exclude self (for similar, default true)"},`+
				`"hardware":{"type":"string","description":"Filter by hardware (for gaps, aggregate)"},`+
				`"min_benchmarks":{"type":"integer","description":"Gap threshold (for gaps, default 3)"},`+
				`"model":{"type":"string","description":"Filter by model (for aggregate)"},`+
				`"group_by":{"type":"string","enum":["engine","hardware","model"],"description":"Group dimension (for aggregate, default engine)"},`+
				`"limit":{"type":"integer","description":"Max results"}`,
			"query"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			switch p.Query {
			case "compare":
				if deps.CompareConfigs == nil {
					return ErrorResult("knowledge.compare not implemented"), nil
				}
				data, err := deps.CompareConfigs(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("compare configs: %w", err)
				}
				return TextResult(string(data)), nil
			case "similar":
				if deps.SimilarConfigs == nil {
					return ErrorResult("knowledge.similar not implemented"), nil
				}
				data, err := deps.SimilarConfigs(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("similar configs: %w", err)
				}
				return TextResult(string(data)), nil
			case "lineage":
				if deps.LineageConfigs == nil {
					return ErrorResult("knowledge.lineage not implemented"), nil
				}
				var pl struct {
					ConfigID string `json:"config_id"`
				}
				if err := json.Unmarshal(params, &pl); err != nil {
					return nil, fmt.Errorf("parse params: %w", err)
				}
				if pl.ConfigID == "" {
					return ErrorResult("config_id is required for lineage"), nil
				}
				data, err := deps.LineageConfigs(ctx, pl.ConfigID)
				if err != nil {
					return nil, fmt.Errorf("lineage %s: %w", pl.ConfigID, err)
				}
				return TextResult(string(data)), nil
			case "gaps":
				if deps.GapsKnowledge == nil {
					return ErrorResult("knowledge.gaps not implemented"), nil
				}
				data, err := deps.GapsKnowledge(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("knowledge gaps: %w", err)
				}
				return TextResult(string(data)), nil
			case "aggregate":
				if deps.AggregateKnowledge == nil {
					return ErrorResult("knowledge.aggregate not implemented"), nil
				}
				data, err := deps.AggregateKnowledge(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("knowledge aggregate: %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown query %q; supported: compare, similar, lineage, gaps, aggregate", p.Query)), nil
			}
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

	// knowledge.evaluate — merge validate + engine_switch_cost + open_questions via action param
	s.RegisterTool(&Tool{
		Name:        "knowledge.evaluate",
		Description: "Knowledge evaluation actions. action=validate: compare predicted vs actual performance (flags >20% deviation). action=engine_switch_cost: quantify cost vs benefit of switching engines. action=open_questions: list, resolve, or launch exploration runs for open questions from knowledge assets.",
		InputSchema: schema(
			`"action":{"type":"string","enum":["validate","engine_switch_cost","open_questions"],"description":"Evaluation action"},`+
				`"hardware":{"type":"string","description":"GPU architecture (for validate, open_questions)"},`+
				`"engine":{"type":"string","description":"Engine type (for validate, open_questions)"},`+
				`"model":{"type":"string","description":"Model name (for validate, open_questions)"},`+
				`"current_engine":{"type":"string","description":"Currently deployed engine (for engine_switch_cost)"},`+
				`"target_engine":{"type":"string","description":"Engine to evaluate switching to (for engine_switch_cost)"},`+
				`"open_question_action":{"type":"string","enum":["list","resolve","run","validate"],"description":"Sub-action for open_questions"},`+
				`"status":{"type":"string","description":"Filter by question status (for open_questions list)"},`+
				`"id":{"type":"string","description":"Question ID (for open_questions resolve)"},`+
				`"result":{"type":"string","description":"Test result (for open_questions resolve)"},`+
				`"endpoint":{"type":"string","description":"Inference endpoint override (for open_questions run)"},`+
				`"requested_by":{"type":"string","description":"Who requested (for open_questions run)"},`+
				`"concurrency":{"type":"integer","description":"Benchmark concurrency (for open_questions run)"},`+
				`"rounds":{"type":"integer","description":"Benchmark rounds (for open_questions run)"}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action string `json:"action"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			switch p.Action {
			case "validate":
				if deps.ValidateKnowledge == nil {
					return ErrorResult("validation not available"), nil
				}
				data, err := deps.ValidateKnowledge(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("knowledge.evaluate validate: %w", err)
				}
				return TextResult(string(data)), nil
			case "engine_switch_cost":
				if deps.EngineSwitchCost == nil {
					return ErrorResult("engine switch cost not available"), nil
				}
				data, err := deps.EngineSwitchCost(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("knowledge.evaluate engine_switch_cost: %w", err)
				}
				return TextResult(string(data)), nil
			case "open_questions":
				if deps.OpenQuestions == nil {
					return ErrorResult("open questions not available"), nil
				}
				// Forward the full params (sub-action is open_question_action inside)
				data, err := deps.OpenQuestions(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("knowledge.evaluate open_questions: %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: validate, engine_switch_cost, open_questions", p.Action)), nil
			}
		},
	})
}
