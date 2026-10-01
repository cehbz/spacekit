# Arrangement model, slice 2a (relaunch binding) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When an app restarts and recreates its windows, each recreated window is bound to the record its predecessor left, on evidence, and owed its placement, so a Chrome relaunch puts its windows back; a window opened in an app that did not restart stays a new window.

**Architecture:** A window-server id belongs to one run of an app (the owning process's start time). A fresh id in a run the store has not seen is matched by the pure function `arrangement.Bind` against stored windows of the same app that are neither live nor from that run; a pair is bound only on unambiguous evidence (same title, or frame within the hysteresis band). Bound windows are marked owed and the existing `Decide` loop repairs them. A run stays "relaunching" until a look finds none of its windows fresh. Snapshot-based login convergence is untouched in this slice.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `golang.org/x/sys/unix` (`SysctlKinfoProc`), existing packages.

**Spec:** `docs/superpowers/specs/2026-10-01-arrangement-model.md`, amended by Task 4 (slice 2 split; binding on evidence; no snapshot seeding; fullscreen placements deferred).

## Global Constraints

- Work on `main`. Commit messages are in "Commit messages" below, in `/tmp/cg-batch-spacekit-765a`; subagents stage and checkpoint with `git stash create`, the orchestrator commits.
- `go build ./... && go vet ./... && go test ./...` clean at every commit; `gofmt -l cmd internal` empty.
- TDD: failing test on an assertion first (stubs so it compiles).
- `internal/arrangement` stays free of cgo and storage. All SkyLight and Accessibility calls stay on the main thread via `sysevents.OnMain`.
- Evidence for binding: same non-empty title, or frame within `arrangement.Tolerance` on every edge. Ambiguous evidence binds nothing.
- Subagents do not run `make install`, `launchctl`, or the built binaries.
- Comments state what exists, tersely; no "new", "now", "old".

---

### Task 1: arrangement.Bind

**Files:** Modify `internal/arrangement/arrangement.go` (add `Run` to `Seen`). Create `internal/arrangement/bind.go`, `internal/arrangement/bind_test.go`.

**Produces:**

```go
// in Seen:
Run int64 // start time of the owning process, unix microseconds; 0 if unknown

type Stored struct {
	Window             int64
	Bundle, App, Title string
	Placement
	Boot    int64  // boot (unix seconds) of its latest binding; 0 if it has none
	Binding uint32 // window-server id in that boot
	Run     int64  // app run that binding belonged to
}
func Bind(fresh []Seen, stored []Stored, boot int64, live map[uint32]bool) map[uint32]int64
```

- [ ] **Step 1: failing tests** (`bind_test.go`)

```go
package arrangement

import (
	"testing"

	"github.com/cehbz/spacekit/internal/layout"
)

const thisBoot = int64(1790652986)

func fresh(wid uint32, title string, f layout.Rect) Seen {
	return Seen{Binding: wid, PID: 5, Bundle: "com.google.Chrome", App: "Google Chrome", Title: title, Run: 200,
		Placement: Placement{Space: "S5", Frame: f}}
}

// left is a stored Chrome window from the app's previous run in this boot.
func left(win int64, title string, space string, f layout.Rect) Stored {
	return Stored{Window: win, Bundle: "com.google.Chrome", App: "Google Chrome", Title: title,
		Placement: Placement{Space: space, Frame: f}, Boot: thisBoot, Binding: uint32(win), Run: 100}
}

var (
	rightHalf = layout.Rect{X: 1280, Y: 30, W: 1280, H: 1410}
	odd       = layout.Rect{X: 300, Y: 200, W: 900, H: 700}
)

func TestBindByTitle(t *testing.T) {
	got := Bind([]Seen{fresh(900, "autobrr", full)}, []Stored{left(1, "autobrr", "S2", rightHalf), left(2, "Review", "S3", full)}, thisBoot, map[uint32]bool{900: true})
	if len(got) != 1 || got[900] != 1 {
		t.Fatalf("got %v, want 900->1", got)
	}
}

func TestBindTitleBeatsSharedFrame(t *testing.T) {
	// Both stored windows have the fresh window's frame; only one has its title.
	st := []Stored{left(1, "autobrr", "S2", full), left(2, "Review", "S3", full)}
	got := Bind([]Seen{fresh(900, "Review", full)}, st, thisBoot, map[uint32]bool{900: true})
	if got[900] != 2 {
		t.Fatalf("got %v, want 900->2", got)
	}
}

func TestBindByFrameWhenTheTitleChanged(t *testing.T) {
	st := []Stored{left(1, "old title", "S2", odd), left(2, "Review", "S3", full)}
	got := Bind([]Seen{fresh(900, "renamed tab", odd)}, st, thisBoot, map[uint32]bool{900: true})
	if got[900] != 1 {
		t.Fatalf("got %v, want 900->1", got)
	}
}

func TestBindNothingWithoutEvidence(t *testing.T) {
	got := Bind([]Seen{fresh(900, "brand new", odd)}, []Stored{left(1, "autobrr", "S2", full)}, thisBoot, map[uint32]bool{900: true})
	if len(got) != 0 {
		t.Fatalf("no shared title or frame: got %v", got)
	}
}

func TestBindNothingWhenAmbiguous(t *testing.T) {
	// Two untitled stored windows with the same frame: no telling which.
	st := []Stored{left(1, "", "S2", odd), left(2, "", "S3", odd)}
	if got := Bind([]Seen{fresh(900, "", odd)}, st, thisBoot, map[uint32]bool{900: true}); len(got) != 0 {
		t.Fatalf("ambiguous stored windows: got %v", got)
	}
	// Two fresh windows with the title of one stored window.
	two := []Seen{fresh(900, "New Tab", full), fresh(901, "New Tab", rightHalf)}
	if got := Bind(two, []Stored{left(1, "New Tab", "S2", odd)}, thisBoot, map[uint32]bool{900: true, 901: true}); len(got) != 0 {
		t.Fatalf("ambiguous fresh windows: got %v", got)
	}
}

func TestBindTitleAndFrameResolvesTwins(t *testing.T) {
	// Same title twice; frames tell them apart.
	st := []Stored{left(1, "New Tab", "S2", full), left(2, "New Tab", "S3", rightHalf)}
	two := []Seen{fresh(900, "New Tab", rightHalf), fresh(901, "New Tab", full)}
	got := Bind(two, st, thisBoot, map[uint32]bool{900: true, 901: true})
	if got[900] != 2 || got[901] != 1 {
		t.Fatalf("got %v, want 900->2 901->1", got)
	}
}

func TestBindSkipsLiveAndSameRunAndOtherApps(t *testing.T) {
	liveOne := left(1, "autobrr", "S2", full) // still on screen under its id
	sameRun := left(2, "autobrr", "S2", full) // closed during this run: not relaunch debris
	sameRun.Run = 200
	other := left(3, "autobrr", "S2", full)
	other.Bundle, other.App = "com.apple.Safari", "Safari"
	got := Bind([]Seen{fresh(900, "autobrr", full)}, []Stored{liveOne, sameRun, other}, thisBoot, map[uint32]bool{900: true, 1: true})
	if len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestBindAcrossBoots(t *testing.T) {
	prev := left(1, "autobrr", "S2", full)
	prev.Boot, prev.Run = thisBoot-86400, 200 // an earlier boot; a run id cannot collide across boots
	never := Stored{Window: 2, Bundle: "com.google.Chrome", App: "Google Chrome", Title: "Review", Placement: Placement{Space: "S3", Frame: rightHalf}}
	two := []Seen{fresh(900, "autobrr", full), fresh(901, "Review", rightHalf)}
	got := Bind(two, []Stored{prev, never}, thisBoot, map[uint32]bool{900: true, 901: true})
	if got[900] != 1 || got[901] != 2 {
		t.Fatalf("got %v, want 900->1 901->2", got)
	}
}

func TestBindEachStoredWindowOnce(t *testing.T) {
	st := []Stored{left(1, "autobrr", "S2", full)}
	two := []Seen{fresh(900, "autobrr", full), fresh(901, "other", full)}
	got := Bind(two, st, thisBoot, map[uint32]bool{900: true, 901: true})
	if len(got) != 1 || got[900] != 1 {
		t.Fatalf("title and frame beats frame alone: got %v, want only 900->1", got)
	}
}
```

- [ ] **Step 2:** add `Run` to `Seen`, create `bind.go` with `Stored` and `Bind` returning `nil`; run and see assertion failures.

- [ ] **Step 3: implement** (`bind.go`)

```go
package arrangement

// Stored is a placed window as the store last knew it bound: a candidate for
// binding to a fresh window-server id after its app restarts.
type Stored struct {
	Window             int64
	Bundle, App, Title string
	Placement
	Boot    int64  // boot (unix seconds) of its latest binding; 0 if it has none
	Binding uint32 // window-server id in that boot
	Run     int64  // app run that binding belonged to
}

// Evidence that a fresh window is a stored one recreated, strongest first.
const (
	none          = iota
	sameFrameOnly // frame within the band; the title changed
	sameTitle     // same non-empty title
	titleAndFrame
)

// Bind pairs fresh windows with the stored windows they recreate. A stored
// window is a candidate for a fresh one of the same app unless it is still on
// screen under its id or was bound in the fresh window's own run (then it was
// closed, not lost to a restart). A pair is made only when the evidence is
// unambiguous at its strength: one candidate for the fresh window, and one
// fresh window for that candidate. Stronger evidence is settled first. The
// result maps a fresh window's id to the stored window.
func Bind(fresh []Seen, stored []Stored, boot int64, live map[uint32]bool) map[uint32]int64 {
	out := make(map[uint32]int64)
	taken := make(map[int64]bool)
	for level := titleAndFrame; level >= sameFrameOnly; level-- {
		for changed := true; changed; {
			changed = false
			for _, f := range fresh {
				if _, done := out[f.Binding]; done {
					continue
				}
				var match *Stored
				n := 0
				for i := range stored {
					o := &stored[i]
					if !taken[o.Window] && evidence(f, *o, boot, live) >= level {
						match = o
						n++
					}
				}
				if n != 1 {
					continue
				}
				rivals := 0
				for _, g := range fresh {
					if _, done := out[g.Binding]; !done && evidence(g, *match, boot, live) >= level {
						rivals++
					}
				}
				if rivals == 1 {
					out[f.Binding] = match.Window
					taken[match.Window] = true
					changed = true
				}
			}
		}
	}
	return out
}

func evidence(f Seen, o Stored, boot int64, live map[uint32]bool) int {
	if appKey(f.Bundle, f.App) != appKey(o.Bundle, o.App) {
		return none
	}
	if o.Boot == boot && (live[o.Binding] || o.Run == f.Run) {
		return none
	}
	title := f.Title != "" && f.Title == o.Title
	frame := sameFrame(f.Frame, o.Frame)
	switch {
	case title && frame:
		return titleAndFrame
	case title:
		return sameTitle
	case frame:
		return sameFrameOnly
	}
	return none
}

// appKey identifies an app: its bundle id, or its name when it has none.
func appKey(bundle, app string) string {
	if bundle != "" {
		return "b:" + bundle
	}
	return "o:" + app
}
```

- [ ] **Step 4:** tests pass; vet and gofmt clean. Stage, checkpoint. Commit message 1.

---

### Task 2: the store knows app runs

**Files:** Modify `internal/store/store.go`, `internal/store/store_test.go`.

**Consumes:** `arrangement.Stored`, `arrangement.Seen.Run`.

**Produces:** schema migration via `PRAGMA user_version` (migration 1: `binding.run`); `newWindow` records the run; `Stored(arr int64) ([]arrangement.Stored, error)`; `KnownRuns(boot time.Time) (map[int64]bool, error)`; `NoteRuns(boot time.Time, seen []arrangement.Seen) error`; `type Bound struct { Window int64; Seen arrangement.Seen }`; `Bind(arr int64, boot, at time.Time, cause string, bound []Bound) error`.

- [ ] **Step 1: failing tests** (append to `store_test.go`)

```go
func seenRun(wid uint32, run int64, title, space string, f layout.Rect) arrangement.Seen {
	s := seen(wid, space, f)
	s.Run, s.Title = run, title
	return s
}

func TestOpenMigratesAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM pragma_table_info('binding') WHERE name = 'run'`); n != 1 {
		t.Fatalf("binding.run missing after Open")
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if n := count(t, s2, `PRAGMA user_version`); n < 1 {
		t.Fatalf("user_version = %d", n)
	}
}

