package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// TranscriptionRequester implements Requester for ASR (automatic speech recognition)
// benchmarking. It sends pre-loaded audio files to an OpenAI-compatible
// /v1/audio/transcriptions endpoint and measures latency and output quality.
type TranscriptionRequester struct {
	Model      string
	AudioFiles []AudioInput // pre-loaded into memory
	Language   string       // optional: "zh", "en"
	APIKey     string
	Timeout    time.Duration
}

func (r *TranscriptionRequester) Modality() string   { return "asr" }
func (r *TranscriptionRequester) WarmupRequests() int { return 2 }

func (r *TranscriptionRequester) Do(ctx context.Context, endpoint string, seq int) (*Sample, error) {
	if len(r.AudioFiles) == 0 {
		return &Sample{Seq: seq, Error: fmt.Errorf("no audio files configured for ASR benchmark")}, nil
	}

	idx := seq
	if idx < 0 {
		idx = 0
	}
	audio := r.AudioFiles[idx%len(r.AudioFiles)]

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build multipart/form-data body.
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// "file" field: audio binary with filename
	filePart, err := writer.CreateFormFile("file", audio.Filename)
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("create multipart file field: %w", err)}, nil
	}
	if _, err := filePart.Write(audio.Data); err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("write audio data: %w", err)}, nil
	}

	// "model" field
	if err := writer.WriteField("model", r.Model); err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("write model field: %w", err)}, nil
	}

	// "language" field (optional)
	if r.Language != "" {
		if err := writer.WriteField("language", r.Language); err != nil {
			return &Sample{Seq: seq, Error: fmt.Errorf("write language field: %w", err)}, nil
		}
	}

	if err := writer.Close(); err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("close multipart writer: %w", err)}, nil
	}

	url := baseEndpoint(endpoint) + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, &buf)
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("create ASR request: %w", err)}, nil
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	startTime := time.Now()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("send ASR request: %w", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &Sample{Seq: seq, Error: fmt.Errorf("ASR HTTP %d: %s", resp.StatusCode, string(errBody))}, nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("read ASR response: %w", err)}, nil
	}

	latency := time.Since(startTime)

	// Parse the transcription response: {"text": "..."}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("parse ASR response: %w", err)}, nil
	}

	return &Sample{
		Seq:         seq,
		LatencyMs:   float64(latency.Microseconds()) / 1000.0,
		InputAudioS: audio.DurationS,
		OutputChars: len(result.Text),
	}, nil
}
