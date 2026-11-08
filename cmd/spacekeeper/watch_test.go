package main

import (
	"bytes"
	"errors"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/settle"
	"github.com/cehbz/spacekit/internal/skylight"
	"github.com/cehbz/spacekit/internal/sysevents"
)

// A look put off again and again while the overview is showing is logged
// once, when it is first put off; the retried look logs itself when it runs.
func TestPutOffLogsOnce(t *testing.T) {
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(out); log.SetFlags(flags) }()

	w := newTestWatcher()
	for i := 0; i < 3; i++ {
		w.putOff("interval", "the overview is showing")
	}
	if w.retry != "interval" || !w.spaces.Open() {
		t.Fatalf("retry %q, spaces open %v; want the look queued", w.retry, w.spaces.Open())
	}
	if n := strings.Count(buf.String(), "put off"); n != 1 {
		t.Fatalf("logged %d put-off lines, want 1:\n%s", n, buf.String())
	}
}

// newTestWatcher is a watcher with settle windows and no store, session or
// window server: place and send are stubs the test controls.
func newTestWatcher() *watcher {
	w := &watcher{
		displays: settle.New(displayQuiet),
		spaces:   settle.New(spaceQuiet),
		windows:  settle.New(windowQuiet),
	}
	w.onMain = func(fn func()) { fn() }
	w.place = func(uint32) bool { return true }
	w.send = func([]uint32) error { return nil }
	return w
}

func TestHandleWindowEventOpensWindowSettle(t *testing.T) {
	w := newTestWatcher()
	at := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	w.handle(sysevents.Event{Kind: sysevents.WindowMoved, At: at, Window: 42})
	if !w.windows.Open() {
		t.Fatal("a window move should open the window settle window")
	}
	if got, want := w.windows.Deadline(), at.Add(windowQuiet); !got.Equal(want) {
		t.Fatalf("Deadline = %v, want %v", got, want)
	}
	if w.lastWindow != sysevents.WindowMoved {
		t.Fatalf("lastWindow = %v, want window moved", w.lastWindow)
	}
}

func TestHandleWindowEventAbsorbedDuringBurst(t *testing.T) {
	at := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	for name, open := range map[string]func(*watcher){
		"space change":  func(w *watcher) { w.spaces.Note(at) },
		"display burst": func(w *watcher) { w.displays.Note(at) },
	} {
		w := newTestWatcher()
		open(w)
		w.handle(sysevents.Event{Kind: sysevents.WindowShown, At: at.Add(time.Second), Window: 42})
		if w.windows.Open() {
			t.Fatalf("%s: a window event during the burst should add nothing", name)
		}
	}
}

func TestHandleAttachedRequestsOnlyPlaceable(t *testing.T) {
	at := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	for _, placeable := range []bool{true, false} {
		w := newTestWatcher()
		w.watched.Bound([]uint32{3})
		w.place = func(uint32) bool { return placeable }
		var sent [][]uint32
		w.send = func(ids []uint32) error { sent = append(sent, ids); return nil }
		w.handle(sysevents.Event{Kind: sysevents.WindowAttached, At: at, Window: 9, Space: 5})
		if !w.windows.Open() {
			t.Fatalf("placeable=%v: an attach should open the window settle window", placeable)
		}
		if placeable {
			if len(sent) != 1 || !reflect.DeepEqual(sent[0], []uint32{3, 9}) {
				t.Fatalf("placeable: sent %v, want one request for [3 9]", sent)
			}
		} else if len(sent) != 0 {
			t.Fatalf("not placeable: sent %v, want nothing", sent)
		}
	}
}

func TestHandleDestroyedOpensNothing(t *testing.T) {
	at := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	w := newTestWatcher()
	w.watched.Bound([]uint32{3, 9})
	var sent [][]uint32
	w.send = func(ids []uint32) error { sent = append(sent, ids); return nil }
	w.handle(sysevents.Event{Kind: sysevents.WindowDestroyed, At: at, Window: 9})
	if w.windows.Open() {
		t.Fatal("a destroy should not open the window settle window")
	}
	if len(sent) != 1 || !reflect.DeepEqual(sent[0], []uint32{3}) {
		t.Fatalf("sent %v, want one request for [3]", sent)
	}
	w.handle(sysevents.Event{Kind: sysevents.WindowDestroyed, At: at, Window: 77})
	if len(sent) != 1 {
		t.Fatalf("destroying an unknown window re-sent: %v", sent)
	}
}

func TestHandleSpaceEventsJoinSpaceSettle(t *testing.T) {
	at := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	for _, k := range []sysevents.Kind{sysevents.SpaceCreated, sysevents.SpaceDestroyed, sysevents.SpaceSwitched} {
		w := newTestWatcher()
		w.handle(sysevents.Event{Kind: k, At: at, Space: 5})
		if !w.spaces.Open() || w.windows.Open() {
			t.Fatalf("%v: spaces open %v, windows open %v; want the space settle window only", k, w.spaces.Open(), w.windows.Open())
		}
	}
}

