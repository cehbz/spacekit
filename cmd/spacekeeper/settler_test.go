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
	go newSettler(b, 150*time.Millisecond, time.Second, time.Second).run(ctx)
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 1})
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 2})
	time.Sleep(30 * time.Millisecond)
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 1}) // keeps 1 busy
	if e := expectEvent(t, out, WindowQuiet{}, 400*time.Millisecond); e.(WindowQuiet).Window != 2 {
		t.Fatalf("first quiet %+v, want window 2", e)
	}
	if e := expectEvent(t, out, WindowQuiet{}, 400*time.Millisecond); e.(WindowQuiet).Window != 1 {
		t.Fatalf("second quiet %+v, want window 1", e)
	}
}

func TestWindowEventsInsideSpaceRunAreDropped(t *testing.T) {
	b := bus.New()
	out := b.Subscribe("t", WindowQuiet{}, SpaceQuiet{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newSettler(b, 20*time.Millisecond, 100*time.Millisecond, time.Second).run(ctx)
	b.Publish(event.SpaceSwitched{Stamp: event.Now()})
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 1})
	expectEvent(t, out, SpaceQuiet{}, 300*time.Millisecond)
	expectNone(t, out, 100*time.Millisecond)
}

func TestSpaceRunClosesOpenWindowRuns(t *testing.T) {
	b := bus.New()
	out := b.Subscribe("t", WindowQuiet{}, SpaceQuiet{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newSettler(b, 50*time.Millisecond, 100*time.Millisecond, time.Second).run(ctx)
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 1})
	b.Publish(event.SpaceSwitched{Stamp: event.Now()})
	expectEvent(t, out, SpaceQuiet{}, 300*time.Millisecond)
	expectNone(t, out, 100*time.Millisecond)
}

func TestDisplayRunEvents(t *testing.T) {
	b := bus.New()
	out := b.Subscribe("t", DisplayBurstBegan{}, DisplayQuiet{}, WindowQuiet{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newSettler(b, 20*time.Millisecond, time.Second, 60*time.Millisecond).run(ctx)
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
	go newSettler(b, 20*time.Millisecond, time.Second, 100*time.Millisecond).run(ctx)
	b.Publish(event.DisplayReconfigured{Stamp: event.Now()})
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 3})
	expectEvent(t, out, DisplayBurstBegan{}, 100*time.Millisecond)
	expectEvent(t, out, DisplayQuiet{}, 300*time.Millisecond)
	expectNone(t, out, 100*time.Millisecond)
}

// The window debounce runs on the screen's WindowChanged: a raw window
// event alone is not yet in Current, so it settles nothing.
func TestRawWindowEventMakesNoQuiet(t *testing.T) {
	b := bus.New()
	out := b.Subscribe("t", WindowQuiet{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newSettler(b, 20*time.Millisecond, time.Second, time.Second).run(ctx)
	b.Publish(event.WindowMoved{Stamp: event.Now(), Window: 1})
	expectNone(t, out, 150*time.Millisecond)
}

func TestSpaceRunEvents(t *testing.T) {
	b := bus.New()
	out := b.Subscribe("t", SpaceBurstBegan{}, SpaceQuiet{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newSettler(b, 20*time.Millisecond, 60*time.Millisecond, time.Second).run(ctx)
	for range 2 {
		b.Publish(event.SpaceSwitched{Stamp: event.Now()})
		b.Publish(event.SpaceCreated{Stamp: event.Now()}) // extends; no second began
		expectEvent(t, out, SpaceBurstBegan{}, 100*time.Millisecond)
		expectEvent(t, out, SpaceQuiet{}, 300*time.Millisecond)
		expectNone(t, out, 50*time.Millisecond)
	}
}
