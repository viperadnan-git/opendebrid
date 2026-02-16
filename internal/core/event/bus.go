package event

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

type Handler func(ctx context.Context, event Event) error

type Bus interface {
	Publish(ctx context.Context, event Event) error
	Subscribe(eventType EventType, handler Handler) (unsubscribe func())
}

// NewBus creates an in-process event bus.
func NewBus() Bus {
	return &inProcessBus{
		subscribers: make(map[EventType][]subscriberEntry),
	}
}

type subscriberEntry struct {
	id      uint64
	handler Handler
}

type inProcessBus struct {
	mu          sync.RWMutex
	subscribers map[EventType][]subscriberEntry
	nextID      uint64
}

func (b *inProcessBus) Publish(ctx context.Context, event Event) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	b.mu.RLock()
	subs := make([]subscriberEntry, len(b.subscribers[event.Type]))
	copy(subs, b.subscribers[event.Type])
	b.mu.RUnlock()

	for _, sub := range subs {
		if err := sub.handler(ctx, event); err != nil {
			log.Error().Err(err).
				Str("event", string(event.Type)).
				Msg("event handler error")
		}
	}
	return nil
}

func (b *inProcessBus) Subscribe(eventType EventType, handler Handler) func() {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.subscribers[eventType] = append(b.subscribers[eventType], subscriberEntry{
		id:      id,
		handler: handler,
	})
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		subs := b.subscribers[eventType]
		for i, s := range subs {
			if s.id == id {
				b.subscribers[eventType] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
	}
}