func TestAdoptRecordsTheRunAndKnownRuns(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Adopt, Seen: seenRun(42, 777, "t", "S1", full)}}); err != nil {
		t.Fatal(err)
	}
	runs, err := s.KnownRuns(boot)
	if err != nil || !runs[777] || len(runs) != 1 {
		t.Fatalf("KnownRuns = %v, %v", runs, err)
	}
	if other, _ := s.KnownRuns(boot.Add(time.Hour)); len(other) != 0 {
		t.Fatalf("another boot knows no runs: %v", other)
	}
}

func TestNoteRunsFillsUnknownRunsOnly(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	adoptNew(t, s, arr, 42, "S1", full) // seen() carries no run
	if err := s.NoteRuns(boot, []arrangement.Seen{seenRun(42, 555, "t", "S1", full)}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT run FROM binding WHERE wid = 42`); n != 555 {
		t.Fatalf("run = %d, want 555", n)
	}
	if err := s.NoteRuns(boot, []arrangement.Seen{seenRun(42, 999, "t", "S1", full)}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT run FROM binding WHERE wid = 42`); n != 555 {
		t.Fatalf("a known run must not be overwritten: %d", n)
	}
}

func TestStoredCarriesTheLatestBinding(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Adopt, Seen: seenRun(42, 100, "autobrr", "S2", full)}}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Stored(arr)
	if err != nil || len(st) != 1 {
		t.Fatalf("Stored = %+v, %v", st, err)
	}
	o := st[0]
	if o.Title != "autobrr" || o.Bundle != "com.example" || o.Space != "S2" || o.Frame != full || o.Boot != boot.Unix() || o.Binding != 42 || o.Run != 100 {
		t.Fatalf("stored = %+v", o)
	}
}

