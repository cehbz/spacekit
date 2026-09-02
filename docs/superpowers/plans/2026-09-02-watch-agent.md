# spacekeeper watch agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the interval save agent and the login restore agent with one resident `spacekeeper watch` agent that saves on a timer and on sleep/space events, restores displaced windows after a transient display drop (the wake-from-sleep Chrome displacement), and runs the login convergence.

**Architecture:** A cgo-free reconcile engine (`reconciler`) drives repeated matching passes of one saved layout; `internal/sysevents` turns NSWorkspace and CoreGraphics display notifications into one Go channel and runs SkyLight work on the main thread; `internal/settle` tracks event bursts. `watch.go` is a single-goroutine state machine over those. A display reconfiguration burst holds saves; when it settles and the display set equals the newest pre-burst snapshot's, one restore pass runs from that snapshot.

**Tech Stack:** Go 1.26 + cgo/ObjC (AppKit, CoreGraphics), private SkyLight via existing `internal/skylight`, launchd agent.

**Spec:** The design discussion in this session (2026-09-02). Evidence: every display wake produces a Dell hotplug out/in cycle (WindowServer `Processing hotplug`); WindowServer's `PKGWindowMoveOnMatchingDisplayChangedSeed ... likely misplaced` verdicts for Chrome land ~7 s after the first callback; displaced windows end up on the built-in display's space; `restore -from <pre-event snapshot>` fixes space and frame (verified 4/4 moved).

## Global Constraints

- Work in place on branch `watch-agent` (no worktree: `signing.local.mk` is gitignored and `make install` needs it).
- Every commit message is listed below verbatim; nothing else is committed. Do not run `git commit` for anything not on the list.
- `go test ./...` must pass at every commit. `go vet ./...` clean.
- No new TCC grants: only public NSWorkspace/CoreGraphics notification APIs are added. All SkyLight/AX calls in the watcher run on the main thread via `sysevents.OnMain`.
- No AI-isms in prose or comments ("footgun", "leverage", etc.). Match the existing comment voice.
- Constants: interval save 3m; display-burst quiet 10s; space-change save debounce 3s; boot convergence: launch delay 1.5s, sweep 15s, quiet-exit 2m, cap 10m; boot detection = uptime < `-settled` (10m).
- Logs go under `~/Library/Logs/spacekeeper/`, never `/tmp`.

---

### Task 1: layout — LatestBefore and Stats.SameDisplays

**Files:**
- Modify: `internal/layout/layout.go` (after `DefaultRestoreIndex`, ~line 119)
- Test: `internal/layout/history_test.go`

**Interfaces:**
- Produces: `func LatestBefore(ls []Layout, t time.Time) int` (index of newest layout with `SavedAt` strictly before `t`, -1 if none); `func (s Stats) SameDisplays(o Stats) bool` (same display UUIDs with same desktop counts, order-independent).

- [ ] **Step 1: Write the failing tests** (append to `history_test.go`)

```go
func TestLatestBeforePicksNewestStrictlyBefore(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 46, 0, 0, time.UTC)
	ls := []Layout{
		{SavedAt: base.Add(5 * time.Minute)},
		{SavedAt: base.Add(-1 * time.Minute)},
		{SavedAt: base.Add(-9 * time.Minute)},
	}
	if got := LatestBefore(ls, base); got != 1 {
		t.Fatalf("LatestBefore = %d, want 1", got)
	}
}

func TestLatestBeforeExcludesEqualAndEmpty(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 46, 0, 0, time.UTC)
	if got := LatestBefore([]Layout{{SavedAt: base}}, base); got != -1 {
		t.Fatalf("equal timestamp: got %d, want -1", got)
	}
	if got := LatestBefore(nil, base); got != -1 {
		t.Fatalf("empty: got %d, want -1", got)
	}
}

func TestSameDisplaysIgnoresOrderAndCatchesChanges(t *testing.T) {
	a := Stats{Displays: []DisplaySpaces{{"D1", 5}, {"D2", 1}}}
	b := Stats{Displays: []DisplaySpaces{{"D2", 1}, {"D1", 5}}}
	fewer := Stats{Displays: []DisplaySpaces{{"D1", 5}}}
	count := Stats{Displays: []DisplaySpaces{{"D1", 4}, {"D2", 1}}}
	if !a.SameDisplays(b) {
		t.Fatal("order should not matter")
	}
	if a.SameDisplays(fewer) {
		t.Fatal("missing display should differ")
	}
	if a.SameDisplays(count) {
		t.Fatal("desktop count change should differ")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/layout/ -run 'LatestBefore|SameDisplays' -v`
Expected: compile error "undefined: LatestBefore" — add stubs returning `-1` / `false` so the tests compile, rerun, and see assertion failures.

- [ ] **Step 3: Implement**

```go
// LatestBefore returns the index of the newest layout saved strictly before
// t, or -1. Used to pick the reference for a post-reconfiguration restore:
// the last snapshot from before the displays started changing.
func LatestBefore(ls []Layout, t time.Time) int {
	best := -1
	for i, l := range ls {
		if !l.SavedAt.Before(t) {
			continue
		}
		if best == -1 || l.SavedAt.After(ls[best].SavedAt) {
			best = i
		}
	}
	return best
}

// SameDisplays reports whether both stats describe the same displays with the
// same number of desktops each, regardless of order. A transient display drop
// ends in the same topology it started from; anything else is not one.
func (s Stats) SameDisplays(o Stats) bool {
	if len(s.Displays) != len(o.Displays) {
		return false
	}
	counts := make(map[string]int, len(s.Displays))
	for _, d := range s.Displays {
		counts[d.DisplayUUID] = d.Spaces
	}
	for _, d := range o.Displays {
		if n, ok := counts[d.DisplayUUID]; !ok || n != d.Spaces {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/layout/ -v`
