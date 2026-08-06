package knowledge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResolvePortBindingsLegacy(t *testing.T) {
	bindings := ResolvePortBindings(EngineStartup{}, map[string]any{"port": 8000})
	if len(bindings) != 1 {
		t.Fatalf("len(bindings) = %d, want 1", len(bindings))
	}
	if bindings[0].Flag != "--port" {
		t.Fatalf("flag = %q, want --port", bindings[0].Flag)
	}
	if !bindings[0].Primary {
		t.Fatal("legacy binding should be primary")
	}
}

func TestResolvePortBindingsLegacyPersistedJSONNumber(t *testing.T) {
	bindings := ResolvePortBindings(EngineStartup{}, map[string]any{"port": json.Number("8080")})
	if len(bindings) != 1 {
		t.Fatalf("len(bindings) = %d, want 1", len(bindings))
	}
	if bindings[0].Port != 8080 {
		t.Fatalf("port = %d, want 8080", bindings[0].Port)
	}

	command := AppendPortBindings([]string{"llama-server"}, bindings)
	if got := strings.Join(command, " "); !strings.Contains(got, "--port 8080") {
		t.Fatalf("command = %q, missing persisted port flag", got)
	}
}

func TestResolvePortBindingsCustom(t *testing.T) {
	startup := EngineStartup{
		Ports: []StartupPort{
			{Name: "grpc-v1beta1", Flag: "--grpc_port_v1beta1", ConfigKey: "grpc_port_v1beta1"},
			{Name: "grpc", Flag: "--grpc_port", ConfigKey: "grpc_port"},
			{Name: "http", Flag: "--http_port", ConfigKey: "port", Primary: true},
		},
	}
	config := map[string]any{
		"grpc_port_v1beta1": 32108,
		"grpc_port":         32109,
		"port":              32110,
	}

	bindings := ResolvePortBindings(startup, config)
	if len(bindings) != 3 {
		t.Fatalf("len(bindings) = %d, want 3", len(bindings))
	}
	if bindings[2].Port != 32110 {
		t.Fatalf("http port = %d, want 32110", bindings[2].Port)
	}
	if !bindings[2].Primary {
		t.Fatal("http binding should be primary")
	}
	if bindings[0].Primary || bindings[1].Primary {
		t.Fatalf("only explicit primary binding should be primary, got %+v", bindings)
	}
}

func TestResolvePortBindingsExplicitPrimaryWinsOverFirstBinding(t *testing.T) {
	bindings := ResolvePortBindingsFromSpecs([]StartupPort{
		{Name: "grpc", Flag: "--grpc_port", ConfigKey: "grpc_port"},
		{Name: "http", Flag: "--http_port", ConfigKey: "port", Primary: true},
	}, map[string]any{
		"grpc_port": 32001,
		"port":      32002,
	})

	if len(bindings) != 2 {
		t.Fatalf("len(bindings) = %d, want 2", len(bindings))
	}
	if bindings[0].Primary {
		t.Fatalf("first binding should not become primary when explicit primary exists: %+v", bindings)
	}
	if !bindings[1].Primary {
		t.Fatalf("explicit primary binding should stay primary: %+v", bindings)
	}
}

func TestAppendPortBindings(t *testing.T) {
	command := []string{"./start_server.sh", "--target_voices", "AIBC006_lite"}
	command = AppendPortBindings(command, []PortBinding{
		{Name: "grpc-v1beta1", Flag: "--grpc_port_v1beta1", ConfigKey: "grpc_port_v1beta1", Port: 32108},
		{Name: "http", Flag: "--http_port", ConfigKey: "port", Port: 32110, Primary: true},
	})
	got := strings.Join(command, " ")
	if !strings.Contains(got, "--grpc_port_v1beta1 32108") {
		t.Fatalf("command = %q, missing gRPC v1beta1 port flag", got)
	}
	if !strings.Contains(got, "--http_port 32110") {
		t.Fatalf("command = %q, missing http port flag", got)
	}
}
