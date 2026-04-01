package support

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type httpStatusError struct {
	StatusCode int
	Detail     string
	Body       string
}

func (e *httpStatusError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("support service returned HTTP %d: %s", e.StatusCode, e.Detail)
	}
	if e.Body != "" {
		return fmt.Sprintf("support service returned HTTP %d: %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("support service returned HTTP %d", e.StatusCode)
}

func newHTTPStatusError(statusCode int, body []byte) error {
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	detail, _ := payload["detail"].(string)
	return &httpStatusError{
		StatusCode: statusCode,
		Detail:     detail,
		Body:       strings.TrimSpace(string(body)),
	}
}

func (s *Service) doJSON(ctx context.Context, method, url, token string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		payload = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		return fmt.Errorf("build request %s %s: %w", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response %s %s: %w", method, url, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newHTTPStatusError(resp.StatusCode, data)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response %s %s: %w", method, url, err)
	}
	return nil
}

func classifyRegistrationError(err error) error {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return err
	}
	detail := strings.ToLower(strings.TrimSpace(statusErr.Detail))
	switch {
	case needsRecoveryPrompt(detail):
		return &RegistrationPromptError{
			Kind:   RegistrationPromptRecovery,
			Detail: statusErr.Detail,
		}
	case needsInvitePrompt(detail):
		return &RegistrationPromptError{
			Kind:   RegistrationPromptInviteOrWorker,
			Detail: statusErr.Detail,
		}
	default:
		return err
	}
}

func needsInvitePrompt(detail string) bool {
	if detail == "" {
		return false
	}
	return strings.Contains(detail, "invite_code") ||
		strings.Contains(detail, "invalid invite code") ||
		strings.Contains(detail, "worker_enrollment_code") ||
		strings.Contains(detail, "worker enrollment code") ||
		strings.Contains(detail, "new invite_code required") ||
		strings.Contains(detail, "invite quota exhausted")
}

func needsRecoveryPrompt(detail string) bool {
	if detail == "" {
		return false
	}
	return strings.Contains(detail, "recovery_code") ||
		strings.Contains(detail, "saved recovery code")
}

func isAuthError(err error) bool {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	switch statusErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	default:
		return false
	}
}

func isTransientError(err error) bool {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return statusErr.StatusCode >= 500 && statusErr.StatusCode < 600
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

func (s *Service) retryTransient(ctx context.Context, stage string, err error, delay time.Duration) error {
	s.logger.Warn("support transient failure; retrying", "stage", stage, "error", err, "retry_in", delay.String())
	return sleepContext(ctx, delay)
}

func normalizeEndpoint(raw string) string {
	raw = strings.TrimSpace(strings.TrimRight(raw, "/"))
	if raw == "" {
		return ""
	}
	if strings.HasSuffix(raw, "/api/v1") {
		return raw
	}
	return raw + "/api/v1"
}

func decodeCommand(command, encoding string) (string, error) {
	if !strings.EqualFold(encoding, "base64") {
		return command, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(command)
	if err != nil {
		return "", fmt.Errorf("decode base64 command: %w", err)
	}
	return string(decoded), nil
}
