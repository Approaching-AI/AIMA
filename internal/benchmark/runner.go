package benchmark

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// RunConfig configures a single benchmark run.
type RunConfig struct {
	Endpoint       string        `json:"endpoint"`
	Model          string        `json:"model"`
	APIKey         string        `json:"api_key,omitempty"`
	Concurrency    int           `json:"concurrency"`
	NumRequests    int           `json:"num_requests"`    // per round
	MaxTokens      int           `json:"max_tokens"`
	InputTokens    int           `json:"input_tokens"`
	Temperature    float64       `json:"temperature"`
	WarmupCount    int           `json:"warmup_count"`
	Timeout        time.Duration `json:"timeout"`
	Rounds         int           `json:"rounds"`            // measurement rounds (default 1)
	MinOutputRatio float64       `json:"min_output_ratio"`  // retry if output < ratio * max_tokens (0 = disabled)
	MaxRetries     int           `json:"max_retries"`       // per-request retries (default 0)
	RetryDelay     time.Duration `json:"retry_delay"`       // initial retry delay (default 1s)
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
	if c.Rounds <= 0 {
		c.Rounds = 1
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = time.Second
	}
	if c.MinOutputRatio > 0 && c.MaxRetries <= 0 {
		c.MaxRetries = 3
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

// RoundResult holds per-round summary metrics.
type RoundResult struct {
	RoundID        int     `json:"round_id"`
	AvgTTFTms      float64 `json:"avg_ttft_ms"`
	AvgTPOTms      float64 `json:"avg_tpot_ms"`
	SuccessfulReqs int     `json:"successful_requests"`
	FailedReqs     int     `json:"failed_requests"`
}

// RunResult holds aggregated metrics from a completed benchmark run.
type RunResult struct {
	Config         RunConfig `json:"config"`
	TotalRequests  int       `json:"total_requests"`
	SuccessfulReqs int       `json:"successful_requests"`
	FailedReqs     int       `json:"failed_requests"`
	DurationMs     float64   `json:"duration_ms"`

	// TTFT statistics (milliseconds)
	TTFTP50ms float64 `json:"ttft_p50_ms"`
	TTFTP95ms float64 `json:"ttft_p95_ms"`
	TTFTP99ms float64 `json:"ttft_p99_ms"`
	TTFTStdMs float64 `json:"ttft_std_ms"`
	TTFTCVPct float64 `json:"ttft_cv_pct"` // coefficient of variation as percentage
	TTFTMinMs float64 `json:"ttft_min_ms"`
	TTFTMaxMs float64 `json:"ttft_max_ms"`

	// TPOT statistics (milliseconds)
	TPOTP50ms float64 `json:"tpot_p50_ms"`
	TPOTP95ms float64 `json:"tpot_p95_ms"`
	TPOTStdMs float64 `json:"tpot_std_ms"`
	TPOTCVPct float64 `json:"tpot_cv_pct"`
	TPOTMinMs float64 `json:"tpot_min_ms"`
	TPOTMaxMs float64 `json:"tpot_max_ms"`

	ThroughputTPS float64 `json:"throughput_tps"`
	QPS           float64 `json:"qps"`

	AvgInputTokens  int `json:"avg_input_tokens"`
	AvgOutputTokens int `json:"avg_output_tokens"`

	ErrorRate float64 `json:"error_rate"`

	Rounds       int           `json:"rounds,omitempty"`
	RoundResults []RoundResult `json:"round_results,omitempty"`

	Samples []RequestSample `json:"-"`
}

// Run executes a benchmark against an OpenAI-compatible streaming endpoint.
// With Rounds > 1, it runs multiple measurement rounds and aggregates results.
func Run(ctx context.Context, cfg RunConfig) (*RunResult, error) {
	cfg.applyDefaults()

	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("model is required")
	}

	// Warmup once before all measurement rounds
	for i := 0; i < cfg.WarmupCount; i++ {
		sendStreamingRequest(ctx, cfg)
	}

	start := time.Now()
	var allSamples []RequestSample
	var roundResults []RoundResult

	for round := 1; round <= cfg.Rounds; round++ {
		sem := make(chan struct{}, cfg.Concurrency)
		results := make(chan RequestSample, cfg.NumRequests)
		var wg sync.WaitGroup

		for i := 0; i < cfg.NumRequests; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				sample := sendWithRetry(ctx, cfg)
				<-sem
				results <- sample
			}()
		}

		go func() { wg.Wait(); close(results) }()

		var roundSamples []RequestSample
		for s := range results {
			roundSamples = append(roundSamples, s)
		}

		allSamples = append(allSamples, roundSamples...)
		roundResults = append(roundResults, summarizeRound(round, roundSamples))
	}

	duration := time.Since(start)

	result := aggregate(allSamples, duration)
	result.Config = cfg
	result.TotalRequests = len(allSamples)
	result.Samples = allSamples
	if cfg.Rounds > 1 {
		result.Rounds = cfg.Rounds
		result.RoundResults = roundResults
	}

	return result, nil
}

