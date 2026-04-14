package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ImageGenRequester implements Requester for text-to-image benchmarks.
type ImageGenRequester struct {
	Model         string
	Prompt        string
	Width         int
	Height        int
	Steps         int
	GuidanceScale float64
	NumImages     int
	APIKey        string
	Timeout       time.Duration
}

func (r *ImageGenRequester) Modality() string    { return "image_gen" }
func (r *ImageGenRequester) WarmupRequests() int  { return 3 }

func (r *ImageGenRequester) Do(ctx context.Context, endpoint string, seq int) (*Sample, error) {
	prompt := r.Prompt
	if prompt == "" {
		prompt = "A photo of an astronaut riding a horse on Mars"
	}
	width := r.Width
	if width == 0 {
		width = 1024
	}
	height := r.Height
	if height == 0 {
		height = 1024
	}
	steps := r.Steps
	if steps == 0 {
		steps = 28
	}
	numImages := r.NumImages
	if numImages == 0 {
		numImages = 1
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	base := baseEndpoint(endpoint)

	// Try OpenAI-compatible endpoint first
	sample := r.tryOpenAI(ctx, base, prompt, width, height, numImages)
	if sample.Error == nil {
		sample.Seq = seq
		sample.StepsCompleted = steps
		sample.WidthPx = width
		sample.HeightPx = height
		return &sample, nil
	}

	// Check if error was 404 — fall back to custom format
	if !isNotFoundError(sample.Error) {
		sample.Seq = seq
		sample.StepsCompleted = steps
		sample.WidthPx = width
		sample.HeightPx = height
		return &sample, nil
	}

	// Fall back to custom format: POST directly to endpoint
	sample = r.tryCustom(ctx, endpoint, prompt, width, height, steps, numImages)
	sample.Seq = seq
	sample.StepsCompleted = steps
	sample.WidthPx = width
	sample.HeightPx = height
	return &sample, nil
}

func (r *ImageGenRequester) tryOpenAI(ctx context.Context, base, prompt string, width, height, numImages int) Sample {
	url := strings.TrimRight(base, "/") + "/v1/images/generations"

	payload := map[string]any{
		"model":  r.Model,
		"prompt": prompt,
		"size":   fmt.Sprintf("%dx%d", width, height),
		"n":      numImages,
	}

	start := time.Now()
	resp, err := r.doPost(ctx, url, payload)
	latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		return Sample{LatencyMs: latencyMs, Error: err}
	}

	imagesGenerated := numImages
	if n, ok := resp["created"]; ok {
		// OpenAI response: check data array length
		if data, ok := resp["data"].([]any); ok {
			imagesGenerated = len(data)
		}
		_ = n
	}

	return Sample{
		LatencyMs:       latencyMs,
		ImagesGenerated: imagesGenerated,
	}
}

func (r *ImageGenRequester) tryCustom(ctx context.Context, endpoint, prompt string, width, height, steps, numImages int) Sample {
	payload := map[string]any{
		"prompt":              prompt,
		"width":               width,
		"height":              height,
		"num_inference_steps": steps,
		"num_images":          numImages,
	}
	if r.GuidanceScale > 0 {
		payload["guidance_scale"] = r.GuidanceScale
	}

	start := time.Now()
	resp, err := r.doPost(ctx, endpoint, payload)
	latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		return Sample{LatencyMs: latencyMs, Error: err}
	}

	imagesGenerated := numImages
	if images, ok := resp["images"].([]any); ok {
		imagesGenerated = len(images)
	}

	return Sample{
		LatencyMs:       latencyMs,
		ImagesGenerated: imagesGenerated,
	}
}

func (r *ImageGenRequester) doPost(ctx context.Context, url string, payload map[string]any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal image request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create image request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send image request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read image response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, &notFoundError{status: resp.StatusCode, body: string(respBody)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("image HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 512))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		// Non-JSON response is OK for image endpoints (could be raw image data)
		return map[string]any{}, nil
	}
	return result, nil
}

type notFoundError struct {
	status int
	body   string
}

func (e *notFoundError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*notFoundError)
	return ok
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