Expected: PASS (all, including existing).

- [ ] **Step 5: Commit**

```bash
git add internal/layout/layout.go internal/layout/history_test.go
git commit -m "layout: LatestBefore and Stats.SameDisplays for reconfiguration restores"
```

---

### Task 2: settle — quiet-window tracker for event bursts

**Files:**
- Create: `internal/settle/settle.go`
- Test: `internal/settle/settle_test.go`

**Interfaces:**
- Produces:
  - `func New(quiet time.Duration) *Window`
  - `func (w *Window) Note(now time.Time) bool` — records an event; true if it opened a burst
  - `func (w *Window) Resume(start, now time.Time)` — reopen a burst that began at `start` (crash recovery)
  - `func (w *Window) Open() bool`
  - `func (w *Window) Start() time.Time` — first event of the open burst
  - `func (w *Window) Deadline() time.Time` — last event + quiet; zero when closed
  - `func (w *Window) Close()`

- [ ] **Step 1: Write the failing tests**

```go
package settle

import (
	"testing"
	"time"
)

func TestNoteOpensThenExtends(t *testing.T) {
	t0 := time.Date(2026, 9, 2, 10, 46, 7, 0, time.UTC)
	w := New(10 * time.Second)
	if w.Open() {
		t.Fatal("new window should be closed")
	}
	if !w.Note(t0) {
		t.Fatal("first Note should open the burst")
	}
	if w.Note(t0.Add(6 * time.Second)) {
		t.Fatal("second Note should not report a new burst")
	}
	if got := w.Start(); !got.Equal(t0) {
		t.Fatalf("Start = %v, want %v", got, t0)
	}
	if got, want := w.Deadline(), t0.Add(16*time.Second); !got.Equal(want) {
		t.Fatalf("Deadline = %v, want %v", got, want)
	}
}

func TestCloseResets(t *testing.T) {
	t0 := time.Date(2026, 9, 2, 10, 46, 7, 0, time.UTC)
	w := New(10 * time.Second)
	w.Note(t0)
	w.Close()
	if w.Open() || !w.Deadline().IsZero() {
		t.Fatal("Close should leave the window closed with no deadline")
	}
	if !w.Note(t0.Add(time.Minute)) {
		t.Fatal("Note after Close should open a new burst")
	}
}

func TestResumeKeepsStartAndDefersDeadline(t *testing.T) {
	start := time.Date(2026, 9, 2, 10, 46, 7, 0, time.UTC)
	now := start.Add(30 * time.Second)
	w := New(10 * time.Second)
	w.Resume(start, now)
	if !w.Open() || !w.Start().Equal(start) {
		t.Fatal("Resume should open a burst at the given start")
	}
	if got, want := w.Deadline(), now.Add(10*time.Second); !got.Equal(want) {
		t.Fatalf("Deadline = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/settle/ -v`
Expected: compile error; create the file with stub methods (`Note` returns false, `Open` false, zero times), rerun, assertion failures.

- [ ] **Step 3: Implement**

```go
// Package settle tracks a burst of related events (display reconfiguration
// callbacks, space switches) and reports when the burst has been quiet for
// long enough to act on.
package settle

import "time"

type Window struct {
	quiet       time.Duration
	start, last time.Time
}

func New(quiet time.Duration) *Window { return &Window{quiet: quiet} }

// Note records an event at now. It returns true when this event opened a
// new burst; later events only extend the deadline.
func (w *Window) Note(now time.Time) bool {
	opened := w.start.IsZero()
	if opened {
		w.start = now
	}
	w.last = now
	return opened
}

// Resume reopens a burst that began at start, with the deadline counted from
// now. Used after a restart, when the burst's start was recovered from disk.
func (w *Window) Resume(start, now time.Time) {
	w.start, w.last = start, now
}

func (w *Window) Open() bool { return !w.start.IsZero() }

// Start is the first event of the open burst; zero when closed.
func (w *Window) Start() time.Time { return w.start }

// Deadline is when the open burst counts as settled: the last event plus
// the quiet period. Zero when closed.
func (w *Window) Deadline() time.Time {
	if w.last.IsZero() {
		return time.Time{}
	}
	return w.last.Add(w.quiet)
}

func (w *Window) Close() { w.start, w.last = time.Time{}, time.Time{} }
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/settle/ -v` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/settle/
git commit -m "settle: quiet-window tracker for event bursts"
```

---

### Task 3: sysevents — notifications channel and main-thread dispatch

**Files:**
- Create: `internal/sysevents/sysevents.go`, `internal/sysevents/sysevents.m`

**Interfaces:**
- Produces: `type Kind int` with constants `AppLaunched, DisplayReconfigured, ScreensSleep, ScreensWake, SystemWillSleep, SystemWake, SpaceChanged` (in that order, matching the C enum); `type Event struct{ Kind Kind; At time.Time; Name string; Display uint32; Flags uint32 }`; `func Events() <-chan Event`; `func Start()`; `func Run()` (blocks on main thread); `func Stop()`; `func OnMain(fn func())` (runs fn on the main run loop and waits; must be called from a goroutine, not the thread running `Run`).

No unit test: this package is cgo/ObjC glue. Verified in Task 5's manual check.

- [ ] **Step 1: Write `sysevents.m`**

```objc
#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <CoreGraphics/CoreGraphics.h>
#include <stdint.h>

