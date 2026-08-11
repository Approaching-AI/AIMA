package inferencehttp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

type AdapterContext struct {
	Command    []string
	ModelPath  string
	Config     map[string]any
	InstanceID string
}

type AdapterContextResolver func(ctx context.Context, deploymentName string) (AdapterContext, error)
type ProbeRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

type PreparedRequest struct {
	Body   []byte
	Finish func(success bool)
}

type RequestPreparer func(
	ctx context.Context,
	path string,
	contentType string,
	model string,
	upstreamModel string,
	engineType string,
	deploymentName string,
	body []byte,
) (PreparedRequest, error)

type AdapterError struct {
	status    int
	errorType string
	err       error
}

func (e *AdapterError) Error() string {
	if e == nil || e.err == nil {
		return "request adapter error"
	}
	return e.err.Error()
}

func (e *AdapterError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *AdapterError) HTTPStatus() int {
	if e == nil || e.status == 0 {
		return 502
	}
	return e.status
}

func (e *AdapterError) ErrorType() string {
	if e == nil || e.errorType == "" {
		return "backend_adapter_error"
	}
	return e.errorType
}

func invalidRequestError(format string, args ...any) error {
	return &AdapterError{
		status:    400,
		errorType: "invalid_request",
		err:       fmt.Errorf(format, args...),
	}
}

func backendAdapterError(format string, args ...any) error {
	return &AdapterError{
		status:    502,
		errorType: "backend_adapter_error",
		err:       fmt.Errorf(format, args...),
	}
}

type exactContextState struct {
	mu           sync.Mutex
	hasCommitted bool
	tokenIDs     []uint32
	padding      string
}

type exactContextStates struct {
	mu      sync.Mutex
	states  map[string]*exactContextState
	current map[string]string
}

func (s *exactContextStates) state(key, instanceID string) *exactContextState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = make(map[string]*exactContextState)
		s.current = make(map[string]string)
	}
	stateKey := key + "\x00" + instanceID
	if previous := s.current[key]; previous != "" && previous != stateKey {
		delete(s.states, previous)
	}
	s.current[key] = stateKey
	if state := s.states[stateKey]; state != nil {
		return state
	}
	state := &exactContextState{}
	s.states[stateKey] = state
	return state
}

