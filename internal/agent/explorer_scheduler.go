package agent

import (
	"context"
	"log/slog"
	"time"
)

// ScheduleConfig controls the Explorer's periodic behavior.
type ScheduleConfig struct {
	GapScanInterval   time.Duration // default 24h
	FullAuditInterval time.Duration // default 7d
	SyncInterval      time.Duration // default 6h
	MaxConcurrentRuns int           // default 1
	QuietStart        int           // hour 0-23, default 2
	QuietEnd          int           // hour 0-23, default 6
}

func DefaultScheduleConfig() ScheduleConfig {
	return ScheduleConfig{
		GapScanInterval:   24 * time.Hour,
		FullAuditInterval: 7 * 24 * time.Hour,
		SyncInterval:      6 * time.Hour,
		MaxConcurrentRuns: 1,
		QuietStart:        2,
		QuietEnd:          6,
	}
}

// Scheduler emits timed ExplorerEvents to the EventBus.
type Scheduler struct {
	config ScheduleConfig
	bus    *EventBus
}

func NewScheduler(config ScheduleConfig, bus *EventBus) *Scheduler {
	return &Scheduler{config: config, bus: bus}
}

// Start runs the gap scan timer loop until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	s.runLoop(ctx, s.config.GapScanInterval, EventScheduledGapScan)
}

// StartAll starts all timer loops concurrently.
func (s *Scheduler) StartAll(ctx context.Context) {
	if s.config.GapScanInterval > 0 {
		go s.runLoop(ctx, s.config.GapScanInterval, EventScheduledGapScan)
	}
	if s.config.SyncInterval > 0 {
		go s.runLoop(ctx, s.config.SyncInterval, EventScheduledSync)
	}
	if s.config.FullAuditInterval > 0 {
		go s.runLoop(ctx, s.config.FullAuditInterval, EventScheduledAudit)
	}
}

func (s *Scheduler) runLoop(ctx context.Context, interval time.Duration, eventType string) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if s.isQuietHour(time.Now().Hour()) {
				slog.Debug("scheduler: quiet hour, skipping", "event", eventType)
			} else {
				s.bus.Publish(ExplorerEvent{Type: eventType})
			}
			timer.Reset(interval)
		}
	}
}

func (s *Scheduler) isQuietHour(hour int) bool {
	if s.config.QuietStart == s.config.QuietEnd {
		return false // no quiet hours configured
	}
	return hour >= s.config.QuietStart && hour < s.config.QuietEnd
}
