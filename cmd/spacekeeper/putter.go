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
	"github.com/cehbz/spacekit/internal/spacekey"
)

// orders are the window-server calls a put issues; liveOrders is the real
// set, run on the main thread by the putter.
type orders interface {
	move(wids []uint32, space uint64) error
	setFrame(pid int, wid uint32, f layout.Rect) error
	fullscreen(pid int, wid uint32) error
	switchSpace(display string, right bool) error
}

type liveOrders struct{}

func (liveOrders) move(wids []uint32, space uint64) error {
	return skylight.MoveWindowsToSpace(wids, space)
}
func (liveOrders) setFrame(pid int, wid uint32, f layout.Rect) error {
	return skylight.SetWindowFrame(pid, wid, f.X, f.Y, f.W, f.H)
}
func (liveOrders) fullscreen(pid int, wid uint32) error {
	return skylight.SetWindowFullscreen(pid, wid)
}
func (liveOrders) switchSpace(display string, right bool) error {
	return spacekey.StepOn(display, right)
}

// target is what a step expects Current to report for one window: the
// space it should be on, the frame it should have, or the display whose
// fullscreen space it should be on.
type target struct {
	space      uint64
	frame      *layout.Rect
	fullscreen string
}

func (t target) confirmedBy(c WindowChanged) bool {
	switch {
	case c.Gone:
		return false
	case t.fullscreen != "":
		return c.Fullscreen == t.fullscreen
	case t.frame != nil:
		return arrangement.SameFrame(*t.frame, c.Frame)
	}
	return c.Space == t.space
}

// wants is what a step's orders should bring about, each confirmed by one
// of the screen's events: windows at their targets (WindowChanged), displays
// showing a space (shows) or no longer showing one (leaves), by SpaceShown.
// unplaced and unswitched are the windows and displays the step could not
// order: they fail at once.
type wants struct {
	windows    map[uint32]target
	shows      map[string]uint64
	leaves     map[string]uint64
	unplaced   []uint32
	unswitched []string
}

func (w wants) pending() bool { return len(w.windows)+len(w.shows)+len(w.leaves) > 0 }

// empty reports whether the step neither ordered nor failed anything.
func (w wants) empty() bool { return !w.pending() && len(w.unplaced)+len(w.unswitched) == 0 }

// confirm takes off what e confirms and reports whether it confirmed anything.
func (w wants) confirm(e event.Event) bool {
	switch e := e.(type) {
	case WindowChanged:
		if t, ok := w.windows[e.Window]; ok && t.confirmedBy(e) {
			delete(w.windows, e.Window)
			return true
		}
	case SpaceShown:
		if s, ok := w.shows[e.Display]; ok && s == e.Space {
			delete(w.shows, e.Display)
			return true
		}
		if s, ok := w.leaves[e.Display]; ok && s != e.Space {
			delete(w.leaves, e.Display)
			return true
		}
	}
	return false
}

// step is one stage of a put: orders to issue against the latest Current,
// returning what they should bring about. A repeating step is issued again
// after each wait that confirmed everything, until it orders nothing.
type step struct {
	name   string
	issue  func(o orders, cur *snapshot) wants
	repeat bool
}

// putter carries out put requests one at a time as sequential steps, each
// confirmed by the screen's events or ended by the deadline.
type putter struct {
	b        *bus.Bus
	sub      *bus.Subscription
	o        orders
	onMain   func(func())
	deadline time.Duration
	cur      *snapshot // the latest Current while a put runs
}

