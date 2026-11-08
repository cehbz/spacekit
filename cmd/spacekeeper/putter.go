package main

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/skylight"
)

// orders are the window-server calls a put issues; liveOrders is the real
// set, run on the main thread by the putter.
type orders interface {
	move(wids []uint32, space uint64) error
	setFrame(pid int, wid uint32, f layout.Rect) error
}

type liveOrders struct{}

func (liveOrders) move(wids []uint32, space uint64) error {
	return skylight.MoveWindowsToSpace(wids, space)
}
func (liveOrders) setFrame(pid int, wid uint32, f layout.Rect) error {
	return skylight.SetWindowFrame(pid, wid, f.X, f.Y, f.W, f.H)
}

// target is what a step expects Current to report for one window: the
// space it should be on, or the frame it should have.
type target struct {
	space uint64
	frame *layout.Rect
}

func (t target) confirmedBy(c WindowChanged) bool {
	if c.Gone {
		return false
	}
	if t.frame != nil {
		return arrangement.SameFrame(*t.frame, c.Frame)
	}
	return c.Space == t.space
}

// step is one stage of a put: orders to issue, returning the targets they
// set up.
type step struct {
	name  string
	issue func(o orders) map[uint32]target
}

// putter carries out put requests one at a time as sequential steps, each
// confirmed by the screen's WindowChanged events or ended by the deadline.
type putter struct {
	b        *bus.Bus
	sub      *bus.Subscription
	o        orders
	onMain   func(func())
	deadline time.Duration
}

// newPutter subscribes at once. stepTimeout (2 s) is declared with the
// other timings in watch.go.
func newPutter(b *bus.Bus, o orders, onMain func(func())) *putter {
	return &putter{b: b, o: o, onMain: onMain, deadline: stepTimeout,
		sub: b.Subscribe("putter", PutWanted{}, WindowChanged{}, DisplayBurstBegan{})}
}

func (p *putter) run(ctx context.Context) {
	defer p.sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-p.sub.C():
			if !ok {
				return
			}
			if req, ok := e.(PutWanted); ok {
				p.put(ctx, req)
			}
		}
	}
}

// plan turns the decisions into steps: stage hidden-space windows to the
// space their display shows (Accessibility resizes only shown windows),
// resize, send windows home. A window on the wrong space whose frame is
// right only goes home.
func plan(req PutWanted) []step {
	resolved := layout.ResolveSpaces(req.Spaces, req.Current.displays)
	displayOf := make(map[string]string, len(req.Spaces))
	for _, sp := range req.Spaces {
		displayOf[sp.UUID] = sp.DisplayUUID
	}
	home := make(map[uint64][]uint32)
	stage := make(map[uint64][]uint32)
	var resize []arrangement.Decision
	for _, d := range req.Decisions {
		dest, ok := resolved[d.Want.Space]
		if !ok {
			continue
		}
		wid := d.Seen.Binding
		if !d.Resize {
			if d.Move {
				home[dest] = append(home[dest], wid)
			}
			continue
		}
		resize = append(resize, d)
		shown := req.Current.currentOf[displayOf[d.Want.Space]]
		at := req.Current.winSpace[wid]
		if shown == 0 || (at == shown && dest == shown) {
			continue
		}
		if at != shown {
			stage[shown] = append(stage[shown], wid)
		}
		if dest != shown {
			home[dest] = append(home[dest], wid)
		}
	}
	moves := func(groups map[uint64][]uint32) func(o orders) map[uint32]target {
		return func(o orders) map[uint32]target {
			want := map[uint32]target{}
			for space, wids := range groups {
				if err := o.move(wids, space); err != nil {
					log.Printf("moving %d window(s) to space %d: %v", len(wids), space, err)
					continue
				}
				for _, wid := range wids {
					want[wid] = target{space: space}
				}
			}
			return want
		}
	}
	var steps []step
	if len(stage) > 0 {
		steps = append(steps, step{"stage", moves(stage)})
	}
	if len(resize) > 0 {
		steps = append(steps, step{"resize", func(o orders) map[uint32]target {
			want := map[uint32]target{}
			for _, d := range resize {
				f := d.Want.Frame
				if err := o.setFrame(d.Seen.PID, d.Seen.Binding, f); err != nil {
					log.Printf("resizing window %d: %v", d.Seen.Binding, err)
					continue
				}
				want[d.Seen.Binding] = target{frame: &f}
			}
			return want
		}})
	}
	if len(home) > 0 {
		steps = append(steps, step{"home", moves(home)})
	}
	return steps
}

func (p *putter) put(ctx context.Context, req PutWanted) {
	steps := plan(req)
	p.b.Publish(PutStarted{Stamp: event.Now(), Windows: len(req.Decisions)})
	var rep PutEnded
	for i, s := range steps {
		if i > 0 && p.cancelledBeforeOrder(ctx) {
			rep.Cancelled = true
			break
		}
		var want map[uint32]target
		p.onMain(func() { want = s.issue(p.o) })
		res := PutStep{Name: s.name}
		t0 := time.Now()
		deadline := time.After(p.deadline)
	wait:
		for len(want) > 0 {
			select {
			case <-ctx.Done():
				rep.Cancelled = true
				break wait
			case <-deadline:
				break wait
			case e, ok := <-p.sub.C():
				if !ok {
					rep.Cancelled = true
					break wait
				}
				switch e := e.(type) {
				case WindowChanged:
					if t, ok := want[e.Window]; ok && t.confirmedBy(e) {
						delete(want, e.Window)
						res.Confirmed++
					}
				case PutWanted:
					log.Print("put: a put request arrived mid-put; dropped")
				case DisplayBurstBegan:
					rep.Cancelled = true
					break wait
				}
			}
		}
		res.Took = time.Since(t0)
		for wid := range want {
			res.Unconfirmed = append(res.Unconfirmed, wid)
		}
		sort.Slice(res.Unconfirmed, func(i, j int) bool { return res.Unconfirmed[i] < res.Unconfirmed[j] })
		rep.Steps = append(rep.Steps, res)
		if rep.Cancelled {
			break
		}
	}
	rep.Stamp = event.Now()
	log.Print(rep.String())
	p.b.Publish(rep)
}

// cancelledBeforeOrder reports whether shutdown or a queued display run
// stops the put before its next order. Queued WindowChanged events precede
// the order and cannot confirm it, so they are discarded.
func (p *putter) cancelledBeforeOrder(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	for {
		select {
		case e, ok := <-p.sub.C():
			if !ok {
				return true
			}
			switch e.(type) {
			case DisplayBurstBegan:
				return true
			case PutWanted:
				log.Print("put: a put request arrived mid-put; dropped")
			}
		default:
			return false
		}
	}
}
