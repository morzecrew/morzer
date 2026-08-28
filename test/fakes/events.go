package fakes

import (
	"sync"

	"github.com/morzecrew/morzer/internal/events"
)

// Collector is an events.Sink that records everything, so a test can assert on
// what an operation announced.
//
// It lived in internal/events, next to the bus, with a comment saying it was
// for tests and for the JSON presenter. The presenter stopped using it and the
// comment did not notice, which is how a test helper spends a release looking
// like production code.
type Collector struct {
	mu     sync.Mutex
	events []events.Event
}

func NewCollector() *Collector { return &Collector{} }

var _ events.Sink = (*Collector)(nil)

func (c *Collector) Handle(e events.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

// Events returns a copy of what was collected.
func (c *Collector) Events() []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.Event(nil), c.events...)
}

// OfKind filters the collected events.
func (c *Collector) OfKind(k events.Kind) []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []events.Event
	for _, e := range c.events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = nil
}
