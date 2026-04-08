package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerFleetTools(s *Server, deps *ToolDeps) {
	// fleet.info — no device_id → list devices, with device_id → device info + tools
	s.RegisterTool(&Tool{
		Name:        "fleet.info",
		Description: "Fleet device information. Without device_id: list all AIMA devices on the LAN via mDNS. With device_id: get detailed information about a remote device (hardware, models, deployments) combined with its available MCP tools.",
		InputSchema: schema(
			`"device_id":{"type":"string","description":"Device ID from fleet.info (no args), e.g. 'gb10', 'mac-m4'. Omit to list all devices."}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				DeviceID string `json:"device_id"`
			}
			if len(params) > 0 {
				_ = json.Unmarshal(params, &p)
			}

			if p.DeviceID == "" {
				// List all devices
				if deps.FleetListDevices == nil {
					return ErrorResult("fleet.info not implemented"), nil
				}
				data, err := deps.FleetListDevices(ctx)
				if err != nil {
					return nil, fmt.Errorf("fleet list devices: %w", err)
				}
				return TextResult(string(data)), nil
			}

			// Get device info + tools, combine into single response
			result := map[string]json.RawMessage{}
			if deps.FleetDeviceInfo != nil {
				info, err := deps.FleetDeviceInfo(ctx, p.DeviceID)
				if err != nil {
					return nil, fmt.Errorf("fleet device info %s: %w", p.DeviceID, err)
				}
				result["info"] = info
			}
			if deps.FleetDeviceTools != nil {
				tools, err := deps.FleetDeviceTools(ctx, p.DeviceID)
				if err == nil {
					result["tools"] = tools
				}
			}
			if len(result) == 0 {
				return ErrorResult("fleet.info not implemented"), nil
			}
			out, _ := json.Marshal(result)
			return TextResult(string(out)), nil
		},
	})

	// fleet.exec — renamed from fleet.exec_tool, handler unchanged
	s.RegisterTool(&Tool{
		Name:        "fleet.exec",
		Description: "Execute any MCP tool on a remote fleet device. Agent guardrails apply to the inner tool_name.",
		InputSchema: schema(
			`"device_id":{"type":"string","description":"Device ID from fleet.info, e.g. 'gb10', 'linux-1'. Call fleet.info first if unsure."},`+
				`"tool_name":{"type":"string","description":"MCP tool name to execute remotely, e.g. 'hardware.detect', 'model.list', 'deploy.status'. Call fleet.info with device_id to see available tools."},`+
				`"params":{"type":"object","description":"Tool parameters as a JSON object. Omit or pass {} if the tool takes no parameters."}`,
			"device_id", "tool_name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.FleetExecTool == nil {
				return ErrorResult("fleet.exec not implemented"), nil
			}
			var p struct {
				DeviceID string          `json:"device_id"`
				ToolName string          `json:"tool_name"`
				Params   json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.DeviceID == "" || p.ToolName == "" {
				return ErrorResult("device_id and tool_name are required"), nil
			}
			data, err := deps.FleetExecTool(ctx, p.DeviceID, p.ToolName, p.Params)
			if err != nil {
				return nil, fmt.Errorf("fleet exec %s on %s: %w", p.ToolName, p.DeviceID, err)
			}
			return TextResult(string(data)), nil
		},
	})
}