extern void syseventsEmit(int kind, char *name, uint32_t display, uint32_t flags);
extern void syseventsMainCall(uintptr_t handle);

// Must match the Kind constants in sysevents.go.
enum {
	kAppLaunched,
	kDisplayReconfigured,
	kScreensSleep,
	kScreensWake,
	kSystemWillSleep,
	kSystemWake,
	kSpaceChanged,
};

static void observe(NSNotificationName name, int kind) {
	[[[NSWorkspace sharedWorkspace] notificationCenter]
		addObserverForName:name
		            object:nil
		             queue:nil
		        usingBlock:^(NSNotification *note) {
		const char *app = "";
		if (kind == kAppLaunched) {
			NSRunningApplication *a = note.userInfo[NSWorkspaceApplicationKey];
			if (a.localizedName) app = a.localizedName.UTF8String;
		}
		syseventsEmit(kind, (char *)app, 0, 0);
	}];
}

static void displayChanged(CGDirectDisplayID display, CGDisplayChangeSummaryFlags flags, void *userInfo) {
	syseventsEmit(kDisplayReconfigured, "", display, (uint32_t)flags);
}

void sysevents_start(void) {
	observe(NSWorkspaceDidLaunchApplicationNotification, kAppLaunched);
	observe(NSWorkspaceScreensDidSleepNotification, kScreensSleep);
	observe(NSWorkspaceScreensDidWakeNotification, kScreensWake);
	observe(NSWorkspaceWillSleepNotification, kSystemWillSleep);
	observe(NSWorkspaceDidWakeNotification, kSystemWake);
	observe(NSWorkspaceActiveSpaceDidChangeNotification, kSpaceChanged);
	CGDisplayRegisterReconfigurationCallback(displayChanged, NULL);
}

void sysevents_run(void)  { CFRunLoopRun(); }
void sysevents_stop(void) { CFRunLoopStop(CFRunLoopGetMain()); }

// Runs the Go function behind handle on the main run loop.
void sysevents_on_main(uintptr_t handle) {
	CFRunLoopPerformBlock(CFRunLoopGetMain(), kCFRunLoopCommonModes, ^{
		syseventsMainCall(handle);
	});
	CFRunLoopWakeUp(CFRunLoopGetMain());
}
```

- [ ] **Step 2: Write `sysevents.go`**

```go
// Package sysevents surfaces the macOS notifications the watcher acts on as
// one Go channel: app launches, display reconfiguration, screen and system
// sleep/wake, and active-space changes. Delivery needs a run loop on the main
// OS thread: call Run there; it blocks until Stop. OnMain runs a function on
// that thread, which is where SkyLight and Accessibility work belongs.
package sysevents

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AppKit -framework CoreGraphics
#include <stdint.h>
void sysevents_start(void);
void sysevents_run(void);
void sysevents_stop(void);
void sysevents_on_main(uintptr_t handle);
*/
import "C"

import (
	"runtime/cgo"
	"time"
)

type Kind int

// Order must match the enum in sysevents.m.
const (
	AppLaunched Kind = iota
	DisplayReconfigured
	ScreensSleep
	ScreensWake
	SystemWillSleep
	SystemWake
	SpaceChanged
)

func (k Kind) String() string {
	switch k {
	case AppLaunched:
		return "app launched"
	case DisplayReconfigured:
		return "display reconfigured"
	case ScreensSleep:
		return "screens sleep"
	case ScreensWake:
		return "screens wake"
	case SystemWillSleep:
		return "system will sleep"
	case SystemWake:
		return "system wake"
	case SpaceChanged:
		return "space changed"
	}
	return "unknown"
}

type Event struct {
	Kind Kind
	At   time.Time
	Name string // launched app's name; empty for other kinds
	// Display and Flags carry CGDisplayRegisterReconfigurationCallback's
	// arguments for DisplayReconfigured events, for logging.
	Display uint32
	Flags   uint32
}

var events = make(chan Event, 64)

//export syseventsEmit
func syseventsEmit(kind C.int, name *C.char, display C.uint32_t, flags C.uint32_t) {
	e := Event{Kind: Kind(kind), At: time.Now(), Name: C.GoString(name),
		Display: uint32(display), Flags: uint32(flags)}
	select {
	case events <- e:
	default: // consumers re-gather everything; dropping an event is fine
	}
}

//export syseventsMainCall
func syseventsMainCall(h C.uintptr_t) {
	handle := cgo.Handle(h)
	handle.Value().(func())()
	handle.Delete()
}

// Events returns the stream of notifications.
func Events() <-chan Event { return events }

// Start registers the observers. Call before Run.
func Start() { C.sysevents_start() }

// Run pumps the main run loop until Stop. Must run on the main OS thread or
// NSWorkspace notifications are not delivered.
func Run() { C.sysevents_run() }

// Stop ends Run. Safe from any goroutine.
func Stop() { C.sysevents_stop() }

