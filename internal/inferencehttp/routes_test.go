package inferencehttp

import (
	"bytes"
	"context"
	"encoding/base64"
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

type staticCatalog struct {
	adapters map[string][]Adapter
}

func (s staticCatalog) RequestPatches(string) []RequestPatch  { return nil }
func (s staticCatalog) RequestAdapter(string) *RequestAdapter { return nil }
func (s staticCatalog) Adapters(name string) []Adapter {
	return append([]Adapter(nil), s.adapters[name]...)
}

type mockCatalog struct{}

func (m *mockCatalog) Adapters(string) []Adapter             { return nil }
func (m *mockCatalog) RequestAdapter(string) *RequestAdapter { return nil }

func (m *mockCatalog) RequestPatches(name string) []RequestPatch {
	if name != "qwen3.5-9b" {
		return nil
	}
	return []RequestPatch{{
		Path:           "/v1/chat/completions",
		EnginePrefixes: []string{"vllm"},
		Body: map[string]any{
			"chat_template_kwargs": map[string]any{
				"enable_thinking": false,
			},
		},
	}}
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
				EngineType: "local-tts",
				Address:    strings.TrimPrefix(backend.URL, "http://"),
				Ready:      true,
			},
		}},
		Catalog: staticCatalog{adapters: map[string][]Adapter{
			"litetts-mnn": []Adapter{{Path: "/v1/audio/speech", Kind: adapterLiteTTSHTTP}},
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

func TestHandleTTSLiteTTSRewriteCustomPath(t *testing.T) {
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
				EngineType: "local-tts",
				Address:    strings.TrimPrefix(backend.URL, "http://"),
				Ready:      true,
			},
		}},
		Catalog: staticCatalog{adapters: map[string][]Adapter{
			"litetts-mnn": []Adapter{{Path: "/v1/tts", Kind: adapterLiteTTSHTTP}},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/tts", strings.NewReader(`{"model":"litetts-mnn","text":"hello","voice":"default","response_format":"wav"}`))
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
}

func TestHandleTTSProxyNormalizesSpeechTextAlias(t *testing.T) {
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
			"qwen3-tts-0.6b": {
				ModelName:  "qwen3-tts-0.6b",
				EngineType: "qwen-tts-fastapi-cuda",
				Address:    strings.TrimPrefix(backend.URL, "http://"),
				Ready:      true,
			},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(`{"model":"qwen3-tts-0.6b","text":"hello","voice":"default"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if gotPath != "/v1/audio/speech" {
		t.Fatalf("backend path = %q, want %q", gotPath, "/v1/audio/speech")
	}
	if gotBody["input"] != "hello" {
		t.Fatalf("payload input = %#v, want %q", gotBody["input"], "hello")
	}
}

func TestHandleTTSProxyCustomPathPassesReferenceFields(t *testing.T) {
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
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"audio_base64":"UklGRg==","format":"wav"}`))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"qwen3-tts-0.6b": {
				ModelName:  "qwen3-tts-0.6b",
				EngineType: "qwen-tts-fastapi-cuda",
				Address:    strings.TrimPrefix(backend.URL, "http://"),
				Ready:      true,
			},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/tts", strings.NewReader(`{"model":"qwen3-tts-0.6b","input":"hello","voice":"default","reference_audio":"file:///tmp/ref.wav","reference_text":"你好"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotPath != "/v1/tts" {
		t.Fatalf("backend path = %q, want %q", gotPath, "/v1/tts")
	}
	if gotBody["text"] != "hello" {
		t.Fatalf("payload text = %#v, want %q", gotBody["text"], "hello")
	}
	if gotBody["reference_audio"] != "file:///tmp/ref.wav" {
		t.Fatalf("payload reference_audio = %#v, want %q", gotBody["reference_audio"], "file:///tmp/ref.wav")
	}
	if gotBody["reference_text"] != "你好" {
		t.Fatalf("payload reference_text = %#v, want %q", gotBody["reference_text"], "你好")
	}
}

