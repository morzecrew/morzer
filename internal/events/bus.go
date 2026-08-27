package events

import (
	"fmt"
	"sync"
)

func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

// Sink consumes events. Presenters, the log handler, and the Notifier adapter
// all implement it.
//
// A Sink must not block: the engine publishes synchronously so that ordering
// is preserved and a crash cannot lose the events that explain it. Sinks that
// need to do slow work buffer internally.
type Sink interface {
	Handle(Event)
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(Event)

func (f SinkFunc) Handle(e Event) { f(e) }

// Bus fans events from the engine out to subscribers.
//
// This is the only channel through which the UI learns anything. The engine
// never imports a presenter, and no subscriber can influence control flow --
// Handle returns nothing, so a presenter has no way to signal back.
type Bus struct {
	mu     sync.RWMutex
	sinks  []subscription
	nextID int

	// recover controls whether a panicking sink is contained. It is on in
	// production: a renderer that panics because the terminal was resized
	// into nonsense must not abort an update that is halfway through
	// migrating a database. Tests turn it off so a broken sink fails
	// loudly instead of silently.
	recover bool

	// onPanic is notified when a sink panics, so the failure lands in the
	// log rather than vanishing.
	onPanic func(any)
}

type subscription struct {
	id   int
	sink Sink
}

// NewBus returns a bus that contains panicking sinks.
func NewBus() *Bus {
	return &Bus{recover: true}
}

// NewStrictBus returns a bus that lets sink panics propagate. For tests.
func NewStrictBus() *Bus {
	return &Bus{recover: false}
}

// OnPanic registers a callback for contained sink panics.
func (b *Bus) OnPanic(f func(any)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onPanic = f
}

// Subscribe adds a sink and returns a function that removes it.
func (b *Bus) Subscribe(s Sink) (unsubscribe func()) {
	if s == nil {
		return func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.sinks = append(b.sinks, subscription{id: id, sink: s})
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, sub := range b.sinks {
			if sub.id == id {
				b.sinks = append(b.sinks[:i], b.sinks[i+1:]...)
				return
			}
		}
	}
}

// SubscribeFunc is Subscribe for a plain function.
func (b *Bus) SubscribeFunc(f func(Event)) (unsubscribe func()) {
	return b.Subscribe(SinkFunc(f))
}

// Publish delivers an event to every subscriber, in subscription order.
//
// Delivery is synchronous and best-effort: a failed presenter is logged and
// dropped, never propagated. The operation continues regardless of what the
// UI does with the news.
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	sinks := make([]Sink, len(b.sinks))
	for i, sub := range b.sinks {
		sinks[i] = sub.sink
	}
	onPanic := b.onPanic
	shouldRecover := b.recover
	b.mu.RUnlock()

	for _, s := range sinks {
		deliver(s, e, shouldRecover, onPanic)
	}
}

func deliver(s Sink, e Event, shouldRecover bool, onPanic func(any)) {
	if shouldRecover {
		defer func() {
			if r := recover(); r != nil && onPanic != nil {
				onPanic(r)
			}
		}()
	}
	s.Handle(e)
}
