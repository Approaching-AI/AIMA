package benchmark

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// sseHandler returns an HTTP handler that sends SSE chunks simulating a streaming response.
func sseHandler(chunks int, delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)

		for i := 0; i < chunks; i++ {
			if delay > 0 {
				time.Sleep(delay)
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"word%d \"}}]}\n\n", i)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: {\"usage\":{\"prompt_tokens\":32,\"completion_tokens\":%d}}\n\n", chunks)
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}
}

func TestChatRequester_SendStreamingRequest_Basic(t *testing.T) {
	ts := httptest.NewServer(sseHandler(5, 0))
	defer ts.Close()

	req := &ChatRequester{
		Model:       "test",
		MaxTokens:   256,
		InputTokens: 128,
		Timeout:     10 * time.Second,
	}

	sample, err := req.Do(context.Background(), ts.URL, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sample.Error != nil {
		t.Fatalf("unexpected sample error: %v", sample.Error)
	}
	if sample.TTFTMs <= 0 {
		t.Errorf("expected TTFTMs > 0, got %v", sample.TTFTMs)
	}
	if sample.OutputTokens != 5 {
		t.Errorf("expected 5 output tokens, got %d", sample.OutputTokens)
	}
	if sample.InputTokens != 32 {
		t.Errorf("expected 32 input tokens, got %d", sample.InputTokens)
	}
	if sample.LatencyMs <= 0 {
		t.Errorf("expected LatencyMs > 0, got %v", sample.LatencyMs)
	}
}

func TestChatRequester_SendStreamingRequest_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", 400)
	}))
	defer ts.Close()

	req := &ChatRequester{
		Model:   "test",
		Timeout: 5 * time.Second,
	}

	sample, _ := req.Do(context.Background(), ts.URL, 0)
	if sample.Error == nil {
		t.Fatal("expected error for HTTP 400")
	}
}

