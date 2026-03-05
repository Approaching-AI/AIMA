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

func TestSendStreamingRequest_Basic(t *testing.T) {
	ts := httptest.NewServer(sseHandler(5, 0))
	defer ts.Close()

	sample := sendStreamingRequest(context.Background(), RunConfig{
		Endpoint:    ts.URL,
		Model:       "test",
		MaxTokens:   256,
		InputTokens: 128,
		Timeout:     10 * time.Second,
	})

	if sample.Error != nil {
		t.Fatalf("unexpected error: %v", sample.Error)
	}
	if sample.TTFT <= 0 {
		t.Errorf("expected TTFT > 0, got %v", sample.TTFT)
	}
	if sample.OutputTokens != 5 {
		t.Errorf("expected 5 output tokens, got %d", sample.OutputTokens)
	}
	if sample.InputTokens != 32 {
		t.Errorf("expected 32 input tokens, got %d", sample.InputTokens)
	}
	if sample.TotalTime <= 0 {
		t.Errorf("expected TotalTime > 0, got %v", sample.TotalTime)
	}
}

func TestSendStreamingRequest_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", 400)
	}))
	defer ts.Close()

	sample := sendStreamingRequest(context.Background(), RunConfig{
		Endpoint: ts.URL, Model: "test", Timeout: 5 * time.Second,
	})

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

	result, err := Run(context.Background(), RunConfig{
		Endpoint:    ts.URL,
		Model:       "test",
		Concurrency: 4,
		NumRequests: 8,
		WarmupCount: 0,
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	})

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

	result, err := Run(context.Background(), RunConfig{
		Endpoint:    ts.URL,
		Model:       "test",
		NumRequests: 5,
		WarmupCount: 2,
		MaxTokens:   10,
		InputTokens: 10,
		Timeout:     10 * time.Second,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Result should have stats based on 5 requests (7 total minus 2 warmup)
	if result.TotalRequests != 5 {
		t.Errorf("expected 5 total requests after warmup discard, got %d", result.TotalRequests)
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

func TestAggregate_AllErrors(t *testing.T) {
	samples := []RequestSample{
		{Error: fmt.Errorf("fail1")},
		{Error: fmt.Errorf("fail2")},
	}
	r := aggregate(samples, time.Second)
	if r.SuccessfulReqs != 0 {
		t.Errorf("expected 0 successful, got %d", r.SuccessfulReqs)
	}
	if r.ErrorRate != 1.0 {
		t.Errorf("expected error rate 1.0, got %f", r.ErrorRate)
	}
}
