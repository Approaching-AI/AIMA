package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerModelTools(s *Server, deps *ToolDeps) {
	// model.scan
	s.RegisterTool(&Tool{
		Name:        "model.scan",
		Description: "Scan the local filesystem for model files (GGUF, SafeTensors) and register newly discovered ones in the database.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ScanModels == nil {
				return ErrorResult("model.scan not implemented"), nil
			}
			data, err := deps.ScanModels(ctx)
			if err != nil {
				return nil, fmt.Errorf("scan models: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// model.list
	s.RegisterTool(&Tool{
		Name:        "model.list",
		Description: "List models registered in the local database with names, file paths, sizes, and statuses.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListModels == nil {
				return ErrorResult("model.list not implemented"), nil
			}
			data, err := deps.ListModels(ctx)
			if err != nil {
				return nil, fmt.Errorf("list models: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// model.pull
	s.RegisterTool(&Tool{
		Name:        "model.pull",
		Description: "Download a model by name from a remote source and register it in the database.",
		InputSchema: schema(`"name":{"type":"string","description":"Model name to download, e.g. 'qwen3-0.6b', 'qwen3.5-35b-a3b'. Must match a name in the knowledge base (call knowledge.list_models to see available names)."}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PullModel == nil {
				return ErrorResult("model.pull not implemented"), nil
			}
			var p struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			if err := deps.PullModel(ctx, p.Name); err != nil {
				return nil, fmt.Errorf("pull model %s: %w", p.Name, err)
			}
			return TextResult(fmt.Sprintf("model %s pull started", p.Name)), nil
		},
	})

	// model.import
	s.RegisterTool(&Tool{
		Name:        "model.import",
		Description: "Import a model from a local file path and register it in the database.",
		InputSchema: schema(`"path":{"type":"string","description":"Absolute path to a model file (e.g. '/data/models/qwen3-0.6b.gguf') or directory containing model files"}`, "path"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ImportModel == nil {
				return ErrorResult("model.import not implemented"), nil
			}
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Path == "" {
				return ErrorResult("path is required"), nil
			}
			data, err := deps.ImportModel(ctx, p.Path)
			if err != nil {
				return nil, fmt.Errorf("import model from %s: %w", p.Path, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// model.info
	s.RegisterTool(&Tool{
		Name:        "model.info",
		Description: "Get detailed information about a specific model: file path, size, format, quantization, and knowledge base metadata.",
		InputSchema: schema(`"name":{"type":"string","description":"Model name as registered in the database, e.g. 'qwen3-0.6b'. Call model.list to see available names."}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.GetModelInfo == nil {
				return ErrorResult("model.info not implemented"), nil
			}
			var p struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			data, err := deps.GetModelInfo(ctx, p.Name)
			if err != nil {
				return nil, fmt.Errorf("get model info %s: %w", p.Name, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// model.remove
	s.RegisterTool(&Tool{
		Name:        "model.remove",
		Description: "Remove a model record from the database. Optionally deletes model files from disk. This is a destructive operation (a rollback snapshot is created automatically). Blocked for agent-initiated calls.",
		InputSchema: schema(`"name":{"type":"string","description":"Model name to remove, e.g. 'qwen3-0.6b'. Call model.list to see registered models."},"delete_files":{"type":"boolean","description":"If true, also delete model files from disk. If false (default), only removes the database record."}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RemoveModel == nil {
				return ErrorResult("model.remove not implemented"), nil
			}
			var p struct {
				Name        string `json:"name"`
				DeleteFiles bool   `json:"delete_files"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			if err := deps.RemoveModel(ctx, p.Name, p.DeleteFiles); err != nil {
				return nil, fmt.Errorf("remove model %s: %w", p.Name, err)
			}
			if p.DeleteFiles {
				return TextResult(fmt.Sprintf("model %s removed (files deleted)", p.Name)), nil
			}
			return TextResult(fmt.Sprintf("model %s removed (database only)", p.Name)), nil
		},
	})

}