// OnMain runs fn on the main thread's run loop and waits for it to finish.
// Call it from a goroutine while Run is pumping; calling it from the main
// thread itself would deadlock.
func OnMain(fn func()) {
	done := make(chan struct{})
	h := cgo.NewHandle(func() {
		fn()
		close(done)
	})
	C.sysevents_on_main(C.uintptr_t(h))
	<-done
}
```

- [ ] **Step 3: Build and vet**

Run: `go build ./... && go vet ./internal/sysevents/`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add internal/sysevents/
git commit -m "sysevents: display, sleep/wake, and space notifications with main-thread dispatch"
```

---

### Task 4: reconciler extraction; converge restore on sysevents; drop appwatch

**Files:**
- Create: `cmd/spacekeeper/reconcile.go`
- Modify: `cmd/spacekeeper/main.go` (`convergeRestore`, ~lines 493-583; import of appwatch)
- Delete: `internal/appwatch/appwatch.go`

**Interfaces:**
- Consumes: `sysevents.Events/Start/Run/Stop`, existing `gather`, `createMissingSpaces`, `applyPass`, `passStats`.
- Produces:
  - `type reconciler struct` with `newReconciler(l layout.Layout, frames, fullscreen bool) *reconciler`
  - `func (r *reconciler) pass(create bool) (bool, passStats, error)` — gather, optionally create missing desktops, act on new matches; returns done
  - `func (r *reconciler) passOn(s *snapshot) (bool, passStats, error)` — same on an already gathered snapshot
  - `func (r *reconciler) done() bool`, `func (r *reconciler) progress() string` ("handled/total")

- [ ] **Step 1: Create `reconcile.go`**

```go
package main

import (
	"fmt"

	"github.com/cehbz/spacekit/internal/layout"
)

// reconciler drives repeated matching passes of one saved layout against the
// live session. Each saved window is acted on at most once across passes, so
// convergence never fights the user's own rearranging (see applyPass).
type reconciler struct {
	layout            layout.Layout
	handled           map[int]bool
	frames, fullscreen bool
}

func newReconciler(l layout.Layout, frames, fullscreen bool) *reconciler {
	return &reconciler{layout: l, handled: make(map[int]bool), frames: frames, fullscreen: fullscreen}
}

// pass gathers the live session, optionally recreates missing desktops, and
// acts on windows matched for the first time. It reports whether every saved
// window has now been handled.
func (r *reconciler) pass(create bool) (bool, passStats, error) {
	s, err := gather()
	if err != nil {
		return false, passStats{}, err
	}
	if create {
		if s, err = createMissingSpaces(r.layout, s, false); err != nil {
			return false, passStats{}, err
		}
	}
	return r.passOn(s)
}

// passOn is pass over an already gathered snapshot.
func (r *reconciler) passOn(s *snapshot) (bool, passStats, error) {
	st, err := applyPass(r.layout, s, r.handled, r.frames, r.fullscreen)
	if err != nil {
		return false, st, err
	}
	return r.done(), st, nil
}

func (r *reconciler) done() bool { return len(r.handled) == len(r.layout.Windows) }

func (r *reconciler) progress() string {
	return fmt.Sprintf("%d/%d", len(r.handled), len(r.layout.Windows))
}
```

- [ ] **Step 2: Rewrite `convergeRestore` in `main.go` on the reconciler and sysevents**

Replace the whole `convergeRestore` function body with:

```go
func convergeRestore(l layout.Layout, frames, create, fullscreen bool, window time.Duration) error {
	r := newReconciler(l, frames, fullscreen)
	pass := func(create bool) (bool, error) {
		done, st, err := r.pass(create)
		if err != nil {
			return false, err
		}
		if st.matched > 0 {
			fmt.Printf("converge: +%d matched (%d moved, %d in place), %s total\n",
				st.matched, st.moved, st.inPlace, r.progress())
		}
		return done, nil
	}

	done, err := pass(create)
	if err != nil || done {
		fmt.Printf("converged: %s saved windows handled\n", r.progress())
		return err
	}

	sysevents.Start()
	finished := make(chan error, 1)
	go func() {
		defer sysevents.Stop()
		deadline := time.After(window)
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		lastActivity := time.Now()
		for {
			select {
			case e := <-sysevents.Events():
				if e.Kind != sysevents.AppLaunched {
					continue
				}
				lastActivity = time.Now()
				time.Sleep(1500 * time.Millisecond) // let the app map its windows
				before := len(r.handled)
				done, err := pass(false)
				if len(r.handled) > before {
					lastActivity = time.Now()
				}
				if err != nil || done {
					if e.Name != "" && done {
						fmt.Printf("converge: complete after %s launched\n", e.Name)
					}
					finished <- err
					return
				}
			case <-tick.C:
				before := len(r.handled)
				done, err := pass(false)
				if len(r.handled) > before {
					lastActivity = time.Now()
				}
				if err != nil || done {
					finished <- err
					return
				}
				if time.Since(lastActivity) > quietExit {
					fmt.Printf("converge: quiet for %s, stopping\n", quietExit)
					finished <- nil
					return
				}
			case <-deadline:
				finished <- nil
				return
			}
		}
	}()
	sysevents.Run() // blocks the main thread pumping AppKit notifications
	err = <-finished
	fmt.Printf("converged: %s saved windows handled\n", r.progress())
	return err
}
```

