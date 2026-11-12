package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/layout"
)

type fakeOrders struct {
	calls    chan string
	moveErr  error
	frameErr error

	mu         sync.Mutex
	switchedOn []string      // the display of each switchSpace, in order
	frames     []layout.Rect // the frame of each setFrame, in order
}

func (f *fakeOrders) move(wids []uint32, space uint64) error { f.calls <- "move"; return f.moveErr }
func (f *fakeOrders) setFrame(pid int, wid uint32, fr layout.Rect) error {
	f.mu.Lock()
	f.frames = append(f.frames, fr)
	f.mu.Unlock()
	f.calls <- "frame"
	return f.frameErr
}
func (f *fakeOrders) fullscreen(pid int, wid uint32) error { f.calls <- "fullscreen"; return nil }
func (f *fakeOrders) switchSpace(display string, right bool) error {
	f.mu.Lock()
	f.switchedOn = append(f.switchedOn, display)
	f.mu.Unlock()
	if right {
		f.calls <- "right"
	} else {
		f.calls <- "left"
	}
	return nil
}

func (f *fakeOrders) switchDisplays() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.switchedOn...)
}

// lastFrame is the frame of the latest setFrame, if any.
func (f *fakeOrders) lastFrame() (layout.Rect, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) == 0 {
		return layout.Rect{}, false
	}
	return f.frames[len(f.frames)-1], true
}