func TestBindGivesAStoredWindowAFreshIdAndOwesIt(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Adopt, Seen: seenRun(42, 100, "autobrr", "S2", full)}}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Stored(arr)
	f := seenRun(900, 200, "autobrr - renamed", "S5", full)
	if err := s.Bind(arr, boot, t0.Add(time.Minute), "relaunch", []Bound{{Window: st[0].Window, Seen: f}}); err != nil {
		t.Fatal(err)
	}
	rs, _ := s.Recorded(arr, boot)
	var got *arrangement.Recorded
	for i := range rs {
		if rs[i].Binding == 900 {
			got = &rs[i]
		}
	}
	if got == nil || got.Window != st[0].Window || !got.Owed || got.Space != "S2" {
		t.Fatalf("the fresh id is the same window, owed its recorded placement: %+v", rs)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM window`); n != 1 {
		t.Fatalf("binding must not create a window: %d", n)
	}
	st2, _ := s.Stored(arr)
	if st2[0].Binding != 900 || st2[0].Run != 200 || st2[0].Title != "autobrr - renamed" {
		t.Fatalf("latest binding and title: %+v", st2[0])
	}
	if n := count(t, s, `SELECT COUNT(*) FROM event WHERE kind = 'bound'`); n != 1 {
		t.Fatalf("bound events = %d", n)
	}
}
```

- [ ] **Step 2:** add stubs (`Stored`, `KnownRuns`, `NoteRuns`, `Bind`, `Bound`) returning zero values; run and see assertion failures (`TestOpenMigratesAndReopens` fails on the missing column).

- [ ] **Step 3: implement.** In `Open`, after the schema exec:

```go
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
```

and add:

```go
// migrations bring a database from PRAGMA user_version n to n+1. schema.sql
// is version 0.
var migrations = []string{
	`ALTER TABLE binding ADD COLUMN run INTEGER NOT NULL DEFAULT 0`,
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for ; v < len(migrations); v++ {
		if _, err := db.Exec(migrations[v]); err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			return err
		}
	}
	return nil
}
```

`newWindow` inserts the run:

```go
	_, err = tx.Exec(`INSERT INTO binding(window_id, boot, wid, run) VALUES (?, ?, ?, ?)`, win, boot.Unix(), s.Binding, s.Run)
```

and the rest:

```go
// Stored returns every window with a placement in the arrangement, with its
// latest binding: the candidates for binding fresh ids after a relaunch.
func (s *Store) Stored(arr int64) ([]arrangement.Stored, error) {
	rows, err := s.db.Query(`
		SELECT w.id, w.bundle, w.app, w.title, p.space, p.x, p.y, p.w, p.h,
		       COALESCE(b.boot, 0), COALESCE(b.wid, 0), COALESCE(b.run, 0)
		FROM placement p
		JOIN window w ON w.id = p.window_id
		LEFT JOIN binding b ON b.rowid = (SELECT MAX(rowid) FROM binding WHERE window_id = w.id)
		WHERE p.arrangement_id = ? AND p.closed_by IS NULL
		ORDER BY w.id`, arr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []arrangement.Stored
	for rows.Next() {
		var o arrangement.Stored
		if err := rows.Scan(&o.Window, &o.Bundle, &o.App, &o.Title, &o.Space, &o.Frame.X, &o.Frame.Y, &o.Frame.W, &o.Frame.H,
			&o.Boot, &o.Binding, &o.Run); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// KnownRuns returns the app runs that have bindings in this boot.
func (s *Store) KnownRuns(boot time.Time) (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT run FROM binding WHERE boot = ? AND run <> 0`, boot.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var run int64
		if err := rows.Scan(&run); err != nil {
			return nil, err
		}
		out[run] = true
	}
	return out, rows.Err()
}

