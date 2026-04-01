package support

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

func (s *Service) executeCommands(ctx context.Context, endpoint string, state deviceState, resp pollResponse) error {
	current := resp
	for current.CommandID != "" && current.Command != "" {
		ack, err := s.executeSingleCommand(ctx, endpoint, state, current)
		if err != nil {
			return err
		}
		if ack.PollIntervalSeconds > 0 {
			state.PollIntervalSeconds = ack.PollIntervalSeconds
			_ = s.store.SetConfig(ctx, configStatePollIntervalSec, fmt.Sprintf("%d", ack.PollIntervalSeconds))
		}
		if ack.NextCommandID == "" || ack.NextCommand == "" {
			return nil
		}
		current = pollResponse{
			CommandID:             ack.NextCommandID,
			Command:               ack.NextCommand,
			CommandEncoding:       ack.NextCommandEncoding,
			CommandTimeoutSeconds: ack.NextCommandTimeoutSeconds,
			CommandIntent:         ack.NextCommandIntent,
		}
	}
	return nil
}

func (s *Service) executeSingleCommand(ctx context.Context, endpoint string, state deviceState, resp pollResponse) (commandResultAckResponse, error) {
	command, err := decodeCommand(resp.Command, resp.CommandEncoding)
	if err != nil {
		return commandResultAckResponse{}, err
	}

	// Surface command intent to message log so UI can show what's happening
	if resp.CommandIntent != "" {
		s.appendMessage(MessageSnapshot{
			Message: resp.CommandIntent,
			Type:    "command_intent",
			Phase:   "start",
		})
	}

	timeoutSeconds := resp.CommandTimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 300
	}
	startedAt := s.now()
	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	stdoutBuf := newSafeBuffer(s.outputLimit)
	stderrBuf := newSafeBuffer(s.outputLimit)
	cmd := shellCommand(cmdCtx, command)
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		stderrBuf.AppendString(err.Error())
		return s.submitResultWithRetry(ctx, endpoint, state, map[string]any{
			"command_id": resp.CommandID,
			"exit_code":  127,
			"stdout":     stdoutBuf.String(),
			"stderr":     stderrBuf.String(),
			"result_id":  buildResultID(s.now()),
		})
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	ticker := time.NewTicker(s.progressInterval)
	defer ticker.Stop()

	remoteCanceled := false
	for {
		select {
		case err := <-waitCh:
			if errors.Is(ctx.Err(), context.Canceled) {
				return commandResultAckResponse{}, ctx.Err()
			}

			exitCode := 0
			switch {
			case remoteCanceled:
				exitCode = 130
				stderrBuf.AppendString("\nCommand cancelled after remote request\n")
			case errors.Is(cmdCtx.Err(), context.DeadlineExceeded):
				exitCode = 124
				stderrBuf.AppendString(fmt.Sprintf("\nCommand timed out after %ds\n", timeoutSeconds))
			case err == nil:
				exitCode = 0
			default:
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = 1
					stderrBuf.AppendString("\n" + err.Error() + "\n")
				}
			}

			return s.submitResultWithRetry(ctx, endpoint, state, map[string]any{
				"command_id": resp.CommandID,
				"exit_code":  exitCode,
				"stdout":     stdoutBuf.String(),
				"stderr":     stderrBuf.String(),
				"result_id":  buildResultID(s.now()),
			})

		case <-ticker.C:
			elapsed := s.now().Sub(startedAt).Round(time.Second)
			ack, err := s.submitProgress(ctx, endpoint, state, resp.CommandID, stdoutBuf.Snapshot(s.previewLimit), stderrBuf.Snapshot(s.previewLimit), fmt.Sprintf("Command still running (%s)", elapsed))
			if err != nil {
				s.logger.Warn("support progress update failed", "command_id", resp.CommandID, "error", err)
				continue
			}
			if ack.CancelRequested || strings.EqualFold(ack.CommandStatus, "cancelled") {
				remoteCanceled = true
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			}

		case <-ctx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-waitCh
			return commandResultAckResponse{}, ctx.Err()
		}
	}
}

func (s *Service) submitProgress(ctx context.Context, endpoint string, state deviceState, commandID, stdout, stderr, message string) (commandProgressAckResponse, error) {
	var resp commandProgressAckResponse
	err := s.doJSON(ctx, http.MethodPost, endpoint+"/devices/"+state.DeviceID+"/commands/"+commandID+"/progress", state.Token, map[string]any{
		"stdout":  stdout,
		"stderr":  stderr,
		"message": message,
	}, &resp)
	return resp, err
}

func (s *Service) submitResultWithRetry(ctx context.Context, endpoint string, state deviceState, body map[string]any) (commandResultAckResponse, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		var resp commandResultAckResponse
		err := s.doJSON(ctx, http.MethodPost, endpoint+"/devices/"+state.DeviceID+"/result", state.Token, body, &resp)
		if err == nil {
			return resp, nil
		}
		if isAuthError(err) {
			return commandResultAckResponse{}, err
		}
		lastErr = err
		if err := sleepContext(ctx, time.Duration(attempt+1)*time.Second); err != nil {
			return commandResultAckResponse{}, err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("submit result failed")
	}
	return commandResultAckResponse{}, lastErr
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-lc", command)
}

func buildResultID(now time.Time) string {
	return fmt.Sprintf("res_%d", now.UnixNano())
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func newSafeBuffer(max int) *safeBuffer {
	return &safeBuffer{max: max}
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max <= 0 {
		return len(p), nil
	}
	remaining := b.max - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
		} else {
			_, _ = b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *safeBuffer) AppendString(s string) {
	_, _ = b.Write([]byte(s))
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *safeBuffer) Snapshot(limit int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	if limit <= 0 || len(s) <= limit {
		return s
	}
	// Find last valid UTF-8 boundary before limit
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}
