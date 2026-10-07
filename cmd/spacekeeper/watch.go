package main

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/settle"
	"github.com/cehbz/spacekit/internal/skylight"
	"github.com/cehbz/spacekit/internal/store"
	"github.com/cehbz/spacekit/internal/sysevents"
)

// Timing. A display wake produces two hotplug out/in cycles over ~6s and
// WindowServer's own window-return attempts give up ~7s after the first, so
// displayQuiet after the last callback lands after macOS has finished.
const (
	displayQuiet  = 10 * time.Second
	spaceQuiet    = 3 * time.Second
	stillGap      = 400 * time.Millisecond // between the two samples of a look
	launchDelay   = 1500 * time.Millisecond
	sweepInterval = 15 * time.Second
	// quietExit ends login convergence once nothing has happened for this
	// long (no launch, no new match): the login storm is over. The cap is a
	// backstop, not a tuning knob.
	quietExit = 2 * time.Minute
	// savesEvery is how many interval looks pass between snapshot saves.
	savesEvery = 3
	// retention is how long history is kept.
	retention = 90 * 24 * time.Hour
	// pruneEvery is the number of interval looks between prunes (one-minute looks).
	pruneEvery = 1440
)

type watchOptions struct {
	interval, settle, bootCap  time.Duration
	forceBoot                  bool
	keep                       int
	frames, create, fullscreen bool
}

// watcher is the resident agent: it looks at the session on a timer and on
// sleep and space-change events and reconciles it with the arrangement, owes
// every window after a display change, saves snapshots, and runs the login
// convergence. All SkyLight work runs on the main thread via
// sysevents.OnMain; this loop only decides.
type watcher struct {
	opt      watchOptions
	displays *settle.Window // an open display-reconfiguration burst
	spaces   *settle.Window // debounce for active-space changes

	boot                      *reconciler // non-nil while login convergence runs
	bootStarted, bootActivity time.Time

	store       *store.Store
	burst       int64           // open display disturbance in the store, 0 if none
	oweNext     bool            // a disturbance ended while the agent was down: owe at the next look
	prevVisible map[string]bool // space keys visible at the previous look
	asleep      bool            // screens or system asleep: the timer still fires in dark wake
	ticks       int

	retry string // trigger of a look put off: the screen was moving or the overview showing

	// poweringOff is set by the power-off notification: the layout was saved
	// at that moment and every later save and look is held, so the half-quit
	// state of a logout in progress never enters history.
	poweringOff bool
}

