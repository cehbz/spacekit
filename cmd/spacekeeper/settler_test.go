package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
)

func expectEvent(t *testing.T, s *bus.Subscription, want event.Event, within time.Duration) event.Event {
	t.Helper()
	select {
	case e := <-s.C():
		if reflect.TypeOf(e) != reflect.TypeOf(want) {
			t.Fatalf("got %T, want %T", e, want)
		}
		return e
	case <-time.After(within):
		t.Fatalf("no %T within %v", want, within)
	}
	return nil
}

func expectNone(t *testing.T, s *bus.Subscription, within time.Duration) {
	t.Helper()
	select {
	case e := <-s.C():
		t.Fatalf("unexpected %T", e)
	case <-time.After(within):
	}
}

func TestWindowQuietPerWindow(t *testing.T) {
	b := bus.New()
	out := b.Subscribe("t", WindowQuiet{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newSettler(b, 150*time.Millisecond, time.Second).run(ctx)
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 1})
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 2})
	time.Sleep(30 * time.Millisecond)
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 1}) // keeps 1 busy
	if e := expectEvent(t, out, WindowQuiet{}, 400*time.Millisecond); e.(WindowQuiet).Window != 2 {
		t.Fatalf("first settled %+v, want window 2", e)
	}
	if e := expectEvent(t, out, WindowQuiet{}, 400*time.Millisecond); e.(WindowQuiet).Window != 1 {
		t.Fatalf("second settled %+v, want window 1", e)
	}
}

func TestSpaceEventsDoNotSettle(t *testing.T) {
	b := bus.New()
	out := b.Subscribe("t", WindowQuiet{}, DisplayQuiet{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newSettler(b, 150*time.Millisecond, time.Second).run(ctx)
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 1})
	b.Publish(event.SpaceSwitched{Stamp: event.Now()}) // a switch neither opens a run nor drops window 1's
	if e := expectEvent(t, out, WindowQuiet{}, 400*time.Millisecond); e.(WindowQuiet).Window != 1 {
		t.Fatalf("settled %+v, want window 1", e)
	}
	expectNone(t, out, 300*time.Millisecond)
}

func TestDisplayRunEvents(t *testing.T) {
	b := bus.New()
	out := b.Subscribe("t", DisplayBurstBegan{}, DisplayQuiet{}, WindowQuiet{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newSettler(b, 20*time.Millisecond, 60*time.Millisecond).run(ctx)
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 3})
	b.Publish(event.DisplayReconfigured{Stamp: event.Now()})
	expectEvent(t, out, DisplayBurstBegan{}, 100*time.Millisecond)
	b.Publish(event.DisplayReconfigured{Stamp: event.Now()}) // extends; no second began
	expectEvent(t, out, DisplayQuiet{}, 300*time.Millisecond)
	expectNone(t, out, 50*time.Millisecond)
}

func TestWindowEventsInsideDisplayRunAreDropped(t *testing.T) {
	b := bus.New()
	out := b.Subscribe("t", DisplayBurstBegan{}, DisplayQuiet{}, WindowQuiet{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newSettler(b, 20*time.Millisecond, 100*time.Millisecond).run(ctx)
	b.Publish(event.DisplayReconfigured{Stamp: event.Now()})
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 3})
	expectEvent(t, out, DisplayBurstBegan{}, 100*time.Millisecond)
	expectEvent(t, out, DisplayQuiet{}, 300*time.Millisecond)
	expectNone(t, out, 100*time.Millisecond)
}

// The window debounce runs on the screen's WindowChanged: a raw window
// event alone is not yet in Current, so it settles nothing.
func TestRawWindowEventSettlesNothing(t *testing.T) {
	b := bus.New()
	out := b.Subscribe("t", WindowQuiet{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newSettler(b, 20*time.Millisecond, time.Second).run(ctx)
	b.Publish(event.WindowMoved{Stamp: event.Now(), Window: 1})
	expectNone(t, out, 150*time.Millisecond)
}