func TestRun_Concurrency(t *testing.T) {
	var concurrent int64
	var maxConcurrent int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt64(&concurrent, 1)
		for {
			old := atomic.LoadInt64(&maxConcurrent)
			if cur <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt64(&concurrent, -1)

		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer ts.Close()

	req := &ChatRequester{
		Model:       "test",
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	}

	result, err := Run(context.Background(), RunConfig{
		Endpoint:    ts.URL,
		Model:       "test",
		Concurrency: 4,
		NumRequests: 8,
		WarmupCount: 0,
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	}, req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&maxConcurrent) > 4 {
		t.Errorf("max concurrent %d exceeds semaphore limit 4", maxConcurrent)
	}
	if result.TotalRequests != 8 {
		t.Errorf("expected 8 total requests, got %d", result.TotalRequests)
	}
	if result.SuccessfulReqs != 8 {
		t.Errorf("expected 8 successful, got %d", result.SuccessfulReqs)
	}
}

func TestRun_WarmupDiscard(t *testing.T) {
	ts := httptest.NewServer(sseHandler(3, 0))
	defer ts.Close()

	req := &ChatRequester{
		Model:       "test",
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	}

	result, err := Run(context.Background(), RunConfig{
		Endpoint:    ts.URL,
		Model:       "test",
		NumRequests: 5,
		WarmupCount: 2,
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	}, req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Result should have stats based on 5 requests (7 total minus 2 warmup)
	if result.TotalRequests != 5 {
		t.Errorf("expected 5 total requests after warmup discard, got %d", result.TotalRequests)
	}
}

func TestRun_MultiRound(t *testing.T) {
	ts := httptest.NewServer(sseHandler(3, 0))
	defer ts.Close()

	req := &ChatRequester{
		Model:       "test",
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	}

	result, err := Run(context.Background(), RunConfig{
		Endpoint:    ts.URL,
		Model:       "test",
		NumRequests: 4,
		Rounds:      3,
		WarmupCount: 0,
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	}, req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 3 rounds x 4 requests = 12 total
	if result.TotalRequests != 12 {
		t.Errorf("expected 12 total requests, got %d", result.TotalRequests)
	}
	if result.Rounds != 3 {
		t.Errorf("expected 3 rounds, got %d", result.Rounds)
	}
	if len(result.RoundResults) != 3 {
		t.Errorf("expected 3 round results, got %d", len(result.RoundResults))
	}
	for i, rr := range result.RoundResults {
		if rr.RoundID != i+1 {
			t.Errorf("round %d: expected RoundID %d, got %d", i, i+1, rr.RoundID)
		}
		if rr.SuccessfulReqs != 4 {
			t.Errorf("round %d: expected 4 successful, got %d", i+1, rr.SuccessfulReqs)
		}
		if rr.AvgTTFTms <= 0 {
			t.Errorf("round %d: expected AvgTTFTms > 0", i+1)
		}
	}
}

func TestRun_SingleRound_NoRoundResults(t *testing.T) {
	ts := httptest.NewServer(sseHandler(3, 0))
	defer ts.Close()

	req := &ChatRequester{
		Model:       "test",
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	}

	result, err := Run(context.Background(), RunConfig{
		Endpoint:    ts.URL,
		Model:       "test",
		NumRequests: 3,
		Rounds:      1,
		WarmupCount: 0,
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	}, req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Single round should not populate RoundResults
	if result.Rounds != 0 {
		t.Errorf("expected 0 Rounds for single round, got %d", result.Rounds)
	}
	if len(result.RoundResults) != 0 {
		t.Errorf("expected no round results for single round, got %d", len(result.RoundResults))
	}
}

func TestChatRequester_SendWithRetry_HTTPErrorRetries(t *testing.T) {
	var attempts int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&attempts, 1)
		if n < 3 {
			http.Error(w, "server error", 500)
			return
		}
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer ts.Close()

	req := &ChatRequester{
		Model:       "test",
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
		MaxRetries:  3,
		RetryDelay:  10 * time.Millisecond,
	}
	sample, _ := req.Do(context.Background(), ts.URL, 0)

	if sample.Error != nil {
		t.Fatalf("expected success after retry, got: %v", sample.Error)
	}
	if got := atomic.LoadInt64(&attempts); got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
}

func TestChatRequester_SendWithRetry_OutputTooShort(t *testing.T) {
	var attempts int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		flusher.Flush()
		// Only 1 output token, but max_tokens=100 with ratio 0.5 -> need 50
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer ts.Close()

	req := &ChatRequester{
		Model:          "test",
		MaxTokens:      100,
		InputTokens:    10,
		Timeout:        10 * time.Second,
		MinOutputRatio: 0.5,
		MaxRetries:     2,
		RetryDelay:     10 * time.Millisecond,
	}
	sample, _ := req.Do(context.Background(), ts.URL, 0)

	// Final attempt should still return (just with short output)
	if sample.Error != nil {
		t.Fatalf("expected no error on final attempt, got: %v", sample.Error)
	}
	if sample.OutputTokens != 1 {
		t.Errorf("expected 1 output token, got %d", sample.OutputTokens)
	}
	// Should have retried: 1 initial + 2 retries = 3 total
	if got := atomic.LoadInt64(&attempts); got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
}

func TestChatRequester_SendWithRetry_NoRetryNeeded(t *testing.T) {
	ts := httptest.NewServer(sseHandler(5, 0))
	defer ts.Close()

	req := &ChatRequester{
		Model:       "test",
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
		MaxRetries:  3,
		RetryDelay:  10 * time.Millisecond,
	}
	sample, _ := req.Do(context.Background(), ts.URL, 0)

	if sample.Error != nil {
		t.Fatalf("unexpected error: %v", sample.Error)
	}
	if sample.OutputTokens != 5 {
		t.Errorf("expected 5 output tokens, got %d", sample.OutputTokens)
	}
}

func TestPercentile(t *testing.T) {
	data := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	tests := []struct {
		p    float64
		want float64
	}{
		{50, 5.5},
		{95, 9.55},
		{99, 9.91},
		{0, 1},
		{100, 10},
	}
	for _, tt := range tests {
		got := percentile(data, tt.p)
		if math.Abs(got-tt.want) > 0.01 {
			t.Errorf("percentile(data, %.0f) = %.4f, want %.4f", tt.p, got, tt.want)
		}
	}
}

func TestPercentile_Empty(t *testing.T) {
	got := percentile(nil, 50)
	if got != 0 {
		t.Errorf("percentile(nil, 50) = %f, want 0", got)
	}
}

func TestPercentile_Single(t *testing.T) {
	got := percentile([]float64{42}, 99)
	if got != 42 {
		t.Errorf("percentile([42], 99) = %f, want 42", got)
	}
}

func TestMean(t *testing.T) {
	tests := []struct {
		values []float64
		want   float64
	}{
		{[]float64{1, 2, 3, 4, 5}, 3.0},
		{[]float64{10}, 10.0},
		{nil, 0},
	}
	for _, tt := range tests {
		got := mean(tt.values)
		if got != tt.want {
			t.Errorf("mean(%v) = %f, want %f", tt.values, got, tt.want)
		}
	}
}

func TestStddev(t *testing.T) {
	tests := []struct {
		values []float64
		want   float64
	}{
		{[]float64{2, 4, 4, 4, 5, 5, 7, 9}, 2.138},
		{[]float64{10}, 0},        // single value
		{nil, 0},                   // empty
		{[]float64{5, 5, 5, 5}, 0}, // no variance
	}
	for _, tt := range tests {
		got := stddev(tt.values)
		if math.Abs(got-tt.want) > 0.01 {
			t.Errorf("stddev(%v) = %f, want ~%f", tt.values, got, tt.want)
		}
	}
}

func TestApplyDefaults_MinOutputRatioAutoRetries(t *testing.T) {
	cfg := RunConfig{MinOutputRatio: 0.5}
	cfg.applyDefaults()
	if cfg.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3 when MinOutputRatio>0, got %d", cfg.MaxRetries)
	}

	// Explicit MaxRetries should not be overridden
	cfg2 := RunConfig{MinOutputRatio: 0.5, MaxRetries: 5}
	cfg2.applyDefaults()
	if cfg2.MaxRetries != 5 {
		t.Errorf("expected MaxRetries=5 (explicit), got %d", cfg2.MaxRetries)
	}
}

func TestGeneratePrompt(t *testing.T) {
	p := generatePrompt(128)
	expectedLen := 128 * 4
	if len(p) != expectedLen {
		t.Errorf("generatePrompt(128) length = %d, want %d", len(p), expectedLen)
	}

	// Small target
	p2 := generatePrompt(5)
	if len(p2) != 20 {
		t.Errorf("generatePrompt(5) length = %d, want 20", len(p2))
	}
}

func TestGeneratePrompt_Randomized(t *testing.T) {
	p1 := generatePrompt(128)
	p2 := generatePrompt(128)
	if p1 == p2 {
		t.Error("expected randomized prompts to differ between calls")
	}
	if len(p1) != len(p2) {
		t.Errorf("randomized prompts should have same length: %d vs %d", len(p1), len(p2))
	}
}

func TestGeneratePrompt_LargeInput(t *testing.T) {
	// 4096 tokens = 16KB -- should be fast with pre-generated padding
	start := time.Now()
	for i := 0; i < 100; i++ {
		p := generatePrompt(4096)
		if len(p) != 4096*4 {
			t.Fatalf("generatePrompt(4096) length = %d, want %d", len(p), 4096*4)
		}
	}
	elapsed := time.Since(start)
	// 100 calls should complete well under 100ms with pre-generated padding
	if elapsed > 100*time.Millisecond {
		t.Errorf("generatePrompt(4096) x100 took %v, expected < 100ms", elapsed)
	}
}

func TestAggregateLLMMetrics_StdDevAndMinMax(t *testing.T) {
	samples := []RequestSample{
		{TTFT: 100 * time.Millisecond, TotalTime: 500 * time.Millisecond, OutputTokens: 10, InputTokens: 32},
		{TTFT: 200 * time.Millisecond, TotalTime: 600 * time.Millisecond, OutputTokens: 10, InputTokens: 32},
		{TTFT: 300 * time.Millisecond, TotalTime: 700 * time.Millisecond, OutputTokens: 10, InputTokens: 32},
	}

	r := aggregateLLMMetrics(samples, time.Second)

	if r.TTFTMinMs != 100 {
		t.Errorf("TTFTMinMs = %f, want 100", r.TTFTMinMs)
	}
	if r.TTFTMaxMs != 300 {
		t.Errorf("TTFTMaxMs = %f, want 300", r.TTFTMaxMs)
	}
	if r.TTFTStdMs <= 0 {
		t.Error("expected TTFTStdMs > 0")
	}
	if r.TTFTCVPct <= 0 {
		t.Error("expected TTFTCVPct > 0")
	}
	if r.TPOTMinMs <= 0 {
		t.Error("expected TPOTMinMs > 0")
	}
	if r.TPOTMaxMs <= 0 {
		t.Error("expected TPOTMaxMs > 0")
	}
	if r.TPOTStdMs < 0 {
		t.Error("expected TPOTStdMs >= 0")
	}
}

func TestAggregateLLMMetrics_AllErrors(t *testing.T) {
	samples := []RequestSample{
		{Error: fmt.Errorf("fail1")},
		{Error: fmt.Errorf("fail2")},
	}
	r := aggregateLLMMetrics(samples, time.Second)
	if r.SuccessfulReqs != 0 {
		t.Errorf("expected 0 successful, got %d", r.SuccessfulReqs)
	}
	if r.ErrorRate != 1.0 {
		t.Errorf("expected error rate 1.0, got %f", r.ErrorRate)
	}
}

func TestRun_Modality(t *testing.T) {
	ts := httptest.NewServer(sseHandler(3, 0))
	defer ts.Close()

	req := &ChatRequester{
		Model:       "test",
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	}

	result, err := Run(context.Background(), RunConfig{
		Endpoint:    ts.URL,
		Model:       "test",
		NumRequests: 2,
		WarmupCount: 0,
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	}, req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Modality != "llm" {
		t.Errorf("expected Modality = 'llm', got %q", result.Modality)
	}

	// VLM mode
	vlmReq := &ChatRequester{
		Model:       "test",
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
		ImageURLs:   []string{"https://example.com/test.jpg"},
	}

	result, err = Run(context.Background(), RunConfig{
		Endpoint:    ts.URL,
		Model:       "test",
		NumRequests: 2,
		WarmupCount: 0,
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	}, vlmReq)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Modality != "vlm" {
		t.Errorf("expected Modality = 'vlm', got %q", result.Modality)
	}
}

func TestBaseEndpoint(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://localhost:8000/v1/chat/completions", "http://localhost:8000"},
		{"http://localhost:8000/v1", "http://localhost:8000"},
		{"http://localhost:8000/v1/", "http://localhost:8000"},
		{"http://localhost:8000", "http://localhost:8000"},
		{"http://localhost:8000/", "http://localhost:8000"},
		{"http://host:6188/v1/audio/speech", "http://host:6188"},
		{"http://host:6188/v1/audio/transcriptions", "http://host:6188"},
		{"http://host:6188/v1/images/generations", "http://host:6188"},
		{"http://host:6188/chat/completions", "http://host:6188"},
	}
	for _, tt := range tests {
		got := baseEndpoint(tt.input)
		if got != tt.want {
			t.Errorf("baseEndpoint(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
