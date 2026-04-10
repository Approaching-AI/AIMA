package agent

import (
	"context"
	"testing"
	"time"
)

func TestScheduleConfig_Defaults(t *testing.T) {
	c := DefaultScheduleConfig()
	if c.GapScanInterval != 24*time.Hour {
		t.Errorf("GapScanInterval = %v, want 24h", c.GapScanInterval)
	}
	if c.MaxConcurrentRuns != 1 {
		t.Errorf("MaxConcurrentRuns = %d, want 1", c.MaxConcurrentRuns)
	}
}

func TestScheduler_IsQuietHour(t *testing.T) {
	s := &Scheduler{config: ScheduleConfig{QuietStart: 2, QuietEnd: 6}}
	tests := []struct {
		hour int
		want bool
	}{
		{1, false},
		{2, true},
		{4, true},
		{5, true},
		{6, false},
		{12, false},
		{23, false},
	}
	for _, tt := range tests {
		got := s.isQuietHour(tt.hour)
		if got != tt.want {
			t.Errorf("isQuietHour(%d) = %v, want %v", tt.hour, got, tt.want)
		}
	}
}

func TestScheduler_EmitsEvents(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()
	s := NewScheduler(ScheduleConfig{
		GapScanInterval:   50 * time.Millisecond,
		SyncInterval:      0, // disabled
		FullAuditInterval: 0, // disabled
		MaxConcurrentRuns: 1,
		QuietStart:        0,
		QuietEnd:          0, // no quiet hours
	}, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go s.Start(ctx)

	// Should receive at least one gap scan event
	select {
	case ev := <-ch:
		if ev.Type != EventScheduledGapScan {
			t.Errorf("type = %q, want %q", ev.Type, EventScheduledGapScan)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for scheduled event")
	}
}

func TestScheduler_SetConfigReloadsIntervals(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()
	s := NewScheduler(ScheduleConfig{
		GapScanInterval:   0,
		SyncInterval:      0,
		FullAuditInterval: 0,
		MaxConcurrentRuns: 1,
		QuietStart:        0,
		QuietEnd:          0,
	}, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go s.Start(ctx)

	time.Sleep(30 * time.Millisecond)
	s.SetConfig(ScheduleConfig{
		GapScanInterval:   40 * time.Millisecond,
		SyncInterval:      0,
		FullAuditInterval: 0,
		MaxConcurrentRuns: 1,
		QuietStart:        0,
		QuietEnd:          0,
	})

	select {
	case ev := <-ch:
		if ev.Type != EventScheduledGapScan {
			t.Fatalf("type = %q, want %q", ev.Type, EventScheduledGapScan)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for reloaded scheduler event")
	}
}