// hiddenPut: window 2 on hidden space 200 (display D1 shows 100) wants
// frame 10,10 200x200 on its home space s2: stage to 100, resize, home to 200.
func hiddenPut() PutWanted {
	p := twoWindows()
	p.windows[1].Frame = layout.Rect{X: 10, Y: 10, W: 100, H: 100}
	d := arrangement.Decision{Kind: arrangement.Put, Window: 2,
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

func noOrder(t *testing.T, o *fakeOrders, within time.Duration) {
	t.Helper()
	select {
	case got := <-o.calls:
		t.Fatalf("order %q, want none", got)
	case <-time.After(within):
	}
}

// fsPicture is twoWindows on D1, whose spaces are s1 (100), s2 (200) and
// then the fullscreen spaces fs in order; D1 shows shown, its visible frame
// is 0,30 2560x1410, and each of fsWin is fullscreen on D1.
func fsPicture(shown uint64, fs []uint64, fsWin ...uint32) *snapshot {
	p := twoWindows()
	p.order = map[string][]uint64{"D1": append([]uint64{100, 200}, fs...)}
	for _, id := range fs {
		p.fsSpace[id] = "D1"
	}
	p.current = map[uint64]bool{shown: true}
	p.displays[0].Visible = layout.Rect{X: 0, Y: 30, W: 2560, H: 1410}
	p.currentOf = map[string]uint64{"D1": shown}
	for _, w := range fsWin {
		delete(p.winSpace, w)
		p.fsWindow[w] = "D1"
	}
	return p
}

// fullscreenPut asks for each of wids, in order, to be fullscreen on D1,
// with Current p.
func fullscreenPut(p *snapshot, wids ...uint32) PutWanted {
	var ds []arrangement.Decision
	for _, wid := range wids {
		w, _ := p.window(wid)
		ds = append(ds, arrangement.Decision{Kind: arrangement.Put, Window: int64(wid),
			Seen: arrangement.Seen{Binding: wid, PID: w.OwnerPID, App: w.OwnerName, Title: w.Title,
				Placement: arrangement.Placement{Space: p.idToKey[p.winSpace[wid]], Frame: w.Frame}},
			Want: arrangement.FullscreenOn("D1"), Move: true})
	}
	return PutWanted{Stamp: event.Now(), Arr: 1, Current: p, Spaces: p.spaces, Decisions: ds}
}

// shows publishes Current p and D1 showing its shown space, as the screen
// does after a space event.
func shows(b *bus.Bus, p *snapshot) {
	b.Publish(current(p))
	b.Publish(SpaceShown{Stamp: event.Now(), Display: "D1", Space: p.currentOf["D1"]})
}

// attaches publishes Current p and window wid attached to D1's fullscreen
// space, as the screen does after the attach event.
func attaches(b *bus.Bus, p *snapshot, wid uint32) {
	b.Publish(current(p))
	b.Publish(WindowChanged{Stamp: event.Now(), Window: wid, Fullscreen: "D1"})
}

func stepNames(rep PutEnded) string {
	var names []string
	for _, s := range rep.Steps {
		names = append(names, s.Name)
	}
	return strings.Join(names, " ")
}

// Window 2, on hidden s2, is staged to s1, which D1 shows; once there it is
// given D1's visible frame; AXFullScreen is set only once that frame is
// reported; its attach to fullscreen space 300 and D1's switch away confirm
// that; then D1 is stepped back 300 -> 200 -> 100, each step confirmed.
func TestPutFullscreenStagesFramesSetsFullscreenAndReturnsTheDisplay(t *testing.T) {
	b, o, out, cancel := startPutter(t, time.Second)
	defer cancel()
	vis := layout.Rect{X: 0, Y: 30, W: 2560, H: 1410}
	b.Publish(fullscreenPut(fsPicture(100, nil), 2))
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move")                 // stage to the space D1 shows
	noOrder(t, o, 100*time.Millisecond) // the frame waits for the window to be there
	b.Publish(changed(2, 100, layout.Rect{X: 10, Y: 10, W: 200, H: 200}))
	order(t, o, "frame")
	if got, ok := o.lastFrame(); !ok || got != vis {
		t.Fatalf("frame ordered %v, %v; want D1's visible frame %v", got, ok, vis)
	}
	noOrder(t, o, 100*time.Millisecond) // AXFullScreen waits for the frame
	b.Publish(changed(2, 100, vis))
	order(t, o, "fullscreen")
	shows(b, fsPicture(300, []uint64{300}, 2))
	attaches(b, fsPicture(300, []uint64{300}, 2), 2)
	order(t, o, "left")
	shows(b, fsPicture(200, []uint64{300}, 2))
	order(t, o, "left")
	shows(b, fsPicture(100, []uint64{300}, 2))
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if got := stepNames(rep); got != "stage frame fullscreen return return" || rep.Cancelled {
		t.Fatalf("steps %q, cancelled %v; want stage frame fullscreen return return", got, rep.Cancelled)
	}
	for _, s := range rep.Steps {
		if s.Confirmed == 0 || len(s.Unconfirmed)+len(s.Unswitched) > 0 {
			t.Fatalf("step %+v, want it confirmed", s)
		}
	}
	if got, want := o.switchDisplays(), []string{"D1", "D1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("switches ordered on %q, want %q", got, want)
	}
	noOrder(t, o, 50*time.Millisecond)
}

// A staged window that already has D1's visible frame is not framed: the
// frame step orders nothing and AXFullScreen follows the stage's confirm.
func TestPutFullscreenSkipsTheFrameWhenTheWindowAlreadyHasIt(t *testing.T) {
	b, o, out, cancel := startPutter(t, time.Second)
	defer cancel()
	vis := layout.Rect{X: 0, Y: 30, W: 2560, H: 1410}
	p := fsPicture(100, nil)
	for i := range p.windows {
		if p.windows[i].ID == 2 {
			p.windows[i].Frame = vis
		}
	}
	b.Publish(fullscreenPut(p, 2))
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move")
	b.Publish(changed(2, 100, vis))
	order(t, o, "fullscreen")
	shows(b, fsPicture(300, []uint64{300}, 2))
	attaches(b, fsPicture(300, []uint64{300}, 2), 2)
	order(t, o, "left")
	shows(b, fsPicture(200, []uint64{300}, 2))
	order(t, o, "left")
	shows(b, fsPicture(100, []uint64{300}, 2))
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if got := stepNames(rep); got != "stage frame fullscreen return return" || rep.Cancelled {
		t.Fatalf("steps %q, cancelled %v; want stage frame fullscreen return return", got, rep.Cancelled)
	}
	if f := rep.Steps[1]; f.Confirmed != 0 || len(f.Unconfirmed)+len(f.Unswitched) > 0 {
		t.Fatalf("frame step %+v, want nothing ordered and nothing unconfirmed", f)
	}
	noOrder(t, o, 50*time.Millisecond)
}

// A frame within the tolerance confirms the frame step; one 10 points off
// does not, and the put ends with the step unconfirmed and no AXFullScreen.
func TestPutFullscreenFrameConfirmsWithinTolerance(t *testing.T) {
	for _, c := range []struct {
		name    string
		got     layout.Rect
		confirm bool
	}{
		{"3 points off", layout.Rect{X: 0, Y: 27, W: 2560, H: 1413}, true},
		{"10 points off", layout.Rect{X: 0, Y: 30, W: 2560, H: 1400}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, o, out, cancel := startPutter(t, 300*time.Millisecond)
			defer cancel()
			b.Publish(fullscreenPut(fsPicture(100, nil), 2))
			expectEvent(t, out, PutStarted{}, time.Second)
			order(t, o, "move")
			b.Publish(changed(2, 100, layout.Rect{X: 10, Y: 10, W: 200, H: 200}))
			order(t, o, "frame")
			b.Publish(changed(2, 100, c.got))
			if c.confirm {
				order(t, o, "fullscreen")
				return
			}
			noOrder(t, o, 400*time.Millisecond)
			rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
			last := rep.Steps[len(rep.Steps)-1]
			if stepNames(rep) != "stage frame" || last.Confirmed != 0 || !reflect.DeepEqual(last.Unconfirmed, []uint32{2}) {
				t.Fatalf("report %+v, want stage then an unconfirmed frame step for window 2", rep)
			}
		})
	}
}

// A frame order that fails leaves the window unplaced: the put ends after
// the frame step with no AXFullScreen.
func TestPutFullscreenFrameOrderErrorEndsThePut(t *testing.T) {
	b, o, out, cancel := startPutter(t, 300*time.Millisecond)
	defer cancel()
	o.frameErr = errors.New("ax refused")
	b.Publish(fullscreenPut(fsPicture(100, nil), 2))
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move")
	b.Publish(changed(2, 100, layout.Rect{X: 10, Y: 10, W: 200, H: 200}))
	order(t, o, "frame")
	noOrder(t, o, 100*time.Millisecond)
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	last := rep.Steps[len(rep.Steps)-1]
	if stepNames(rep) != "stage frame" || !reflect.DeepEqual(last.Unconfirmed, []uint32{2}) {
		t.Fatalf("report %+v, want stage then a frame step unplaced for window 2", rep)
	}
}

// A stage the window server never reports ends the put after the stage
// step: no frame and no AXFullScreen are ordered.
func TestPutFullscreenUnconfirmedStageEndsThePut(t *testing.T) {
	b, o, out, cancel := startPutter(t, 100*time.Millisecond)
	defer cancel()
	b.Publish(fullscreenPut(fsPicture(100, nil), 2))
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "move")
	noOrder(t, o, 300*time.Millisecond)
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if stepNames(rep) != "stage" || !reflect.DeepEqual(rep.Steps[0].Unconfirmed, []uint32{2}) {
		t.Fatalf("report %+v, want one unconfirmed stage step for window 2", rep)
	}
}