// NoteRuns records the app run of bindings that lack one.
func (s *Store) NoteRuns(boot time.Time, seen []arrangement.Seen) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, w := range seen {
		if w.Run == 0 {
			continue
		}
		if _, err := tx.Exec(`UPDATE binding SET run = ? WHERE boot = ? AND wid = ? AND run = 0`, w.Run, boot.Unix(), w.Binding); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Bound pairs a stored window with the fresh window that recreates it.
type Bound struct {
	Window int64
	Seen   arrangement.Seen
}

// Bind gives stored windows their fresh ids and owes each its placement.
func (s *Store) Bind(arr int64, boot, at time.Time, cause string, bound []Bound) error {
	if len(bound) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO change(at, cause, note) VALUES (?, ?, ?)`, at.UnixMilli(), cause, fmt.Sprintf("%d windows bound", len(bound)))
	if err != nil {
		return err
	}
	change, _ := res.LastInsertId()
	for _, b := range bound {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO binding(window_id, boot, wid, run) VALUES (?, ?, ?, ?)`,
			b.Window, boot.Unix(), b.Seen.Binding, b.Seen.Run); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE window SET title = ?, app = ? WHERE id = ?`, b.Seen.Title, b.Seen.App, b.Window); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO owed(window_id, arrangement_id, progress, since) VALUES (?, ?, 0, ?)
			ON CONFLICT(window_id, arrangement_id) DO UPDATE SET progress = 0`, b.Window, arr, change); err != nil {
			return err
		}
		if err := event(tx, change, b.Window, "bound"); err != nil {
			return err
		}
	}
	return tx.Commit()
}
```

- [ ] **Step 4:** all tests pass; vet and gofmt clean. Stage, checkpoint. Commit message 2.

---

### Task 3: the agent binds after a relaunch

**Files:** Modify `cmd/spacekeeper/watch.go`, `cmd/spacekeeper/boottime_darwin.go`.

**Consumes:** `arrangement.Bind`, `store.Stored`, `store.KnownRuns`, `store.NoteRuns`, `store.Bind`, `store.Bound`.

- [ ] **Step 1: process start.** In `boottime_darwin.go`:

```go
// processStart returns when a process started, in unix microseconds, or 0 if
// the kernel will not say. It identifies one run of an app.
func processStart(pid int) int64 {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0
	}
	tv := kp.Proc.P_starttime
	return int64(tv.Sec)*1_000_000 + int64(tv.Usec)
}
```

- [ ] **Step 2: `seenWindows` fills `Run`**, one lookup per pid:

```go
	runs := map[int]int64{}
	run := func(pid int) int64 {
		r, ok := runs[pid]
		if !ok {
			r = processStart(pid)
			runs[pid] = r
		}
		return r
	}
