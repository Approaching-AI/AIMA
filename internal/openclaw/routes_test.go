package openclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

type staticBackendLister struct {
	backends map[string]*Backend
}

func (s staticBackendLister) ListBackends() map[string]*Backend {
	out := make(map[string]*Backend, len(s.backends))
	for k, v := range s.backends {
		cp := *v
		out[k] = &cp
	}
	return out
}

func TestHandleTTSLiteTTSRewrite(t *testing.T) {
	var (
		gotPath string
		gotBody map[string]any
	)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("Unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFF"))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"litetts-mnn": {
				ModelName:  "litetts-mnn",
				EngineType: "litetts",
				Address:    strings.TrimPrefix(backend.URL, "http://"),
				Ready:      true,
			},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(`{"model":"litetts-mnn","input":"hello","voice":"default","response_format":"mp3"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if gotPath != "/tts/api/v1/generate" {
		t.Fatalf("backend path = %q, want %q", gotPath, "/tts/api/v1/generate")
	}
	if gotBody["text"] != "hello" {
		t.Fatalf("payload text = %#v, want %q", gotBody["text"], "hello")
	}
	if gotBody["speaker"] != "AIBC006_lite" {
		t.Fatalf("payload speaker = %#v, want %q", gotBody["speaker"], "AIBC006_lite")
	}
	if gotBody["version"] != "v2.0" {
		t.Fatalf("payload version = %#v, want %q", gotBody["version"], "v2.0")
	}
	if ct := w.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("content-type = %q, want %q", ct, "audio/wav")
	}
}

func TestHandleASRMooERRewrite(t *testing.T) {
	orig := mooerRecognize
	defer func() { mooerRecognize = orig }()

	var (
		gotTarget string
		gotAudio  []byte
	)
	mooerRecognize = func(ctx context.Context, target string, audioData []byte) (*mooerRecognizeResponse, error) {
		gotTarget = target
		gotAudio = append([]byte(nil), audioData...)
		return &mooerRecognizeResponse{
			Status: mooerStatusOK,
			Text:   "hello from mooer",
		}, nil
	}

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"mooer-asr-1.5b": {
				ModelName:  "mooer-asr-1.5b",
				EngineType: "mooer-asr-musa",
				Address:    "127.0.0.1:32107",
				Ready:      true,
			},
		}},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "mooer-asr-1.5b"); err != nil {
		t.Fatalf("WriteField model: %v", err)
	}
	if err := writer.WriteField("response_format", "json"); err != nil {
		t.Fatalf("WriteField response_format: %v", err)
	}
	part, err := writer.CreateFormFile("file", "sample.wav")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("RIFFdemo")); err != nil {
		t.Fatalf("Write file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotTarget != "127.0.0.1:32107" {
		t.Fatalf("target = %q, want %q", gotTarget, "127.0.0.1:32107")
	}
	if string(gotAudio) != "RIFFdemo" {
		t.Fatalf("audio = %q, want %q", string(gotAudio), "RIFFdemo")
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if resp["text"] != "hello from mooer" {
		t.Fatalf("response text = %#v, want %q", resp["text"], "hello from mooer")
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want %q", ct, "application/json")
	}
}

func TestHandleASRMooERTextResponse(t *testing.T) {
	orig := mooerRecognize
	defer func() { mooerRecognize = orig }()

	mooerRecognize = func(ctx context.Context, target string, audioData []byte) (*mooerRecognizeResponse, error) {
		return &mooerRecognizeResponse{
			Status: mooerStatusOK,
			Text:   "plain transcript",
		}, nil
	}

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"mooer-asr-1.5b": {
				ModelName:  "mooer-asr-1.5b",
				EngineType: "mooer",
				Address:    "127.0.0.1:32107",
				Ready:      true,
			},
		}},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "mooer-asr-1.5b")
	_ = writer.WriteField("response_format", "text")
	part, err := writer.CreateFormFile("file", "sample.wav")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("RIFFdemo")); err != nil {
		t.Fatalf("Write file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := strings.TrimSpace(w.Body.String()); got != "plain transcript" {
		t.Fatalf("body = %q, want %q", got, "plain transcript")
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("content-type = %q, want %q", ct, "text/plain; charset=utf-8")
	}
}

func TestRequestBodyRewriterAppliesCatalogPatch(t *testing.T) {
	rewriter := RequestBodyRewriter(&mockCatalog{})
	body := []byte(`{"model":"qwen3.5-9b","messages":[]}`)
	out := rewriter("/v1/chat/completions", "application/json; charset=utf-8", "qwen3.5-9b", "vllm-nightly", body)
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	kwargs, ok := req["chat_template_kwargs"].(map[string]any)
	if !ok {
		t.Fatal("chat_template_kwargs not injected")
	}
	if kwargs["enable_thinking"] != false {
		t.Fatalf("enable_thinking = %v, want false", kwargs["enable_thinking"])
	}
}

func TestRequestBodyRewriterPreservesExplicitValues(t *testing.T) {
	rewriter := RequestBodyRewriter(&mockCatalog{})
	body := []byte(`{"model":"qwen3.5-9b","messages":[],"chat_template_kwargs":{"enable_thinking":true}}`)
	out := rewriter("/v1/chat/completions", "application/json", "qwen3.5-9b", "vllm-nightly", body)
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	kwargs := req["chat_template_kwargs"].(map[string]any)
	if kwargs["enable_thinking"] != true {
		t.Fatalf("enable_thinking = %v, want true", kwargs["enable_thinking"])
	}
}

func TestRequestBodyRewriterSkipsNonMatchingEngine(t *testing.T) {
	rewriter := RequestBodyRewriter(&mockCatalog{})
	body := []byte(`{"model":"qwen3.5-9b","messages":[]}`)
	out := rewriter("/v1/chat/completions", "application/json", "qwen3.5-9b", "sglang", body)
	if string(out) != string(body) {
		t.Fatalf("rewriter should leave non-matching engine untouched: %s", string(out))
	}
}

func TestStripASRPrefix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"chinese", "language Chinese<asr_text>你好，世界。", "你好，世界。"},
		{"english", "language English<asr_text>Hello world.", "Hello world."},
		{"no prefix", "just plain text", "just plain text"},
		{"empty", "", ""},
		{"marker only", "<asr_text>text", "text"},
		{"nested markers", "language Chinese<asr_text>has <asr_text> inside", "has <asr_text> inside"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripASRPrefix(tt.in)
			if got != tt.want {
				t.Errorf("stripASRPrefix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCleanASRResponse(t *testing.T) {
	in := `{"text":"language Chinese<asr_text>你好世界","usage":{"type":"duration","seconds":2}}`
	out := cleanASRResponse([]byte(in))
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp["text"] != "你好世界" {
		t.Fatalf("text = %q, want %q", resp["text"], "你好世界")
	}
	// usage should be preserved
	usage, ok := resp["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing or wrong type")
	}
	if usage["seconds"] != float64(2) {
		t.Fatalf("usage.seconds = %v, want 2", usage["seconds"])
	}
}

func TestHandleASRCleanResponse(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"language Chinese<asr_text>测试清洗","usage":{"type":"duration","seconds":1}}`))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"qwen3-asr-1.7b": {
				ModelName:  "qwen3-asr-1.7b",
				EngineType: "vllm-nightly-audio",
				Address:    strings.TrimPrefix(backend.URL, "http://"),
				Ready:      true,
			},
		}},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "qwen3-asr-1.7b")
	part, _ := writer.CreateFormFile("file", "test.wav")
	part.Write([]byte("RIFF"))
	writer.Close()

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp["text"] != "测试清洗" {
		t.Fatalf("text = %q, want %q", resp["text"], "测试清洗")
	}
}

func TestStripOrphanedToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		wantKeys []string // keys that should remain
		noKeys   []string // keys that should be stripped
	}{
		{
			name:     "tool_choice with empty tools",
			input:    map[string]any{"model": "test", "tool_choice": "auto", "tools": []any{}},
			wantKeys: []string{"model"},
			noKeys:   []string{"tool_choice", "tools"},
		},
		{
			name:     "tool_choice without tools key",
			input:    map[string]any{"model": "test", "tool_choice": "auto"},
			wantKeys: []string{"model"},
			noKeys:   []string{"tool_choice"},
		},
		{
			name:     "tool_choice with non-empty tools - keep both",
			input:    map[string]any{"model": "test", "tool_choice": "auto", "tools": []any{map[string]any{"type": "function"}}},
			wantKeys: []string{"model", "tool_choice", "tools"},
		},
		{
			name:     "no tool_choice - unchanged",
			input:    map[string]any{"model": "test", "messages": []any{}},
			wantKeys: []string{"model", "messages"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.input)
			result := stripOrphanedToolChoice(body)
			var got map[string]any
			if err := json.Unmarshal(result, &got); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			for _, key := range tt.wantKeys {
				if _, ok := got[key]; !ok {
					t.Errorf("expected key %q to be present", key)
				}
			}
			for _, key := range tt.noKeys {
				if _, ok := got[key]; ok {
					t.Errorf("expected key %q to be stripped", key)
				}
			}
		})
	}
}

func TestConsumeMooerPayloadPackedTokens(t *testing.T) {
	packed := []byte{}
	packed = protowire.AppendVarint(packed, 101)
	packed = protowire.AppendVarint(packed, 202)

	payload := []byte{}
	payload = protowire.AppendTag(payload, 1, protowire.BytesType)
	payload = protowire.AppendString(payload, "hello")
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, packed)

	text, tokens, err := consumeMooerPayload(payload)
	if err != nil {
		t.Fatalf("consumeMooerPayload: %v", err)
	}
	if text != "hello" {
		t.Fatalf("text = %q, want %q", text, "hello")
	}
	if len(tokens) != 2 || tokens[0] != 101 || tokens[1] != 202 {
		t.Fatalf("tokens = %v, want [101 202]", tokens)
	}
}
