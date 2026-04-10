package agent

import (
	"testing"
	"time"
)

func TestPatrolEmitEventPublishesOOMOnlyForOOMCrash(t *testing.T) {
	bus := NewEventBus()
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)

	patrol := NewPatrol(DefaultPatrolConfig(), nil, nil, WithEventBus(bus))
	patrol.emitEvent(Alert{
		Type:    "deploy_crash",
		Message: "deployment failed: crashloop from configuration error",
	})

	select {
	case ev := <-sub:
		t.Fatalf("unexpected event published: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	patrol.emitEvent(Alert{
		Type:    "deploy_crash",
		Message: "deployment failed: CUDA OOM while allocating KV cache",
	})

	select {
	case ev := <-sub:
		if ev.Type != EventPatrolOOM {
			t.Fatalf("event type = %q, want %q", ev.Type, EventPatrolOOM)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for patrol OOM event")
	}
}

func TestPatrolEmitEventPublishesIdle(t *testing.T) {
	bus := NewEventBus()
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)

	patrol := NewPatrol(DefaultPatrolConfig(), nil, nil, WithEventBus(bus))
	patrol.emitEvent(Alert{Type: "gpu_idle", Message: "gpu idle"})

	select {
	case ev := <-sub:
		if ev.Type != EventPatrolIdle {
			t.Fatalf("event type = %q, want %q", ev.Type, EventPatrolIdle)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for patrol idle event")
	}
}

func TestPatrolEmitEventSkipsWithoutBus(t *testing.T) {
	patrol := NewPatrol(DefaultPatrolConfig(), nil, nil)
	patrol.emitEvent(Alert{Type: "deploy_crash", Message: "OOM"})
}