// newPutter subscribes at once. stepTimeout (2 s) is declared with the
// other timings in watch.go.
func newPutter(b *bus.Bus, o orders, onMain func(func())) *putter {
	return &putter{b: b, o: o, onMain: onMain, deadline: stepTimeout,
		sub: b.Subscribe("putter", PutWanted{}, WindowChanged{}, SpaceShown{}, CurrentChanged{}, DisplayBurstBegan{})}
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

// moves orders each group of windows to its space and expects each there.
func moves(groups map[uint64][]uint32) func(o orders, _ *snapshot) wants {
	return func(o orders, _ *snapshot) wants {
		w := wants{windows: map[uint32]target{}}
		for space, wids := range groups {
			if err := o.move(wids, space); err != nil {
				log.Printf("moving %d window(s) to space %d: %v", len(wids), space, err)
				continue
			}
			for _, wid := range wids {
				w.windows[wid] = target{space: space}
			}
		}
		return w
	}
}

// plan turns the decisions into steps: stage hidden-space windows to the
// space their display shows (Accessibility resizes only shown windows),
// resize, send windows home. A window on the wrong space whose frame is
// right only goes home. The windows wanting fullscreen are returned apart:
// each one's steps are planned from Current when its turn comes.
func plan(req PutWanted) (steps []step, fullscreens []arrangement.Decision) {
	resolved := layout.ResolveSpaces(req.Spaces, req.Current.displays)
	displayOf := make(map[string]string, len(req.Spaces))
	for _, sp := range req.Spaces {
		displayOf[sp.UUID] = sp.DisplayUUID
	}
	home := make(map[uint64][]uint32)
	stage := make(map[uint64][]uint32)
	var resize []arrangement.Decision
	for _, d := range req.Decisions {
		if d.Want.Fullscreen != "" {
			fullscreens = append(fullscreens, d)
			continue
		}
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
	if len(stage) > 0 {
		steps = append(steps, step{name: "stage", issue: moves(stage)})
	}
	if len(resize) > 0 {
		steps = append(steps, step{name: "resize", issue: func(o orders, _ *snapshot) wants {
			w := wants{windows: map[uint32]target{}}
			for _, d := range resize {
				f := d.Want.Frame
				if err := o.setFrame(d.Seen.PID, d.Seen.Binding, f); err != nil {
					log.Printf("resizing window %d: %v", d.Seen.Binding, err)
					continue
				}
				w.windows[d.Seen.Binding] = target{frame: &f}
			}
			return w
		}})
	}
	if len(home) > 0 {
		steps = append(steps, step{name: "home", issue: moves(home)})
	}
	return steps, fullscreens
}

// fullscreenSteps take one window fullscreen on its target display: staged
// to the space the display shows when it is elsewhere (Chrome's
// Accessibility reaches only windows on a shown space), AXFullScreen set and
// confirmed by the window's attach to a fullscreen space on that display and
// the display's switch away from the shown space, then the display stepped
// back to that space. A display showing no user space (an earlier window's
// return failed) has nowhere to stage to: the window fails without an order.
func fullscreenSteps(cur *snapshot, d arrangement.Decision) []step {
	display, wid := d.Want.Fullscreen, d.Seen.Binding
	shown := cur.currentOf[display]
	if _, user := cur.idToKey[shown]; !user {
		return []step{{name: "fullscreen", issue: func(orders, *snapshot) wants {
			return wants{unplaced: []uint32{wid}}
		}}}
	}
	var steps []step
	if cur.winSpace[wid] != shown {
		steps = append(steps, step{name: "stage", issue: moves(map[uint64][]uint32{shown: {wid}})})
	}
	return append(steps,
		step{name: "fullscreen", issue: func(o orders, _ *snapshot) wants {
			if err := o.fullscreen(d.Seen.PID, wid); err != nil {
				log.Printf("making window %d fullscreen: %v", wid, err)
				return wants{unplaced: []uint32{wid}}
			}
			return wants{windows: map[uint32]target{wid: {fullscreen: display}}, leaves: map[string]uint64{display: shown}}
		}},
		step{name: "return", issue: returnTo(display, shown), repeat: true})
}

// returnTo steps display one space toward shown with Ctrl+Arrow and expects
// the display to show the next space along its order. Nothing is ordered
// once the display shows shown; when either space is not in its order the
// display fails unswitched.
// Ctrl+Arrow acts on the display under the pointer, so each step is posted
// with the pointer on display; the first, posted at the attach, switches,
// and a window staged onto a display's shown space goes fullscreen there.
func returnTo(display string, shown uint64) func(o orders, cur *snapshot) wants {
	return func(o orders, cur *snapshot) wants {
		at := cur.currentOf[display]
		if at == shown {
			return wants{}
		}
		order := cur.order[display]
		from, to := indexOf(order, at), indexOf(order, shown)
		if from < 0 || to < 0 {
			log.Printf("switching display %.8s toward space %d: space %d or %d not in its order", display, shown, at, shown)
			return wants{unswitched: []string{display}}
		}
		right, next := to > from, from-1
		if right {
			next = from + 1
		}
		if err := o.switchSpace(display, right); err != nil {
			log.Printf("switching display %.8s toward space %d: %v", display, shown, err)
			return wants{unswitched: []string{display}}
		}
		return wants{shows: map[string]uint64{display: order[next]}}
	}
}

func indexOf(ids []uint64, id uint64) int {
	for i, x := range ids {
		if x == id {
			return i
		}
	}
	return -1
}

func (p *putter) put(ctx context.Context, req PutWanted) {
	p.cur = req.Current
	steps, fullscreens := plan(req)
	p.b.Publish(PutStarted{Stamp: event.Now(), Windows: len(req.Decisions)})
	var rep PutEnded
	p.runSteps(ctx, steps, &rep)
	for _, d := range fullscreens {
		if rep.Cancelled || (len(rep.Steps) > 0 && p.cancelledBeforeOrder(ctx)) {
			rep.Cancelled = true
			break
		}
		p.runSteps(ctx, fullscreenSteps(p.cur, d), &rep)
	}
	rep.Stamp = event.Now()
	log.Print(rep.String())
	p.b.Publish(rep)
}

// runSteps issues each step and waits for it, recording it in rep; a
// repeating step stops at the first wait that does not confirm everything.
func (p *putter) runSteps(ctx context.Context, steps []step, rep *PutEnded) {
	for _, s := range steps {
		for {
			if len(rep.Steps) > 0 && p.cancelledBeforeOrder(ctx) {
				rep.Cancelled = true
				return
			}
			var want wants
			p.onMain(func() { want = s.issue(p.o, p.cur) })
			if s.repeat && want.empty() {
				break
			}
			res := p.await(ctx, s.name, want, rep)
			rep.Steps = append(rep.Steps, res)
			if rep.Cancelled {
				return
			}
			if !s.repeat || res.Confirmed == 0 || len(res.Unconfirmed)+len(res.Unswitched) > 0 {
				break
			}
		}
	}
}

// await waits until the screen's events confirm everything the step wants,
// the deadline passes, or shutdown or a display run cancels the put. Current
// keeps the putter's copy fresh meanwhile.
func (p *putter) await(ctx context.Context, name string, want wants, rep *PutEnded) PutStep {
	res := PutStep{Name: name}
	t0 := time.Now()
	deadline := time.After(p.deadline)
wait:
	for want.pending() {
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
			case CurrentChanged:
				p.cur = e.Current
			case PutWanted:
				log.Print("put: a put request arrived mid-put; dropped")
			case DisplayBurstBegan:
				rep.Cancelled = true
				break wait
			default:
				if want.confirm(e) {
					res.Confirmed++
				}
			}
		}
	}
	res.Took = time.Since(t0)
	res.Unconfirmed = append(res.Unconfirmed, want.unplaced...)
	for wid := range want.windows {
		res.Unconfirmed = append(res.Unconfirmed, wid)
	}
	sort.Slice(res.Unconfirmed, func(i, j int) bool { return res.Unconfirmed[i] < res.Unconfirmed[j] })
	res.Unswitched = append(res.Unswitched, want.unswitched...)
	for d := range want.shows {
		res.Unswitched = append(res.Unswitched, d)
	}
	for d := range want.leaves {
		res.Unswitched = append(res.Unswitched, d)
	}
	sort.Strings(res.Unswitched)
	return res
}

// cancelledBeforeOrder reports whether shutdown or a queued display run
// stops the put before its next order. Queued WindowChanged and SpaceShown
// events precede the order and cannot confirm it, so they are discarded;
// queued Current is kept.
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
			switch e := e.(type) {
			case CurrentChanged:
				p.cur = e.Current
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
