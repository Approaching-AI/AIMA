package benchmark

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// RunConfig configures a single benchmark run.
type RunConfig struct {
	Endpoint       string        `json:"endpoint"`
	Model          string        `json:"model"`
	APIKey         string        `json:"api_key,omitempty"`
	Concurrency    int           `json:"concurrency"`
	NumRequests    int           `json:"num_requests"` // per round
	MaxTokens      int           `json:"max_tokens"`
	InputTokens    int           `json:"input_tokens"`
	Temperature    float64       `json:"temperature"`
	WarmupCount    int           `json:"warmup_count"`
	Timeout        time.Duration `json:"timeout"`
	Rounds         int           `json:"rounds"`           // measurement rounds (default 1)
	MinOutputRatio float64       `json:"min_output_ratio"` // retry if output < ratio * max_tokens (0 = disabled)
	MaxRetries     int           `json:"max_retries"`      // per-request retries (default 0)
	RetryDelay     time.Duration `json:"retry_delay"`      // initial retry delay (default 1s)
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

// RequestSample holds per-request measurements (legacy LLM format).
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
	Modality       string    `json:"modality"`
	TotalRequests  int       `json:"total_requests"`
	SuccessfulReqs int       `json:"successful_requests"`
	FailedReqs     int       `json:"failed_requests"`
	DurationMs     float64   `json:"duration_ms"`

	// TTFT statistics (milliseconds) — LLM/VLM
	TTFTP50ms float64 `json:"ttft_p50_ms"`
	TTFTP95ms float64 `json:"ttft_p95_ms"`
	TTFTP99ms float64 `json:"ttft_p99_ms"`
	TTFTStdMs float64 `json:"ttft_std_ms"`
	TTFTCVPct float64 `json:"ttft_cv_pct"` // coefficient of variation as percentage
	TTFTMinMs float64 `json:"ttft_min_ms"`
	TTFTMaxMs float64 `json:"ttft_max_ms"`

	// TPOT statistics (milliseconds) — LLM/VLM
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

	ErrorRate  float64 `json:"error_rate"`
	FirstError string  `json:"first_error,omitempty"`

	Rounds       int           `json:"rounds,omitempty"`
	RoundResults []RoundResult `json:"round_results,omitempty"`

	Samples []RequestSample `json:"-"`

	// ---- TTS/ASR shared ----
	RTFP50  float64 `json:"rtf_p50,omitempty"`
	RTFP95  float64 `json:"rtf_p95,omitempty"`
	RTFMean float64 `json:"rtf_mean,omitempty"`

	// ---- TTS specific ----
	TTFAP50ms         float64 `json:"ttfa_p50_ms,omitempty"`
	TTFAP95ms         float64 `json:"ttfa_p95_ms,omitempty"`
	AudioThroughput   float64 `json:"audio_throughput,omitempty"` // audio-seconds generated / wall-second
	AvgInputChars     int     `json:"avg_input_chars,omitempty"`
	AvgAudioDurationS float64 `json:"avg_audio_duration_s,omitempty"`

	// ---- ASR specific ----
	ASRThroughput  float64 `json:"asr_throughput,omitempty"` // audio-hours processed / wall-hour
	AvgInputAudioS float64 `json:"avg_input_audio_s,omitempty"`
	AvgOutputChars int     `json:"avg_output_chars,omitempty"`

	// ---- T2I specific ----
	LatencyP50ms float64 `json:"latency_p50_ms,omitempty"`
	LatencyP95ms float64 `json:"latency_p95_ms,omitempty"`
	LatencyP99ms float64 `json:"latency_p99_ms,omitempty"`
	ImagesPerSec float64 `json:"images_per_sec,omitempty"`
	AvgSteps     int     `json:"avg_steps,omitempty"`
	ImageWidth   int     `json:"image_width,omitempty"`
	ImageHeight  int     `json:"image_height,omitempty"`

	// ---- Embedding specific ----
	EmbeddingLatencyP50ms  float64 `json:"embedding_latency_p50_ms,omitempty"`
	EmbeddingLatencyP95ms  float64 `json:"embedding_latency_p95_ms,omitempty"`
	EmbeddingLatencyP99ms  float64 `json:"embedding_latency_p99_ms,omitempty"`
	EmbeddingsPerSec       float64 `json:"embeddings_per_sec,omitempty"`
	InputTokensPerSec      float64 `json:"input_tokens_per_sec,omitempty"`
	AvgEmbeddingDimensions int     `json:"avg_embedding_dimensions,omitempty"`

	// ---- Reranker specific ----
	RerankLatencyP50ms float64 `json:"rerank_latency_p50_ms,omitempty"`
	RerankLatencyP95ms float64 `json:"rerank_latency_p95_ms,omitempty"`
	ReranksPerSec      float64 `json:"reranks_per_sec,omitempty"`
	AvgDocuments       int     `json:"avg_documents,omitempty"`

	// ---- T2V specific ----
	VideoLatencyP50s  float64 `json:"video_latency_p50_s,omitempty"`
	VideoLatencyP95s  float64 `json:"video_latency_p95_s,omitempty"`
	VideosPerHour     float64 `json:"videos_per_hour,omitempty"`
	AvgVideoDurationS float64 `json:"avg_video_duration_s,omitempty"`
	AvgFrames         int     `json:"avg_frames,omitempty"`
	VideoFPS          int     `json:"video_fps,omitempty"`
	VideoWidth        int     `json:"video_width,omitempty"`
	VideoHeight       int     `json:"video_height,omitempty"`
	VideoSteps        int     `json:"video_steps,omitempty"`
}

