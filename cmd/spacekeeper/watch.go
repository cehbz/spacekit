package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/settle"
	"github.com/cehbz/spacekit/internal/skylight"
	"github.com/cehbz/spacekit/internal/sysevents"
)

// Timing. A display wake produces two hotplug out/in cycles over ~6s and
// WindowServer's own window-return attempts give up ~7s after the first, so
// displayQuiet after the last callback lands after macOS has finished.
const (
	displayQuiet  = 10 * time.Second
	spaceQuiet    = 3 * time.Second
	launchDelay   = 1500 * time.Millisecond
	sweepInterval = 15 * time.Second
	// quietExit ends login convergence once nothing has happened for this
	// long (no launch, no new match): the login storm is over. The cap is a
	// backstop, not a tuning knob.
	quietExit = 2 * time.Minute
)

type watchOptions struct {
	interval, settle, bootCap  time.Duration
	forceBoot                  bool
	keep                       int
	frames, create, fullscreen bool
}

// watcher is the resident agent: it saves on a timer and on sleep and
// space-change events, restores after a transient display drop, and runs
// the login convergence. All SkyLight work runs on the main thread via
// sysevents.OnMain; this loop only decides.
type watcher struct {
	opt      watchOptions
	displays *settle.Window // an open display-reconfiguration burst
	spaces   *settle.Window // debounce for active-space changes

	boot                      *reconciler // non-nil while login convergence runs
	bootStarted, bootActivity time.Time

	// debts are windows a display drop left at the wrong size, repaired as
	// their spaces become active (layout.FrameDebt).
	debts []layout.FrameDebt

	// poweringOff is set by the power-off notification: the layout was saved
	// at that moment and every later save is held, so the half-quit state of
	// a logout in progress never enters history.
	poweringOff bool
}

func watchCmd(o watchOptions) error {
	log.SetFlags(log.Ldate | log.Ltime)
	w := &watcher{opt: o, displays: settle.New(displayQuiet), spaces: settle.New(spaceQuiet)}
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

	w.recoverPending()
	w.startBoot()
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
			w.save("interval")
		case <-sweep.C:
			w.sweep()
		case <-settled:
			w.displaysSettled()
		case <-spaceQuietC:
			w.spaces.Close()
			if len(w.debts) > 0 {
				sysevents.OnMain(func() {
					if s, err := gather(); err == nil {
						w.payDebts(s)
					}
				})
			}
			w.save("space change")
		}
	}
}

