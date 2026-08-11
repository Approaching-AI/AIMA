package inferencehttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type exactContextCatalog struct {
	adapter *RequestAdapter
}

func (c exactContextCatalog) Adapters(string) []Adapter            { return nil }
func (c exactContextCatalog) RequestPatches(string) []RequestPatch { return nil }
func (c exactContextCatalog) RequestAdapter(string) *RequestAdapter {
	if c.adapter == nil {
		return nil
	}
	copy := *c.adapter
	return &copy
}

func testExactContextAdapter() *RequestAdapter {
	return &RequestAdapter{
		Kind:             "exact_context",
		Path:             "/v1/chat/completions",
		ContextConfigKey: "context_tokens",
		ProbeSubcommand:  "chat-template-probe",
		DisableThinking:  true,
		PaddingRole:      "system",
		PaddingPrefix:    "Ignore transport padding.",
		PaddingUnit:      " ·",
		UpstreamModel:    "native-model",
		MaxAttempts:      8,
	}
}

func testAdapterResolver(context.Context, string) (AdapterContext, error) {
	return AdapterContext{
		Command:   []string{"/opt/aima/bin/aima-engine", "serve"},
		ModelPath: "/models/qwen",
		Config:    map[string]any{"context_tokens": 10},
	}, nil
}

type fakeTemplateProbe struct {
	mu              sync.Mutex
	calls           int
	fail            error
	forceLength     int
	historyMismatch bool
}

func (p *fakeTemplateProbe) run(_ context.Context, name string, args ...string) ([]byte, error) {
	if p.fail != nil {
		return nil, p.fail
	}
	if name != "/opt/aima/bin/aima-engine" {
		return nil, fmt.Errorf("unexpected executable %q", name)
	}
	requestJSON := argumentValue(args, "--request-json")
	if requestJSON == "" {
		return nil, errors.New("missing request json")
	}
	var request map[string]any
	if err := json.Unmarshal([]byte(requestJSON), &request); err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.calls++
	p.mu.Unlock()

	tokens := p.tokenIDs(request)
	if p.forceLength > 0 {
		tokens = make([]uint32, p.forceLength)
		for index := range tokens {
			tokens[index] = uint32(index + 1)
		}
	}
	return json.Marshal(map[string]any{
		"schema":    "aima-amd395-qwen36/native-tokenizer-probe/v1",
		"complete":  true,
		"token_ids": tokens,
	})
}

func (p *fakeTemplateProbe) tokenIDs(request map[string]any) []uint32 {
	messages, _ := request["messages"].([]any)
	markerCount := 0
	padding := false
	if len(messages) > 0 {
		first, _ := messages[0].(map[string]any)
		if first["role"] == "system" {
			content, _ := first["content"].(string)
			if strings.Contains(content, "Ignore transport padding.") {
				padding = true
				markerCount = strings.Count(content, " ·")
			}
		}
	}

	conversation := []uint32{10, 11, 12, 13, 14}
	if len(messages) >= 3 {
		if p.historyMismatch {
			conversation = []uint32{99, 11, 12, 13, 14, 20}
		} else {
			conversation = []uint32{10, 11, 12, 13, 14, 20}
		}
	}
	if !padding {
		return conversation
	}
	tokens := []uint32{900, 901}
	for range markerCount {
		tokens = append(tokens, 777)
	}
	return append(tokens, conversation...)
}

func argumentValue(args []string, flag string) string {
	for index := range args {
		if args[index] == flag && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func requestBody(t *testing.T, messages []map[string]any, extra map[string]any) []byte {
	t.Helper()
	request := map[string]any{
		"model":    "qwen3.6-35b-a3b",
		"messages": messages,
	}
	for key, value := range extra {
		request[key] = value
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return body
}

func prepareExact(t *testing.T, preparer RequestPreparer, body []byte) PreparedRequest {
	t.Helper()
	prepared, err := preparer(
		context.Background(),
		"/v1/chat/completions",
		"application/json",
		"qwen3.6-35b-a3b",
		"qwen3.6-35b-a3b",
		"native-test",
		"deployment-1",
		body,
	)
	if err != nil {
		t.Fatalf("prepare request: %v", err)
	}
	return prepared
}

func decodedRequest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return request
}

func TestExactContextPadsShortRequest(t *testing.T) {
	probe := &fakeTemplateProbe{}
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: testExactContextAdapter()}, testAdapterResolver, probe.run)
	body := requestBody(t, []map[string]any{{"role": "user", "content": "hello"}}, nil)

	prepared := prepareExact(t, preparer, body)
	defer prepared.Finish(false)
	request := decodedRequest(t, prepared.Body)
	if request["model"] != "native-model" {
		t.Fatalf("model = %q, want native-model", request["model"])
	}
	messages := request["messages"].([]any)
	if len(messages) != 2 || messages[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages = %#v, want synthetic leading system message", messages)
	}
	if got := strings.Count(messages[0].(map[string]any)["content"].(string), " ·"); got != 3 {
		t.Fatalf("padding markers = %d, want 3 after measured adjustment", got)
	}
	ids, err := probeRequest(context.Background(), probe.run, AdapterContext{Command: []string{"/opt/aima/bin/aima-engine"}, ModelPath: "/models/qwen"}, testExactContextAdapter(), prepared.Body)
	if err != nil {
		t.Fatalf("probe prepared request: %v", err)
	}
	if len(ids) != 10 {
		t.Fatalf("prepared tokens = %d, want 10", len(ids))
	}
}