// A target display Current holds no visible frame for fails the staged
// window without an order.
func TestPutFullscreenFailsWithoutAVisibleFrame(t *testing.T) {
	b, o, out, cancel := startPutter(t, 300*time.Millisecond)
	defer cancel()
	p := fsPicture(100, nil)
	p.displays[0].Visible = layout.Rect{}
	b.Publish(fullscreenPut(p, 2))
	expectEvent(t, out, PutStarted{}, time.Second)
	noOrder(t, o, 100*time.Millisecond)
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if stepNames(rep) != "frame" || !reflect.DeepEqual(rep.Steps[0].Unconfirmed, []uint32{2}) {
		t.Fatalf("report %+v, want one frame step with window 2 unplaced and no order", rep)
	}
}

// Only the window's attach to a fullscreen space on the target display
// confirms AXFullScreen: a change on the user space or fullscreen on another
// display does not. Unconfirmed, with D1 still on 100, nothing is returned.
func TestPutFullscreenIsConfirmedOnlyByTheAttachOnItsDisplay(t *testing.T) {
	b, o, out, cancel := startPutter(t, 200*time.Millisecond)
	defer cancel()
	p := fsPicture(100, nil)
	p.winSpace[2] = 100 // already on the space D1 shows: no stage
	b.Publish(fullscreenPut(p, 2))
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "fullscreen")
	b.Publish(changed(2, 100, layout.Rect{X: 10, Y: 10, W: 200, H: 200}))
	b.Publish(WindowChanged{Stamp: event.Now(), Window: 2, Fullscreen: "D2"})
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if len(rep.Steps) != 1 || rep.Steps[0].Name != "fullscreen" || rep.Steps[0].Confirmed != 0 || len(rep.Steps[0].Unconfirmed) != 1 {
		t.Fatalf("report %+v, want one unconfirmed fullscreen step", rep)
	}
	noOrder(t, o, 50*time.Millisecond)
}