// RequestBodyPreparer combines existing static catalog patches with optional
// probe-backed engine request adaptation. A successful adapted request owns a
// per-deployment lease until its Finish callback is invoked.
func RequestBodyPreparer(cat CatalogReader, resolve AdapterContextResolver, run ProbeRunner) RequestPreparer {
	if cat == nil {
		return nil
	}
	if run == nil {
		run = runProbeCommand
	}
	states := &exactContextStates{}

	return func(ctx context.Context, path, contentType, model, upstreamModel, engineType, deploymentName string, body []byte) (PreparedRequest, error) {
		noop := func(bool) {}
		if !isJSONContentType(contentType) {
			return PreparedRequest{Body: body, Finish: noop}, nil
		}
		body = applyStaticRequestRewrites(cat, path, model, engineType, body)
		adapter := cat.RequestAdapter(engineType)
		if adapter == nil || adapter.Kind == "" || adapter.Path != path {
			if upstreamModel != "" && upstreamModel != model {
				body = rewriteRequestModel(body, upstreamModel)
			}
			return PreparedRequest{Body: body, Finish: noop}, nil
		}
		if adapter.Kind != "exact_context" {
			return PreparedRequest{}, backendAdapterError("unsupported request adapter kind %q", adapter.Kind)
		}
		if resolve == nil {
			return PreparedRequest{}, backendAdapterError("request adapter runtime context resolver is unavailable")
		}

		request, err := decodeRequestObject(body)
		if err != nil {
			return PreparedRequest{}, invalidRequestError("invalid JSON request body")
		}
		request["model"] = adapter.UpstreamModel
		body, err = json.Marshal(request)
		if err != nil {
			return PreparedRequest{}, invalidRequestError("encode JSON request body")
		}

		runtimeContext, err := resolve(ctx, deploymentName)
		if err != nil {
			return PreparedRequest{}, backendAdapterError("resolve native request adapter context: %v", err)
		}
		if len(runtimeContext.Command) == 0 || strings.TrimSpace(runtimeContext.Command[0]) == "" {
			return PreparedRequest{}, backendAdapterError("native request adapter executable is unavailable")
		}
		if strings.TrimSpace(runtimeContext.ModelPath) == "" {
			return PreparedRequest{}, backendAdapterError("native request adapter model path is unavailable")
		}
		contextTokens, ok := positiveConfigInt(runtimeContext.Config[adapter.ContextConfigKey])
		if !ok {
			return PreparedRequest{}, backendAdapterError("native request adapter config %q is missing or invalid", adapter.ContextConfigKey)
		}

		stateKey := strings.TrimSpace(deploymentName)
		if stateKey == "" {
			stateKey = strings.ToLower(strings.TrimSpace(engineType)) + "\x00" + strings.ToLower(strings.TrimSpace(model))
		}
		state := states.state(stateKey, runtimeContext.InstanceID)
		state.mu.Lock()
		leaseTransferred := false
		defer func() {
			if !leaseTransferred {
				state.mu.Unlock()
			}
		}()

		unpaddedIDs, err := probeRequest(ctx, run, runtimeContext, adapter, body)
		if err != nil {
			return PreparedRequest{}, backendAdapterError("native chat-template probe failed: %v", err)
		}

		candidateBody := body
		candidateIDs := unpaddedIDs
		candidatePadding := ""
		if state.hasCommitted {
			reusedBody, err := applyTransportPadding(body, adapter, state.padding)
			if err != nil {
				return PreparedRequest{}, invalidRequestError("invalid chat messages for request adapter")
			}
			reusedIDs, err := probeRequest(ctx, run, runtimeContext, adapter, reusedBody)
			if err != nil {
				return PreparedRequest{}, backendAdapterError("native chat-template probe failed during prefix verification: %v", err)
			}
			if tokenPrefix(state.tokenIDs, reusedIDs) {
				candidateBody = reusedBody
				candidateIDs = reusedIDs
				candidatePadding = state.padding
				leaseTransferred = true
				return preparedWithState(state, candidateBody, candidateIDs, candidatePadding), nil
			}
		}

		if len(unpaddedIDs) > contextTokens {
			return PreparedRequest{}, invalidRequestError("chat prompt has %d tokens, exceeding static cold context %d without a reusable prefix", len(unpaddedIDs), contextTokens)
		}
		if len(unpaddedIDs) < contextTokens {
			maxAttempts := adapter.MaxAttempts
			if maxAttempts <= 0 {
				maxAttempts = 8
			}
			markers := contextTokens - len(unpaddedIDs)
			matched := false
			for attempt := 0; attempt < maxAttempts; attempt++ {
				if markers <= 0 {
					break
				}
				padding := adapter.PaddingPrefix + strings.Repeat(adapter.PaddingUnit, markers)
				paddedBody, err := applyTransportPadding(body, adapter, padding)
				if err != nil {
					return PreparedRequest{}, invalidRequestError("invalid chat messages for request adapter")
				}
				paddedIDs, err := probeRequest(ctx, run, runtimeContext, adapter, paddedBody)
				if err != nil {
					return PreparedRequest{}, backendAdapterError("native chat-template probe failed during exact-context padding: %v", err)
				}
				if len(paddedIDs) == contextTokens {
					candidateBody = paddedBody
					candidateIDs = paddedIDs
					candidatePadding = padding
					matched = true
					break
				}
				markers += contextTokens - len(paddedIDs)
			}
			if !matched {
				return PreparedRequest{}, backendAdapterError("exact-context adapter could not reach %d tokens after %d attempts", contextTokens, maxAttempts)
			}
		}

		leaseTransferred = true
		return preparedWithState(state, candidateBody, candidateIDs, candidatePadding), nil
	}
}

func preparedWithState(state *exactContextState, body []byte, tokenIDs []uint32, padding string) PreparedRequest {
	var once sync.Once
	finish := func(success bool) {
		once.Do(func() {
			if success {
				state.hasCommitted = true
				state.tokenIDs = append([]uint32(nil), tokenIDs...)
				state.padding = padding
			}
			state.mu.Unlock()
		})
	}
	return PreparedRequest{Body: body, Finish: finish}
}