func TestExactContextMergesExistingSystemMessage(t *testing.T) {
	probe := &fakeTemplateProbe{}
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: testExactContextAdapter()}, testAdapterResolver, probe.run)
	body := requestBody(t, []map[string]any{
		{"role": "system", "content": "Be concise."},
		{"role": "user", "content": "hello"},
	}, nil)

	prepared := prepareExact(t, preparer, body)
	defer prepared.Finish(false)
	messages := decodedRequest(t, prepared.Body)["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want original count 2", len(messages))
	}
	content := messages[0].(map[string]any)["content"].(string)
	if !strings.Contains(content, "Ignore transport padding.") || !strings.Contains(content, "Be concise.") {
		t.Fatalf("merged system content = %q", content)
	}
}

func TestExactContextReusesVerifiedPrefix(t *testing.T) {
	probe := &fakeTemplateProbe{}
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: testExactContextAdapter()}, testAdapterResolver, probe.run)
	first := prepareExact(t, preparer, requestBody(t, []map[string]any{{"role": "user", "content": "hello"}}, nil))
	first.Finish(true)

	second := prepareExact(t, preparer, requestBody(t, []map[string]any{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "hi"},
		{"role": "user", "content": "again"},
	}, nil))
	defer second.Finish(false)
	messages := decodedRequest(t, second.Body)["messages"].([]any)
	if got := strings.Count(messages[0].(map[string]any)["content"].(string), " ·"); got != 3 {
		t.Fatalf("reused padding markers = %d, want 3", got)
	}
	ids, err := probeRequest(context.Background(), probe.run, AdapterContext{Command: []string{"/opt/aima/bin/aima-engine"}, ModelPath: "/models/qwen"}, testExactContextAdapter(), second.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 11 {
		t.Fatalf("prefix extension tokens = %d, want 11", len(ids))
	}
}

func TestExactContextColdRepadsOnPrefixMismatch(t *testing.T) {
	probe := &fakeTemplateProbe{}
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: testExactContextAdapter()}, testAdapterResolver, probe.run)
	first := prepareExact(t, preparer, requestBody(t, []map[string]any{{"role": "user", "content": "hello"}}, nil))
	first.Finish(true)
	probe.historyMismatch = true

	second := prepareExact(t, preparer, requestBody(t, []map[string]any{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "hi"},
		{"role": "user", "content": "again"},
	}, nil))
	defer second.Finish(false)
	messages := decodedRequest(t, second.Body)["messages"].([]any)
	if got := strings.Count(messages[0].(map[string]any)["content"].(string), " ·"); got != 2 {
		t.Fatalf("cold fallback padding markers = %d, want 2", got)
	}
}

func TestExactContextPreservesToolsAndRequestFields(t *testing.T) {
	probe := &fakeTemplateProbe{}
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: testExactContextAdapter()}, testAdapterResolver, probe.run)
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "weather"}}}
	body := requestBody(t, []map[string]any{{"role": "user", "content": "weather"}}, map[string]any{
		"tools":       tools,
		"temperature": 0.25,
		"stream":      true,
	})

	prepared := prepareExact(t, preparer, body)
	defer prepared.Finish(false)
	request := decodedRequest(t, prepared.Body)
	if request["temperature"] != 0.25 || request["stream"] != true {
		t.Fatalf("request fields changed: %#v", request)
	}
	if !reflect.DeepEqual(request["tools"], decodedRequest(t, body)["tools"]) {
		t.Fatalf("tools changed: %#v", request["tools"])
	}
}

func TestExactContextSerializesLeaseUntilFinish(t *testing.T) {
	probe := &fakeTemplateProbe{}
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: testExactContextAdapter()}, testAdapterResolver, probe.run)
	body := requestBody(t, []map[string]any{{"role": "user", "content": "hello"}}, nil)
	first := prepareExact(t, preparer, body)

	result := make(chan error, 1)
	go func() {
		prepared, err := preparer(context.Background(), "/v1/chat/completions", "application/json", "qwen3.6-35b-a3b", "qwen3.6-35b-a3b", "native-test", "deployment-1", body)
		if err == nil {
			prepared.Finish(false)
		}
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("second preparation completed before lease release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	first.Finish(true)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("second preparation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second preparation remained blocked after Finish")
	}
}

