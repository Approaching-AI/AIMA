package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"
	"unicode"
)

type reconcilerSourceKey struct{}
type reconcilerClaimKey struct{}

// WithReconcilerSource marks an internal deployment apply as recovery-controller work.
func WithReconcilerSource(ctx context.Context) context.Context {
	return context.WithValue(ctx, reconcilerSourceKey{}, true)
}

// WithReconcilerClaim carries the exact durable Intent revision committed by
// the controller. The private context key keeps the claim off the public MCP
// schema while allowing the deploy path to reject stale recovery work.
func WithReconcilerClaim(ctx context.Context, intent Intent) context.Context {
	ctx = WithReconcilerSource(ctx)
	return context.WithValue(ctx, reconcilerClaimKey{}, cloneIntent(intent))
}

// ReconcilerClaim returns a defensive copy of the controller's committed claim.
func ReconcilerClaim(ctx context.Context) (Intent, bool) {
	intent, ok := ctx.Value(reconcilerClaimKey{}).(Intent)
	if !ok {
		return Intent{}, false
	}
	return cloneIntent(intent), true
}

// IsReconcilerSource reports whether a deployment apply originated in the recovery controller.
func IsReconcilerSource(ctx context.Context) bool {
	if _, ok := ReconcilerClaim(ctx); ok {
		return true
	}
	marked, _ := ctx.Value(reconcilerSourceKey{}).(bool)
	return marked
}

const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"

	StateHealthy     = "healthy"
	StateWaiting     = "waiting"
	StateRecovering  = "recovering"
	StateQuarantined = "quarantined"
)

// Intent is the durable desired state and recovery state for one deployment.
type Intent struct {
	Name, Model, EngineAsset, EngineVersion, Slot, Runtime string
	Revision                                               int64
	Config                                                 map[string]any
	DesiredState, RecoveryState                            string
	Policy                                                 Policy
	AttemptCount, ConsecutiveFailureCount                  int
	ObservedRestartCount                                   int
	WindowStartedAt, NextAttemptAt, HealthySince           time.Time
	LastExitCode                                           *int
	LastError                                              string
	CreatedAt, UpdatedAt                                   time.Time
}

// SanitizeConfig returns a deep copy of config with credentials redacted.
func SanitizeConfig(config map[string]any) map[string]any {
	sanitized, err := SanitizeConfigChecked(config)
	if err != nil {
		return map[string]any{}
	}
	for key, value := range config {
		if !sensitiveConfigKey(key) && jsonScalar(value) {
			sanitized[key] = value
		}
	}
	return sanitized
}

func jsonScalar(value any) bool {
	switch value.(type) {
	case nil, bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number:
		return true
	default:
		return false
	}
}

// SanitizeConfigChecked canonicalizes config through JSON before redacting it.
// It returns an error for values that cannot be represented safely as JSON.
func SanitizeConfigChecked(config map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	canonical, err := decodeConfigJSON(encoded)
	if err != nil {
		return nil, err
	}
	return sanitizeCanonicalConfig(canonical), nil
}

func decodeConfigJSON(encoded []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var config map[string]any
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("unexpected trailing JSON value")
		}
		return nil, err
	}
	return config, nil
}

func sanitizeCanonicalConfig(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	copy := make(map[string]any, len(config))
	for key, value := range config {
		if sensitiveConfigKey(key) {
			copy[key] = "[REDACTED]"
			continue
		}
		copy[key] = sanitizeCanonicalValue(value)
	}
	return copy
}

func sanitizeCanonicalValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return sanitizeCanonicalConfig(typed)
	case []any:
		copy := make([]any, len(typed))
		for i, item := range typed {
			copy[i] = sanitizeCanonicalValue(item)
		}
		return copy
	default:
		return value
	}
}

func sensitiveConfigKey(key string) bool {
	tokens := configKeyTokens(key)
	for i, token := range tokens {
		switch token {
		case "apikey", "accesskey", "privatekey", "password", "passwd", "secret", "credential", "bearer", "authorization":
			return true
		case "token":
			if (i == 0 || tokens[i-1] != "max") && (i == len(tokens)-1 || tokens[i+1] != "count") {
				return true
			}
		}
		if i+1 < len(tokens) {
			if (token == "api" || token == "access" || token == "private") && tokens[i+1] == "key" {
				return true
			}
			if token == "pass" && tokens[i+1] == "word" {
				return true
			}
		}
	}
	return false
}

func configKeyTokens(key string) []string {
	runes := []rune(key)
	tokens := make([]string, 0, 3)
	word := make([]rune, 0, len(runes))
	flush := func() {
		if len(word) == 0 {
			return
		}
		tokens = append(tokens, string(word))
		word = word[:0]
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(word) > 0 && unicode.IsUpper(r) {
			previous := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(previous) || unicode.IsDigit(previous) || (unicode.IsUpper(previous) && nextLower) {
				flush()
			}
		}
		word = append(word, unicode.ToLower(r))
	}
	flush()
	return tokens
}

var (
	credentialAssignment = regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:api[_.-]?key|access[_.-]?key(?:[_.-]?id)?|private[_.-]?key|password|passwd|secret|credential|bearer|authorization)|(?:[a-z0-9]+[_.-])?token)\s*([=:])\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	bearerCredential     = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;]+`)
)

// SanitizeText redacts credential assignments and bearer credentials.
func SanitizeText(text string) string {
	text = bearerCredential.ReplaceAllString(text, "Bearer [REDACTED]")
	return credentialAssignment.ReplaceAllString(text, "$1$2[REDACTED]")
}
