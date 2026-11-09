package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/skylight"
)

// reader is the screen's view of the window server; liveReader is the real
// one and tests script their own.
type reader interface {
	readAll() (*snapshot, error)
	window(wid uint32) (layout.LiveWindow, []uint64, error)
	spaceList() ([]skylight.Display, error)
}

type liveReader struct{}

func (liveReader) readAll() (*snapshot, error)                            { return readAll() }
func (liveReader) window(wid uint32) (layout.LiveWindow, []uint64, error) { return readWindow(wid) }
func (liveReader) spaceList() ([]skylight.Display, error)                 { return skylight.ManagedDisplaySpaces() }

// screen keeps Current from the window server's events and publishes it by
// value after every change. It owns the per-window request set, kept at
// every placeable window it knows. The tick validates Current against a read
// of the screen.
type screen struct {
	b                        *bus.Bus
	sub                      *bus.Subscription
	r                        reader
	onMain                   func(func())
	send                     func([]uint32) error
	pic                      *snapshot
	set                      watched
	pending                  map[uint32]*layout.LiveWindow // last validation's differences: the window as read, nil when gone
	pendingSpace             map[uint32]uint64
	pendingGone              map[uint32]bool // listed windows the last validation's read did not list
	sendFailing, overflowing bool
}

// newScreen subscribes at once; run takes the seed read.
func newScreen(b *bus.Bus, r reader, onMain func(func())) *screen {
	return &screen{b: b, r: r, onMain: onMain, send: skylight.RequestWindowNotifications,
		sub: b.Subscribe("screen", event.WindowAttached{}, event.WindowDetached{}, event.WindowMoved{}, event.WindowResized{},
			event.WindowDestroyed{}, event.SpaceCreated{}, event.SpaceDestroyed{}, event.SpaceSwitched{}, event.DisplayReconfigured{},
			Tick{})}
}

func (s *screen) run(ctx context.Context) {
	defer s.sub.Close()
	s.seed()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-s.sub.C():
			if !ok {
				return
			}
			s.handle(e)
		}
	}
}

func (s *screen) handle(e event.Event) {
	if s.pic == nil { // not started: the seed read failed
		if _, ok := e.(Tick); ok {
			s.seed()
		}
		return
	}
	switch e := e.(type) {
	case Tick:
		s.validate(event.Name(e))
	case event.SpaceCreated, event.SpaceDestroyed, event.SpaceSwitched, event.DisplayReconfigured:
		s.readSpaces()
	case event.WindowAttached:
		s.readOne(e.Window)
	case event.WindowMoved:
		s.readOne(e.Window)
	case event.WindowResized:
		s.readOne(e.Window)
	case event.WindowDetached:
		if s.pic.winSpace[e.Window] == e.Space {
			s.remove(e.Window, true)
		}
	case event.WindowDestroyed:
		s.remove(e.Window, false)
	}
}

// readOne lists one window and places it in Current; one the server no
// longer lists, or that is not placeable, is removed.
func (s *screen) readOne(wid uint32) {
	var w layout.LiveWindow
	var spaces []uint64
	var err error
	s.onMain(func() { w, spaces, err = s.r.window(wid) })
	if err != nil {
		s.remove(wid, !errors.Is(err, skylight.ErrNotListed))
		return
	}
	var user []uint64
	fsDisplay := ""
	for _, id := range spaces {
		if _, ok := s.pic.idToKey[id]; ok {
			user = append(user, id)
		} else if d, ok := s.pic.fsSpace[id]; ok {
			fsDisplay = d
		}
	}
	switch {
	case len(user) == 1:
		delete(s.pic.fsWindow, wid)
		s.pic.winSpace[wid] = user[0]
	case len(user) == 0 && fsDisplay != "":
		delete(s.pic.winSpace, wid)
		s.pic.fsWindow[wid] = fsDisplay
	default:
		s.remove(wid, true)
		return
	}
	replaced := false
	for i := range s.pic.windows {
		if s.pic.windows[i].ID == wid {
			s.pic.windows[i] = w
			replaced = true
		}
	}
	if !replaced {
		s.pic.windows = append(s.pic.windows, w)
		listed := false
		for _, a := range s.pic.all {
			listed = listed || a.ID == wid
		}
		if !listed {
			s.pic.all = append(s.pic.all, layout.ListedWindow{ID: wid, OwnerName: w.OwnerName, Title: w.Title})
		}
		s.request()
	}
	s.publish(WindowChanged{Stamp: event.Now(), Window: wid, Space: s.pic.winSpace[wid], Frame: w.Frame})
}