// Run executes a benchmark against an inference endpoint using the given Requester.
// With Rounds > 1, it runs multiple measurement rounds and aggregates results.
func Run(ctx context.Context, cfg RunConfig, req Requester) (*RunResult, error) {
	cfg.applyDefaults()

	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("model is required")
	}

	// Warmup using requester's recommended count
	warmupCount := cfg.WarmupCount
	if req != nil {
		warmupCount = req.WarmupRequests()
	}
	for i := 0; i < warmupCount; i++ {
		if req != nil {
			req.Do(ctx, cfg.Endpoint, -1)
		}
	}

	start := time.Now()
	var allSamples []*Sample
	var roundResults []RoundResult

	for round := 1; round <= cfg.Rounds; round++ {
		sem := make(chan struct{}, cfg.Concurrency)
		results := make(chan *Sample, cfg.NumRequests)
		var wg sync.WaitGroup

		for i := 0; i < cfg.NumRequests; i++ {
			wg.Add(1)
			seq := i
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				sample, _ := req.Do(ctx, cfg.Endpoint, seq)
				<-sem
				results <- sample
			}()
		}

		go func() { wg.Wait(); close(results) }()

		var roundSamples []*Sample
		for s := range results {
			roundSamples = append(roundSamples, s)
		}

		allSamples = append(allSamples, roundSamples...)

		// Summarize round using legacy format for compatibility
		reqSamples := samplesToRequestSamples(roundSamples)
		roundResults = append(roundResults, summarizeRound(round, reqSamples))
	}

	duration := time.Since(start)
	modality := "llm"
	if req != nil {
		modality = req.Modality()
	}

	// Aggregate LLM metrics for backward compatibility (always populated for llm/vlm)
	reqSamples := samplesToRequestSamples(allSamples)
	result := aggregateLLMMetrics(reqSamples, duration)
	result.Modality = modality
	result.Config = cfg
	result.TotalRequests = len(allSamples)
	result.Samples = reqSamples
	if cfg.Rounds > 1 {
		result.Rounds = cfg.Rounds
		result.RoundResults = roundResults
	}

	// Aggregate modality-specific metrics
	aggregateByModality(modality, allSamples, duration, result)

	return result, nil
}

