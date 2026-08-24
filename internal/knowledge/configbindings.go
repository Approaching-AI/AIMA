package knowledge

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var configBindingEnvNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ApplyConfigBindings transports final, fit-adjusted AIMA config values into
// engine-specific runtime inputs. The input environment is never mutated.
func ApplyConfigBindings(config map[string]any, env map[string]string, bindings map[string]ConfigBinding) (map[string]string, error) {
	out := cloneStringMap(env)
	if out == nil {
		out = make(map[string]string)
	}
	for key, binding := range bindings {
		transport := strings.ToLower(strings.TrimSpace(binding.Transport))
		if transport != "env" {
			return nil, fmt.Errorf("config binding %q: unsupported transport %q", key, binding.Transport)
		}
		target := strings.TrimSpace(binding.Target)
		if !configBindingEnvNameRE.MatchString(target) {
			return nil, fmt.Errorf("config binding %q: invalid environment target %q", key, binding.Target)
		}
		value, ok := config[key]
		if !ok {
			continue
		}
		formatted, err := formatConfigBindingValue(value, binding)
		if err != nil {
			return nil, fmt.Errorf("config binding %q: %w", key, err)
		}
		out[target] = formatted
	}
	return out, nil
}

func formatConfigBindingValue(value any, binding ConfigBinding) (string, error) {
	if binding.TrueValue != "" || binding.FalseValue != "" {
		boolean, ok := bindingBool(value)
		if !ok {
			return "", fmt.Errorf("boolean mapping requires a boolean value, got %T", value)
		}
		if boolean {
			return binding.TrueValue, nil
		}
		return binding.FalseValue, nil
	}
	switch value := value.(type) {
	case string:
		return value, nil
	case bool:
		return strconv.FormatBool(value), nil
	case int:
		return strconv.Itoa(value), nil
	case int32:
		return strconv.FormatInt(int64(value), 10), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case float32:
		return strconv.FormatFloat(float64(value), 'f', -1, 32), nil
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), nil
	case json.Number:
		return value.String(), nil
	default:
		return fmt.Sprint(value), nil
	}
}

func bindingBool(value any) (bool, bool) {
	switch value := value.(type) {
	case bool:
		return value, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		return parsed, err == nil
	default:
		return false, false
	}
}

func cloneConfigBindings(in map[string]ConfigBinding) map[string]ConfigBinding {
	if in == nil {
		return nil
	}
	out := make(map[string]ConfigBinding, len(in))
	for key, binding := range in {
		out[key] = binding
	}
	return out
}

func inheritConfigBindings(dst, src map[string]ConfigBinding) map[string]ConfigBinding {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]ConfigBinding, len(src))
	}
	for key, binding := range src {
		if _, exists := dst[key]; !exists {
			dst[key] = binding
		}
	}
	return dst
}
