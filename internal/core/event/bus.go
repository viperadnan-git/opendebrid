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
	// Close shuts down dispatch goroutines. Safe to call multiple times.
	Close()
}

const defaultChannelSize = 256

// NewBus creates an in-process event bus with async dispatch.
// Each event type with subscribers gets a dedicated buffered channel and
// dispatch goroutine. Publish is non-blocking as long as the channel has
// capacity; events are dropped (with a warning) if the buffer is full.
func NewBus() Bus {
	return &asyncBus{
		subscribers: make(map[EventType][]subscriberEntry),
		channels:    make(map[EventType]chan Event),
		done:        make(chan struct{}),
	}
}

type subscriberEntry struct {
	id      uint64
	handler Handler
}

type asyncBus struct {
	mu          sync.RWMutex
	subscribers map[EventType][]subscriberEntry
	channels    map[EventType]chan Event
	nextID      uint64

	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
}

func (b *asyncBus) Publish(_ context.Context, event Event) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	b.mu.RLock()
	ch, ok := b.channels[event.Type]
	b.mu.RUnlock()
	if !ok {
		return nil // no subscribers for this event type
	}

	select {
	case ch <- event:
	default:
		log.Warn().
			Str("event", string(event.Type)).
			Msg("event bus channel full, dropping event")
	}
	return nil
}

func (b *asyncBus) Subscribe(eventType EventType, handler Handler) func() {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.subscribers[eventType] = append(b.subscribers[eventType], subscriberEntry{
		id:      id,
		handler: handler,
	})

	// Start dispatch goroutine for this event type if it doesn't exist yet
	if _, ok := b.channels[eventType]; !ok {
		ch := make(chan Event, defaultChannelSize)
		b.channels[eventType] = ch
		b.wg.Add(1)
		go b.dispatch(eventType, ch)
	}
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

// dispatch reads events from the channel and calls all handlers for the event type.
func (b *asyncBus) dispatch(eventType EventType, ch <-chan Event) {
	defer b.wg.Done()
	for {
		select {
		case <-b.done:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b.mu.RLock()
			subs := make([]subscriberEntry, len(b.subscribers[eventType]))
			copy(subs, b.subscribers[eventType])
			b.mu.RUnlock()

			for _, sub := range subs {
				if err := sub.handler(context.Background(), ev); err != nil {
					log.Error().Err(err).
						Str("event", string(eventType)).
						Msg("event handler error")
				}
			}
		}
	}
}

func (b *asyncBus) Close() {
	b.closeOnce.Do(func() {
		close(b.done)
		b.wg.Wait()
	})
}