// sendWithRetry wraps sendStreamingRequest with retry logic and output length checks.
func sendWithRetry(ctx context.Context, cfg RunConfig) RequestSample {
	if cfg.MaxRetries <= 0 && cfg.MinOutputRatio <= 0 {
		return sendStreamingRequest(ctx, cfg)
	}

	const maxDelay = 30 * time.Second
	delay := cfg.RetryDelay
	var lastSample RequestSample
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return RequestSample{Error: ctx.Err()}
			case <-time.After(delay):
				delay = min(delay*2, maxDelay)
			}
		}
		lastSample = sendStreamingRequest(ctx, cfg)

		if attempt == cfg.MaxRetries {
			break
		}

		// Retry on error
		if lastSample.Error != nil {
			continue
		}

		// Retry on output too short
		if cfg.MinOutputRatio > 0 {
			minTokens := int(float64(cfg.MaxTokens) * cfg.MinOutputRatio)
			if lastSample.OutputTokens < minTokens {
				continue
			}
		}

		break
	}
	return lastSample
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

// promptPadding is a pre-generated padding string (64KB) reused across calls.
// Only the random prefix differs per call, avoiding O(n) string building.
var promptPadding = func() string {
	const unit = "The quick brown fox jumps over the lazy dog. "
	var sb strings.Builder
	sb.Grow(64 * 1024)
	for sb.Len() < 64*1024 {
		sb.WriteString(unit)
	}
	return sb.String()
}()

// generatePrompt generates a randomized prompt of approximately targetTokens length.
// Each call produces a unique prompt to avoid KV cache prefix matching.
func generatePrompt(targetTokens int) string {
	prefix := fmt.Sprintf("[%d] Please write a detailed response about the following topic. ", rand.Uint64())
	targetChars := targetTokens * 4
	if len(prefix) >= targetChars {
		return prefix[:targetChars]
	}
	need := targetChars - len(prefix)
	if need > len(promptPadding) {
		// Extremely large prompt — extend padding on the fly (rare path)
		var sb strings.Builder
		sb.Grow(targetChars)
		sb.WriteString(prefix)
		for sb.Len() < targetChars {
			sb.WriteString(promptPadding)
		}
		return sb.String()[:targetChars]
	}
	return prefix + promptPadding[:need]
}

func summarizeRound(roundID int, samples []RequestSample) RoundResult {
	rr := RoundResult{RoundID: roundID}
	var ttftSum, tpotSum float64
	var tpotCount int
	for _, s := range samples {
		if s.Error != nil {
			rr.FailedReqs++
			continue
		}
		rr.SuccessfulReqs++
		ttftSum += float64(s.TTFT.Microseconds()) / 1000.0
		if s.OutputTokens > 0 {
			genTime := s.TotalTime - s.TTFT
			divisor := s.OutputTokens - 1
			if divisor < 1 {
				divisor = 1
			}
			tpotSum += float64(genTime.Microseconds()) / 1000.0 / float64(divisor)
			tpotCount++
		}
	}
	if rr.SuccessfulReqs > 0 {
		rr.AvgTTFTms = ttftSum / float64(rr.SuccessfulReqs)
	}
	if tpotCount > 0 {
		rr.AvgTPOTms = tpotSum / float64(tpotCount)
	}
	return rr
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

	// TTFT statistics
	ttftValues := make([]float64, len(successSamples))
	for i, s := range successSamples {
		ttftValues[i] = float64(s.TTFT.Microseconds()) / 1000.0
	}
	sort.Float64s(ttftValues)
	result.TTFTP50ms = percentile(ttftValues, 50)
	result.TTFTP95ms = percentile(ttftValues, 95)
	result.TTFTP99ms = percentile(ttftValues, 99)
	result.TTFTStdMs = stddev(ttftValues)
	if m := mean(ttftValues); m > 0 {
		result.TTFTCVPct = result.TTFTStdMs / m * 100
	}
	result.TTFTMinMs = ttftValues[0]
	result.TTFTMaxMs = ttftValues[len(ttftValues)-1]

	// TPOT statistics: (totalTime - ttft) / max(outputTokens-1, 1)
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
	if len(tpotValues) > 0 {
		result.TPOTStdMs = stddev(tpotValues)
		if m := mean(tpotValues); m > 0 {
			result.TPOTCVPct = result.TPOTStdMs / m * 100
		}
		result.TPOTMinMs = tpotValues[0]
		result.TPOTMaxMs = tpotValues[len(tpotValues)-1]
	}

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

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// stddev computes sample standard deviation (Bessel's correction: N-1).
func stddev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	m := mean(values)
	var sumSq float64
	for _, v := range values {
		d := v - m
		sumSq += d * d
	}
	return math.Sqrt(sumSq / float64(len(values)-1))
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
