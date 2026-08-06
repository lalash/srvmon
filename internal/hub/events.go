package hub

import "sync"

// eventHub fans the dashboard payload out to every open Server-Sent Events
// stream. Slow readers are skipped rather than blocking the broadcaster.
type eventHub struct {
	mu   sync.RWMutex
	subs map[chan []byte]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{subs: map[chan []byte]struct{}{}}
}

func (e *eventHub) subscribe() chan []byte {
	ch := make(chan []byte, 4)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch
}

func (e *eventHub) unsubscribe(ch chan []byte) {
	e.mu.Lock()
	if _, ok := e.subs[ch]; ok {
		delete(e.subs, ch)
		close(ch)
	}
	e.mu.Unlock()
}

func (e *eventHub) broadcast(payload []byte) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for ch := range e.subs {
		select {
		case ch <- payload:
		default:
		}
	}
}

func (e *eventHub) hasSubscribers() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.subs) > 0
}