Change the import `"github.com/cehbz/spacekit/internal/appwatch"` to `"github.com/cehbz/spacekit/internal/sysevents"`. Delete `internal/appwatch/`.

- [ ] **Step 3: Build, vet, test, and smoke-test**

Run: `go build ./... && go vet ./... && go test ./...` → clean/PASS.
Run: `go run ./cmd/spacekeeper restore -latest -n` → prints a plan (dry run unchanged).

- [ ] **Step 4: Commit**

```bash
git add cmd/spacekeeper/reconcile.go cmd/spacekeeper/main.go
git rm -r internal/appwatch
git commit -m "spacekeeper: extract reconciler; converge restore on sysevents; drop appwatch"
```

---

### Task 5: `spacekeeper watch`

**Files:**
- Create: `cmd/spacekeeper/watch.go`
- Modify: `cmd/spacekeeper/main.go` — usage text, flags `-interval`, `-boot`, `-boot-cap`, `watch` case; refactor `saveCmd` into `saveSnapshot` + `saveCmd`.

**Interfaces:**
- Consumes: `reconciler`, `sysevents`, `settle.Window`, `layout.LatestBefore`, `Stats.SameDisplays`, `listSnapshots`, `resolveSnapshot`, `gather`, `applyPass`, `bootTime`, `dataDir`.
- Produces: `func saveSnapshot(keep int) (path string, err error)` (empty path = unchanged, nothing written); `func watchCmd(o watchOptions) error`.

- [ ] **Step 1: Refactor save in `main.go`**

Replace `saveCmd` with:

```go
// saveSnapshot writes the current layout into history unless it is identical
// to the newest snapshot. It returns the written path, or "" when unchanged.
func saveSnapshot(keep int) (string, error) {
	s, err := gather()
	if err != nil {
		return "", err
	}
	l := buildLayout(s)
	refs, _ := listSnapshots()
	if n := newestSnap(refs); n != nil && n.l.Signature() == l.Signature() {
		return "", nil
	}
	path := filepath.Join(snapshotsDir(), "layout-"+l.SavedAt.Format("20060102-150405")+".json")
	if err := writeLayout(path, l); err != nil {
		return "", err
	}
	pruneSnapshots(keep)
	return path, nil
}

func saveCmd(explicit string, keep int) error {
	if !skylight.ScreenRecordingGranted() {
		skylight.RequestScreenRecording() // register the binary; silent, not narrated
		fmt.Fprintln(os.Stderr, `note: Screen Recording is unavailable to this process; `+
			`window titles are limited to the active space, weakening cross-space matching. `+
			`See the README "Screen Recording" section.`)
	}
	if explicit != "" {
		s, err := gather()
		if err != nil {
			return err
		}
		l := buildLayout(s)
		if err := writeLayout(explicit, l); err != nil {
			return err
		}
		fmt.Printf("saved %d windows to %s\n", len(l.Windows), explicit)
		return nil
	}
	path, err := saveSnapshot(keep)
	if err != nil {
		return err
	}
	if path == "" {
		fmt.Println("unchanged since the last snapshot; nothing saved")
		return nil
	}
	l, err := loadLayout(path)
	if err != nil {
		return err
	}
	st := l.Stats()
	fmt.Printf("snapshot %s: %d windows, %s\n", filepath.Base(path), st.Windows, displaySummary(st))
	return nil
}
```

(The "pruned N old snapshot(s)" line goes away; pruning is silent. `list` shows what is retained.)

- [ ] **Step 2: Add flags and the `watch` case in `main`**

Usage text, add after the `show` line and the flag list:

```
  watch     run resident: periodic and event-driven saves, restore after a
            transient display drop, and login convergence (the launchd agent)
...
  -interval D  watch: periodic save interval (default 3m)
  -boot        watch: run login convergence even if uptime exceeds -settled
  -boot-cap D  watch: hard cap on login convergence (default 10m)
```

Flags:

```go
interval := fs.Duration("interval", 3*time.Minute, "watch: periodic save interval")
forceBoot := fs.Bool("boot", false, "watch: run login convergence regardless of uptime")
bootCap := fs.Duration("boot-cap", 10*time.Minute, "watch: hard cap on login convergence")
```

Case:

```go
case "watch":
	err = watchCmd(watchOptions{
		interval: *interval, settle: *settled, bootCap: *bootCap, forceBoot: *forceBoot,
		keep: *keep, frames: *frames, create: *create, fullscreen: *fullscreen,
	})
```

- [ ] **Step 3: Write `watch.go`**