func watchCmd(o watchOptions) error {
	log.SetFlags(0)
	log.SetOutput(newZoneWriter(log.Writer(), "/etc/localtime"))
	st, err := store.Open(filepath.Join(dataDir(), "spacekeeper.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	w := &watcher{opt: o, store: st, displays: settle.New(displayQuiet), spaces: settle.New(spaceQuiet)}
	sysevents.Start()
	go func() {
		w.loop()
		sysevents.Stop()
	}()
	sysevents.Run()
	return nil
}

func (w *watcher) loop() {
	tick := time.NewTicker(w.opt.interval)
	defer tick.Stop()
	sweep := time.NewTicker(sweepInterval)
	defer sweep.Stop()

	w.prune()
	w.recoverDisturbances()
	w.startBoot()
	w.look("startup")
	w.save("startup")
	for {
		var settled, spaceQuietC <-chan time.Time
		if w.displays.Open() {
			settled = time.After(time.Until(w.displays.Deadline()))
		}
		if w.spaces.Open() {
			spaceQuietC = time.After(time.Until(w.spaces.Deadline()))
		}
		select {
		case e := <-sysevents.Events():
			w.handle(e)
		case <-tick.C:
			w.look("interval")
			if w.ticks++; w.ticks%savesEvery == 0 {
				w.save("interval")
			}
			if w.ticks%pruneEvery == 0 {
				w.prune()
			}
		case <-sweep.C:
			w.sweep()
		case <-settled:
			w.displaysSettled()
		case <-spaceQuietC:
			w.spaces.Close()
			trigger := "space change"
			if w.retry != "" {
				trigger, w.retry = w.retry, ""
			}
			w.look(trigger)
			if trigger == "space change" {
				w.save(trigger)
			}
		}
	}
}

// prune deletes history past the retention period.
func (w *watcher) prune() {
	n, err := w.store.Prune(time.Now().Add(-retention), bootTime())
	if err != nil {
		log.Printf("prune failed: %v", err)
		return
	}
	if n > 0 {
		log.Printf("pruned %d placement version(s) older than %d days", n, int(retention.Hours()/24))
	}
}

func (w *watcher) handle(e sysevents.Event) {
	switch e.Kind {
	case sysevents.DisplayReconfigured:
		if w.displays.Note(e.At) {
			log.Printf("display change began (display %d, flags %#x); looks and saves held", e.Display, e.Flags)
			if id, err := w.store.BeginDisturbance("display", e.At); err == nil {
				w.burst = id
			}
		}
	case sysevents.ScreensSleep, sysevents.SystemWillSleep:
		w.look(e.Kind.String())
		w.save(e.Kind.String())
		w.asleep = true
	case sysevents.WillPowerOff:
		w.look(e.Kind.String())
		w.save(e.Kind.String())
		w.poweringOff = true
		log.Printf("holding looks and saves until exit")
	case sysevents.SpaceChanged:
		w.spaces.Note(e.At)
	case sysevents.AppLaunched:
		time.Sleep(launchDelay) // let the app map its windows
		if w.boot != nil {
			w.bootPass(e.Name + " launched")
		} else {
			w.look(e.Name + " launched")
		}
	case sysevents.ScreensWake, sysevents.SystemWake:
		w.asleep = false
		log.Printf("%s", e.Kind)
	}
}

// --- saving ---

func (w *watcher) save(reason string) {
	if w.poweringOff {
		return
	}
	if w.displays.Open() {
		log.Printf("save (%s) held: display reconfiguration in progress", reason)
		return
	}
	var path string
	var err error
	sysevents.OnMain(func() { path, err = saveSnapshot(w.opt.keep, w.opt.settle) })
	switch {
	case errors.Is(err, errOverviewOpen):
		log.Printf("save (%s) skipped: %v", reason, err)
	case err != nil:
		log.Printf("save (%s) failed: %v", reason, err)
	case path != "":
		if l, err := loadLayout(path); err == nil {
			st := l.Stats()
			log.Printf("snapshot %s (%s): %d windows, %s", filepath.Base(path), reason, st.Windows, displaySummary(st))
		}
	}
}

// --- looking ---

// recoverDisturbances closes display disturbances left open by a restart
// and arranges for the first look to owe every window, as their settle
// would have.
func (w *watcher) recoverDisturbances() {
	open, err := w.store.OpenDisturbances()
	if err != nil {
		log.Printf("cannot read open disturbances: %v", err)
		return
	}
	for _, d := range open {
		w.store.EndDisturbance(d.ID, time.Now())
		w.oweNext = true
		log.Printf("a %s disturbance from %s was left open; windows will be owed", d.Kind, d.Began.Format("15:04:05"))
	}
}

func (w *watcher) displaysSettled() {
	w.displays.Close()
	if w.burst != 0 {
		w.store.EndDisturbance(w.burst, time.Now())
		w.burst = 0
	}
	if w.boot != nil {
		w.bootPass("displays settled")
		return
	}
	w.oweNext = true
	w.look("display change")
	w.save("after reconfiguration")
}

// look observes the session and reconciles it with the arrangement. It
// does nothing while a disturbance is in progress: the screen is not
// intent then.
func (w *watcher) look(trigger string) {
	if w.poweringOff || w.asleep || w.displays.Open() || w.boot != nil {
		return
	}
	// Two samples a moment apart: a look is only taken on a still screen.
	var first []arrangement.Seen
	var err error
	sysevents.OnMain(func() { first, err = sample() })
	switch {
	case errors.Is(err, errOverviewOpen):
		w.putOff(trigger, err.Error())
		return
	case err != nil:
		log.Printf("look (%s) failed: %v", trigger, err)
		return
	}
	if first == nil {
		return
	}
	time.Sleep(stillGap)
	var moving bool
	sysevents.OnMain(func() { moving, err = w.lookOnMain(trigger, first) })
	switch {
	case errors.Is(err, errOverviewOpen):
		w.putOff(trigger, err.Error())
	case err != nil:
		log.Printf("look (%s) failed: %v", trigger, err)
	case moving:
		w.putOff(trigger, "the screen is in motion")
	}
}

// putOff retries the look under the same trigger once the space-change
// quiet period elapses. A look put off again while it waits is logged only
// the first time: the overview can show for minutes.
func (w *watcher) putOff(trigger, why string) {
	if w.retry == "" {
		log.Printf("look (%s) put off: %s", trigger, why)
	}
	w.retry = trigger
	w.spaces.Note(time.Now())
}

// sample observes the session's windows. It returns errOverviewOpen while
// the overview is showing, and nil while the session is locked: the screen
// is not intent then.
func sample() ([]arrangement.Seen, error) {
	s, err := gather()
	if err != nil {
		return nil, err
	}
	if layout.OverviewOpen(s.all) {
		return nil, errOverviewOpen
	}
	if skylight.SessionLocked() {
		return nil, nil
	}
	return seenWindows(s), nil
}

// lookOnMain takes the look unless the session moved since the first sample,
// which it reports as moving, or the overview is showing, which it reports
// as errOverviewOpen.
func (w *watcher) lookOnMain(trigger string, first []arrangement.Seen) (moving bool, err error) {
	s, err := gather()
	if err != nil {
		return false, err
	}
	if layout.OverviewOpen(s.all) {
		return false, errOverviewOpen
	}
	if skylight.SessionLocked() {
		return false, nil
	}
	seen := seenWindows(s)
	if !arrangement.Still(first, seen) {
		return true, nil
	}
	boot, now := bootTime(), time.Now()
	arr, err := w.store.Arrangement(displaySet(s.spaces))
	if err != nil {
		return false, err
	}
	if err := w.store.SetSpaces(arr, s.spaces); err != nil {
		return false, err
	}
	if w.oweNext {
		n, err := w.store.OweAll(arr, boot, now, trigger, bindings(seen))
		if err != nil {
			return false, err
		}
		w.oweNext = false
		log.Printf("%s: %d window(s) owed their placement", trigger, n)
	}
	recorded, err := w.store.Recorded(arr, boot)
	if err != nil {
		return false, err
	}
	if recorded, err = w.rebind(arr, boot, now, trigger, recorded, seen, s.all); err != nil {
		return false, err
	}
	visible := visibleKeys(s)
	both := make(map[string]bool, len(visible)+len(w.prevVisible))
	for k := range visible {
		both[k] = true
	}
	for k := range w.prevVisible {
		both[k] = true
	}
	ds := arrangement.Decide(recorded, arrangement.Look{Windows: seen, Visible: both})
	if err := w.repair(s, arr, ds); err != nil {
		return false, err
	}
	if err := w.store.Apply(arr, boot, now, trigger, ds); err != nil {
		return false, err
	}
	if err := w.store.RefreshTitles(boot, seen); err != nil {
		return false, err
	}
	w.prevVisible = visible
	logDecisions(trigger, ds)
	return false, nil
}

// rebind binds every seen window no record of this boot claims to the record
// it recreates, if the evidence is unambiguous, and owes it its placement. A
// record is a candidate once its id is absent from the whole window list.
func (w *watcher) rebind(arr int64, boot, now time.Time, cause string, recorded []arrangement.Recorded, seen []arrangement.Seen, all []layout.ListedWindow) ([]arrangement.Recorded, error) {
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
	stored, err := w.store.Stored(arr)
	if err != nil {
		return nil, err
	}
	live := make(map[uint32]bool, len(all))
	for _, l := range all {
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
	if err := w.store.Bind(arr, boot, now, cause, bs); err != nil {
		return nil, err
	}
	log.Printf("%s: bound %d of %d unclaimed window(s) to their records: %s", cause, len(bs), len(fresh), strings.Join(names, "; "))
	return w.store.Recorded(arr, boot)
}

// repair acts on Repair decisions. A window on the wrong space whose frame is
// right is moved home. A window whose frame is wrong is resized through
// Accessibility, which only reaches a window on a shown space: one on a
// hidden space is first moved, with all the others, to the space its display
// is showing, resized there, and moved home. Each step is awaited by polling
// the window server. A window whose space no longer exists is skipped and
// keeps no progress.
func (w *watcher) repair(s *snapshot, arr int64, ds []arrangement.Decision) error {
	saved, err := w.store.Spaces(arr)
	if err != nil {
		return err
	}
	resolved := layout.ResolveSpaces(saved, s.displays)
	displayOf := make(map[string]string, len(saved)) // space key -> display UUID
	for _, sp := range saved {
		displayOf[sp.UUID] = sp.DisplayUUID
	}
	home := make(map[uint64][]uint32)  // home space -> windows to send there
	stage := make(map[uint64][]uint32) // shown space -> windows to bring to it
	var resize []*arrangement.Decision
	for i := range ds {
		d := &ds[i]
		if d.Kind != arrangement.Repair {
			continue
		}
		target, ok := resolved[d.Want.Space]
		if !ok {
			d.Move, d.Resize = false, false
			continue
		}
		wid := d.Seen.Binding
		if !d.Resize {
			if d.Move {
				home[target] = append(home[target], wid)
			}
			continue
		}
		resize = append(resize, d)
		shown := s.currentOf[displayOf[d.Want.Space]]
		at := s.winSpace[wid]
		if shown == 0 || (at == shown && target == shown) {
			continue // already on its shown home space
		}
		if at != shown {
			stage[shown] = append(stage[shown], wid)
		}
		if target != shown {
			home[target] = append(home[target], wid)
		}
	}
	if len(home) == 0 && len(resize) == 0 {
		return nil
	}
	staged, tStage := moveAndWait(stage)
	t0 := time.Now()
	want := make(map[uint32]layout.Rect, len(resize))
	for _, d := range resize {
		f := d.Want.Frame
		if err := skylight.SetWindowFrame(d.Seen.PID, d.Seen.Binding, f.X, f.Y, f.W, f.H); err == nil {
			want[d.Seen.Binding] = f
		}
	}
	_, framed := waitFor(stepTimeout, func() bool { return framesAre(want) })
	tResize := time.Since(t0)
	returned, tHome := moveAndWait(home)
	if len(resize) > 0 {
		log.Printf("repair: %d window(s) resized (%d staged in %s, resize %s settled=%v, %d moved home in %s)",
			len(resize), staged, tStage.Round(time.Millisecond), tResize.Round(time.Millisecond), framed, returned, tHome.Round(time.Millisecond))
	}
	return nil
}

// stepTimeout bounds each awaited step of a repair.
const stepTimeout = 2 * time.Second

// moveAndWait moves each group of windows to its space and waits until the
// window server reports every one there. It returns how many it moved and
// how long the wait took.
func moveAndWait(groups map[uint64][]uint32) (int, time.Duration) {
	n := 0
	for space, wids := range groups {
		if err := skylight.MoveWindowsToSpace(wids, space); err != nil {
			log.Printf("moving %d window(s) to space %d: %v", len(wids), space, err)
			continue
		}
		n += len(wids)
	}
	if n == 0 {
		return 0, 0
	}
	took, _ := waitFor(stepTimeout, func() bool {
		for space, wids := range groups {
			for _, wid := range wids {
				ids, err := skylight.SpacesForWindow(wid)
				if err != nil || len(ids) != 1 || ids[0] != space {
					return false
				}
			}
		}
		return true
	})
	return n, took
}

// framesAre reports whether the window server shows each window at its
// wanted frame.
func framesAre(want map[uint32]layout.Rect) bool {
	if len(want) == 0 {
		return true
	}
	wins, err := skylight.WindowList()
	if err != nil {
		return false
	}
	seen := 0
	for _, w := range wins {
		f, ok := want[w.Number]
		if !ok {
			continue
		}
		seen++
		if !arrangement.SameFrame(f, layout.Rect{X: w.Bounds.X, Y: w.Bounds.Y, W: w.Bounds.Width, H: w.Bounds.Height}) {
			return false
		}
	}
	return seen == len(want)
}

// waitFor polls cond every 20 ms until it holds or the timeout passes.
func waitFor(timeout time.Duration, cond func() bool) (time.Duration, bool) {
	start := time.Now()
	for {
		if cond() {
			return time.Since(start), true
		}
		if time.Since(start) >= timeout {
			return time.Since(start), false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// bindings are the window-server ids of the windows a look saw.
func bindings(seen []arrangement.Seen) []uint32 {
	out := make([]uint32, len(seen))
	for i, s := range seen {
		out[i] = s.Binding
	}
	return out
}

// displaySet is the arrangement key for the displays present: their UUIDs,
// sorted and comma-joined.
func displaySet(spaces []layout.SavedSpace) string {
	seen := map[string]bool{}
	var ids []string
	for _, sp := range spaces {
		if !seen[sp.DisplayUUID] {
			seen[sp.DisplayUUID] = true
			ids = append(ids, sp.DisplayUUID)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// visibleKeys are the keys of the spaces each display is showing.
func visibleKeys(s *snapshot) map[string]bool {
	out := make(map[string]bool, len(s.current))
	for id := range s.current {
		if key, ok := s.idToKey[id]; ok {
			out[key] = true
		}
	}
	return out
}

// seenWindows are the gathered windows on user desktops; fullscreen
// windows are not placed.
func seenWindows(s *snapshot) []arrangement.Seen {
	out := make([]arrangement.Seen, 0, len(s.windows))
	for _, l := range s.windows {
		if _, fs := s.fsWindow[l.ID]; fs {
			continue
		}
		out = append(out, arrangement.Seen{
			Binding: l.ID, PID: l.OwnerPID, Bundle: l.BundleID, App: l.OwnerName, Title: l.Title,
			Placement: arrangement.Placement{Space: s.idToKey[s.winSpace[l.ID]], Frame: l.Frame},
		})
	}
	return out
}

// logDecisions writes one line per look that did something, and one line
// per window given up on.
func logDecisions(trigger string, ds []arrangement.Decision) {
	var adopted, owed, released, repaired, gaveUp int
	name := func(s arrangement.Seen) string {
		t := s.Title
		if len(t) > 24 {
			t = t[:24]
		}
		return s.App + " | " + t
	}
	var touched []string
	named := map[uint32]bool{}
	touch := func(s arrangement.Seen) {
		if !named[s.Binding] {
			named[s.Binding] = true
			touched = append(touched, name(s))
		}
	}
	for _, d := range ds {
		switch d.Kind {
		case arrangement.Adopt:
			adopted++
		case arrangement.Owe:
			owed++
			touch(d.Seen)
		case arrangement.Release:
			released++
		case arrangement.Repair:
			if _, ok := d.Progress(); ok {
				repaired++
				touch(d.Seen)
			}
		case arrangement.GiveUp:
			gaveUp++
			f := d.Seen.Frame
			log.Printf("released unrepaired: %s at %.0f,%.0f %.0fx%.0f on %.8s", name(d.Seen), f.X, f.Y, f.W, f.H, d.Seen.Space)
		}
	}
	if adopted+owed+released+repaired+gaveUp == 0 {
		return
	}
	line := fmt.Sprintf("look (%s): adopted %d, owed %d, repaired %d, released %d, gave up %d", trigger, adopted, owed, repaired, released, gaveUp)
	if len(touched) > 0 {
		line += ": " + strings.Join(touched, "; ")
	}
	log.Print(line)
}

// --- login convergence ---

func (w *watcher) startBoot() {
	if !w.opt.forceBoot {
		refs, err := listSnapshots()
		if err != nil {
			log.Printf("login convergence skipped: cannot read history: %v", err)
			return
		}
		if !layout.FirstStartOfBoot(layouts(refs), bootTime()) {
			log.Printf("login convergence skipped: this boot already has snapshots")
			return
		}
	}
	l, path, err := resolveSnapshot("", "", false, false, w.opt.settle)
	if err != nil {
		log.Printf("login convergence skipped: %v", err)
		return
	}
	st := l.Stats()
	log.Printf("login convergence from %s (saved %s): %d windows, %s",
		filepath.Base(path), l.SavedAt.Format("2006-01-02 15:04"), st.Windows, displaySummary(st))
	w.boot = newReconciler(l, w.opt.frames, w.opt.fullscreen)
	w.bootStarted, w.bootActivity = time.Now(), time.Now()
	w.bootPassCreate("startup", w.opt.create)
}

func (w *watcher) bootPass(trigger string) { w.bootPassCreate(trigger, false) }

func (w *watcher) bootPassCreate(trigger string, create bool) {
	var done bool
	var st passStats
	var err error
	sysevents.OnMain(func() { done, st, err = w.boot.pass(create) })
	if err != nil {
		log.Printf("login convergence (%s) failed: %v", trigger, err)
		return
	}
	if st.matched > 0 {
		w.bootActivity = time.Now()
		log.Printf("login convergence (%s): +%d matched (%d moved, %d verified, %d in place), %s handled",
			trigger, st.matched, st.moved, st.verified, st.inPlace, w.boot.progress())
	}
	if done {
		log.Printf("login convergence complete (%s)", trigger)
		w.boot = nil
	}
}

func (w *watcher) sweep() {
	if w.boot == nil {
		return
	}
	w.bootPass("sweep")
	if w.boot == nil {
		return
	}
	switch {
	case time.Since(w.bootActivity) > quietExit:
		log.Printf("login convergence: quiet for %s, stopping at %s", quietExit, w.boot.progress())
		w.boot = nil
	case time.Since(w.bootStarted) > w.opt.bootCap:
		log.Printf("login convergence: cap %s reached, stopping at %s", w.opt.bootCap, w.boot.progress())
		w.boot = nil
	}
}
