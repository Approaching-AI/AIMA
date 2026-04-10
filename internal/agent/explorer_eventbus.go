package agent

import (
	"encoding/json"
	"sync"
	"time"
)

// Event types emitted by various AIMA components.
const (
	EventDeployCompleted  = "deploy.completed"
	EventPatrolOOM        = "patrol.alert.oom"
	EventPatrolIdle       = "patrol.alert.idle"
	EventModelDiscovered  = "model.discovered"
	EventCentralAdvisory  = "central.advisory"
	EventCentralScenario  = "central.scenario"
	EventScheduledGapScan = "scheduled.gap_scan"
	EventScheduledAudit   = "scheduled.full_audit"
	EventScheduledSync    = "scheduled.sync"
)

// ExplorerEvent carries event data through the EventBus.
type ExplorerEvent struct {
	Type      string
	Model     string
	Engine    string
	AlertID   string
	Advisory  json.RawMessage // advisory payload for central.advisory events
	Timestamp time.Time
}

// EventBus is a simple in-process pub/sub for Explorer events.
type EventBus struct {
	mu   sync.RWMutex
	subs map[chan ExplorerEvent]struct{}
}

func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[chan ExplorerEvent]struct{})}
}

func (b *EventBus) Subscribe() chan ExplorerEvent {
	ch := make(chan ExplorerEvent, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *EventBus) Unsubscribe(ch chan ExplorerEvent) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

func (b *EventBus) Publish(ev ExplorerEvent) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Drop if subscriber is full -- non-blocking.
		}
	}
}