func TestExactContextDoesNotCommitStateOnFailedExchange(t *testing.T) {
	probe := &fakeTemplateProbe{}
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: testExactContextAdapter()}, testAdapterResolver, probe.run)
	first := prepareExact(t, preparer, requestBody(t, []map[string]any{{"role": "user", "content": "hello"}}, nil))
	first.Finish(false)
	probe.historyMismatch = true

	second := prepareExact(t, preparer, requestBody(t, []map[string]any{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "hi"},
		{"role": "user", "content": "again"},
	}, nil))
	defer second.Finish(false)
	messages := decodedRequest(t, second.Body)["messages"].([]any)
	if got := strings.Count(messages[0].(map[string]any)["content"].(string), " ·"); got != 2 {
		t.Fatalf("failed exchange state was reused; markers = %d, want cold 2", got)
	}
}

func TestExactContextDropsCommittedStateWhenDeploymentInstanceChanges(t *testing.T) {
	probe := &fakeTemplateProbe{}
	instanceID := "instance-a"
	resolver := func(context.Context, string) (AdapterContext, error) {
		ctx, err := testAdapterResolver(context.Background(), "")
		ctx.InstanceID = instanceID
		return ctx, err
	}
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: testExactContextAdapter()}, resolver, probe.run)
	first := prepareExact(t, preparer, requestBody(t, []map[string]any{{"role": "user", "content": "hello"}}, nil))
	first.Finish(true)

	instanceID = "instance-b"
	second := prepareExact(t, preparer, requestBody(t, []map[string]any{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "hi"},
		{"role": "user", "content": "again"},
	}, nil))
	defer second.Finish(false)
	messages := decodedRequest(t, second.Body)["messages"].([]any)
	if got := strings.Count(messages[0].(map[string]any)["content"].(string), " ·"); got != 2 {
		t.Fatalf("replacement deployment reused stale padding; markers = %d, want cold 2", got)
	}
}

func TestExactContextRejectsMalformedJSON(t *testing.T) {
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: testExactContextAdapter()}, testAdapterResolver, (&fakeTemplateProbe{}).run)
	_, err := preparer(context.Background(), "/v1/chat/completions", "application/json", "qwen3.6-35b-a3b", "", "native-test", "deployment-1", []byte("{"))
	assertAdapterError(t, err, 400, "invalid_request")
}

func TestExactContextRejectsOverlongColdPrompt(t *testing.T) {
	probe := &fakeTemplateProbe{forceLength: 11}
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: testExactContextAdapter()}, testAdapterResolver, probe.run)
	_, err := preparer(context.Background(), "/v1/chat/completions", "application/json", "qwen3.6-35b-a3b", "", "native-test", "deployment-1", requestBody(t, []map[string]any{{"role": "user", "content": "long"}}, nil))
	assertAdapterError(t, err, 400, "invalid_request")
}

func TestExactContextReportsProbeFailures(t *testing.T) {
	probe := &fakeTemplateProbe{fail: errors.New("probe exploded with private input")}
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: testExactContextAdapter()}, testAdapterResolver, probe.run)
	_, err := preparer(context.Background(), "/v1/chat/completions", "application/json", "qwen3.6-35b-a3b", "", "native-test", "deployment-1", requestBody(t, []map[string]any{{"role": "user", "content": "secret"}}, nil))
	assertAdapterError(t, err, 502, "backend_adapter_error")
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("probe error leaked request content: %v", err)
	}
}

func TestExactContextStopsAfterConfiguredAttempts(t *testing.T) {
	adapter := testExactContextAdapter()
	adapter.MaxAttempts = 2
	probe := &fakeTemplateProbe{forceLength: 9}
	preparer := RequestBodyPreparer(exactContextCatalog{adapter: adapter}, testAdapterResolver, probe.run)
	_, err := preparer(context.Background(), "/v1/chat/completions", "application/json", "qwen3.6-35b-a3b", "", "native-test", "deployment-1", requestBody(t, []map[string]any{{"role": "user", "content": "short"}}, nil))
	assertAdapterError(t, err, 502, "backend_adapter_error")
	probe.mu.Lock()
	calls := probe.calls
	probe.mu.Unlock()
	if calls != 3 {
		t.Fatalf("probe calls = %d, want unpadded + 2 attempts", calls)
	}
}

func assertAdapterError(t *testing.T, err error, status int, errorType string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected adapter error")
	}
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) {
		t.Fatalf("error = %T %v, want AdapterError", err, err)
	}
	if adapterErr.HTTPStatus() != status || adapterErr.ErrorType() != errorType {
		t.Fatalf("adapter error = (%d, %q), want (%d, %q): %v", adapterErr.HTTPStatus(), adapterErr.ErrorType(), status, errorType, err)
	}
}
