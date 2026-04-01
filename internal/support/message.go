package support

import (
	"context"
	"fmt"
	"time"
)

// appendMessage adds a message to the in-memory log with a monotonically increasing seq.
func (s *Service) appendMessage(msg MessageSnapshot) {
	s.msgMu.Lock()
	defer s.msgMu.Unlock()
	s.msgSeq++
	msg.Seq = s.msgSeq
	msg.UpdatedAt = s.now().UTC().Format(time.RFC3339)
	s.msgLog = append(s.msgLog, msg)
	if len(s.msgLog) > maxMessageLog {
		s.msgLog = s.msgLog[len(s.msgLog)-maxMessageLog:]
	}
}

// MessagesSince returns all messages with seq > afterSeq.
func (s *Service) MessagesSince(afterSeq int64) []MessageSnapshot {
	s.msgMu.Lock()
	defer s.msgMu.Unlock()
	var result []MessageSnapshot
	for _, m := range s.msgLog {
		if m.Seq > afterSeq {
			result = append(result, m)
		}
	}
	return result
}

func (s *Service) emitNotification(ctx context.Context, notify NotifyFunc, notification Notification) {
	if notify != nil {
		notify(ctx, notification)
		return
	}
	if notification.Message != "" {
		s.logger.Info("support notification", "message", notification.Message, "type", notification.Type, "phase", notification.Phase)
		return
	}
	if notification.TaskID != "" {
		s.logger.Info("support task completed", "task_id", notification.TaskID, "status", notification.TaskStatus)
	}
}

func defaultInteractionAnswer(resp pollResponse) string {
	return fmt.Sprintf("AIMA local support client has no interactive answer for %q. Continue autonomously if possible, and explain any assumptions you make.", resp.Question)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextPollInterval(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultPollInterval
	}
	return time.Duration(seconds) * time.Second
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
