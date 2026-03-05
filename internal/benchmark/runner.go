package benchmark

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// RunConfig configures a single benchmark run.
type RunConfig struct {
	Endpoint    string        `json:"endpoint"`
	Model       string        `json:"model"`
	APIKey      string        `json:"api_key,omitempty"`
	Concurrency int           `json:"concurrency"`
	NumRequests int           `json:"num_requests"`
	MaxTokens   int           `json:"max_tokens"`
	InputTokens int           `json:"input_tokens"`
	Temperature float64       `json:"temperature"`
	WarmupCount int           `json:"warmup_count"`
	Timeout     time.Duration `json:"timeout"`
}

func (c *RunConfig) applyDefaults() {
	if c.Concurrency <= 0 {
		c.Concurrency = 1
	}
	if c.NumRequests <= 0 {
		c.NumRequests = 10
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = 256
	}
	if c.InputTokens <= 0 {
		c.InputTokens = 128
	}
	if c.Temperature <= 0 {
		c.Temperature = 0.01
	}
	if c.WarmupCount < 0 {
		c.WarmupCount = 0
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Minute
	}
}

// RequestSample holds per-request measurements.
type RequestSample struct {
	TTFT         time.Duration `json:"-"`
	TotalTime    time.Duration `json:"-"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	Error        error         `json:"-"`
}

// RunResult holds aggregated metrics from a completed benchmark run.
type RunResult struct {
	Config         RunConfig `json:"config"`
	TotalRequests  int       `json:"total_requests"`
	SuccessfulReqs int       `json:"successful_requests"`
	FailedReqs     int       `json:"failed_requests"`
	DurationMs     float64   `json:"duration_ms"`

	TTFTP50ms float64 `json:"ttft_p50_ms"`
	TTFTP95ms float64 `json:"ttft_p95_ms"`
	TTFTP99ms float64 `json:"ttft_p99_ms"`
	TPOTP50ms float64 `json:"tpot_p50_ms"`
	TPOTP95ms float64 `json:"tpot_p95_ms"`

	ThroughputTPS float64 `json:"throughput_tps"`
	QPS           float64 `json:"qps"`

	AvgInputTokens  int `json:"avg_input_tokens"`
	AvgOutputTokens int `json:"avg_output_tokens"`

	ErrorRate float64 `json:"error_rate"`

	Samples []RequestSample `json:"-"`
}

// Run executes a benchmark against an OpenAI-compatible streaming endpoint.
func Run(ctx context.Context, cfg RunConfig) (*RunResult, error) {
	cfg.applyDefaults()

	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("model is required")
	}

	// Run warmup requests sequentially before measurement
	for i := 0; i < cfg.WarmupCount; i++ {
		sendStreamingRequest(ctx, cfg)
	}

	// Run measurement requests with concurrency
	sem := make(chan struct{}, cfg.Concurrency)
	results := make(chan RequestSample, cfg.NumRequests)
	var wg sync.WaitGroup

	start := time.Now()

	for i := 0; i < cfg.NumRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			sample := sendStreamingRequest(ctx, cfg)
			<-sem
			results <- sample
		}()
	}

	go func() { wg.Wait(); close(results) }()

	var samples []RequestSample
	for s := range results {
		samples = append(samples, s)
	}

	duration := time.Since(start)

	result := aggregate(samples, duration)
	result.Config = cfg
	result.TotalRequests = len(samples)
	result.Samples = samples

	return result, nil
}

func sendStreamingRequest(ctx context.Context, cfg RunConfig) RequestSample {
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	payload := map[string]any{
		"model":       cfg.Model,
		"messages":    []map[string]string{{"role": "user", "content": generatePrompt(cfg.InputTokens)}},
		"max_tokens":  cfg.MaxTokens,
		"temperature": cfg.Temperature,
		"stream":      true,
		"stream_options": map[string]bool{
			"include_usage": true,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return RequestSample{Error: fmt.Errorf("marshal request: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return RequestSample{Error: fmt.Errorf("create request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	startTime := time.Now()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return RequestSample{Error: fmt.Errorf("send request: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return RequestSample{Error: fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))}
	}

	scanner := bufio.NewScanner(resp.Body)
	// Increase scanner buffer for large SSE payloads
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	var ttft time.Duration
	var outputTokens, inputTokens int

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}

		if chunk.Usage != nil {
			inputTokens = chunk.Usage.PromptTokens
			outputTokens = chunk.Usage.CompletionTokens
		}

		if len(chunk.Choices) > 0 {
			d := chunk.Choices[0].Delta
			content := d.Content + d.Reasoning + d.ReasoningContent
			if content != "" && ttft == 0 {
				ttft = time.Since(startTime)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return RequestSample{Error: fmt.Errorf("read SSE stream: %w", err)}
	}

	return RequestSample{
		TTFT:         ttft,
		TotalTime:    time.Since(startTime),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
}

func generatePrompt(targetTokens int) string {
	base := "Please write a detailed response about the following topic. "
	padding := "The quick brown fox jumps over the lazy dog. "
	targetChars := targetTokens * 4
	if len(base) >= targetChars {
		return base[:targetChars]
	}
	var sb strings.Builder
	sb.WriteString(base)
	for sb.Len() < targetChars {
		sb.WriteString(padding)
	}
	s := sb.String()
	if len(s) > targetChars {
		s = s[:targetChars]
	}
	return s
}

func aggregate(samples []RequestSample, totalDuration time.Duration) *RunResult {
	result := &RunResult{}

	var successSamples []RequestSample
	for _, s := range samples {
		if s.Error == nil {
			successSamples = append(successSamples, s)
		}
	}

	result.SuccessfulReqs = len(successSamples)
	result.FailedReqs = len(samples) - len(successSamples)
	result.DurationMs = float64(totalDuration.Milliseconds())

	if len(samples) > 0 {
		result.ErrorRate = float64(result.FailedReqs) / float64(len(samples))
	}

	if len(successSamples) == 0 {
		return result
	}

	// TTFT percentiles
	ttftValues := make([]float64, len(successSamples))
	for i, s := range successSamples {
		ttftValues[i] = float64(s.TTFT.Microseconds()) / 1000.0
	}
	sort.Float64s(ttftValues)
	result.TTFTP50ms = percentile(ttftValues, 50)
	result.TTFTP95ms = percentile(ttftValues, 95)
	result.TTFTP99ms = percentile(ttftValues, 99)

	// TPOT: (totalTime - ttft) / max(outputTokens-1, 1)
	tpotValues := make([]float64, 0, len(successSamples))
	for _, s := range successSamples {
		if s.OutputTokens > 0 {
			genTime := s.TotalTime - s.TTFT
			divisor := s.OutputTokens - 1
			if divisor < 1 {
				divisor = 1
			}
			tpotMs := float64(genTime.Microseconds()) / 1000.0 / float64(divisor)
			tpotValues = append(tpotValues, tpotMs)
		}
	}
	sort.Float64s(tpotValues)
	result.TPOTP50ms = percentile(tpotValues, 50)
	result.TPOTP95ms = percentile(tpotValues, 95)

	// Throughput: total output tokens / total duration
	var totalOutputTokens, totalInputTokens int
	for _, s := range successSamples {
		totalOutputTokens += s.OutputTokens
		totalInputTokens += s.InputTokens
	}
	durationS := totalDuration.Seconds()
	if durationS > 0 {
		result.ThroughputTPS = float64(totalOutputTokens) / durationS
		result.QPS = float64(result.SuccessfulReqs) / durationS
	}

	result.AvgInputTokens = totalInputTokens / len(successSamples)
	result.AvgOutputTokens = totalOutputTokens / len(successSamples)

	return result
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := p / 100.0 * float64(len(sorted)-1)
	lower := int(idx)
	upper := lower + 1
	if upper >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := idx - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}
