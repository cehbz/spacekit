package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/bus"
	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/store"
)

// model decides Current against Target and is the only writer of Target. A
// WindowQuiet decides that window; a DisplayQuiet decides every window
// against the arrangement the displays now form; the first Current decides
// at startup; a PutEnded decides the put's windows; a Tick decides the
// windows marked dirty with no put tried (owe, undo). It decides on the
// event-kept Current, never on a read. It holds while nothing seen is the
// user's (locked, asleep, power-off announced), through a display run and
// while a put runs; a decide that falls inside a hold is taken when the hold
// ends. A change has a system cause within the grace period after displays
// settled, when the window reappeared and was bound this decide, or when the
// window was in a put that ended within putGrace; any other change is the
// user's.
type model struct {
	b              *bus.Bus
	sub            *bus.Subscription
	st             *store.Store
	boot           func() time.Time // the boot time; tests substitute
	pic            *snapshot
	hold           struct{ locked, asleep, poweringOff, display, putting bool }
	pending        pendingDecide
	putting        []uint32 // the windows of the PutWanted in flight
	displaySettled time.Time
	grace          time.Duration   // after displays settle, window changes are the display change's
	lastPut        map[uint32]bool // the windows of the put that ended last
	putEnded       time.Time
	putGrace       time.Duration // after a put ends, its windows' changes are the put's
	started        bool
}

// pendingDecide is what holds deferred, per window so that each keeps its own
// cause.
type pendingDecide struct {
	cause   string          // the last held cause; "" when nothing is pending
	all     bool            // a full decide is pending
	system  bool            // ... and it has a system cause
	windows map[uint32]bool // narrowed decides pending: window → its change has a system cause
}

// newModel subscribes at once.
func newModel(b *bus.Bus, st *store.Store) *model {
	return &model{b: b, st: st, boot: bootTime, grace: displayGrace, putGrace: putGrace,
		sub: b.Subscribe("model", CurrentChanged{}, WindowQuiet{}, DisplayQuiet{}, DisplayBurstBegan{}, PutEnded{}, Tick{},
			event.SessionLocked{}, event.SessionUnlocked{}, event.ScreensSlept{}, event.ScreensWoke{},
			event.SystemSleeping{}, event.SystemWoke{}, event.PoweringOff{})}
}

func (m *model) run(ctx context.Context) {
	defer m.sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-m.sub.C():
			if !ok {
				return
			}
			m.handle(e)
		}
	}
}

func (m *model) handle(e event.Event) {
	switch e := e.(type) {
	case CurrentChanged:
		m.pic = e.Current
		if !m.started {
			m.started = true
			m.decide("startup", nil, false, nil)
		}
	case WindowQuiet:
		m.decide("window settled", []uint32{e.Window}, time.Since(m.displaySettled) < m.grace, nil)
	case DisplayQuiet:
		m.hold.display = false
		m.displaySettled = time.Now()
		m.decide("display settled", nil, true, nil)
	case Tick:
		m.decideMarked()
	case DisplayBurstBegan:
		m.hold.display = true
		log.Printf("display change began; decides held")
	case PutEnded:
		// Current already carries the put's results: the screen published
		// each before the WindowChanged the putter read it from.
		m.hold.putting = false
		ids := m.putting
		m.putting = nil
		m.putEnded = time.Now()
		m.lastPut = make(map[uint32]bool, len(ids))
		for _, id := range ids {
			m.lastPut[id] = true
		}
		if m.pending.cause != "" { // the release decides them, as the put's whatever the grace
			if m.pending.windows == nil {
				m.pending.windows = make(map[uint32]bool, len(ids))
			}
			for _, id := range ids {
				m.pending.windows[id] = true
			}
		} else if len(ids) > 0 { // nil would decide all
			m.decide("put ended", ids, true, nil)
		}
		m.release()
	case event.SessionLocked:
		m.hold.locked = true
	case event.SessionUnlocked:
		m.hold.locked = false
		m.release()
	case event.ScreensSlept, event.SystemSleeping:
		m.decide(event.Name(e), nil, false, nil)
		m.hold.asleep = true
	case event.ScreensWoke, event.SystemWoke:
		m.hold.asleep = false
		m.release()
	case event.PoweringOff:
		m.decide(event.Name(e), nil, false, nil)
		m.hold.poweringOff = true
		log.Printf("holding decides until exit")
	}
}

// decideMarked decides, as the system's, the windows in Current that are
// marked dirty with no put tried (owe, undo): the explicit instruction to put
// them back. Nothing marked, nothing decided.
func (m *model) decideMarked() {
	if m.pic == nil {
		return
	}
	arr, err := m.st.Arrangement(displaySet(m.pic.spaces))
	if err != nil {
		log.Printf("decide (tick) failed: %v", err)
		return
	}
	marked, err := m.st.DirtyUntried(arr, m.boot())
	if err != nil {
		log.Printf("decide (tick) failed: %v", err)
		return
	}
	var ids []uint32
	for _, id := range marked {
		if _, ok := m.pic.window(id); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		m.decide("tick", ids, true, nil)
	}
}

func (m *model) held() bool {
	h := m.hold
	return m.pic == nil || h.locked || h.asleep || h.poweringOff || h.display || h.putting
}

// release takes the decides a hold deferred, each window with its own cause.
func (m *model) release() {
	if m.pending.cause == "" || m.held() {
		return
	}
	p := m.pending
	m.pending = pendingDecide{}
	if p.all {
		m.decide(p.cause+" (held)", nil, p.system, p.windows)
		return
	}
	ids := make([]uint32, 0, len(p.windows))
	for id := range p.windows {
		ids = append(ids, id)
	}
	m.decide(p.cause+" (held)", ids, false, p.windows)
}