// remove drops a window from Current. A window the window server still
// lists stays in all, the whole window list; one it no longer lists leaves
// all even if Current never held it.
func (s *screen) remove(wid uint32, listed bool) {
	_, known := s.pic.winSpace[wid]
	_, fs := s.pic.fsWindow[wid]
	inCurrent := known || fs
	if !listed {
		for i := range s.pic.all {
			if s.pic.all[i].ID == wid {
				s.pic.all = append(s.pic.all[:i], s.pic.all[i+1:]...)
				break
			}
		}
	}
	if !inCurrent {
		return
	}
	delete(s.pic.winSpace, wid)
	delete(s.pic.fsWindow, wid)
	for i := range s.pic.windows {
		if s.pic.windows[i].ID == wid {
			s.pic.windows = append(s.pic.windows[:i], s.pic.windows[i+1:]...)
			break
		}
	}
	s.request()
	s.publish(WindowChanged{Stamp: event.Now(), Window: wid, Gone: true})
}

// readSpaces rebuilds the space list in Current and publishes it.
func (s *screen) readSpaces() {
	var displays []skylight.Display
	var err error
	s.onMain(func() { displays, err = s.r.spaceList() })
	if err != nil {
		log.Printf("screen: reading spaces failed: %v", err)
		return
	}
	s.pic.setSpaces(displays)
	s.b.Publish(CurrentChanged{Stamp: event.Now(), Current: s.pic.clone()})
}

// publish emits Current, as a fresh copy, and then the change. Bus delivery
// is FIFO per subscriber, so whatever a consumer publishes in reaction to the
// change (the putter's PutEnded) reaches the model after the Current it
// describes.
func (s *screen) publish(c WindowChanged) {
	s.b.Publish(CurrentChanged{Stamp: event.Now(), Current: s.pic.clone()})
	s.b.Publish(c)
}

// seed takes the one read Current starts from. It is retried on the tick
// while it fails.
func (s *screen) seed() {
	var fresh *snapshot
	var err error
	s.onMain(func() { fresh, err = s.r.readAll() })
	if err != nil {
		log.Printf("screen (startup): read failed: %v", err)
		return
	}
	s.pic = fresh
	s.request()
	s.b.Publish(CurrentChanged{Stamp: event.Now(), Current: s.pic.clone()})
}

// validate compares a read of the screen with Current. Every difference is
// logged and published, the measure of how well the events keep Current.
// Current is not replaced: a difference is adopted only when the previous
// validation reported the same one for that window, so a read taken while
// the screen is in motion (a space slide) changes nothing, and a missed
// event is caught one validation late. A failed read, or one taken while the
// overview shows thumbnails, compares nothing and leaves the previous
// validation's differences standing, so consecutive validations span it.
// A window read on a space Current does not know waits for the space event.
func (s *screen) validate(cause string) {
	var fresh *snapshot
	var err error
	s.onMain(func() { fresh, err = s.r.readAll() })
	if err != nil {
		log.Printf("validate (%s): read failed: %v", cause, err)
		return
	}
	if layout.OverviewOpen(fresh.all) {
		log.Printf("validate (%s): overview open, nothing compared", cause)
		return
	}
	next := map[uint32]*layout.LiveWindow{}
	nextSpace := map[uint32]uint64{}
	var adopt []difference
	for _, d := range differences(s.pic, fresh) {
		log.Printf("validate (%s): %s", cause, d.text)
		s.b.Publish(Difference{Stamp: event.Now(), Text: d.text})
		if d.window == 0 {
			continue // spaces follow space events; a difference there is only reported
		}
		w, placed := fresh.window(d.window)
		if placed {
			next[d.window] = &w
			nextSpace[d.window] = fresh.winSpace[d.window]
		} else {
			next[d.window] = nil
		}
		prev, seen := s.pending[d.window]
		switch {
		case !seen:
		case prev == nil && !placed:
			adopt = append(adopt, d)
		case prev != nil && placed && s.pendingSpace[d.window] == fresh.winSpace[d.window] && arrangement.SameFrame(prev.Frame, w.Frame) &&
			s.knownSpace(d.window, fresh):
			adopt = append(adopt, d)
		}
	}
	s.pending, s.pendingSpace = next, nextSpace
	for _, d := range adopt {
		log.Printf("validate (%s): adopted %s", cause, d.text)
		if w := next[d.window]; w != nil {
			s.put(*w, nextSpace[d.window], fresh)
		} else {
			s.remove(d.window, fresh.lists(d.window))
		}
		delete(s.pending, d.window)
	}
	s.unlist(cause, fresh)
}

// knownSpace reports whether Current knows the space fresh read the window
// on; a fullscreen window's space is its display's.
func (s *screen) knownSpace(wid uint32, fresh *snapshot) bool {
	if _, fs := fresh.fsWindow[wid]; fs {
		return true
	}
	_, ok := s.pic.idToKey[fresh.winSpace[wid]]
	return ok
}