func (w *watcher) handle(e sysevents.Event) {
	switch e.Kind {
	case sysevents.DisplayReconfigured:
		if w.displays.Note(e.At) {
			log.Printf("display reconfiguration began (display %d, flags %#x); holding saves", e.Display, e.Flags)
			w.writePending(e.At)
		}
	case sysevents.ScreensSleep, sysevents.SystemWillSleep:
		w.save(e.Kind.String())
	case sysevents.WillPowerOff:
		w.save(e.Kind.String())
		w.poweringOff = true
		log.Printf("holding saves until exit")
	case sysevents.SpaceChanged:
		w.spaces.Note(e.At)
	case sysevents.AppLaunched:
		if w.boot != nil {
			time.Sleep(launchDelay) // let the app map its windows
			w.bootPass(e.Name + " launched")
		}
	case sysevents.ScreensWake, sysevents.SystemWake:
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

// --- transient display drop ---

// pendingPath records an open display burst so a restarted watcher can still
// finish it (crash recovery). Removed when the burst is handled.
func pendingPath() string { return filepath.Join(dataDir(), "reconfig-pending") }

func (w *watcher) writePending(at time.Time) {
	if err := os.WriteFile(pendingPath(), []byte(at.Format(time.RFC3339Nano)), 0o600); err != nil {
		log.Printf("could not record pending reconfiguration: %v", err)
	}
}

func (w *watcher) recoverPending() {
	data, err := os.ReadFile(pendingPath())
	if err != nil {
		return
	}
	start, err := time.Parse(time.RFC3339Nano, string(data))
	if err != nil {
		os.Remove(pendingPath())
		return
	}
	w.displays.Resume(start, time.Now())
	log.Printf("resuming display reconfiguration that began %s", start.Format("15:04:05"))
}

func (w *watcher) displaysSettled() {
	start := w.displays.Start()
	w.displays.Close()
	os.Remove(pendingPath())
	if w.boot != nil {
		w.bootPass("displays settled")
		return
	}
	refs, err := listSnapshots()
	if err != nil {
		log.Printf("displays settled; cannot read history: %v", err)
		return
	}
	sysevents.OnMain(func() {
		s, err := gather()
		if err != nil {
			log.Printf("displays settled; gather failed: %v", err)
			return
		}
		now := layout.Layout{Spaces: s.spaces}.Stats()
		i := layout.ReferenceFor(layouts(refs), start, bootTime(), now)
		if i < 0 {
			log.Printf("displays settled to %s; no snapshot with that display set this boot; leaving windows alone", displaySummary(now))
			return
		}
		ref := refs[i]
		r := newReconciler(ref.l, w.opt.frames, w.opt.fullscreen)
		_, st, err := r.passOn(s)
		if err != nil {
			log.Printf("displays settled; restore from %s failed: %v", filepath.Base(ref.path), err)
			return
		}
		log.Printf("displays settled; restored from %s: matched %d, moved %d (%d verified), %d in place",
			filepath.Base(ref.path), st.matched, st.moved, st.verified, st.inPlace)
		w.debts = layout.FrameDebts(ref.l.Windows, layout.Match(ref.l.Windows, s.windows), s.windows)
		if len(w.debts) > 0 {
			log.Printf("frame debt: %d window(s) at the wrong size: %s", len(w.debts), debtNames(w.debts, s.windows))
			if moved, err := gather(); err == nil {
				w.payDebts(moved) // spaces just changed under the moved windows
			}
		}
	})
	w.save("after reconfiguration")
}

// debtNames lists the apps and titles behind a set of debts, for the log.
func debtNames(debts []layout.FrameDebt, live []layout.LiveWindow) string {
	byID := make(map[uint32]layout.LiveWindow, len(live))
	for _, l := range live {
		byID[l.ID] = l
	}
	names := make([]string, 0, len(debts))
	for _, d := range debts {
		l := byID[d.ID]
		t := l.Title
		if len(t) > 24 {
			t = t[:24]
		}
		names = append(names, l.OwnerName+" | "+t)
	}
	return strings.Join(names, "; ")
}

// payDebts repairs the frames of debts whose windows are on an active space
// and unchanged since the debt was recorded, and forgets debts whose windows
// are gone or were changed by the user. Runs on the main thread.
func (w *watcher) payDebts(s *snapshot) {
	byID := make(map[uint32]*layout.LiveWindow, len(s.windows))
	for i := range s.windows {
		byID[s.windows[i].ID] = &s.windows[i]
	}
	var keep, attempted []layout.FrameDebt
	dropped := 0
	for _, d := range w.debts {
		pay, drop := d.Settle(byID[d.ID], s.current, s.winSpace)
		switch {
		case drop:
			dropped++
		case pay:
			if err := skylight.SetWindowFrame(d.PID, d.ID, d.Want.X, d.Want.Y, d.Want.W, d.Want.H); err != nil {
				keep = append(keep, d)
				continue
			}
			attempted = append(attempted, d)
		default:
			keep = append(keep, d)
		}
	}
	// An AX write can return success without taking effect, so a payment
	// counts only when the window server shows the wanted frame. A partial
	// result keeps the debt with the new frame as its baseline.
	paid := 0
	if len(attempted) > 0 {
		time.Sleep(300 * time.Millisecond) // AX resizes apply asynchronously
		if after, err := gather(); err == nil {
			now := make(map[uint32]layout.Rect, len(after.windows))
			for _, l := range after.windows {
				now[l.ID] = l.Frame
			}
			for _, d := range attempted {
				if now[d.ID] == d.Want {
					paid++
				} else {
					d.Seen = now[d.ID]
					keep = append(keep, d)
				}
			}
		} else {
			keep = append(keep, attempted...)
		}
	}
	w.debts = keep
	if paid > 0 || dropped > 0 || len(attempted) > 0 {
		log.Printf("frame debt: paid %d, dropped %d, %d outstanding", paid, dropped, len(keep))
	}
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
