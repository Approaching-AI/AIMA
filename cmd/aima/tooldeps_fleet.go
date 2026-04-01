package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jguan/aima/internal/fleet"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
)

// buildFleetDeps wires fleet.list_devices, fleet.device_info, fleet.device_tools,
// and fleet.exec_tool MCP tools.
func buildFleetDeps(deps *mcp.ToolDeps,
	fleetRegistry *fleet.Registry,
	fleetClient *fleet.Client,
	mcpServer *mcp.Server,
) {
	// fleetEnsureDiscovery runs a one-shot mDNS scan if the registry is empty.
	// This ensures fleet MCP tools work without serve --discover (INV-5 parity).
	fleetEnsureDiscovery := func(ctx context.Context) {
		if len(fleetRegistry.List()) > 0 {
			return
		}
		services, err := proxy.Discover(ctx, 3*time.Second)
		if err != nil {
			return
		}
		fleetRegistry.Update(services)
	}

	deps.FleetListDevices = func(ctx context.Context) (json.RawMessage, error) {
		// Always discover — this is the canonical "find devices" operation
		services, err := proxy.Discover(ctx, 3*time.Second)
		if err != nil {
			return nil, fmt.Errorf("mDNS discovery: %w", err)
		}
		fleetRegistry.Update(services)
		return json.Marshal(fleetRegistry.List())
	}
	deps.FleetDeviceInfo = func(ctx context.Context, deviceID string) (json.RawMessage, error) {
		fleetEnsureDiscovery(ctx)
		d := fleetRegistry.Get(deviceID)
		if d == nil {
			return nil, fmt.Errorf("device %q not found", deviceID)
		}
		if d.Self {
			if deps.SystemStatus != nil {
				return deps.SystemStatus(ctx)
			}
			return json.Marshal(d)
		}
		return fleetClient.GetDeviceInfo(ctx, d)
	}
	deps.FleetDeviceTools = func(ctx context.Context, deviceID string) (json.RawMessage, error) {
		fleetEnsureDiscovery(ctx)
		d := fleetRegistry.Get(deviceID)
		if d == nil {
			return nil, fmt.Errorf("device %q not found", deviceID)
		}
		if d.Self {
			return json.Marshal(mcpServer.ListTools())
		}
		return fleetClient.ListTools(ctx, d)
	}
	deps.FleetExecTool = func(ctx context.Context, deviceID, toolName string, params json.RawMessage) (json.RawMessage, error) {
		if strings.HasPrefix(toolName, "fleet.") {
			return nil, fmt.Errorf("cannot execute fleet tools remotely (recursive call blocked): %s", toolName)
		}
		// Block destructive tools from fleet execution path (matches agent guardrails)
		if reason, ok := fleetBlockedTools[toolName]; ok {
			return nil, fmt.Errorf("fleet.exec_tool: %s is blocked (%s)", toolName, reason)
		}
		fleetEnsureDiscovery(ctx)
		d := fleetRegistry.Get(deviceID)
		if d == nil {
			return nil, fmt.Errorf("device %q not found", deviceID)
		}
		if d.Self {
			result, err := mcpServer.ExecuteTool(ctx, toolName, params)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		}
		return fleetClient.CallTool(ctx, d, toolName, params)
	}
}