// The window may attach before the display's switch is reported: the return
// waits for the switch.
func TestPutFullscreenWaitsForTheDisplayToLeaveItsSpace(t *testing.T) {
	b, o, out, cancel := startPutter(t, time.Second)
	defer cancel()
	p := fsPicture(100, nil)
	p.winSpace[2] = 100
	b.Publish(fullscreenPut(p, 2))
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "fullscreen")
	attaches(b, fsPicture(100, []uint64{300}, 2), 2) // D1 not switched yet
	noOrder(t, o, 100*time.Millisecond)
	shows(b, fsPicture(300, []uint64{300}, 2))
	order(t, o, "left")
	shows(b, fsPicture(200, []uint64{300}, 2))
	order(t, o, "left")
	shows(b, fsPicture(100, []uint64{300}, 2))
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if got := stepNames(rep); got != "fullscreen return return" {
		t.Fatalf("steps %q, want fullscreen return return", got)
	}
}

// A switch that lands elsewhere (the key acted on another display, or the
// user switched) leaves the return unconfirmed, and no further key is posted.
func TestPutReturnStopsWhenTheSwitchLandsElsewhere(t *testing.T) {
	b, o, out, cancel := startPutter(t, 300*time.Millisecond)
	defer cancel()
	p := fsPicture(100, nil)
	p.winSpace[2] = 100
	b.Publish(fullscreenPut(p, 2))
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "fullscreen")
	shows(b, fsPicture(300, []uint64{300}, 2))
	attaches(b, fsPicture(300, []uint64{300}, 2), 2)
	order(t, o, "left")
	b.Publish(SpaceShown{Stamp: event.Now(), Display: "D2", Space: 900})
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	last := rep.Steps[len(rep.Steps)-1]
	if stepNames(rep) != "fullscreen return" || last.Confirmed != 0 || len(last.Unswitched) != 1 || last.Unswitched[0] != "D1" {
		t.Fatalf("report %+v, want the return unconfirmed for D1", rep)
	}
	noOrder(t, o, 50*time.Millisecond)
}

