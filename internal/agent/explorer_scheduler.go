package agent

import (
	"context"
	"log/slog"
	"sync"
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
	mu     sync.RWMutex
	update chan struct{}
}

func NewScheduler(config ScheduleConfig, bus *EventBus) *Scheduler {
	return &Scheduler{
		config: normalizeScheduleConfig(config),
		bus:    bus,
		update: make(chan struct{}),
	}
}

// Start runs the gap scan timer loop until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	s.runLoop(ctx, EventScheduledGapScan, func(config ScheduleConfig) time.Duration {
		return config.GapScanInterval
	})
}

// StartAll starts all timer loops concurrently.
func (s *Scheduler) StartAll(ctx context.Context) {
	go s.runLoop(ctx, EventScheduledGapScan, func(config ScheduleConfig) time.Duration {
		return config.GapScanInterval
	})
	go s.runLoop(ctx, EventScheduledSync, func(config ScheduleConfig) time.Duration {
		return config.SyncInterval
	})
	go s.runLoop(ctx, EventScheduledAudit, func(config ScheduleConfig) time.Duration {
		return config.FullAuditInterval
	})
}

func (s *Scheduler) Config() ScheduleConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

func (s *Scheduler) SetConfig(config ScheduleConfig) {
	s.mu.Lock()
	s.config = normalizeScheduleConfig(config)
	old := s.update
	s.update = make(chan struct{})
	close(old)
	s.mu.Unlock()
}

func (s *Scheduler) runLoop(ctx context.Context, eventType string, intervalFor func(ScheduleConfig) time.Duration) {
	var timer *time.Timer
	update := s.updateChan()
	reset := func() {
		interval := intervalFor(s.Config())
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		if interval > 0 {
			timer = time.NewTimer(interval)
		}
	}
	reset()
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		if timer == nil {
			select {
			case <-ctx.Done():
				return
			case <-update:
				update = s.updateChan()
				reset()
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-update:
			update = s.updateChan()
			reset()
		case <-timer.C:
			if s.isQuietHour(time.Now().Hour()) {
				slog.Debug("scheduler: quiet hour, skipping", "event", eventType)
			} else {
				s.bus.Publish(ExplorerEvent{Type: eventType})
			}
			reset()
		}
	}
}

func (s *Scheduler) isQuietHour(hour int) bool {
	config := s.Config()
	if config.QuietStart == config.QuietEnd {
		return false // no quiet hours configured
	}
	if config.QuietStart < config.QuietEnd {
		return hour >= config.QuietStart && hour < config.QuietEnd
	}
	// Wrap around midnight: e.g. QuietStart=22, QuietEnd=6 means 22:00-06:00
	return hour >= config.QuietStart || hour < config.QuietEnd
}

func (s *Scheduler) updateChan() chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.update
}

func normalizeScheduleConfig(config ScheduleConfig) ScheduleConfig {
	if config.MaxConcurrentRuns <= 0 {
		config.MaxConcurrentRuns = 1
	}
	if config.QuietStart < 0 {
		config.QuietStart = 0
	}
	if config.QuietStart > 23 {
		config.QuietStart = 23
	}
	if config.QuietEnd < 0 {
		config.QuietEnd = 0
	}
	if config.QuietEnd > 23 {
		config.QuietEnd = 23
	}
	return config
}