// decide compares Current's windows (only these ids when given) with Target
// and applies the outcome. system says the change behind this decide is the
// system's (display change, put); also names windows whose change is the
// system's; a window bound during it, or in a put that ended within
// putGrace, is the system's in any case. A full decide with a system cause is
// a display settle, live or released from a hold: a fresh cause, so a window
// whose put failed is put again. A held decide is recorded per window, and a
// system cause is not made the user's by a later held one.
func (m *model) decide(cause string, only []uint32, system bool, also map[uint32]bool) {
	if m.held() {
		m.pending.cause = cause
		if only == nil {
			m.pending.all = true
			m.pending.system = m.pending.system || system
			return
		}
		if m.pending.windows == nil {
			m.pending.windows = make(map[uint32]bool, len(only))
		}
		for _, id := range only {
			m.pending.windows[id] = m.pending.windows[id] || system
		}
		return
	}
	if only == nil {
		m.pending = pendingDecide{} // a full decide covers any deferred cause
	}
	all := seenWindows(m.pic)
	seen := all
	if only != nil {
		want := make(map[uint32]bool, len(only))
		for _, id := range only {
			want[id] = true
		}
		seen = nil
		for _, s := range all {
			if want[s.Binding] {
				seen = append(seen, s)
			}
		}
	}
	boot, now := m.boot(), time.Now()
	fail := func(err error) { log.Printf("decide (%s) failed: %v", cause, err) }
	arr, err := m.st.Arrangement(displaySet(m.pic.spaces))
	if err != nil {
		fail(err)
		return
	}
	if err := m.st.SetSpaces(arr, m.pic.spaces); err != nil {
		fail(err)
		return
	}
	recorded, err := m.st.Recorded(arr, boot)
	if err != nil {
		fail(err)
		return
	}
	// Binding weighs every unclaimed window, so a rival outside a narrowed
	// decide still makes the evidence ambiguous.
	recorded, bound, err := m.rebind(arr, boot, now, cause, recorded, all)
	if err != nil {
		fail(err)
		return
	}
	putRecent := time.Since(m.putEnded) < m.putGrace
	sys := make(map[uint32]bool, len(seen))
	for _, s := range seen {
		if system || bound[s.Binding] || also[s.Binding] || (putRecent && m.lastPut[s.Binding]) {
			sys[s.Binding] = true
		}
	}
	ds := arrangement.Decide(recorded, arrangement.Look{Windows: seen, System: sys, Retry: only == nil && system})
	saved, err := m.st.Spaces(arr)
	if err != nil {
		fail(err)
		return
	}
	resolved := layout.ResolveSpaces(saved, m.pic.displays)
	var puts []arrangement.Decision
	for i := range ds {
		d := &ds[i]
		if d.Kind != arrangement.Put {
			continue
		}
		if _, ok := resolved[d.Want.Space]; !ok {
			d.Move, d.Resize = false, false // the space is gone: left where it is, no progress recorded
			continue
		}
		if d.Move || d.Resize {
			puts = append(puts, *d)
		}
	}
	if err := m.st.Apply(arr, boot, now, cause, ds); err != nil {
		fail(err)
		return
	}
	if err := m.st.RefreshTitles(boot, seen); err != nil {
		log.Printf("decide (%s): titles: %v", cause, err)
	}
	if len(ds) > 0 {
		m.b.Publish(TargetChanged{Stamp: event.Now(), Cause: cause, Decisions: ds})
	}
	logDecisions(cause, ds)
	if len(puts) > 0 {
		m.hold.putting = true
		m.putting = m.putting[:0]
		for _, d := range puts {
			m.putting = append(m.putting, d.Seen.Binding)
		}
		m.b.Publish(PutWanted{Stamp: event.Now(), Arr: arr, Current: m.pic.clone(), Spaces: saved, Decisions: puts})
	}
}

// rebind binds every seen window no record of this boot claims to the record
// it recreates, if the evidence is unambiguous. It returns the records and
// the bindings it made; the caller treats those windows' changes as the
// system's. A record is a candidate once its id is absent from the whole
// window list.
func (m *model) rebind(arr int64, boot, now time.Time, cause string, recorded []arrangement.Recorded, seen []arrangement.Seen) ([]arrangement.Recorded, map[uint32]bool, error) {
	claimed := make(map[uint32]bool, len(recorded))
	for _, r := range recorded {
		claimed[r.Binding] = true
	}
	var fresh []arrangement.Seen
	for _, s := range seen {
		if !claimed[s.Binding] {
			fresh = append(fresh, s)
		}
	}
	if len(fresh) == 0 {
		return recorded, nil, nil
	}
	stored, err := m.st.Stored(arr)
	if err != nil {
		return nil, nil, err
	}
	live := make(map[uint32]bool, len(m.pic.all))
	for _, l := range m.pic.all {
		live[l.ID] = true
	}
	pairs := arrangement.Bind(fresh, stored, boot.Unix(), live)
	if len(pairs) == 0 {
		return recorded, nil, nil
	}
	var bs []store.Bound
	var names []string
	for _, s := range fresh {
		if win, ok := pairs[s.Binding]; ok {
			bs = append(bs, store.Bound{Window: win, Seen: s})
			t := s.Title
			if len(t) > 24 {
				t = t[:24]
			}
			names = append(names, s.App+" | "+t)
		}
	}
	if err := m.st.Bind(arr, boot, now, cause, bs); err != nil {
		return nil, nil, err
	}
	log.Printf("%s: bound %d of %d unclaimed window(s) to their records: %s", cause, len(bs), len(fresh), strings.Join(names, "; "))
	bound := make(map[uint32]bool, len(pairs))
	for wid := range pairs {
		bound[wid] = true
	}
	rs, err := m.st.Recorded(arr, boot)
	return rs, bound, err
}