// Two windows wanting fullscreen on D1 are put one after the other: the
// second is staged, framed and made fullscreen only once D1 is back on 100.
func TestPutTwoFullscreenWindowsOnOneDisplayOneAfterTheOther(t *testing.T) {
	b, o, out, cancel := startPutter(t, time.Second)
	defer cancel()
	vis := layout.Rect{X: 0, Y: 30, W: 2560, H: 1410}
	b.Publish(fullscreenPut(fsPicture(100, nil), 1, 2)) // 1 on s1, shown; 2 on s2
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "fullscreen") // window 1: already on the shown space
	shows(b, fsPicture(300, []uint64{300}, 1))
	attaches(b, fsPicture(300, []uint64{300}, 1), 1)
	for _, at := range []uint64{200, 100} {
		order(t, o, "left")
		shows(b, fsPicture(at, []uint64{300}, 1))
	}
	order(t, o, "move") // window 2 is staged only now
	b.Publish(changed(2, 100, layout.Rect{X: 10, Y: 10, W: 200, H: 200}))
	order(t, o, "frame")
	b.Publish(changed(2, 100, vis))
	order(t, o, "fullscreen")
	shows(b, fsPicture(400, []uint64{300, 400}, 1, 2))
	attaches(b, fsPicture(400, []uint64{300, 400}, 1, 2), 2)
	for _, at := range []uint64{300, 200, 100} {
		order(t, o, "left")
		shows(b, fsPicture(at, []uint64{300, 400}, 1, 2))
	}
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if got := stepNames(rep); got != "fullscreen return return stage frame fullscreen return return return" {
		t.Fatalf("steps %q", got)
	}
}

// The first window's return is left unconfirmed, so D1 stays on its
// fullscreen space: the second window, planned from that Current, has no
// space to be staged to and fails without an order.
func TestPutSecondFullscreenWindowFailsWhenTheDisplayWasNotReturned(t *testing.T) {
	b, o, out, cancel := startPutter(t, 200*time.Millisecond)
	defer cancel()
	b.Publish(fullscreenPut(fsPicture(100, nil), 1, 2))
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "fullscreen")
	shows(b, fsPicture(300, []uint64{300}, 1))
	attaches(b, fsPicture(300, []uint64{300}, 1), 1)
	order(t, o, "left") // never confirmed: D1 stays on 300
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	last := rep.Steps[len(rep.Steps)-1]
	if got := stepNames(rep); got != "fullscreen return fullscreen" || len(last.Unconfirmed) != 1 || last.Unconfirmed[0] != 2 {
		t.Fatalf("report %+v (steps %q), want window 2's fullscreen failed after the unconfirmed return", rep, got)
	}
	noOrder(t, o, 50*time.Millisecond)
}

// A return toward a space Current's order no longer lists is a failed
// return: D1 is reported unswitched and no key is posted.
func TestPutReturnTowardASpaceMissingFromTheOrderFails(t *testing.T) {
	b, o, out, cancel := startPutter(t, time.Second)
	defer cancel()
	p := fsPicture(100, nil)
	p.winSpace[2] = 100
	b.Publish(fullscreenPut(p, 2))
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "fullscreen")
	after := fsPicture(300, []uint64{300}, 2)
	after.order["D1"] = []uint64{200, 300} // 100 gone
	shows(b, after)
	attaches(b, after, 2)
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	last := rep.Steps[len(rep.Steps)-1]
	if got := stepNames(rep); got != "fullscreen return" || len(last.Unswitched) != 1 || last.Unswitched[0] != "D1" {
		t.Fatalf("report %+v (steps %q), want the return unswitched for D1", rep, got)
	}
	if !strings.Contains(rep.String(), "1 unconfirmed") {
		t.Fatalf("report line %q, want the return counted unconfirmed", rep.String())
	}
	noOrder(t, o, 50*time.Millisecond)
}

// With neither the attach nor the switch confirmed, D1 still shows the space
// it showed when the put began: no key is posted and the window is reported
// unconfirmed.
func TestPutNoReturnWhenTheDisplayDidNotMove(t *testing.T) {
	b, o, out, cancel := startPutter(t, 100*time.Millisecond)
	defer cancel()
	p := fsPicture(100, nil)
	p.winSpace[2] = 100
	b.Publish(fullscreenPut(p, 2))
	expectEvent(t, out, PutStarted{}, time.Second)
	order(t, o, "fullscreen")
	rep := expectEvent(t, out, PutEnded{}, time.Second).(PutEnded)
	if got := stepNames(rep); got != "fullscreen" || len(rep.Steps[0].Unconfirmed) != 1 || rep.Steps[0].Unconfirmed[0] != 2 {
		t.Fatalf("report %+v (steps %q), want window 2 unconfirmed and no return", rep, got)
	}
	noOrder(t, o, 50*time.Millisecond)
}
