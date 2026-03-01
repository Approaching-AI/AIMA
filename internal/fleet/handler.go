package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MCPExecutor is the interface for executing MCP tools locally.
// Returns json.RawMessage to avoid mirroring mcp package types.
type MCPExecutor interface {
	ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error)
	ListToolDefs() json.RawMessage
}

// Deps holds dependencies for fleet HTTP handlers.
type Deps struct {
	Registry   *Registry
	MCP        MCPExecutor
	Client     *Client
	DeviceInfo func(ctx context.Context) (json.RawMessage, error)
}

// RegisterRoutes returns a function that registers fleet API routes on a mux.
func RegisterRoutes(deps *Deps) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		// Local device API (every AIMA instance)
		mux.HandleFunc("GET /api/v1/device", deps.handleLocalDevice)
		mux.HandleFunc("GET /api/v1/tools", deps.handleLocalTools)
		mux.HandleFunc("POST /api/v1/tools/{name}", deps.handleLocalToolCall)

		// Fleet API (manager)
		mux.HandleFunc("GET /api/v1/devices", deps.handleListDevices)
		mux.HandleFunc("GET /api/v1/devices/{id}", deps.handleGetDevice)
		mux.HandleFunc("GET /api/v1/devices/{id}/tools", deps.handleRemoteTools)
		mux.HandleFunc("POST /api/v1/devices/{id}/tools/{name}", deps.handleRemoteToolCall)
	}
}

func (d *Deps) handleLocalDevice(w http.ResponseWriter, r *http.Request) {
	if d.DeviceInfo == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	data, err := d.DeviceInfo(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, data)
}

func (d *Deps) handleLocalTools(w http.ResponseWriter, r *http.Request) {
	if d.MCP == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeRawJSON(w, http.StatusOK, d.MCP.ListToolDefs())
}

func (d *Deps) handleLocalToolCall(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "tool name is required")
		return
	}

	if d.MCP == nil {
		writeError(w, http.StatusNotImplemented, "MCP server not available")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if len(body) == 0 {
		body = []byte(`{}`)
	}

	data, err := d.MCP.ExecuteTool(r.Context(), name, json.RawMessage(body))
	if err != nil {
		if strings.Contains(err.Error(), "tool not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, data)
}

func (d *Deps) handleListDevices(w http.ResponseWriter, r *http.Request) {
	if d.Registry == nil {
		writeJSON(w, http.StatusOK, []*Device{})
		return
	}
	devices := d.Registry.List()
	writeJSON(w, http.StatusOK, devices)
}

func (d *Deps) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if d.Registry == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("device %q not found", id))
		return
	}

	device := d.Registry.Get(id)
	if device == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("device %q not found", id))
		return
	}

	if device.Self {
		d.handleLocalDevice(w, r)
		return
	}

	if d.Client == nil {
		writeJSON(w, http.StatusOK, device)
		return
	}
	data, err := d.Client.GetDeviceInfo(r.Context(), device)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("remote device %q: %s", id, err))
		return
	}
	writeRawJSON(w, http.StatusOK, data)
}

func (d *Deps) handleRemoteTools(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device := d.lookupDevice(w, id)
	if device == nil {
		return
	}

	if device.Self {
		d.handleLocalTools(w, r)
		return
	}

	if d.Client == nil {
		writeError(w, http.StatusBadGateway, "fleet client not available")
		return
	}
	data, err := d.Client.ListTools(r.Context(), device)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("remote device %q: %s", id, err))
		return
	}
	writeRawJSON(w, http.StatusOK, data)
}

func (d *Deps) handleRemoteToolCall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")

	device := d.lookupDevice(w, id)
	if device == nil {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if len(body) == 0 {
		body = []byte(`{}`)
	}

	if device.Self {
		if d.MCP == nil {
			writeError(w, http.StatusNotImplemented, "MCP server not available")
			return
		}
		data, execErr := d.MCP.ExecuteTool(r.Context(), name, json.RawMessage(body))
		if execErr != nil {
			if strings.Contains(execErr.Error(), "tool not found") {
				writeError(w, http.StatusNotFound, execErr.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, execErr.Error())
			return
		}
		writeRawJSON(w, http.StatusOK, data)
		return
	}

	if d.Client == nil {
		writeError(w, http.StatusBadGateway, "fleet client not available")
		return
	}
	data, err := d.Client.CallTool(r.Context(), device, name, json.RawMessage(body))
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("remote device %q tool %q: %s", id, name, err))
		return
	}
	writeRawJSON(w, http.StatusOK, data)
}

func (d *Deps) lookupDevice(w http.ResponseWriter, id string) *Device {
	if d.Registry == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("device %q not found", id))
		return nil
	}
	device := d.Registry.Get(id)
	if device == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("device %q not found", id))
		return nil
	}
	return device
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeRawJSON(w http.ResponseWriter, code int, data json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(data)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
