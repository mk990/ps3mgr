package events

import (
	"sync"
	"time"

	"ps3mgr/internal/domain"
)

type Bus struct {
	mu   sync.RWMutex
	next int
	subs map[int]chan domain.Event
}

func New() *Bus { return &Bus{subs: make(map[int]chan domain.Event)} }

func (b *Bus) Publish(eventType string, payload any) {
	event := domain.Event{Type: eventType, Time: time.Now(), Payload: payload}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- event:
		default:
		}
	}
}

func (b *Bus) Subscribe(buffer int) (<-chan domain.Event, func()) {
	if buffer < 1 {
		buffer = 1
	}
	b.mu.Lock()
	id := b.next
	b.next++
	ch := make(chan domain.Event, buffer)
	b.subs[id] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			close(ch)
			b.mu.Unlock()
		})
	}
}
