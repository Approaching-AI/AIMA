package knowledge

import "testing"

func TestApplyConfigBindings(t *testing.T) {
	original := map[string]string{
		"GPU_MEMORY_UTILIZATION": "0.9465",
		"UNCHANGED":              "yes",
	}
	bindings := map[string]ConfigBinding{
		"gpu_memory_utilization": {
			Transport: "env",
			Target:    "GPU_MEMORY_UTILIZATION",
		},
		"dspark_enabled": {
			Transport:  "env",
			Target:     "MODE",
			TrueValue:  "dspark",
			FalseValue: "mtp0",
		},
		"dspark_tokens": {
			Transport: "env",
			Target:    "DSPARK_TOKENS",
		},
	}

	got, err := ApplyConfigBindings(map[string]any{
		"gpu_memory_utilization": 0.41,
		"dspark_enabled":         false,
		"dspark_tokens":          5,
	}, original, bindings)
	if err != nil {
		t.Fatalf("ApplyConfigBindings() error = %v", err)
	}
	if got["GPU_MEMORY_UTILIZATION"] != "0.41" {
		t.Fatalf("GPU_MEMORY_UTILIZATION = %q, want 0.41", got["GPU_MEMORY_UTILIZATION"])
	}
	if got["MODE"] != "mtp0" {
		t.Fatalf("MODE = %q, want mtp0", got["MODE"])
	}
	if got["DSPARK_TOKENS"] != "5" {
		t.Fatalf("DSPARK_TOKENS = %q, want 5", got["DSPARK_TOKENS"])
	}
	if original["GPU_MEMORY_UTILIZATION"] != "0.9465" {
		t.Fatalf("input environment was mutated: %#v", original)
	}
}

func TestApplyConfigBindingsNoBindingsPreservesEnvironment(t *testing.T) {
	original := map[string]string{"EXISTING": "value"}
	got, err := ApplyConfigBindings(nil, original, nil)
	if err != nil {
		t.Fatalf("ApplyConfigBindings() error = %v", err)
	}
	if got["EXISTING"] != "value" {
		t.Fatalf("EXISTING = %q, want value", got["EXISTING"])
	}
	got["EXISTING"] = "changed"
	if original["EXISTING"] != "value" {
		t.Fatal("input environment was mutated")
	}
}

func TestApplyConfigBindingsRejectsInvalidDeclarations(t *testing.T) {
	tests := []struct {
		name    string
		binding ConfigBinding
	}{
		{
			name: "unsupported transport",
			binding: ConfigBinding{
				Transport: "flag",
				Target:    "GPU_MEMORY_UTILIZATION",
			},
		},
		{
			name: "invalid environment target",
			binding: ConfigBinding{
				Transport: "env",
				Target:    "INVALID-NAME",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ApplyConfigBindings(
				map[string]any{"value": true},
				nil,
				map[string]ConfigBinding{"value": test.binding},
			)
			if err == nil {
				t.Fatal("ApplyConfigBindings() error = nil")
			}
		})
	}
}
