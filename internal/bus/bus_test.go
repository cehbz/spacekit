package bus

import (
	"fmt"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/event"
)

func moved(w uint32) event.Event { return event.WindowMoved{Stamp: event.Now(), Window: w} }

// recv reads one event from s, failing the test naming what it waited for
// if none arrives within two seconds. ok is false when the channel is closed.
func recv(t *testing.T, s *Subscription, what string) (e event.Event, ok bool) {
	t.Helper()
	select {
	case e, ok = <-s.C():
		return e, ok
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil, false
	}
}

func TestSubscriberReceivesInOrder(t *testing.T) {
	b := New()
	s := b.Subscribe("t", event.WindowMoved{})
	for i := 0; i < 100; i++ {
		b.Publish(moved(uint32(i)))
	}
	for i := 0; i < 100; i++ {
		e, _ := recv(t, s, fmt.Sprintf("event %d", i))
		if m, ok := e.(event.WindowMoved); !ok || m.Window != uint32(i) {
			t.Fatalf("event %d: %+v", i, e)
		}
	}
}

func TestSubscriptionByType(t *testing.T) {
	b := New()
	s := b.Subscribe("t", event.WindowResized{})
	b.Publish(moved(1))
	b.Publish(event.WindowResized{Stamp: event.Now(), Window: 2})
	if e, _ := recv(t, s, "the WindowResized"); e == nil || e.(event.WindowResized).Window != 2 {
		t.Fatalf("got %+v", e)
	}
	all := b.Subscribe("all")
	b.Publish(event.SpaceSwitched{Stamp: event.Now()})
	e, _ := recv(t, all, "SpaceSwitched on the subscribe-all")
	if _, ok := e.(event.SpaceSwitched); !ok {
		t.Fatal("subscribe-all missed the event")
	}
}

func TestPublishDropsWhenFullAndCounts(t *testing.T) {
	b := New()
	s := b.Subscribe("slow")
	done := make(chan struct{})
	go func() {
		for i := 0; i < Depth+10; i++ {
			b.Publish(moved(uint32(i)))
		}
		close(done)
	}()
	select {
	case <-done: // Publish never blocked
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
	if s.Dropped() != 10 {
		t.Fatalf("Dropped = %d, want 10", s.Dropped())
	}
	n := 0
	for len(s.C()) > 0 {
		<-s.C()
		n++
	}
	if n != Depth {
		t.Fatalf("delivered %d, want %d", n, Depth)
	}
}

func TestCloseLeavesQueuedEventsReadable(t *testing.T) {
	b := New()
	s := b.Subscribe("t")
	b.Publish(moved(1))
	b.Publish(moved(2))
	b.Close()
	var got []uint32
	for {
		e, ok := recv(t, s, "queued events then close")
		if !ok {
			break
		}
		got = append(got, e.(event.WindowMoved).Window)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("drained %v, want [1 2]", got)
	}
}

func TestSubscriptionClose(t *testing.T) {
	b := New()
	s := b.Subscribe("t")
	s.Close()
	b.Publish(moved(1)) // nowhere to go, no panic
	if _, ok := recv(t, s, "the channel to close"); ok {
		t.Fatal("closed subscription delivered an event")
	}
}
