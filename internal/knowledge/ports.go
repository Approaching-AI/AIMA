package knowledge

import (
	"fmt"
	"strings"
)

// PortBinding is a resolved startup port with a concrete port number.
type PortBinding struct {
	Name      string `json:"name"`
	Flag      string `json:"flag"`
	ConfigKey string `json:"config_key"`
	Port      int    `json:"port"`
	Primary   bool   `json:"primary,omitempty"`
}

func (p StartupPort) effectiveConfigKey() string {
	if p.ConfigKey != "" {
		return p.ConfigKey
	}
	if p.Primary || p.Name == "" || p.Name == "http" {
		return "port"
	}
	return strings.ReplaceAll(p.Name, "-", "_")
}

func (p StartupPort) effectiveFlag() string {
	if p.Flag != "" {
		return p.Flag
	}
	return "--" + strings.ReplaceAll(p.effectiveConfigKey(), "_", "-")
}

// ResolvePortBindings returns resolved engine port bindings derived from YAML
// metadata plus the merged config map. Legacy engines without startup.ports
// synthesize a single primary "--port" binding when config["port"] exists.
func ResolvePortBindings(startup EngineStartup, config map[string]any) []PortBinding {
	return ResolvePortBindingsFromSpecs(startup.Ports, config)
}

// ResolvePortBindingsFromSpecs resolves concrete port bindings from startup
// port specs plus the merged config map. Port values remain sourced from config.
func ResolvePortBindingsFromSpecs(specs []StartupPort, config map[string]any) []PortBinding {
	if len(specs) == 0 {
		if port := configPortValue(config, "port"); port > 0 {
			return []PortBinding{{
				Name:      "http",
				Flag:      "--port",
				ConfigKey: "port",
				Port:      port,
				Primary:   true,
			}}
		}
		return nil
	}

	bindings := make([]PortBinding, 0, len(specs))
	explicitPrimary := false
	for _, spec := range specs {
		if spec.Primary {
			explicitPrimary = true
			break
		}
	}
	primarySeen := false
	for i, p := range specs {
		configKey := p.effectiveConfigKey()
		binding := PortBinding{
			Name:      p.Name,
			Flag:      p.effectiveFlag(),
			ConfigKey: configKey,
			Port:      configPortValue(config, configKey),
			Primary:   p.Primary,
		}
		if !primarySeen && (binding.Primary || (!explicitPrimary && i == 0)) {
			binding.Primary = true
			primarySeen = true
		}
		bindings = append(bindings, binding)
	}
	return bindings
}

// PortConfigKeys returns the config keys occupied by knowledge-driven ports.
func PortConfigKeys(specs []StartupPort) map[string]struct{} {
	if len(specs) == 0 {
		return map[string]struct{}{"port": {}}
	}
	keys := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		keys[spec.effectiveConfigKey()] = struct{}{}
	}
	return keys
}

// PrimaryPort returns the primary externally-addressable port.
func PrimaryPort(bindings []PortBinding) int {
	for _, binding := range bindings {
		if binding.Primary {
			return binding.Port
		}
	}
	if len(bindings) > 0 {
		return bindings[0].Port
	}
	return 0
}

// PrimaryPortOrDefault returns the primary port from config, or fallback when
// no port is configured.
func PrimaryPortOrDefault(specs []StartupPort, config map[string]any, fallback int) int {
	port := PrimaryPort(ResolvePortBindingsFromSpecs(specs, config))
	if port > 0 {
		return port
	}
	return fallback
}

// AppendPortBindings appends exact port flags unless the command already
// contains the corresponding flag. Flags are appended in YAML order.
func AppendPortBindings(command []string, bindings []PortBinding) []string {
	if len(bindings) == 0 {
		return command
	}
	existing := make(map[string]struct{}, len(command))
	for _, arg := range command {
		if strings.HasPrefix(arg, "--") {
			existing[arg] = struct{}{}
		}
	}
	for _, binding := range bindings {
		if binding.Port <= 0 || binding.Flag == "" {
			continue
		}
		if _, ok := existing[binding.Flag]; ok {
			continue
		}
		command = append(command, binding.Flag, fmt.Sprintf("%d", binding.Port))
	}
	return command
}

func configPortValue(config map[string]any, key string) int {
	if config == nil || key == "" {
		return 0
	}
	switch v := config[key].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	}
	return 0
}