func TestHandleTTSProxyAcceptsReferenceAudioBeyondLegacyBodyLimit(t *testing.T) {
	var gotBody map[string]any

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("Unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"audio_base64":"UklGRg==","format":"wav"}`))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"qwen3-tts-0.6b": {
				ModelName:  "qwen3-tts-0.6b",
				EngineType: "qwen-tts-fastapi-cuda",
				Address:    strings.TrimPrefix(backend.URL, "http://"),
				Ready:      true,
			},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	referenceAudio := "data:audio/wav;base64," + strings.Repeat("A", (1<<20)+128)
	req := httptest.NewRequest(http.MethodPost, "/v1/tts", strings.NewReader(`{"model":"qwen3-tts-0.6b","text":"hello","reference_audio":"`+referenceAudio+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotBody["reference_audio"] != referenceAudio {
		t.Fatalf("payload reference_audio length = %d, want %d", len(gotBody["reference_audio"].(string)), len(referenceAudio))
	}
}

func TestHandleTTSJSONFallsBackToSpeechAndWrapsAudio(t *testing.T) {
	var (
		paths   []string
		gotBody map[string]any
	)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if r.URL.Path == "/v1/tts" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
			return
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("Unmarshal fallback body: %v", err)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFFdemo"))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"voxcpm2": {
				ModelName: "voxcpm2",
				Address:   strings.TrimPrefix(backend.URL, "http://"),
				Ready:     true,
			},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/tts", strings.NewReader(`{"model":"voxcpm2","text":"hello","response_format":"wav"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if strings.Join(paths, ",") != "/v1/tts,/v1/audio/speech" {
		t.Fatalf("paths = %v, want fallback from /v1/tts to /v1/audio/speech", paths)
	}
	if gotBody["input"] != "hello" {
		t.Fatalf("fallback input = %#v, want hello", gotBody["input"])
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if resp["audio_base64"] != "UklGRmRlbW8=" {
		t.Fatalf("audio_base64 = %#v, want encoded RIFFdemo", resp["audio_base64"])
	}
	if resp["format"] != "wav" {
		t.Fatalf("format = %#v, want wav", resp["format"])
	}
}

func TestHandleTTSVoxCPMCloneJSONRoutesToCloneAndWrapsAudio(t *testing.T) {
	var (
		gotPath        string
		gotContentType string
		gotFields      map[string][]string
		gotRefAudio    []byte
		gotRefName     string
	)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		gotFields = r.MultipartForm.Value
		file, header, err := r.FormFile("ref_audio")
		if err != nil {
			t.Fatalf("FormFile ref_audio: %v", err)
		}
		defer file.Close()
		gotRefName = header.Filename
		gotRefAudio, err = io.ReadAll(file)
		if err != nil {
			t.Fatalf("ReadAll ref_audio: %v", err)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFFclone"))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"voxcpm2": {
				ModelName: "voxcpm2",
				Address:   strings.TrimPrefix(backend.URL, "http://"),
				Ready:     true,
			},
		}},
		Catalog: staticCatalog{adapters: map[string][]Adapter{
			"voxcpm2": {{Path: "/v1/tts", Kind: adapterVoxCPMClone}},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	reqBody := `{"model":"voxcpm2","text":"hello","response_format":"wav","speed":1.1,"top_p":0.8,"seed":42,"x_vector_only_mode":true,"voice":"explicit","use_default_reference":true,"reference_audio":"data:audio/wav;base64,UklGRg==","reference_text":"sample voice"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tts", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotPath != "/v1/clone" {
		t.Fatalf("backend path = %q, want /v1/clone", gotPath)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data; boundary=") {
		t.Fatalf("content-type = %q, want multipart boundary", gotContentType)
	}
	field := func(name string) string {
		if values := gotFields[name]; len(values) > 0 {
			return values[0]
		}
		return ""
	}
	if field("text") != "hello" {
		t.Fatalf("text field = %q, want hello", field("text"))
	}
	if field("ref_text") != "sample voice" {
		t.Fatalf("ref_text field = %q, want sample voice", field("ref_text"))
	}
	if field("response_format") != "wav" {
		t.Fatalf("response_format field = %q, want wav", field("response_format"))
	}
	if field("speed") != "1.1" {
		t.Fatalf("speed field = %q, want 1.1", field("speed"))
	}
	if field("top_p") != "0.8" {
		t.Fatalf("top_p field = %q, want 0.8", field("top_p"))
	}
	if field("seed") != "42" {
		t.Fatalf("seed field = %q, want 42", field("seed"))
	}
	if field("x_vector_only_mode") != "true" {
		t.Fatalf("x_vector_only_mode field = %q, want true", field("x_vector_only_mode"))
	}
	if field("voice") != "explicit" {
		t.Fatalf("voice field = %q, want explicit", field("voice"))
	}
	if field("model") != "" || field("reference_audio") != "" || field("use_default_reference") != "" {
		t.Fatalf("protocol-only fields leaked to backend: %#v", gotFields)
	}
	if gotRefName != "reference.wav" {
		t.Fatalf("ref_audio filename = %q, want reference.wav", gotRefName)
	}
	if string(gotRefAudio) != "RIFF" {
		t.Fatalf("ref_audio = %q, want RIFF", string(gotRefAudio))
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if resp["audio_base64"] != base64.StdEncoding.EncodeToString([]byte("RIFFclone")) {
		t.Fatalf("audio_base64 = %#v, want encoded RIFFclone", resp["audio_base64"])
	}
	if resp["format"] != "wav" {
		t.Fatalf("format = %#v, want wav", resp["format"])
	}
}

func TestHandleTTSRemoteBackendSkipsLocalAdapter(t *testing.T) {
	var (
		gotPath string
		gotAuth string
		gotBody map[string]any
	)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("Unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"audio_base64":"UklGRg==","format":"wav"}`))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"voxcpm2": {
				ModelName: "voxcpm2",
				Address:   strings.TrimPrefix(backend.URL, "http://"),
				Ready:     true,
				Remote:    true,
			},
		}},
		Catalog: staticCatalog{adapters: map[string][]Adapter{
			"voxcpm2": {{Path: "/v1/tts", Kind: adapterVoxCPMClone}},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	reqBody := `{"model":"voxcpm2","input":"hello","reference_audio":"UklGRg=="}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tts", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer qujing")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotPath != "/v1/tts" {
		t.Fatalf("backend path = %q, want /v1/tts", gotPath)
	}
	if gotAuth != "Bearer qujing" {
		t.Fatalf("authorization = %q, want Bearer qujing", gotAuth)
	}
	if gotBody["text"] != "hello" {
		t.Fatalf("payload text = %#v, want hello", gotBody["text"])
	}
	if _, hasRefAudio := gotBody["reference_audio"]; !hasRefAudio {
		t.Fatalf("payload missing reference_audio: %#v", gotBody)
	}
}

func TestHandleTTSSpeechVoxCPMCloneReturnsAudio(t *testing.T) {
	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFFclone"))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"voxcpm2": {
				ModelName: "voxcpm2",
				Address:   strings.TrimPrefix(backend.URL, "http://"),
				Ready:     true,
			},
		}},
		Catalog: staticCatalog{adapters: map[string][]Adapter{
			"voxcpm2": {{Path: "/v1/audio/speech", Kind: adapterVoxCPMClone}},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	reqBody := `{"model":"voxcpm2","input":"hello","reference_audio":"UklGRg=="}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotPath != "/v1/clone" {
		t.Fatalf("backend path = %q, want /v1/clone", gotPath)
	}
	if got := w.Body.String(); got != "RIFFclone" {
		t.Fatalf("body = %q, want RIFFclone", got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("content-type = %q, want audio/wav", ct)
	}
}

func TestHandleTTSSpeechFallsBackToJSONAndDecodesAudio(t *testing.T) {
	var paths []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/audio/speech" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"audio_base64":"UklGRmRlbW8=","format":"wav"}`))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"json-tts": {
				ModelName: "json-tts",
				Address:   strings.TrimPrefix(backend.URL, "http://"),
				Ready:     true,
			},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(`{"model":"json-tts","input":"hello","response_format":"wav"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if strings.Join(paths, ",") != "/v1/audio/speech,/v1/tts" {
		t.Fatalf("paths = %v, want fallback from /v1/audio/speech to /v1/tts", paths)
	}
	if got := w.Body.String(); got != "RIFFdemo" {
		t.Fatalf("body = %q, want decoded audio", got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("content-type = %q, want audio/wav", ct)
	}
}

func TestHandleAudioQualityJSONRoutesToScore(t *testing.T) {
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
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"pass","mos":4.2}`))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"dnsmos-quality": {
				ModelName: "dnsmos-quality",
				Address:   strings.TrimPrefix(backend.URL, "http://"),
				Ready:     true,
			},
		}},
		Catalog: staticCatalog{adapters: map[string][]Adapter{
			"dnsmos-quality": {{Path: "/v1/audio/quality", Kind: "http_forward", TargetPath: "/score", UploadTargetPath: "/score/upload"}},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/quality", strings.NewReader(`{"model":"dnsmos-quality","path":"/tmp/demo.wav","threshold":3.5}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotPath != "/score" {
		t.Fatalf("backend path = %q, want /score", gotPath)
	}
	if gotBody["path"] != "/tmp/demo.wav" {
		t.Fatalf("payload path = %#v, want /tmp/demo.wav", gotBody["path"])
	}
}