```go
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/settle"
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
	if w.displays.Open() {
		log.Printf("save (%s) held: display reconfiguration in progress", reason)
		return
	}
	var path string
	var err error
	sysevents.OnMain(func() { path, err = saveSnapshot(w.opt.keep) })
	switch {
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
	ls := make([]layout.Layout, len(refs))
	for i, r := range refs {
		ls[i] = r.l
	}
	i := layout.LatestBefore(ls, start)
	if i < 0 {
		log.Printf("displays settled; no snapshot predates the reconfiguration")
		return
	}
	ref := refs[i]
	sysevents.OnMain(func() {
		s, err := gather()
		if err != nil {
			log.Printf("displays settled; gather failed: %v", err)
			return
		}
		now := layout.Layout{Spaces: s.spaces}.Stats()
		if !now.SameDisplays(ref.l.Stats()) {
			log.Printf("displays settled to %s; %s had %s; not a transient drop, leaving windows alone",
				displaySummary(now), filepath.Base(ref.path), displaySummary(ref.l.Stats()))
			return
		}
		r := newReconciler(ref.l, w.opt.frames, w.opt.fullscreen)
		_, st, err := r.passOn(s)
		if err != nil {
			log.Printf("displays settled; restore from %s failed: %v", filepath.Base(ref.path), err)
			return
		}
		log.Printf("displays settled; restored from %s: matched %d, moved %d (%d verified), %d in place",
			filepath.Base(ref.path), st.matched, st.moved, st.verified, st.inPlace)
	})
	w.save("after reconfiguration")
}

// --- login convergence ---

func (w *watcher) startBoot() {
	boot := bootTime()
	if !w.opt.forceBoot && (boot.IsZero() || time.Since(boot) > w.opt.settle) {
		return
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

var _ = fmt.Sprintf // keep fmt available for future summaries
```

Remove the final `var _ = fmt.Sprintf` line and the `fmt` import if `fmt` ends up unused (it is unused as written; drop both).

- [ ] **Step 4: Build, vet, test**

Run: `go build ./... && go vet ./... && go test ./...` → clean/PASS.

- [ ] **Step 5: Manual verification (record output in the commit body if useful)**

1. `go build -o spacekeeper ./cmd/spacekeeper && ./spacekeeper watch -interval 30s` in a terminal.
2. Expect a `snapshot ... (startup)` line or nothing (unchanged), no login convergence (uptime > 10m).
3. Switch spaces with the tilt wheel: within ~3s expect `snapshot ... (space change)` if the layout changed, else silence.
4. Run `pmset displaysleepnow`, wait ~10s, wake with the keyboard. Expect: `display reconfiguration began ...`, `screens wake`, and ~16s after the first callback either `displays settled; restored from layout-...: matched N, moved 0 ...` (nothing displaced) or a non-zero `moved` count if Chrome windows were displaced. The `reconfig-pending` file must not remain under `~/.config/spacekeeper/`.
5. Ctrl-C. Then `./spacekeeper watch -boot -interval 30s` and expect `login convergence from layout-...`. Check which snapshot it names before letting it run: on a machine whose previous-boot snapshots have been pruned (this one, until Task 7 has been in place across a reboot), the default pick is the weeks-old high-water snapshot and the pass will move windows to that layout. If so, Ctrl-C at once and put things back with `restore -from <newest pre-test snapshot>`. Otherwise expect `login convergence complete (startup)`. Ctrl-C.

- [ ] **Step 6: Commit**

```bash
git add cmd/spacekeeper/watch.go cmd/spacekeeper/main.go
git commit -m "spacekeeper watch: resident agent with periodic/event saves, wake reconcile, boot convergence"
```

---

### Task 6: single launchd agent; drop `restore -converge`

**Files:**
- Create: `dist/bz.ceh.spacekeeper.plist`
- Delete: `dist/bz.ceh.spacekeeper-save.plist`, `dist/bz.ceh.spacekeeper-restore.plist`
- Modify: `Makefile` (agent targets, `install` kickstart), `cmd/spacekeeper/main.go` (remove `-converge` flag, `convergeRestore`, `quietExit` stays for watch, usage text)

- [ ] **Step 1: Write `dist/bz.ceh.spacekeeper.plist`**

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!--
  The spacekeeper agent: `spacekeeper watch`, resident for the whole session.

  It snapshots the layout every 3 minutes and on screen/system sleep and
  space changes; after a transient display drop (a monitor that de-registers
  for a second at wake, which scatters windows some apps fail to bring back)
  it restores from the last pre-drop snapshot; and at login it restores the
  layout as of the last shutdown, converging as apps reopen.

  Install/remove via `make agent-install` / `make agent-uninstall`.
  Grant Accessibility and Screen Recording to spacekeeper.
  Log: ~/Library/Logs/spacekeeper/watch.log
-->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>bz.ceh.spacekeeper</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/sh</string>
		<string>-c</string>
		<string>mkdir -p "$HOME/Library/Logs/spacekeeper" &amp;&amp; exec "$HOME/bin/spacekeeper" watch >>"$HOME/Library/Logs/spacekeeper/watch.log" 2>&amp;1</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
```

- [ ] **Step 2: Makefile**

Replace the four agent targets and the `.PHONY` list with:

```make
.PHONY: all build sign install test clean agent-install agent-uninstall

...

install: sign
	mkdir -p $(PREFIX)
	for b in $(BINS); do cp $$b $(PREFIX)/$$b; done
	@# Restart the daemons if loaded, so the running binaries match what was granted.
	@launchctl kickstart -k gui/$$(id -u)/bz.ceh.spaceswitch 2>/dev/null && echo "restarted spaceswitch daemon" || true
	@launchctl kickstart -k gui/$$(id -u)/$(AGENT) 2>/dev/null && echo "restarted spacekeeper agent" || true
	@echo "installed to $(PREFIX)"