// unlist drops from the whole window list every window two consecutive
// reads did not list. A window out of the request set sends no destroy
// event, so the read is what tells Current it is gone.
func (s *screen) unlist(cause string, fresh *snapshot) {
	listed := make(map[uint32]bool, len(fresh.all))
	for _, a := range fresh.all {
		listed[a.ID] = true
	}
	gone := map[uint32]bool{}
	kept := make([]layout.ListedWindow, 0, len(s.pic.all))
	for _, a := range s.pic.all {
		switch {
		case listed[a.ID]:
		case s.pendingGone[a.ID]:
			log.Printf("validate (%s): adopted: window %d is no longer listed", cause, a.ID)
			continue
		default:
			gone[a.ID] = true
		}
		kept = append(kept, a)
	}
	s.pendingGone = gone
	if len(kept) == len(s.pic.all) {
		return
	}
	s.pic.all = kept
	s.b.Publish(CurrentChanged{Stamp: event.Now(), Current: s.pic.clone()})
}

// put places one window in Current as fresh read it (its listing, title,
// frame and space) and publishes the change.
func (s *screen) put(w layout.LiveWindow, space uint64, fresh *snapshot) {
	delete(s.pic.fsWindow, w.ID)
	if d, fs := fresh.fsWindow[w.ID]; fs {
		delete(s.pic.winSpace, w.ID)
		s.pic.fsWindow[w.ID] = d
	} else {
		s.pic.winSpace[w.ID] = space
	}
	replaced := false
	for i := range s.pic.windows {
		if s.pic.windows[i].ID == w.ID {
			s.pic.windows[i] = w
			replaced = true
		}
	}
	if !replaced {
		s.pic.windows = append(s.pic.windows, w)
	}
	if !s.pic.lists(w.ID) {
		s.pic.all = append(s.pic.all, layout.ListedWindow{ID: w.ID, OwnerName: w.OwnerName, Title: w.Title})
	}
	s.request()
	s.publish(WindowChanged{Stamp: event.Now(), Window: w.ID, Space: s.pic.winSpace[w.ID], Frame: w.Frame})
}

// difference is one thing validation found Current had wrong: window names
// the window, 0 for a space difference.
type difference struct {
	window uint32
	text   string
}

// differences lists what Current got wrong, one line each.
func differences(have, want *snapshot) []difference {
	var out []difference
	byID := func(ws []layout.LiveWindow) map[uint32]layout.LiveWindow {
		m := make(map[uint32]layout.LiveWindow, len(ws))
		for _, w := range ws {
			m[w.ID] = w
		}
		return m
	}
	h, w := byID(have.windows), byID(want.windows)
	ids := make([]uint32, 0, len(w))
	for id := range w {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		ww := w[id]
		hw, ok := h[id]
		switch {
		case !ok:
			out = append(out, difference{id, fmt.Sprintf("missed window %d %s | %s", id, ww.OwnerName, ww.Title)})
		case have.winSpace[id] != want.winSpace[id]:
			out = append(out, difference{id, fmt.Sprintf("window %d %s on space %d, had %d", id, ww.OwnerName, want.winSpace[id], have.winSpace[id])})
		case !arrangement.SameFrame(hw.Frame, ww.Frame):
			out = append(out, difference{id, fmt.Sprintf("window %d %s at %.0f,%.0f %.0fx%.0f, had %.0f,%.0f %.0fx%.0f", id, ww.OwnerName,
				ww.Frame.X, ww.Frame.Y, ww.Frame.W, ww.Frame.H, hw.Frame.X, hw.Frame.Y, hw.Frame.W, hw.Frame.H)})
		}
	}
	ids = ids[:0]
	for id := range h {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		hw := h[id]
		if _, ok := w[id]; !ok {
			out = append(out, difference{id, fmt.Sprintf("kept window %d %s | %s that is gone", id, hw.OwnerName, hw.Title)})
		}
	}
	if len(have.spaces) != len(want.spaces) {
		out = append(out, difference{0, fmt.Sprintf("%d spaces, had %d", len(want.spaces), len(have.spaces))})
	}
	disps := make([]string, 0, len(want.currentOf))
	for d := range want.currentOf {
		disps = append(disps, d)
	}
	sort.Strings(disps)
	for _, d := range disps {
		cur := want.currentOf[d]
		if have.currentOf[d] != cur {
			out = append(out, difference{0, fmt.Sprintf("display %.8s shows space %d, had %d", d, cur, have.currentOf[d])})
		}
	}
	return out
}

// request gives the window server the request set: every placeable window
// in Current. A failing send and an overflow are logged when they begin.
func (s *screen) request() {
	ids := make([]uint32, 0, len(s.pic.windows))
	for _, w := range s.pic.windows {
		ids = append(ids, w.ID)
	}
	s.set.Set(ids)
	var err error
	s.onMain(func() { err = s.send(s.set.IDs()) })
	if err != nil && !s.sendFailing {
		log.Printf("requesting window notifications: %v", err)
	}
	s.sendFailing = err != nil
	n := s.set.Overflow()
	if n > 0 && !s.overflowing {
		log.Printf("watching %d windows; %d more are left to validation", watchedCap, n)
	}
	s.overflowing = n > 0
}
