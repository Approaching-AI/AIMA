package agent

import (
	"testing"
	"time"
)

func TestEventBus_PublishSubscribe(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()

	bus.Publish(ExplorerEvent{Type: EventDeployCompleted, Model: "qwen3-8b"})

	select {
	case ev := <-ch:
		if ev.Type != EventDeployCompleted {
			t.Errorf("type = %q, want %q", ev.Type, EventDeployCompleted)
		}
		if ev.Model != "qwen3-8b" {
			t.Errorf("model = %q, want %q", ev.Model, "qwen3-8b")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()

	bus.Publish(ExplorerEvent{Type: EventPatrolOOM})

	for _, ch := range []chan ExplorerEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Type != EventPatrolOOM {
				t.Errorf("type = %q, want %q", ev.Type, EventPatrolOOM)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout")
		}
	}
}

func TestEventBus_NonBlocking(t *testing.T) {
	bus := NewEventBus()
	_ = bus.Subscribe() // subscriber that never reads

	// Publish should not block even if subscriber isn't reading
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			bus.Publish(ExplorerEvent{Type: EventPatrolIdle})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked")
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()
	bus.Unsubscribe(ch)

	bus.Publish(ExplorerEvent{Type: EventPatrolOOM})

	select {
	case <-ch:
		t.Fatal("received event after unsubscribe")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}