func TestHandleAudioQualityMultipartRoutesToScoreUpload(t *testing.T) {
	var (
		gotPath        string
		gotContentType string
		gotBody        string
	)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		gotBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"fail","mos":2.1}`))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"nisqa-tts-quality": {
				ModelName: "nisqa-tts-quality",
				Address:   strings.TrimPrefix(backend.URL, "http://"),
				Ready:     true,
			},
		}},
		Catalog: staticCatalog{adapters: map[string][]Adapter{
			"nisqa-tts-quality": {{Path: "/v1/audio/quality", Kind: "http_forward", TargetPath: "/score", UploadTargetPath: "/score/upload"}},
		}},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "nisqa-tts-quality"); err != nil {
		t.Fatalf("WriteField model: %v", err)
	}
	part, err := writer.CreateFormFile("file", "sample.wav")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("RIFFdemo")); err != nil {
		t.Fatalf("Write file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/quality", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotPath != "/score/upload" {
		t.Fatalf("backend path = %q, want /score/upload", gotPath)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data; boundary=") {
		t.Fatalf("content-type = %q, want multipart boundary", gotContentType)
	}
	if !strings.Contains(gotBody, "nisqa-tts-quality") || !strings.Contains(gotBody, "sample.wav") {
		t.Fatalf("backend body missing fields, got %q", gotBody)
	}
}

func TestHandleAudioQualityRequiresConfiguredAdapterForLocalBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("backend should not be called without a quality adapter")
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"dnsmos-quality": {
				ModelName: "dnsmos-quality",
				Address:   strings.TrimPrefix(backend.URL, "http://"),
				Ready:     true,
			},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/quality", strings.NewReader(`{"model":"dnsmos-quality","path":"/tmp/demo.wav"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var resp apiErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal error response: %v", err)
	}
	if resp.Error.Type != apiErrorInvalidRequest {
		t.Fatalf("error type = %q, want %q", resp.Error.Type, apiErrorInvalidRequest)
	}
	if !strings.Contains(resp.Error.Message, "does not define an HTTP adapter") {
		t.Fatalf("error message = %q, want missing adapter message", resp.Error.Message)
	}
}

