package events

import "sync"

// Bus is a synchronous in-process fan-out. Publish blocks until every
// subscriber has been called; this is intentional so that a log line's
// ordering relative to a step-finished event is preserved.
type Bus struct {
	mu   sync.RWMutex
	subs []func(Event)
}

func NewBus() *Bus { return &Bus{} }

// Subscribe registers a callback. The returned function unsubscribes.
func (b *Bus) Subscribe(fn func(Event)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = append(b.subs, fn)
	idx := len(b.subs) - 1
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if idx < len(b.subs) {
			b.subs[idx] = func(Event) {}
		}
	}
}

// Publish fans out an event.
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	subs := append([]func(Event){}, b.subs...)
	b.mu.RUnlock()
	for _, s := range subs {
		s(e)
	}
}
