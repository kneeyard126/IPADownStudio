package manager

import (
	"sync"
	"time"
)

// Event is pushed to connected web clients via Server-Sent Events (SSE).
type Event struct {
	Type      string    `json:"type"`
	Data      any       `json:"data"`
	Timestamp time.Time `json:"timestamp"`
}

// EventHub broadcasts real-time events to subscribed SSE clients without blocking producers.
type EventHub struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

// NewEventHub creates a new EventHub.
func NewEventHub() *EventHub {
	return &EventHub{
		subs: make(map[chan Event]struct{}),
	}
}

// Subscribe registers a listener channel and returns an unsubscribe cleanup callback.
func (h *EventHub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// Broadcast sends an event to all subscribers, dropping messages for saturated buffers.
func (h *EventHub) Broadcast(eventType string, data any) {
	ev := Event{
		Type:      eventType,
		Data:      data,
		Timestamp: time.Now().UTC(),
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