# The spacekeeper agent (`spacekeeper watch`): periodic and event-driven
# snapshots, restore after a transient display drop, login convergence.
# Replaces the earlier separate save and restore agents, which it unloads.
AGENT := bz.ceh.spacekeeper
LEGACY_AGENTS := bz.ceh.spacekeeper-save bz.ceh.spacekeeper-restore
agent-install:
	for a in $(LEGACY_AGENTS); do launchctl bootout gui/$$(id -u)/$$a 2>/dev/null; rm -f $(HOME)/Library/LaunchAgents/$$a.plist; done; true
	cp dist/$(AGENT).plist $(HOME)/Library/LaunchAgents/$(AGENT).plist
	launchctl bootout gui/$$(id -u)/$(AGENT) 2>/dev/null || true
	launchctl bootstrap gui/$$(id -u) $(HOME)/Library/LaunchAgents/$(AGENT).plist
	@echo "spacekeeper agent enabled (log: ~/Library/Logs/spacekeeper/watch.log)"

agent-uninstall:
	launchctl bootout gui/$$(id -u)/$(AGENT) 2>/dev/null || true
	rm -f $(HOME)/Library/LaunchAgents/$(AGENT).plist
	@echo "spacekeeper agent disabled"
```

(`AGENT` must be defined before `install` uses it: put the `AGENT :=` line next to `BINS :=` at the top.)

- [ ] **Step 3: Remove `-converge` from restore**

In `main.go`: delete the `converge` flag, its usage line, the `convergeRestore` function, and the `converge` parameter of `restoreCmd` (and the `if converge > 0 && !dryRun` branch). Keep `const quietExit = 2 * time.Minute` (move it next to its comment into `watch.go`). Drop the `sysevents` import from `main.go` if now unused.

- [ ] **Step 4: Build, vet, test, install, and switch agents**

Run: `go build ./... && go vet ./... && go test ./...`
Run: `make install && make agent-install`
Check: `launchctl print gui/$(id -u)/bz.ceh.spacekeeper | grep -E 'state|pid'` shows running; `tail ~/Library/Logs/spacekeeper/watch.log` shows the startup line; `launchctl print gui/$(id -u)/bz.ceh.spacekeeper-save` reports not found; `ls ~/Library/LaunchAgents | grep spacekeeper` shows only `bz.ceh.spacekeeper.plist`.

- [ ] **Step 5: Commit**

```bash
git add dist/bz.ceh.spacekeeper.plist Makefile cmd/spacekeeper/main.go cmd/spacekeeper/watch.go
git rm dist/bz.ceh.spacekeeper-save.plist dist/bz.ceh.spacekeeper-restore.plist
git commit -m "agents: single bz.ceh.spacekeeper watch agent replaces save/restore; drop restore -converge"
```

---

### Task 7: prune pins the high-water and default-restore snapshots

**Files:**
- Modify: `internal/layout/layout.go` (add `HighWaterIndex`), `internal/layout/history_test.go`, `cmd/spacekeeper/main.go` (`highWaterSnap`, `pruneSnapshots`, `saveSnapshot` signature gains `settle`)

**Interfaces:**
- Produces: `func HighWaterIndex(ls []Layout) int` (richest per `Richer`, -1 if empty).
- `pruneSnapshots(keep int, settle time.Duration) int` keeps newest `keep` plus `HighWaterIndex` plus `DefaultRestoreIndex`.

- [ ] **Step 1: Failing test**

```go
func TestHighWaterIndexPicksRichest(t *testing.T) {
	base := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	two := []SavedSpace{{UUID: "a", DisplayUUID: "D1"}, {UUID: "b", DisplayUUID: "D2"}}
	ls := []Layout{
		{SavedAt: base.Add(time.Hour), Spaces: two[:1], Windows: make([]SavedWindow, 40)},
		{SavedAt: base, Spaces: two, Windows: make([]SavedWindow, 30)},
	}
	if got := HighWaterIndex(ls); got != 1 {
		t.Fatalf("HighWaterIndex = %d, want 1 (two displays beat more windows)", got)
	}
	if got := HighWaterIndex(nil); got != -1 {
		t.Fatalf("empty: got %d, want -1", got)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/layout/ -run HighWater -v` → compile error; stub returning -1; failure.

- [ ] **Step 3: Implement**

```go
// HighWaterIndex is the richest layout by Richer, -1 when ls is empty. It is
// pinned by pruning so a collapse never deletes the best arrangement.
func HighWaterIndex(ls []Layout) int {
	best := -1
	for i := range ls {
		if best == -1 || Richer(ls[i], ls[best]) {
			best = i
		}
	}
	return best
}
```

In `main.go`, replace `highWaterSnap` with a use of `HighWaterIndex`:

```go
func layouts(refs []snapRef) []layout.Layout {
	ls := make([]layout.Layout, len(refs))
	for i, r := range refs {
		ls[i] = r.l
	}
	return ls
}

func highWaterSnap(refs []snapRef) *snapRef {
	if i := layout.HighWaterIndex(layouts(refs)); i >= 0 {
		return &refs[i]
	}
	return nil
}

// pruneSnapshots keeps the newest `keep` snapshots plus two pinned ones: the
// high-water arrangement and the snapshot restore would pick by default (the
// layout as of the last shutdown). Without the second pin, a few days of
// interval saves push every previous-boot snapshot out, and a bare `restore`
// would fall through to the high-water snapshot from weeks earlier.
func pruneSnapshots(keep int, settle time.Duration) int {
	refs, err := listSnapshots()
	if err != nil || len(refs) <= keep {
		return 0
	}
	ls := layouts(refs)
	pinned := map[int]bool{layout.HighWaterIndex(ls): true, layout.DefaultRestoreIndex(ls, bootTime(), settle): true}
	deleted := 0
	for i, r := range refs {
		if i < keep || pinned[i] {
			continue
		}
		if os.Remove(r.path) == nil {
			deleted++
		}
	}
	return deleted
}
```

Thread `settle` through: `saveSnapshot(keep int, settle time.Duration)`, `saveCmd(explicit string, keep int, settle time.Duration)`, the `save` case passes `*settled`, and `watcher.save` passes `w.opt.settle`. Use `layouts(refs)` in `resolveSnapshot` and `displaysSettled` too, replacing the inline loops.

- [ ] **Step 4: Build, vet, test** → PASS. Then `go run ./cmd/spacekeeper list | grep -v this-boot` still shows the July high-water; after the next real save from the agent the newest previous-boot snapshot would be pinned only if it still exists (it does not on this machine; the pin takes effect from the next reboot on).

- [ ] **Step 5: Commit**

```bash
git add internal/layout/layout.go internal/layout/history_test.go cmd/spacekeeper/main.go cmd/spacekeeper/watch.go
git commit -m "spacekeeper: prune pins the high-water and default-restore snapshots"
```

---

### Task 8: docs

**Files:**
- Modify: `README.md` (spacekeeper section), `TODO.md`

- [ ] **Step 1: README**

In the command block: remove the `restore -converge` line; add `spacekeeper watch   run resident (the launchd agent): saves, wake restore, login convergence`.

Replace the two agent paragraphs ("Run `save` periodically with the opt-in agent…" and "Restore at login is available as an opt-in agent…") with:

```markdown
### The agent

`make agent-install` runs `spacekeeper watch` as a launchd agent (`bz.ceh.spacekeeper`, KeepAlive) that owns the whole lifecycle:

- **Saves** every 3 minutes, on screen and system sleep, and 3 seconds after an active-space change. Saves are deduped, so a quiet session writes nothing.
- **Restores after a transient display drop.** On Apple Silicon an external display de-registers for about a second whenever the screens wake, and macOS asks each app to bring its windows back; Chrome misses that request often enough that some of its windows land on the built-in display (WindowServer logs them as "likely misplaced"). The agent holds saves while the display configuration is changing, waits 10 seconds after the last change, and if the display set matches the newest pre-change snapshot, restores from it. Space and frame both come back. A display that is really unplugged does not match, and nothing is touched.
- **Converges at login.** When uptime is below `-settled`, it restores the newest settled previous-boot snapshot and keeps reconciling as apps launch (a pass 1.5 s after each launch plus a 15 s sweep) until every saved window is handled, two quiet minutes pass, or the 10-minute cap. Windows already handled are never touched again, so rearranging by hand during convergence is safe. `spacekeeper watch -boot` forces this for testing.

Log: `~/Library/Logs/spacekeeper/watch.log`. `make agent-uninstall` removes it. The earlier separate save and restore agents are unloaded by `agent-install`.
```

In "Screen Recording", change "Under the launchd agents (`save`/`restore`)" to "Under the launchd agent (`watch`)".

- [ ] **Step 2: TODO.md**

Replace the spacekeeper section with:

```markdown
## spacekeeper

- Verify the watch agent on a real event: after the next screens wake, `watch.log` should show `display reconfiguration began`, then `displays settled; restored from ...` with `moved` matching any Chrome windows that landed on the built-in display. Check `verified` equals `moved`.
- Verify at the next login that `login convergence` runs from the previous-boot snapshot and the event passes report `verified` counts (moves now run on the main thread via sysevents.OnMain).
- Tune convergence numbers (2m quiet-exit, 10m cap, 15s sweep, 1.5s post-launch delay) once a few real logins have logged pass timings.
- Dell U2725QE "TBT Switch when PC Sleep" was set to OFF on 2026-09-02. At the next system wake, check `log show` for `Processing hotplug` on display 2 at the wake time; if absent, the hotplug cycle is gone for system sleeps.
- Multi-display space creation assumes SLS display order matches the Mission Control AX `mc.display` order. Held on this 2-display setup; revisit if it ever creates desktops on the wrong display.
- AX-title fallback only covers the active space (AX enumerates ~current-space windows); full titles still need Screen Recording. Fine, just noting the ceiling.
```

- [ ] **Step 3: Commit**

```bash
git add README.md TODO.md docs/superpowers/plans/2026-09-02-watch-agent.md
git commit -m "docs: watch agent, display-wake displacement, TODO"
```

---

## Self-review

- Spec coverage: timer + event saves (T5), hold saves during burst (T5), settle 10 s and display-set match (T1, T2, T5), restore from pre-burst snapshot (T1, T5), login convergence in the agent (T5), main-thread moves (T3, T5), crash recovery of an open burst (T2 Resume, T5 pending file), single agent + logs under ~/Library/Logs (T6), bare-restore pruning artifact (T7), docs (T8).
- Names used consistently: `settle.Window{Note,Resume,Open,Start,Deadline,Close}`, `sysevents.{Events,Start,Run,Stop,OnMain,Event,Kind}`, `reconciler{pass,passOn,done,progress}`, `saveSnapshot(keep, settle)` after T7 (T5 introduces it with `keep` only; T7 adds `settle`).