```

and `Run: run(l.OwnerPID)` in the `arrangement.Seen` literal.

- [ ] **Step 3: watcher.** Field `relaunching map[int64]bool // app runs whose windows are still appearing`, initialised in `watchCmd` (`relaunching: map[int64]bool{}`).

  In `lookOnMain`, move `seen := seenWindows(s)` above the `Recorded` call and replace the `Recorded` call and the title-refresh condition:

```go
	seen := seenWindows(s)
	if err := w.store.NoteRuns(boot, seen); err != nil {
		return err
	}
	recorded, err := w.store.Recorded(arr, boot)
	if err != nil {
		return err
	}
	if recorded, err = w.rebind(arr, boot, now, recorded, seen); err != nil {
		return err
	}
```

  and, after `Apply`, refresh titles at every look (a title is identity for binding and writes no change):

```go
	if err := w.store.RefreshTitles(boot, seen); err != nil {
		return err
	}
```

  Add:

```go
// rebind binds the fresh windows of restarted apps to the records their
// previous run left, and owes them their placements. A run is relaunching
// from the first look that sees it until a look in which none of its windows
// is fresh; a fresh window in any other run is simply a new window.
func (w *watcher) rebind(arr int64, boot, now time.Time, recorded []arrangement.Recorded, seen []arrangement.Seen) ([]arrangement.Recorded, error) {
	bound := make(map[uint32]bool, len(recorded))
	for _, r := range recorded {
		bound[r.Binding] = true
	}
	known, err := w.store.KnownRuns(boot)
	if err != nil {
		return nil, err
	}
	live := make(map[uint32]bool, len(seen))
	freshRuns := map[int64]bool{}
	var fresh []arrangement.Seen
	for _, s := range seen {
		live[s.Binding] = true
		if bound[s.Binding] || s.Run == 0 {
			continue
		}
		if !known[s.Run] || w.relaunching[s.Run] {
			fresh = append(fresh, s)
			freshRuns[s.Run] = true
		}
	}
	for run := range w.relaunching {
		if !freshRuns[run] {
			delete(w.relaunching, run)
		}
	}
	for run := range freshRuns {
		w.relaunching[run] = true
	}
	if len(fresh) == 0 {
		return recorded, nil
	}
	stored, err := w.store.Stored(arr)
	if err != nil {
		return nil, err
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
	if err := w.store.Bind(arr, boot, now, "relaunch", bs); err != nil {
		return nil, err
	}
	log.Printf("relaunch: bound %d of %d fresh window(s) to their records: %s", len(bs), len(fresh), strings.Join(names, "; "))
	return w.store.Recorded(arr, boot)
}
```

  `handle`, `AppLaunched` case: a launch outside login convergence triggers a look once the app has mapped its windows.