func samplesToRequestSamples(samples []*Sample) []RequestSample {
	result := make([]RequestSample, len(samples))
	for i, s := range samples {
		result[i] = sampleToRequestSample(s)
	}
	return result
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

func aggregateLLMMetrics(samples []RequestSample, totalDuration time.Duration) *RunResult {
	result := &RunResult{}

	var successSamples []RequestSample
	for _, s := range samples {
		if s.Error == nil {
			successSamples = append(successSamples, s)
		} else if result.FirstError == "" {
			result.FirstError = s.Error.Error()
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

// aggregateByModality dispatches to modality-specific aggregation functions.
func aggregateByModality(modality string, samples []*Sample, totalDuration time.Duration, result *RunResult) {
	switch modality {
	case "llm", "vlm":
		// Already handled by aggregateLLMMetrics
		return
	case "tts":
		aggregateTTSMetrics(samples, totalDuration, result)
	case "asr":
		aggregateASRMetrics(samples, totalDuration, result)
	case "embedding":
		aggregateEmbeddingMetrics(samples, totalDuration, result)
	case "reranker":
		aggregateRerankerMetrics(samples, totalDuration, result)
	case "image_gen":
		aggregateImageGenMetrics(samples, totalDuration, result)
	case "video_gen":
		aggregateVideoGenMetrics(samples, totalDuration, result)
	}
}

func aggregateEmbeddingMetrics(samples []*Sample, totalDuration time.Duration, result *RunResult) {
	var successSamples []*Sample
	for _, s := range samples {
		if s.Error == nil {
			successSamples = append(successSamples, s)
		}
	}
	if len(successSamples) == 0 {
		return
	}

	latencyValues := make([]float64, len(successSamples))
	var totalInputTokens, totalDims int
	for i, s := range successSamples {
		latencyValues[i] = s.LatencyMs
		totalInputTokens += s.InputTokens
		totalDims += s.EmbeddingDimensions
	}
	sort.Float64s(latencyValues)
	result.EmbeddingLatencyP50ms = percentile(latencyValues, 50)
	result.EmbeddingLatencyP95ms = percentile(latencyValues, 95)
	result.EmbeddingLatencyP99ms = percentile(latencyValues, 99)

	durationS := totalDuration.Seconds()
	if durationS > 0 {
		result.EmbeddingsPerSec = float64(len(successSamples)) / durationS
		result.InputTokensPerSec = float64(totalInputTokens) / durationS
		result.ThroughputTPS = result.EmbeddingsPerSec
		result.QPS = result.EmbeddingsPerSec
	}

	result.AvgInputTokens = totalInputTokens / len(successSamples)
	result.AvgEmbeddingDimensions = totalDims / len(successSamples)
	result.AvgOutputTokens = result.AvgEmbeddingDimensions
}

func aggregateRerankerMetrics(samples []*Sample, totalDuration time.Duration, result *RunResult) {
	var successSamples []*Sample
	for _, s := range samples {
		if s.Error == nil {
			successSamples = append(successSamples, s)
		}
	}
	if len(successSamples) == 0 {
		return
	}

	latencyValues := make([]float64, len(successSamples))
	var totalInputTokens, totalDocuments int
	for i, s := range successSamples {
		latencyValues[i] = s.LatencyMs
		totalInputTokens += s.InputTokens
		totalDocuments += s.OutputTokens
	}
	sort.Float64s(latencyValues)
	result.RerankLatencyP50ms = percentile(latencyValues, 50)
	result.RerankLatencyP95ms = percentile(latencyValues, 95)

	durationS := totalDuration.Seconds()
	if durationS > 0 {
		result.ReranksPerSec = float64(len(successSamples)) / durationS
		result.ThroughputTPS = result.ReranksPerSec
		result.QPS = result.ReranksPerSec
	}

	result.AvgInputTokens = totalInputTokens / len(successSamples)
	result.AvgDocuments = totalDocuments / len(successSamples)
	result.AvgOutputTokens = result.AvgDocuments
}

func aggregateTTSMetrics(samples []*Sample, totalDuration time.Duration, result *RunResult) {
	var successSamples []*Sample
	for _, s := range samples {
		if s.Error == nil {
			successSamples = append(successSamples, s)
		}
	}
	if len(successSamples) == 0 {
		return
	}

	// RTF = processing_time_s / audio_duration_s
	rtfValues := make([]float64, 0, len(successSamples))
	ttfaValues := make([]float64, 0, len(successSamples))
	var totalAudioDurationS float64
	var totalInputChars int

	for _, s := range successSamples {
		if s.AudioDurationS > 0 {
			rtf := (s.LatencyMs / 1000.0) / s.AudioDurationS
			rtfValues = append(rtfValues, rtf)
		}
		if s.TTFAMs > 0 {
			ttfaValues = append(ttfaValues, s.TTFAMs)
		}
		totalAudioDurationS += s.AudioDurationS
		totalInputChars += s.InputChars
	}

	if len(rtfValues) > 0 {
		sort.Float64s(rtfValues)
		result.RTFP50 = percentile(rtfValues, 50)
		result.RTFP95 = percentile(rtfValues, 95)
		result.RTFMean = mean(rtfValues)
	}

	if len(ttfaValues) > 0 {
		sort.Float64s(ttfaValues)
		result.TTFAP50ms = percentile(ttfaValues, 50)
		result.TTFAP95ms = percentile(ttfaValues, 95)
	}

	durationS := totalDuration.Seconds()
	if durationS > 0 {
		result.AudioThroughput = totalAudioDurationS / durationS
	}

	result.AvgInputChars = totalInputChars / len(successSamples)
	result.AvgAudioDurationS = totalAudioDurationS / float64(len(successSamples))
}

func aggregateASRMetrics(samples []*Sample, totalDuration time.Duration, result *RunResult) {
	var successSamples []*Sample
	for _, s := range samples {
		if s.Error == nil {
			successSamples = append(successSamples, s)
		}
	}
	if len(successSamples) == 0 {
		return
	}

	// RTF = processing_time_s / input_audio_duration_s
	rtfValues := make([]float64, 0, len(successSamples))
	var totalInputAudioS float64
	var totalOutputChars int

	for _, s := range successSamples {
		if s.InputAudioS > 0 {
			rtf := (s.LatencyMs / 1000.0) / s.InputAudioS
			rtfValues = append(rtfValues, rtf)
		}
		totalInputAudioS += s.InputAudioS
		totalOutputChars += s.OutputChars
	}

	if len(rtfValues) > 0 {
		sort.Float64s(rtfValues)
		result.RTFP50 = percentile(rtfValues, 50)
		result.RTFP95 = percentile(rtfValues, 95)
		result.RTFMean = mean(rtfValues)
	}

	durationS := totalDuration.Seconds()
	if durationS > 0 {
		// ASRThroughput = audio-hours processed / wall-hours
		result.ASRThroughput = (totalInputAudioS / 3600.0) / (durationS / 3600.0)
	}

	result.AvgInputAudioS = totalInputAudioS / float64(len(successSamples))
	result.AvgOutputChars = totalOutputChars / len(successSamples)
}

func aggregateImageGenMetrics(samples []*Sample, totalDuration time.Duration, result *RunResult) {
	var successSamples []*Sample
	for _, s := range samples {
		if s.Error == nil {
			successSamples = append(successSamples, s)
		}
	}
	if len(successSamples) == 0 {
		return
	}

	latencyValues := make([]float64, len(successSamples))
	var totalImages, totalSteps int
	for i, s := range successSamples {
		latencyValues[i] = s.LatencyMs
		totalImages += s.ImagesGenerated
		totalSteps += s.StepsCompleted
	}

	sort.Float64s(latencyValues)
	result.LatencyP50ms = percentile(latencyValues, 50)
	result.LatencyP95ms = percentile(latencyValues, 95)
	result.LatencyP99ms = percentile(latencyValues, 99)

	durationS := totalDuration.Seconds()
	if durationS > 0 {
		result.ImagesPerSec = float64(totalImages) / durationS
		result.ThroughputTPS = result.ImagesPerSec
		result.QPS = result.ImagesPerSec
	}

	result.AvgSteps = totalSteps / len(successSamples)

	// Take width/height from first sample (constant across requests)
	if len(successSamples) > 0 {
		result.ImageWidth = successSamples[0].WidthPx
		result.ImageHeight = successSamples[0].HeightPx
	}
}

func aggregateVideoGenMetrics(samples []*Sample, totalDuration time.Duration, result *RunResult) {
	var successSamples []*Sample
	for _, s := range samples {
		if s.Error == nil {
			successSamples = append(successSamples, s)
		}
	}
	if len(successSamples) == 0 {
		return
	}

	// Latency in seconds (not ms) for video gen
	latencyValues := make([]float64, len(successSamples))
	var totalVideoDurationS float64
	var totalFrames, totalSteps int

	for i, s := range successSamples {
		latencyValues[i] = s.LatencyMs / 1000.0
		totalVideoDurationS += s.VideoDurationS
		totalFrames += s.FramesGenerated
		totalSteps += s.VideoSteps
	}

	sort.Float64s(latencyValues)
	result.VideoLatencyP50s = percentile(latencyValues, 50)
	result.VideoLatencyP95s = percentile(latencyValues, 95)

	durationS := totalDuration.Seconds()
	if durationS > 0 {
		result.VideosPerHour = float64(len(successSamples)) / (durationS / 3600.0)
	}

	result.AvgVideoDurationS = totalVideoDurationS / float64(len(successSamples))
	result.AvgFrames = totalFrames / len(successSamples)
	result.VideoSteps = totalSteps / len(successSamples)

	// Take video params from first sample (constant across requests)
	if len(successSamples) > 0 {
		result.VideoFPS = successSamples[0].FPS
		result.VideoWidth = successSamples[0].VideoWidthPx
		result.VideoHeight = successSamples[0].VideoHeightPx
	}
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
