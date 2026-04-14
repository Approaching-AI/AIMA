package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// defaultTTSTexts provides fallback test texts when none are configured.
var defaultTTSTexts = []string{
	"The quick brown fox jumps over the lazy dog. This sentence contains every letter of the English alphabet and is commonly used for testing.",
}

// AudioSpeechRequester implements Requester for TTS (text-to-speech) benchmarking.
// It sends requests to an OpenAI-compatible /v1/audio/speech endpoint and measures
// latency, time-to-first-audio-chunk, and generated audio duration.
type AudioSpeechRequester struct {
	Model   string
	Voice   string   // e.g. "alloy", "echo"
	Format  string   // "pcm", "mp3", "wav", "opus"
	Texts   []string // test text corpus, cycled by seq
	APIKey  string
	Timeout time.Duration
}

func (r *AudioSpeechRequester) Modality() string   { return "tts" }
func (r *AudioSpeechRequester) WarmupRequests() int { return 2 }

func (r *AudioSpeechRequester) Do(ctx context.Context, endpoint string, seq int) (*Sample, error) {
	texts := r.Texts
	if len(texts) == 0 {
		texts = defaultTTSTexts
	}

	idx := seq
	if idx < 0 {
		idx = 0
	}
	text := texts[idx%len(texts)]

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	voice := r.Voice
	if voice == "" {
		voice = "alloy"
	}
	format := r.Format
	if format == "" {
		format = "pcm"
	}

	payload := map[string]string{
		"model":           r.Model,
		"input":           text,
		"voice":           voice,
		"response_format": format,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("marshal TTS request: %w", err)}, nil
	}

	url := baseEndpoint(endpoint) + "/v1/audio/speech"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("create TTS request: %w", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	startTime := time.Now()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("send TTS request: %w", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &Sample{Seq: seq, Error: fmt.Errorf("TTS HTTP %d: %s", resp.StatusCode, string(errBody))}, nil
	}

	// Read response in chunks to detect time-to-first-audio.
	// The first non-empty read from the body is the first audio chunk.
	var ttfa time.Duration
	buf := make([]byte, 4096)
	var audioData []byte

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if ttfa == 0 {
				ttfa = time.Since(startTime)
			}
			audioData = append(audioData, buf[:n]...)
		}
		if readErr != nil {
			if readErr != io.EOF {
				return &Sample{Seq: seq, Error: fmt.Errorf("read TTS response: %w", readErr)}, nil
			}
			break
		}
	}

	latency := time.Since(startTime)

	// Calculate audio duration from raw PCM data.
	// OpenAI TTS default PCM format: 24000 Hz, mono, 16-bit (2 bytes per sample).
	var audioDurationS float64
	if format == "pcm" {
		const (
			sampleRate     = 24000
			channels       = 1
			bytesPerSample = 2
		)
		bytesPerSecond := sampleRate * channels * bytesPerSample
		if bytesPerSecond > 0 {
			audioDurationS = float64(len(audioData)) / float64(bytesPerSecond)
		}
	}
	// For non-PCM formats (mp3, wav, opus), we cannot reliably calculate
	// duration without a decoder. Leave AudioDurationS as 0; the runner
	// will skip RTF calculation for zero values.

	return &Sample{
		Seq:            seq,
		LatencyMs:      float64(latency.Microseconds()) / 1000.0,
		TTFAMs:         float64(ttfa.Microseconds()) / 1000.0,
		AudioDurationS: audioDurationS,
		InputChars:     len(text),
	}, nil
}