```go
	case sysevents.AppLaunched:
		time.Sleep(launchDelay) // let the app map its windows
		if w.boot != nil {
			w.bootPass(e.Name + " launched")
		} else {
			w.look(e.Name + " launched")
		}
```

  `sweep`: while any run is relaunching, look at every sweep so windows that appear late are bound within seconds.

```go
func (w *watcher) sweep() {
	if w.boot == nil {
		if len(w.relaunching) > 0 {
			w.look("relaunch")
		}
		return
	}
	// login convergence, unchanged below
```

- [ ] **Step 4:** build, vet, test, gofmt clean. Stage, checkpoint.

- [ ] **Step 5: verify on this machine** (orchestrator, at a time the user approves; visible effects: Activity Monitor quits and reopens, a Finder window opens and closes). After `make install`:

  1. First look: no `relaunch:` line; `SELECT COUNT(*) FROM binding WHERE boot = <boot> AND run = 0` is 0 for windows on screen; `PRAGMA user_version` is 1.
  2. New window in a running app: `osascript -e 'tell application "Finder" to make new Finder window'`. The next look logs `adopted 1` and no `relaunch:` line. Close it.
  3. Relaunch: note Activity Monitor's space, then `osascript -e 'quit app "Activity Monitor"'` and `open -a "Activity Monitor"`. Expect `relaunch: bound 1 of 1 fresh window(s) to their records: Activity Monitor | Activity Monitor`, a `repaired 1` look, then `released 1`; the window is back on its space; `SELECT COUNT(*) FROM window WHERE app = 'Activity Monitor'` is unchanged.
  4. The user quits and reopens Chrome when convenient. Expect `relaunch: bound N of M`, repairs, releases; `inspect` shows the Chrome windows on their spaces; the count of Chrome rows in `window` grows only by windows that had no match.

