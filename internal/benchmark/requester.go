package benchmark

import (
	"context"
	"strings"
	"time"
)

// Requester is the interface between the benchmark runner and modality-specific
// adapters. Each modality (LLM/VLM/embedding/reranker/TTS/ASR/T2I/T2V) implements a Requester.
// The runner only cares about concurrency scheduling, timing, and statistical
// aggregation — it delegates all request construction and response parsing to
// the Requester.
type Requester interface {
	// Do constructs and sends a single inference request, returning a sample.
	// seq is the request sequence number (0-based); seq < 0 indicates a warmup
	// request whose result will be discarded.
	Do(ctx context.Context, endpoint string, seq int) (*Sample, error)

	// Modality returns the modality identifier for this adapter.
	// One of: "llm", "vlm", "embedding", "reranker", "tts", "asr", "image_gen", "video_gen".
	Modality() string

	// WarmupRequests returns the number of warmup requests this modality needs.
	// LLM/VLM: typically 2. T2I: 3 (torch.compile). T2V: 1 (too slow).
	WarmupRequests() int
}

// Sample holds measurements from a single inference request.
// Fields are grouped by modality; irrelevant fields remain zero-valued.
type Sample struct {
	// ---- Universal (all modalities) ----
	Seq       int
	LatencyMs float64
	Error     error

	// ---- LLM / VLM ----
	TTFTMs       float64 // Time-to-First-Token (ms)
	InputTokens  int
	OutputTokens int

	// ---- Embedding ----
	EmbeddingDimensions int

	// ---- TTS ----
	TTFAMs         float64 // Time-to-First-Audio chunk (ms)
	AudioDurationS float64 // generated audio duration (seconds)
	InputChars     int     // input text character count

	// ---- ASR ----
	InputAudioS float64 // input audio duration (seconds)
	OutputChars int     // transcribed text character count

	// ---- Image Generation (T2I) ----
	ImagesGenerated int
	StepsCompleted  int
	WidthPx         int
	HeightPx        int

	// ---- Video Generation (T2V / I2V) ----
	VideoDurationS  float64
	FramesGenerated int
	FPS             int
	VideoWidthPx    int
	VideoHeightPx   int
	VideoSteps      int
}

// Compile-time interface compliance checks.
var (
	_ Requester = (*ChatRequester)(nil)
	_ Requester = (*EmbeddingRequester)(nil)
	_ Requester = (*RerankRequester)(nil)
	_ Requester = (*AudioSpeechRequester)(nil)
	_ Requester = (*TranscriptionRequester)(nil)
	_ Requester = (*ImageGenRequester)(nil)
	_ Requester = (*VideoGenRequester)(nil)
)

// AudioInput holds a pre-loaded audio file for ASR benchmarking.
// Audio data is loaded into memory once at init time to avoid disk I/O
// interfering with latency measurements.
type AudioInput struct {
	Filename  string
	Data      []byte
	DurationS float64
}

// baseEndpoint strips any known API path suffix (/v1/chat/completions, etc.)
// from an endpoint URL, returning the scheme+host+port base.
// This lets each requester append its own modality-specific path.
func baseEndpoint(endpoint string) string {
	ep := strings.TrimRight(endpoint, "/")
	for _, suffix := range []string{
		"/v1/chat/completions",
		"/v1/embeddings",
		"/v1/rerank",
		"/v1/audio/speech",
		"/v1/audio/transcriptions",
		"/v1/images/generations",
		"/chat/completions",
		"/embeddings",
		"/rerank",
	} {
		if strings.HasSuffix(ep, suffix) {
			return strings.TrimSuffix(ep, suffix)
		}
	}
	// Strip trailing /v1 as well
	if strings.HasSuffix(ep, "/v1") {
		return strings.TrimSuffix(ep, "/v1")
	}
	return ep
}

// sampleToRequestSample converts a Sample to the legacy RequestSample type
// used by existing LLM/VLM aggregation.
func sampleToRequestSample(s *Sample) RequestSample {
	return RequestSample{
		TTFT:         time.Duration(s.TTFTMs * float64(time.Millisecond)),
		TotalTime:    time.Duration(s.LatencyMs * float64(time.Millisecond)),
		InputTokens:  s.InputTokens,
		OutputTokens: s.OutputTokens,
		Error:        s.Error,
	}
}
