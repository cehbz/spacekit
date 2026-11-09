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

// model judges Current against Target and is the only writer of Target. A
// WindowQuiet judges that window; the Reconciled that follows a space or
// display quiet judges every window, from the Current that reconciliation
// just published. It holds while nothing seen is the user's (locked,
// asleep, the overview, power-off announced), through a space or display
// run and while a put runs; a judgement that falls inside a hold is taken
// when the hold ends. A put's end judges the put's windows, since a put that
// confirmed nothing produced no window events. After a display run every
// window is owed its Target.
type model struct {
	b       *bus.Bus
	sub     *bus.Subscription
	st      *store.Store
	boot    func() time.Time // the boot time; tests substitute
	pic     *snapshot
	hold    struct{ locked, asleep, overview, poweringOff, space, display, putting bool }
	pending string   // cause of a judgement deferred by a hold
	putting []uint32 // the windows of the PutWanted in flight
	// overviewEnded: the latest overview event was OverviewEnded, so the
	// Reconciled for it ends the overview hold.
	overviewEnded bool
	oweNext       bool
	started       bool
}

// newModel subscribes at once.
func newModel(b *bus.Bus, st *store.Store) *model {
	return &model{b: b, st: st, boot: bootTime,
		sub: b.Subscribe("model", CurrentChanged{}, WindowQuiet{}, Reconciled{}, SpaceBurstBegan{}, DisplayBurstBegan{}, PutEnded{},
			event.SessionLocked{}, event.SessionUnlocked{}, event.ScreensSlept{}, event.ScreensWoke{},
			event.SystemSleeping{}, event.SystemWoke{}, event.PoweringOff{}, event.OverviewShown{}, event.OverviewEnded{})}
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
			m.judge("startup", nil)
		}
	case WindowQuiet:
		m.judge("window quiet", []uint32{e.Window})
	case Reconciled:
		// The screen has just published the Current this judgement needs.
		switch e.Cause {
		case event.Name(SpaceQuiet{}):
			m.hold.space = false
			m.judge("space change", nil)
		case event.Name(DisplayQuiet{}):
			m.hold.display = false
			m.oweNext = true
			m.judge("display change", nil)
		case event.Name(event.OverviewEnded{}):
			if m.overviewEnded { // not reopened since: the screen's marker can say it still shows
				m.hold.overview = false
				m.release()
			}
		}
	case SpaceBurstBegan:
		m.hold.space = true
	case DisplayBurstBegan:
		m.hold.display = true
		log.Printf("display change began; judgements held")
	case PutEnded:
		// Current already carries the put's results: the screen published
		// each before the WindowChanged the putter read it from.
		m.hold.putting = false
		ids := m.putting
		m.putting = nil
		if len(ids) > 0 && m.pending == "" { // else the deferred full judgement covers them; nil would judge all
			m.judge("put ended", ids)
		}
		m.release()
	case event.SessionLocked:
		m.hold.locked = true
	case event.SessionUnlocked:
		m.hold.locked = false
		m.release()
	case event.ScreensSlept, event.SystemSleeping:
		m.judge(event.Name(e), nil)
		m.hold.asleep = true
	case event.ScreensWoke, event.SystemWoke:
		m.hold.asleep = false
		m.release()
	case event.PoweringOff:
		m.judge(event.Name(e), nil)
		m.hold.poweringOff = true
		log.Printf("holding judgements until exit")
	case event.OverviewShown:
		m.hold.overview = true
		m.overviewEnded = false
	case event.OverviewEnded:
		m.overviewEnded = true
		// The hold ends on the Reconciled that follows: the screen refreshes
		// Current when the overview closes, and the judgement wants that one.
	}
}

func (m *model) held() bool {
	h := m.hold
	return m.pic == nil || h.locked || h.asleep || h.overview || h.poweringOff || h.space || h.display || h.putting
}

// release takes the judgement a hold deferred.
func (m *model) release() {
	if m.pending != "" && !m.held() {
		cause := m.pending
		m.pending = ""
		m.judge(cause+" (held)", nil)
	}
}

// judge compares Current's windows (only these ids when given) with Target
// and applies the outcome.
func (m *model) judge(cause string, only []uint32) {
	if m.held() {
		m.pending = cause
		return
	}
	if only == nil {
		m.pending = "" // a full judgement covers any deferred cause
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
	fail := func(err error) { log.Printf("judgement (%s) failed: %v", cause, err) }
	arr, err := m.st.Arrangement(displaySet(m.pic.spaces))
	if err != nil {
		fail(err)
		return
	}
	if err := m.st.SetSpaces(arr, m.pic.spaces); err != nil {
		fail(err)
		return
	}
	if m.oweNext {
		n, err := m.st.OweAll(arr, boot, now, cause, bindings(all))
		if err != nil {
			fail(err)
			return
		}
		m.oweNext = false
		log.Printf("%s: %d window(s) owed their placement", cause, n)
	}
	recorded, err := m.st.Recorded(arr, boot)
	if err != nil {
		fail(err)
		return
	}
	// Binding weighs every unclaimed window, so a rival outside a narrowed
	// judgement still makes the evidence ambiguous.
	if recorded, err = m.rebind(arr, boot, now, cause, recorded, all); err != nil {
		fail(err)
		return
	}
	ds := arrangement.Decide(recorded, arrangement.Look{Windows: seen})
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
		log.Printf("judgement (%s): titles: %v", cause, err)
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
// it recreates, if the evidence is unambiguous, and owes it its placement. A
// record is a candidate once its id is absent from the whole window list.
func (m *model) rebind(arr int64, boot, now time.Time, cause string, recorded []arrangement.Recorded, seen []arrangement.Seen) ([]arrangement.Recorded, error) {
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
		return recorded, nil
	}
	stored, err := m.st.Stored(arr)
	if err != nil {
		return nil, err
	}
	live := make(map[uint32]bool, len(m.pic.all))
	for _, l := range m.pic.all {
		live[l.ID] = true
	}
	pairs := arrangement.Bind(fresh, stored, boot.Unix(), live)
	if len(pairs) == 0 {
		return recorded, nil
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
		return nil, err
	}
	log.Printf("%s: bound %d of %d unclaimed window(s) to their records: %s", cause, len(bs), len(fresh), strings.Join(names, "; "))
	return m.st.Recorded(arr, boot)
}