- [ ] **Step 6:** Commit message 3.

---

### Task 4: docs

**Files:** `docs/superpowers/specs/2026-10-01-arrangement-model.md`, `README.md`, `TODO.md`, this plan.

- [ ] Spec: in **Disturbance**, define the app-relaunch disturbance as lasting from the first look that sees a run of the app the store does not know until a look in which none of that run's windows is fresh. In **Binding**, state that a fresh window is bound to a stored window of the same app only on unambiguous evidence, same non-empty title or frame within the band, and is otherwise a window of its own. Replace the slice list with: 1 in-session (done); 2a relaunch binding, snapshot login path kept; 2b login through the model, snapshots and their commands removed, a one-window move/resize command for testing; 3 repair tooling and retention. Remove seeding from the newest snapshot. Move fullscreen placements to "Known limits" as not handled.
- [ ] README "The agent": add one sentence after the repair bullet: an app that restarts has its recreated windows matched to their records by title or frame and put back; a window that matches nothing is treated as new.
- [ ] TODO: replace the "Slice 2" item with "Slice 2b: login through the model (every app is a relaunch), then remove snapshots, their commands and login convergence; add a one-window move/resize command for on-machine tests" and add "Verify relaunch binding on a real Chrome update restart".
- [ ] Commit message 4.

---

## Commit messages (verbatim, in order)

1. `arrangement: bind recreated windows to their records on unambiguous evidence`
2. `store: app runs on bindings, stored windows for re-binding, schema migration`
3. `spacekeeper watch: a restarted app's windows are bound to their records and put back`
4. `docs: slice 2a, relaunch binding`
