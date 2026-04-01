package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerHardwareTools(s *Server, deps *ToolDeps) {
	// hardware.detect
	s.RegisterTool(&Tool{
		Name:        "hardware.detect",
		Description: "Detect this device's hardware capabilities: GPU model, VRAM, compute SDK, CPU cores, total RAM, and NPU if present.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DetectHardware == nil {
				return ErrorResult("hardware.detect not implemented"), nil
			}
			data, err := deps.DetectHardware(ctx)
			if err != nil {
				return nil, fmt.Errorf("detect hardware: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// hardware.metrics
	s.RegisterTool(&Tool{
		Name:        "hardware.metrics",
		Description: "Collect real-time hardware metrics: GPU utilization, memory used/total, temperature, and power draw.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CollectMetrics == nil {
				return ErrorResult("hardware.metrics not implemented"), nil
			}
			data, err := deps.CollectMetrics(ctx)
			if err != nil {
				return nil, fmt.Errorf("collect metrics: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
