package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerCatalogTools(s *Server, deps *ToolDeps) {
	// catalog.list — kind param dispatches to different listing functions
	s.RegisterTool(&Tool{
		Name:        "catalog.list",
		Description: "List catalog assets. kind=profiles: hardware profiles with GPU/CPU/RAM vectors. kind=engines: engine assets with hardware requirements. kind=models: model assets with variants and sources. kind=partitions: partition strategies. kind=scenarios: deployment scenario recipes. kind=summary: counts of all asset types. kind=status: factory vs overlay asset counts. kind=all: all of the above combined.",
		InputSchema: schema(
			`"kind":{"type":"string","enum":["profiles","engines","models","partitions","scenarios","summary","status","all"],"description":"Asset kind to list"}`,
			"kind"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Kind == "" {
				return ErrorResult("kind is required"), nil
			}

			switch p.Kind {
			case "profiles":
				if deps.ListProfiles == nil {
					return ErrorResult("catalog.list profiles not implemented"), nil
				}
				data, err := deps.ListProfiles(ctx)
				if err != nil {
					return nil, fmt.Errorf("list profiles: %w", err)
				}
				return TextResult(string(data)), nil

			case "engines":
				if deps.ListEngineAssets == nil {
					return ErrorResult("catalog.list engines not implemented"), nil
				}
				data, err := deps.ListEngineAssets(ctx)
				if err != nil {
					return nil, fmt.Errorf("list engine assets: %w", err)
				}
				return TextResult(string(data)), nil

			case "models":
				if deps.ListModelAssets == nil {
					return ErrorResult("catalog.list models not implemented"), nil
				}
				data, err := deps.ListModelAssets(ctx)
				if err != nil {
					return nil, fmt.Errorf("list model assets: %w", err)
				}
				return TextResult(string(data)), nil

			case "partitions":
				if deps.ListPartitionStrategies == nil {
					return ErrorResult("catalog.list partitions not implemented"), nil
				}
				data, err := deps.ListPartitionStrategies(ctx)
				if err != nil {
					return nil, fmt.Errorf("list partition strategies: %w", err)
				}
				return TextResult(string(data)), nil

			case "scenarios":
				if deps.ScenarioList == nil {
					return ErrorResult("catalog.list scenarios not implemented"), nil
				}
				data, err := deps.ScenarioList(ctx)
				if err != nil {
					return nil, fmt.Errorf("list scenarios: %w", err)
				}
				return TextResult(string(data)), nil

			case "summary":
				if deps.ListKnowledgeSummary == nil {
					return ErrorResult("catalog.list summary not implemented"), nil
				}
				data, err := deps.ListKnowledgeSummary(ctx)
				if err != nil {
					return nil, fmt.Errorf("knowledge summary: %w", err)
				}
				return TextResult(string(data)), nil

			case "status":
				if deps.CatalogStatus == nil {
					return ErrorResult("catalog.list status not implemented"), nil
				}
				data, err := deps.CatalogStatus(ctx)
				if err != nil {
					return nil, fmt.Errorf("catalog status: %w", err)
				}
				return TextResult(string(data)), nil

			case "all":
				result := map[string]json.RawMessage{}
				if deps.ListProfiles != nil {
					if data, err := deps.ListProfiles(ctx); err == nil {
						result["profiles"] = data
					}
				}
				if deps.ListEngineAssets != nil {
					if data, err := deps.ListEngineAssets(ctx); err == nil {
						result["engines"] = data
					}
				}
				if deps.ListModelAssets != nil {
					if data, err := deps.ListModelAssets(ctx); err == nil {
						result["models"] = data
					}
				}
				if deps.ListPartitionStrategies != nil {
					if data, err := deps.ListPartitionStrategies(ctx); err == nil {
						result["partitions"] = data
					}
				}
				if deps.ScenarioList != nil {
					if data, err := deps.ScenarioList(ctx); err == nil {
						result["scenarios"] = data
					}
				}
				if deps.ListKnowledgeSummary != nil {
					if data, err := deps.ListKnowledgeSummary(ctx); err == nil {
						result["summary"] = data
					}
				}
				if deps.CatalogStatus != nil {
					if data, err := deps.CatalogStatus(ctx); err == nil {
						result["status"] = data
					}
				}
				out, _ := json.Marshal(result)
				return TextResult(string(out)), nil

			default:
				return ErrorResult(fmt.Sprintf("unknown kind %q; supported: profiles, engines, models, partitions, scenarios, summary, status, all", p.Kind)), nil
			}
		},
	})

	// catalog.effective — inspect one effective asset after all overlay layers
	s.RegisterTool(&Tool{
		Name:        "catalog.effective",
		Description: "Return the effective YAML for one catalog asset after factory, central, and user overlays. Read-only.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["engine_profile","engine_asset","model_asset","hardware_profile","partition_strategy","stack_component","deployment_scenario"],"description":"Catalog asset kind"},"name":{"type":"string","description":"metadata.name of the asset"}},"required":["kind","name"]}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CatalogEffective == nil {
				return ErrorResult("catalog.effective not implemented"), nil
			}
			var p struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Kind == "" || p.Name == "" {
				return ErrorResult("kind and name are required"), nil
			}
			data, err := deps.CatalogEffective(ctx, p.Kind, p.Name)
			if err != nil {
				return nil, fmt.Errorf("catalog effective: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// catalog.diff — compare embedded factory asset with the current effective asset
	s.RegisterTool(&Tool{
		Name:        "catalog.diff",
		Description: "Return a factory-to-effective diff for one catalog asset. Useful for validating centrally packaged overlays before rollout.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["engine_profile","engine_asset","model_asset","hardware_profile","partition_strategy","stack_component","deployment_scenario"],"description":"Catalog asset kind"},"name":{"type":"string","description":"metadata.name of the asset"}},"required":["kind","name"]}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CatalogDiff == nil {
				return ErrorResult("catalog.diff not implemented"), nil
			}
			var p struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Kind == "" || p.Name == "" {
				return ErrorResult("kind and name are required"), nil
			}
			data, err := deps.CatalogDiff(ctx, p.Kind, p.Name)
			if err != nil {
				return nil, fmt.Errorf("catalog diff: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// catalog.validate_patch — validate one patch body without writing it
	s.RegisterTool(&Tool{
		Name:        "catalog.validate_patch",
		Description: "Validate a catalog patch body against the current effective catalog and return the merged effective YAML. Read-only; does not write an overlay file.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"content":{"type":"string","description":"YAML content with kind: <asset_kind>_patch and metadata.name"}},"required":["content"]}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CatalogValidatePatch == nil {
				return ErrorResult("catalog.validate_patch not implemented"), nil
			}
			var p struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Content == "" {
				return ErrorResult("content is required"), nil
			}
			data, err := deps.CatalogValidatePatch(ctx, p.Content)
			if err != nil {
				return nil, fmt.Errorf("catalog validate_patch: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// catalog.override — write a user-owned YAML patch to the runtime overlay catalog
	s.RegisterTool(&Tool{
		Name:        "catalog.override",
		Description: "Write a user-owned YAML patch to the runtime overlay catalog (~/.aima/catalog/user/). Accepts base kind or <asset_kind>_patch; full asset content is converted to a patch for compatibility.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["engine_asset","engine_asset_patch","model_asset","model_asset_patch","hardware_profile","hardware_profile_patch","partition_strategy","partition_strategy_patch","stack_component","stack_component_patch"],"description":"Base asset kind or patch kind being patched"},"name":{"type":"string","description":"metadata.name of the asset"},"content":{"type":"string","description":"YAML patch content. Preferred body kind is <asset_kind>_patch with metadata.name."}},"required":["kind","name","content"]}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CatalogOverride == nil {
				return ErrorResult("catalog.override not implemented"), nil
			}
			var p struct {
				Kind    string `json:"kind"`
				Name    string `json:"name"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Kind == "" || p.Name == "" || p.Content == "" {
				return ErrorResult("kind, name, and content are required"), nil
			}
			data, err := deps.CatalogOverride(ctx, p.Kind, p.Name, p.Content)
			if err != nil {
				return nil, fmt.Errorf("catalog override: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// catalog.validate — validate engine YAML catalog
	s.RegisterTool(&Tool{
		Name:        "catalog.validate",
		Description: "Validate engine YAML catalog for schema issues: missing registries, baked-in proxy URLs, single-point-of-failure registries, and local-only distribution markers.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CatalogValidate == nil {
				return ErrorResult("catalog.validate not implemented"), nil
			}
			data, err := deps.CatalogValidate(ctx)
			if err != nil {
				return nil, fmt.Errorf("catalog validate: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
