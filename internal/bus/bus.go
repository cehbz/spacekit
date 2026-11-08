// Package bus is the agent's event stream: every published event reaches
// every subscriber whose types admit it, in order, over a buffered channel.
// Publish never blocks, so the main thread's notification callbacks can
// publish directly; a subscriber that falls Depth events behind loses the
// event and the loss is counted.
package bus

import (
	"log"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/cehbz/spacekit/internal/event"
)

// Depth is each subscriber's channel capacity: 68 s of a drag at frame
// rate with nothing consumed.
const Depth = 4096

type Bus struct {
	mu   sync.Mutex
	subs []*Subscription
}

func New() *Bus { return &Bus{} }

// Publish hands e to every subscription that admits its type.
func (b *Bus) Publish(e event.Event) {
	t := reflect.TypeOf(e)
	b.mu.Lock()
	subs := b.subs
	b.mu.Unlock()
	for _, s := range subs {
		if len(s.types) == 0 || s.types[t] {
			s.push(e)
		}
	}
}

// Subscribe returns a subscription to events of the given types, or to
// every event when none is given. name labels the drop log.
func (b *Bus) Subscribe(name string, types ...event.Event) *Subscription {
	s := &Subscription{bus: b, name: name, types: make(map[reflect.Type]bool, len(types)), ch: make(chan event.Event, Depth)}
	for _, t := range types {
		s.types[reflect.TypeOf(t)] = true
	}
	b.mu.Lock()
	b.subs = append(b.subs, s)
	b.mu.Unlock()
	return s
}

// Close ends every subscription: their channels close once drained.
func (b *Bus) Close() {
	b.mu.Lock()
	subs := b.subs
	b.subs = nil
	b.mu.Unlock()
	for _, s := range subs {
		s.end()
	}
}

type Subscription struct {
	bus     *Bus
	name    string
	types   map[reflect.Type]bool
	ch      chan event.Event
	dropped atomic.Int64
	mu      sync.Mutex
	closed  bool
}

// C delivers the subscription's events in order; it closes after Close,
// once queued events have been read.
func (s *Subscription) C() <-chan event.Event { return s.ch }

// Dropped is how many events were lost to a full channel.
func (s *Subscription) Dropped() int64 { return s.dropped.Load() }

// Close unsubscribes.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	for i, o := range s.bus.subs {
		if o == s {
			s.bus.subs = append(s.bus.subs[:i:i], s.bus.subs[i+1:]...)
			break
		}
	}
	s.bus.mu.Unlock()
	s.end()
}

func (s *Subscription) push(e event.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- e:
	default:
		if s.dropped.Add(1) == 1 {
			log.Printf("bus: %s fell %d events behind; dropping", s.name, Depth)
		}
	}
}

func (s *Subscription) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}
