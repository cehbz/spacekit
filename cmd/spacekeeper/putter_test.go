package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/layout"
)

type fakeOrders struct {
	calls   chan string
	moveErr error
}

func (f *fakeOrders) move(wids []uint32, space uint64) error { f.calls <- "move"; return f.moveErr }
func (f *fakeOrders) setFrame(pid int, wid uint32, fr layout.Rect) error {
	f.calls <- "frame"
	return nil
}

// hiddenPut: window 2 on hidden space 200 (display D1 shows 100) wants
// frame 10,10 200x200 on its home space s2: stage to 100, resize, home to 200.
func hiddenPut() PutWanted {
	p := twoWindows()
	p.windows[1].Frame = layout.Rect{X: 10, Y: 10, W: 100, H: 100}
	d := arrangement.Decision{Kind: arrangement.Repair, Window: 2,
		Seen: arrangement.Seen{Binding: 2, PID: 11, App: "B", Placement: arrangement.Placement{Space: "s2", Frame: p.windows[1].Frame}},
		Want: arrangement.Placement{Space: "s2", Frame: layout.Rect{X: 10, Y: 10, W: 200, H: 200}}, Resize: true}
	return PutWanted{Stamp: event.Now(), Arr: 1, Current: p, Spaces: p.spaces, Decisions: []arrangement.Decision{d}}
}

func startPutter(t *testing.T, deadline time.Duration) (*bus.Bus, *fakeOrders, *bus.Subscription, context.CancelFunc) {
	b := bus.New()
	o := &fakeOrders{calls: make(chan string, 10)}
	p := newPutter(b, o, func(fn func()) { fn() })
	p.deadline = deadline
	out := b.Subscribe("t", PutStarted{}, PutEnded{})
	ctx, cancel := context.WithCancel(context.Background())
	go p.run(ctx)
	return b, o, out, cancel
}

func order(t *testing.T, o *fakeOrders, want string) {
	t.Helper()
	select {
	case got := <-o.calls:
		if got != want {
			t.Fatalf("order %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("no %q order", want)
	}
}

func changed(w uint32, space uint64, f layout.Rect) WindowChanged {
	return WindowChanged{Stamp: event.Now(), Window: w, Space: space, Frame: f}
}

func TestPutStepsConfirmFromChanges(t *testing.T) {
	b, o, out, cancel := startPutter(t, time.Second)
	defer cancel()
	b.Publish(hiddenPut())
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move") // stage
	b.Publish(changed(2, 100, layout.Rect{X: 10, Y: 10, W: 100, H: 100}))
	order(t, o, "frame")
	b.Publish(changed(2, 100, layout.Rect{X: 11, Y: 10, W: 200, H: 201})) // within the band
	order(t, o, "move")                                                   // home
	b.Publish(changed(2, 200, layout.Rect{X: 11, Y: 10, W: 200, H: 201}))
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if rep.Cancelled || len(rep.Steps) != 3 {
		t.Fatalf("report %+v", rep)
	}
	for _, s := range rep.Steps {
		if s.Confirmed != 1 || len(s.Unconfirmed) != 0 || s.Took > 500*time.Millisecond {
			t.Fatalf("step %+v", s)
		}
	}
}

func TestPutStepDeadline(t *testing.T) {
	b, o, out, cancel := startPutter(t, 30*time.Millisecond)
	defer cancel()
	b.Publish(hiddenPut())
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move")
	order(t, o, "frame")
	order(t, o, "move")
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	for _, s := range rep.Steps {
		if s.Confirmed != 0 || len(s.Unconfirmed) != 1 || s.Unconfirmed[0] != 2 || s.Took < 30*time.Millisecond {
			t.Fatalf("step %+v", s)
		}
	}
}

func TestPutFrameOutsideBandDoesNotConfirm(t *testing.T) {
	b, o, out, cancel := startPutter(t, 50*time.Millisecond)
	defer cancel()
	b.Publish(hiddenPut())
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move")
	b.Publish(changed(2, 100, layout.Rect{}))
	order(t, o, "frame")
	b.Publish(changed(2, 100, layout.Rect{X: 10, Y: 10, W: 190, H: 200})) // 10 off
	order(t, o, "move")
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if s := rep.Steps[1]; s.Confirmed != 0 || len(s.Unconfirmed) != 1 {
		t.Fatalf("resize step %+v, want unconfirmed", s)
	}
}

func TestPutCancelledByDisplayRun(t *testing.T) {
	b, o, out, cancel := startPutter(t, time.Second)
	defer cancel()
	b.Publish(hiddenPut())
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move")
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if !rep.Cancelled || len(rep.Steps) != 1 {
		t.Fatalf("report %+v, want cancelled after one step", rep)
	}
	select {
	case got := <-o.calls:
		t.Fatalf("order %q after cancel", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPutCancelledByShutdown(t *testing.T) {
	b, o, out, cancel := startPutter(t, time.Second)
	b.Publish(hiddenPut())
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move")
	cancel()
	if rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded); !rep.Cancelled {
		t.Fatalf("report %+v, want cancelled", rep)
	}
}

func TestPutMoveOnlyWindowGoesHomeInOneStep(t *testing.T) {
	b, o, out, cancel := startPutter(t, time.Second)
	defer cancel()
	req := hiddenPut()
	req.Decisions[0].Move, req.Decisions[0].Resize = true, false
	req.Decisions[0].Seen.Space = "s1"
	req.Current.winSpace[2] = 100
	b.Publish(req)
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move")
	b.Publish(changed(2, 200, layout.Rect{}))
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if len(rep.Steps) != 1 || rep.Steps[0].Name != "home" || rep.Steps[0].Confirmed != 1 {
		t.Fatalf("report %+v", rep)
	}
}

func TestPutQueuedDisplayRunCancelsBeforeNextOrder(t *testing.T) {
	b, o, out, cancel := startPutter(t, time.Second)
	defer cancel()
	b.Publish(hiddenPut())
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move")
	b.Publish(changed(2, 100, layout.Rect{X: 10, Y: 10, W: 100, H: 100}))
	b.Publish(DisplayBurstBegan{Stamp: event.Now()})
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if !rep.Cancelled || len(rep.Steps) != 1 {
		t.Fatalf("report %+v, want cancelled after one step", rep)
	}
	select {
	case got := <-o.calls:
		t.Fatalf("order %q after cancel", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPutDisplayRunCancelsAfterFailedOrders(t *testing.T) {
	b := bus.New()
	o := &fakeOrders{calls: make(chan string, 10), moveErr: errors.New("refused")}
	p := newPutter(b, o, func(fn func()) {
		b.Publish(DisplayBurstBegan{Stamp: event.Now()}) // queued before the step ends
		fn()
	})
	out := b.Subscribe("t", PutStarted{}, PutEnded{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.run(ctx)
	b.Publish(hiddenPut())
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move")
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if !rep.Cancelled || len(rep.Steps) != 1 {
		t.Fatalf("report %+v, want cancelled after one step", rep)
	}
	select {
	case got := <-o.calls:
		t.Fatalf("order %q after cancel", got)
	case <-time.After(50 * time.Millisecond):
	}
}