func TestPlaceableWindow(t *testing.T) {
	ok := skylight.WindowInfo{Layer: 0, Alpha: 1, Bounds: skylight.WindowBounds{Width: 50, Height: 50}}
	if !placeableWindow(ok) {
		t.Fatal("a visible 50x50 layer-0 window is placeable")
	}
	for name, w := range map[string]skylight.WindowInfo{
		"layer":  {Layer: 20, Alpha: 1, Bounds: skylight.WindowBounds{Width: 500, Height: 500}},
		"alpha":  {Layer: 0, Alpha: 0, Bounds: skylight.WindowBounds{Width: 500, Height: 500}},
		"narrow": {Layer: 0, Alpha: 1, Bounds: skylight.WindowBounds{Width: 49, Height: 500}},
		"short":  {Layer: 0, Alpha: 1, Bounds: skylight.WindowBounds{Width: 500, Height: 49}},
	} {
		if placeableWindow(w) {
			t.Errorf("%s: should not be placeable", name)
		}
	}
}

func TestHandleSpaceEventClosesWindowSettle(t *testing.T) {
	at := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	w := newTestWatcher()
	w.handle(sysevents.Event{Kind: sysevents.WindowMoved, At: at, Window: 42})
	w.handle(sysevents.Event{Kind: sysevents.SpaceSwitched, At: at.Add(100 * time.Millisecond)})
	if w.windows.Open() || !w.spaces.Open() {
		t.Fatalf("windows open %v, spaces open %v; want the space settle window only", w.windows.Open(), w.spaces.Open())
	}
}

func TestNoteDisplaysClosesWindowSettle(t *testing.T) {
	at := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	w := newTestWatcher()
	w.windows.Note(at)
	if !w.noteDisplays(at.Add(100 * time.Millisecond)) {
		t.Fatal("the first display event should open a burst")
	}
	if w.windows.Open() {
		t.Fatal("a display event should close the window settle window")
	}
}

// captureLog sends the log to a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(out); log.SetFlags(flags) })
	return &buf
}

func TestPutOffClosesWindowSettle(t *testing.T) {
	captureLog(t)
	w := newTestWatcher()
	w.windows.Note(time.Now())
	w.putOff("interval", "the overview is showing")
	if w.windows.Open() || !w.spaces.Open() {
		t.Fatalf("windows open %v, spaces open %v; want the space settle window only", w.windows.Open(), w.spaces.Open())
	}
}

func TestSendLogsFailureOnce(t *testing.T) {
	buf := captureLog(t)
	at := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	w := newTestWatcher()
	w.watched.Bound([]uint32{1, 2, 3, 4})
	w.send = func([]uint32) error { return errors.New("symbol missing") }
	for wid := uint32(1); wid <= 3; wid++ {
		w.handle(sysevents.Event{Kind: sysevents.WindowDestroyed, At: at, Window: wid})
	}
	if n := strings.Count(buf.String(), "requesting window notifications"); n != 1 {
		t.Fatalf("logged %d send failures, want 1:\n%s", n, buf.String())
	}
}

func TestSendLogsOverflowOnce(t *testing.T) {
	buf := captureLog(t)
	at := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	w := newTestWatcher()
	ids := make([]uint32, watchedCap+5)
	for i := range ids {
		ids[i] = uint32(i + 1)
	}
	w.watched.Bound(ids)
	for wid := uint32(1); wid <= 2; wid++ {
		w.handle(sysevents.Event{Kind: sysevents.WindowDestroyed, At: at, Window: wid})
	}
	if n := strings.Count(buf.String(), "left to the timer"); n != 1 {
		t.Fatalf("logged %d overflow lines, want 1:\n%s", n, buf.String())
	}
}

func TestHandleAttachedAbsorbedDoesNotPlace(t *testing.T) {
	at := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	for name, open := range map[string]func(*watcher){
		"space change":      func(w *watcher) { w.spaces.Note(at) },
		"display burst":     func(w *watcher) { w.displays.Note(at) },
		"login convergence": func(w *watcher) { w.boot = &reconciler{} },
	} {
		w := newTestWatcher()
		open(w)
		var placed, sent int
		w.place = func(uint32) bool { placed++; return true }
		w.send = func([]uint32) error { sent++; return nil }
		w.handle(sysevents.Event{Kind: sysevents.WindowAttached, At: at.Add(time.Second), Window: 9, Space: 5})
		if placed != 0 || sent != 0 || w.windows.Open() {
			t.Errorf("%s: placed %d, sent %d, windows open %v; want the attach absorbed", name, placed, sent, w.windows.Open())
		}
	}
}