func applyStaticRequestRewrites(cat CatalogReader, path, model, engineType string, body []byte) []byte {
	for _, patch := range cat.RequestPatches(model) {
		if matchesRequestPatch(patch, path, engineType) {
			body = mergeRequestPatchBody(body, patch.Body)
		}
	}
	return stripOrphanedToolChoice(body)
}

func rewriteRequestModel(body []byte, model string) []byte {
	request, err := decodeRequestObject(body)
	if err != nil {
		return body
	}
	request["model"] = model
	out, err := json.Marshal(request)
	if err != nil {
		return body
	}
	return out
}

func decodeRequestObject(body []byte) (map[string]any, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil || request == nil {
		return nil, fmt.Errorf("invalid JSON object")
	}
	return request, nil
}

func applyTransportPadding(body []byte, adapter *RequestAdapter, padding string) ([]byte, error) {
	if padding == "" {
		return body, nil
	}
	request, err := decodeRequestObject(body)
	if err != nil {
		return nil, err
	}
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, fmt.Errorf("messages must be a non-empty array")
	}
	if first, ok := messages[0].(map[string]any); ok && first["role"] == adapter.PaddingRole {
		content, ok := first["content"].(string)
		if !ok {
			return nil, fmt.Errorf("system content must be a string")
		}
		copyFirst := make(map[string]any, len(first))
		for key, value := range first {
			copyFirst[key] = value
		}
		copyFirst["content"] = padding + "\n\n" + content
		messages = append([]any(nil), messages...)
		messages[0] = copyFirst
	} else {
		messages = append([]any{map[string]any{
			"role":    adapter.PaddingRole,
			"content": padding,
		}}, messages...)
	}
	request["messages"] = messages
	return json.Marshal(request)
}

func probeRequest(ctx context.Context, run ProbeRunner, runtimeContext AdapterContext, adapter *RequestAdapter, body []byte) ([]uint32, error) {
	args := []string{
		adapter.ProbeSubcommand,
		"--model-dir", runtimeContext.ModelPath,
		"--request-json", string(body),
	}
	if adapter.DisableThinking {
		args = append(args, "--disable-thinking")
	}
	output, err := run(ctx, runtimeContext.Command[0], args...)
	if err != nil {
		return nil, err
	}
	var result struct {
		Complete bool     `json:"complete"`
		TokenIDs []uint32 `json:"token_ids"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("decode probe output: %w", err)
	}
	if !result.Complete || len(result.TokenIDs) == 0 {
		return nil, fmt.Errorf("probe returned incomplete or empty token data")
	}
	return result.TokenIDs, nil
}

func runProbeCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if len(stderr) > 1024 {
			stderr = stderr[len(stderr)-1024:]
		}
		if stderr != "" {
			return nil, fmt.Errorf("probe exited unsuccessfully: %s", stderr)
		}
	}
	return nil, fmt.Errorf("probe exited unsuccessfully: %w", err)
}

func positiveConfigInt(value any) (int, bool) {
	var parsed int64
	switch typed := value.(type) {
	case int:
		parsed = int64(typed)
	case int32:
		parsed = int64(typed)
	case int64:
		parsed = typed
	case uint:
		if uint64(typed) > uint64(^uint(0)>>1) {
			return 0, false
		}
		parsed = int64(typed)
	case uint64:
		if typed > uint64(^uint(0)>>1) {
			return 0, false
		}
		parsed = int64(typed)
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		parsed = int64(typed)
	case json.Number:
		value, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		parsed = value
	case string:
		value, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, false
		}
		parsed = value
	default:
		return 0, false
	}
	if parsed <= 0 || int64(int(parsed)) != parsed {
		return 0, false
	}
	return int(parsed), true
}

func tokenPrefix(prefix, full []uint32) bool {
	if len(prefix) > len(full) {
		return false
	}
	for index := range prefix {
		if prefix[index] != full[index] {
			return false
		}
	}
	return true
}
