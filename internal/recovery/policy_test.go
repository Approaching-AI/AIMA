package recovery

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDefaultPolicyMatchesProductContract(t *testing.T) {
	got := DefaultPolicy()
	want := Policy{
		Enabled: true, CheckIntervalS: 5, ConsecutiveFailures: 3,
		MaxAttempts: 3, WindowS: 600, BackoffS: []int{2, 10, 30},
		StableResetS: 600,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestResolvePolicyRejectsInvalidBounds(t *testing.T) {
	intPtr := func(value int) *int { return &value }
	tests := []struct {
		name  string
		field string
		patch PolicyPatch
	}{
		{"check interval below minimum", "check_interval_s", PolicyPatch{CheckIntervalS: intPtr(0)}},
		{"check interval above maximum", "check_interval_s", PolicyPatch{CheckIntervalS: intPtr(301)}},
		{"consecutive failures below minimum", "consecutive_failures", PolicyPatch{ConsecutiveFailures: intPtr(0)}},
		{"consecutive failures above maximum", "consecutive_failures", PolicyPatch{ConsecutiveFailures: intPtr(21)}},
		{"max attempts below minimum", "max_attempts", PolicyPatch{MaxAttempts: intPtr(0)}},
		{"max attempts above maximum", "max_attempts", PolicyPatch{MaxAttempts: intPtr(21)}},
		{"window below minimum", "window_s", PolicyPatch{WindowS: intPtr(0)}},
		{"window above maximum", "window_s", PolicyPatch{WindowS: intPtr(86401)}},
		{"stable reset below minimum", "stable_reset_s", PolicyPatch{StableResetS: intPtr(0)}},
		{"stable reset above maximum", "stable_reset_s", PolicyPatch{StableResetS: intPtr(86401)}},
		{"backoff entry below minimum", "backoff_s", PolicyPatch{BackoffS: []int{2, 0}}},
		{"backoff entry above maximum", "backoff_s", PolicyPatch{BackoffS: []int{2, 3601}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolvePolicy(DefaultPolicy(), tt.patch)
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("ResolvePolicy error = %v, want %q", err, tt.field)
			}
		})
	}
}

func TestPolicyBackoffRepeatsLastValue(t *testing.T) {
	p := DefaultPolicy()
	p.BackoffS = []int{4}
	if got := p.Backoff(3); got != 4*time.Second {
		t.Fatalf("got %s", got)
	}
}

func TestResolvePolicyAppliesPatchAndCopiesBackoff(t *testing.T) {
	maxAttempts := 5
	backoff := []int{4, 8}
	got, err := ResolvePolicy(DefaultPolicy(), PolicyPatch{
		MaxAttempts: &maxAttempts,
		BackoffS:    backoff,
	})
	if err != nil {
		t.Fatalf("ResolvePolicy: %v", err)
	}
	if got.MaxAttempts != 5 {
		t.Fatalf("max attempts = %d, want 5", got.MaxAttempts)
	}
	if !reflect.DeepEqual(got.BackoffS, []int{4, 8}) {
		t.Fatalf("backoff = %v, want [4 8]", got.BackoffS)
	}
	backoff[0] = 99
	if got.BackoffS[0] != 4 {
		t.Fatalf("resolved backoff aliases patch: %v", got.BackoffS)
	}
}

func TestResolvePolicyReturnsIndependentBackoff(t *testing.T) {
	base := DefaultPolicy()
	got, err := ResolvePolicy(base)
	if err != nil {
		t.Fatalf("ResolvePolicy: %v", err)
	}
	got.BackoffS[0] = 99
	if base.BackoffS[0] != 2 {
		t.Fatalf("resolved backoff aliases base: %v", base.BackoffS)
	}
}

func TestSanitizeConfigRedactsGenericPrefixedKeysInTypedContainers(t *testing.T) {
	typedMap := map[string]string{"openai_api_key": "key-secret", "mode": "fast"}
	typedSlice := []map[string]string{{"github_token": "token-secret"}}
	config := map[string]any{
		"typed_map":   typedMap,
		"typed_slice": typedSlice,
	}

	got := SanitizeConfig(config)
	if !reflect.DeepEqual(config["typed_map"], typedMap) || !reflect.DeepEqual(config["typed_slice"], typedSlice) {
		t.Fatalf("SanitizeConfig mutated caller config: %#v", config)
	}
	gotMap, ok := got["typed_map"].(map[string]any)
	if !ok || gotMap["openai_api_key"] != "[REDACTED]" || gotMap["mode"] != "fast" {
		t.Fatalf("typed map = %#v", got["typed_map"])
	}
	gotSlice, ok := got["typed_slice"].([]any)
	if !ok || len(gotSlice) != 1 {
		t.Fatalf("typed slice = %#v", got["typed_slice"])
	}
	item, ok := gotSlice[0].(map[string]any)
	if !ok || item["github_token"] != "[REDACTED]" {
		t.Fatalf("typed slice item = %#v", gotSlice[0])
	}
}

func TestSanitizeTextRedactsCredentialAssignmentsAndBearerTokens(t *testing.T) {
	got := SanitizeText("restart failed: api_key=key-secret; password: pass-secret; credential=\"quoted-secret still-secret\"; Authorization: Bearer bearer-secret")
	for _, secret := range []string{"key-secret", "pass-secret", "quoted-secret", "still-secret", "bearer-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("SanitizeText leaked %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("SanitizeText did not redact: %q", got)
	}
}

func TestSanitizeConfigCanonicalizesIntegerMapsStructsAndArrays(t *testing.T) {
	type taggedConfig struct {
		APIKey string `json:"api_key"`
		Mode   string `json:"mode"`
	}
	config := map[string]any{
		"integer_map": map[int]map[string]string{7: {"refresh_token": "integer-map-secret"}},
		"struct":      taggedConfig{APIKey: "struct-secret", Mode: "fast"},
		"array":       [2]map[string]string{{"access_key": "array-secret"}, {"mode": "safe"}},
	}

	got := SanitizeConfig(config)
	integerMap, ok := got["integer_map"].(map[string]any)
	if !ok {
		t.Fatalf("integer map = %#v", got["integer_map"])
	}
	integerValue, ok := integerMap["7"].(map[string]any)
	if !ok || integerValue["refresh_token"] != "[REDACTED]" {
		t.Fatalf("integer map value = %#v", integerMap["7"])
	}
	structValue, ok := got["struct"].(map[string]any)
	if !ok || structValue["api_key"] != "[REDACTED]" || structValue["mode"] != "fast" {
		t.Fatalf("struct = %#v", got["struct"])
	}
	array, ok := got["array"].([]any)
	if !ok || len(array) != 2 {
		t.Fatalf("array = %#v", got["array"])
	}
	arrayItem, ok := array[0].(map[string]any)
	if !ok || arrayItem["access_key"] != "[REDACTED]" {
		t.Fatalf("array item = %#v", array[0])
	}
}

func TestSanitizeConfigRedactsCredentialKeyTokensWithoutOverRedactingSettings(t *testing.T) {
	got := SanitizeConfig(map[string]any{
		"bearer":        "bearer-secret",
		"access_key":    "access-secret",
		"access_key_id": "access-id-secret",
		"max_tokens":    128,
		"tokenizer":     "model-tokenizer",
		"token_count":   64,
	})
	for _, key := range []string{"bearer", "access_key", "access_key_id"} {
		if got[key] != "[REDACTED]" {
			t.Fatalf("%s = %#v", key, got[key])
		}
	}
	if got["max_tokens"] != 128 || got["tokenizer"] != "model-tokenizer" || got["token_count"] != 64 {
		t.Fatalf("non-sensitive token settings = %#v", got)
	}
}

func TestSanitizeConfigCheckedRejectsCycles(t *testing.T) {
	config := map[string]any{}
	config["self"] = config
	got, err := SanitizeConfigChecked(config)
	if err == nil {
		t.Fatalf("SanitizeConfigChecked cycle = %#v, want error", got)
	}
}

func TestSanitizeConfigFailsClosedForUnsupportedDirectInput(t *testing.T) {
	got := SanitizeConfig(map[string]any{"secret": make(chan int)})
	if len(got) != 0 {
		t.Fatalf("SanitizeConfig returned unsupported caller value: %#v", got)
	}
}

func TestSanitizeConfigCheckedPreservesLargeIntegerJSONToken(t *testing.T) {
	const want = uint64(9007199254740993)
	got, err := SanitizeConfigChecked(map[string]any{"large_integer": want})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"large_integer":9007199254740993}` {
		t.Fatalf("sanitized JSON = %s", encoded)
	}
}