func TestHandleAudioQualityRemoteBackendForwardsGenericRoute(t *testing.T) {
	var (
		gotPath string
		gotAuth string
	)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"pass","mos":4.2}`))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"dnsmos-quality": {
				ModelName: "dnsmos-quality",
				Address:   strings.TrimPrefix(backend.URL, "http://"),
				Ready:     true,
				Remote:    true,
			},
		}},
	}

	mux := http.NewServeMux()
	RegisterRoutes(deps)(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/quality", strings.NewReader(`{"model":"dnsmos-quality","path":"/tmp/demo.wav"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer qujing")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotPath != "/v1/audio/quality" {
		t.Fatalf("backend path = %q, want /v1/audio/quality", gotPath)
	}
	if gotAuth != "Bearer qujing" {
		t.Fatalf("authorization = %q, want Bearer qujing", gotAuth)
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
				EngineType: "local-asr",
				Address:    "127.0.0.1:32107",
				Ready:      true,
			},
		}},
		Catalog: staticCatalog{adapters: map[string][]Adapter{
			"mooer-asr-1.5b": []Adapter{{Path: "/v1/audio/transcriptions", Kind: adapterMooERGRPC}},
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

func TestHandleASRRemoteBackendSkipsLocalAdapter(t *testing.T) {
	orig := mooerRecognize
	defer func() { mooerRecognize = orig }()
	mooerRecognize = func(context.Context, string, []byte) (*mooerRecognizeResponse, error) {
		t.Fatal("mooer adapter should not be called for a remote backend")
		return nil, nil
	}

	var (
		gotPath string
		gotAuth string
	)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"remote transcript"}`))
	}))
	defer backend.Close()

	deps := &Deps{
		Backends: staticBackendLister{backends: map[string]*Backend{
			"mooer-asr-1.5b": {
				ModelName: "mooer-asr-1.5b",
				Address:   strings.TrimPrefix(backend.URL, "http://"),
				Ready:     true,
				Remote:    true,
			},
		}},
		Catalog: staticCatalog{adapters: map[string][]Adapter{
			"mooer-asr-1.5b": {{Path: "/v1/audio/transcriptions", Kind: adapterMooERGRPC}},
		}},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "mooer-asr-1.5b")
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
	req.Header.Set("Authorization", "Bearer qujing")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotPath != "/v1/audio/transcriptions" {
		t.Fatalf("backend path = %q, want /v1/audio/transcriptions", gotPath)
	}
	if gotAuth != "Bearer qujing" {
		t.Fatalf("authorization = %q, want Bearer qujing", gotAuth)
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
				EngineType: "local-asr",
				Address:    "127.0.0.1:32107",
				Ready:      true,
			},
		}},
		Catalog: staticCatalog{adapters: map[string][]Adapter{
			"mooer-asr-1.5b": []Adapter{{Path: "/v1/audio/transcriptions", Kind: adapterMooERGRPC}},
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
