package fleet

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/proxy"
)

func TestRegistryUpdateAndList(t *testing.T) {
	r := NewRegistry(6188)

	services := []proxy.DiscoveredService{
		{Name: "gb10._llm._tcp.local.", Host: "gb10.local", AddrV4: "192.168.1.10", Port: 6188, Info: []string{"aima=1", "models=qwen3-8b,qwen3.5-35b"}},
		{Name: "light-salt._llm._tcp.local.", Host: "light-salt.local", AddrV4: "192.168.1.20", Port: 6188, Info: []string{"aima=1"}},
	}
	r.Update(services)

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(list))
	}

	gb10 := r.Get("gb10")
	if gb10 == nil {
		t.Fatal("expected to find gb10")
	}
	if gb10.ID != "gb10" {
		t.Errorf("expected id=gb10, got %q", gb10.ID)
	}
	if gb10.AddrV4 != "192.168.1.10" {
		t.Errorf("expected addr=192.168.1.10, got %q", gb10.AddrV4)
	}
	if len(gb10.Models) != 2 || gb10.Models[0] != "qwen3-8b" {
		t.Errorf("unexpected models: %v", gb10.Models)
	}
	if !gb10.Online {
		t.Error("expected gb10 to be online")
	}
}

func TestRegistryMarkOffline(t *testing.T) {
	r := NewRegistry(6188)

	r.Update([]proxy.DiscoveredService{
		{Name: "a._llm._tcp.local.", AddrV4: "1.2.3.4", Port: 6188},
		{Name: "b._llm._tcp.local.", AddrV4: "1.2.3.5", Port: 6188},
	})

	r.Update([]proxy.DiscoveredService{
		{Name: "a._llm._tcp.local.", AddrV4: "1.2.3.4", Port: 6188},
	})

	a := r.Get("a")
	if a == nil || !a.Online {
		t.Error("expected a to be online")
	}
	b := r.Get("b")
	if b == nil || b.Online {
		t.Error("expected b to be offline")
	}
}

func TestRegistryGetNotFound(t *testing.T) {
	r := NewRegistry(6188)
	if d := r.Get("nonexistent"); d != nil {
		t.Errorf("expected nil for nonexistent device, got %+v", d)
	}
}

func TestNormalizeID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gb10._llm._tcp.local.", "gb10"},
		{"Light-Salt._llm._tcp.local.", "light-salt"},
		{"simple", "simple"},
		// macOS dns-sd escapes dots as \. in instance names
		{`guanjiaweideMacBook-Air\.local._llm._tcp.local.`, "guanjiaweidemacbook-air"},
		{`My-Host\.local._llm._tcp.local.`, "my-host"},
		// plain .local suffix without backslash escape
		{"myhost.local._llm._tcp.local.", "myhost"},
	}
	for _, tt := range tests {
		got := normalizeID(tt.input)
		if got != tt.want {
			t.Errorf("normalizeID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

type mockMCP struct {
	toolsJSON json.RawMessage
}

func (m *mockMCP) ExecuteTool(_ context.Context, name string, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"content":[{"type":"text","text":"{\"tool\":\"` + name + `\"}"}]}`), nil
}

func (m *mockMCP) ListToolDefs() json.RawMessage {
	return m.toolsJSON
}

func TestHandlerLocalTools(t *testing.T) {
	toolsJSON, _ := json.Marshal([]map[string]string{
		{"name": "hardware.detect", "description": "Detect HW"},
		{"name": "deploy.list", "description": "List deploys"},
	})
	deps := &Deps{
		MCP: &mockMCP{toolsJSON: json.RawMessage(toolsJSON)},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest("GET", "/api/v1/tools", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tools); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}
}

func TestHandlerLocalToolCall(t *testing.T) {
	deps := &Deps{MCP: &mockMCP{}}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest("POST", "/api/v1/tools/hardware.detect", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "hardware.detect") {
		t.Errorf("expected response to contain tool name, got %s", body)
	}
}

func TestHandlerListDevices(t *testing.T) {
	reg := NewRegistry(6188)
	reg.Update([]proxy.DiscoveredService{
		{Name: "test._llm._tcp.local.", AddrV4: "10.0.0.1", Port: 6188},
	})

	deps := &Deps{Registry: reg}
	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest("GET", "/api/v1/devices", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var devices []*Device
	if err := json.Unmarshal(rec.Body.Bytes(), &devices); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if len(devices) != 1 {
		t.Errorf("expected 1 device, got %d", len(devices))
	}
}

func TestHandlerDeviceNotFound(t *testing.T) {
	deps := &Deps{Registry: NewRegistry(6188)}
	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest("GET", "/api/v1/devices/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}
