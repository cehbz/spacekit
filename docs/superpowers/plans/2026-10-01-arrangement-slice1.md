# Arrangement model, slice 1 (in-session) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The agent records intent as an arrangement in SQLite, adopts calm changes to windows on visible spaces, and after a display disturbance owes every window its placement and repairs it, replacing snapshot references and frame debt for everything that happens within a boot.

**Architecture:** `internal/arrangement` holds the rules (a pure function from recorded state and a look to decisions). `internal/store` holds the state (SQLite, versioned placements, owed windows, journal). `cmd/spacekeeper/watch.go` gathers the session, asks `arrangement.Decide`, actuates repairs, and applies the decisions in one transaction. Snapshot saving and login convergence stay as they are and remain the only login path in this slice.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (cgo-free, WAL), existing cgo/ObjC packages `skylight`, `sysevents`, `settle`, `layout`.

**Spec:** `docs/superpowers/specs/2026-10-01-arrangement-model.md` (rules 1 to 7, storage, slice 1).

## Global Constraints

- Work on `main`. Commits use the exact messages in "Commit messages" below, from `/tmp/cg-batch-spacekit-765a`; subagents stage, the orchestrator commits.
- `go build ./... && go vet ./... && go test ./...` clean at every commit; `gofmt -l cmd internal` prints nothing.
- TDD: each rule and each store behaviour starts as a failing test (stub so it compiles, fail on an assertion).
- `internal/arrangement` imports only `internal/layout` and the standard library. `internal/store` imports `arrangement`, `layout`, `database/sql`, `modernc.org/sqlite`.
- All SkyLight and Accessibility calls run on the main thread via `sysevents.OnMain`.
- Hysteresis band: 3 points per edge. Look interval: 1 minute. Display quiet: 10 s (unchanged). Fullscreen windows are not placed in this slice.
- Comments state what exists, tersely. No "now", "new", "old" narrative.
- Database: `~/.config/spacekeeper/spacekeeper.db`.

---

### Task 1: Placement and the hysteresis band

**Files:** Create `internal/arrangement/arrangement.go`, `internal/arrangement/arrangement_test.go`.

**Produces:** `const Tolerance = 3.0`; `type Placement struct { Space string; Frame layout.Rect }`; `func (p Placement) InPlace(q Placement) bool`; unexported `sameFrame(a, b layout.Rect) bool`.

- [ ] **Step 1: failing test** (`arrangement_test.go`)

```go
package arrangement

import (
	"testing"

	"github.com/cehbz/spacekit/internal/layout"
)

var (
	full   = layout.Rect{X: 0, Y: 30, W: 1280, H: 1410}
	shrunk = layout.Rect{X: 0, Y: 517, W: 735, H: 923}
)

func TestInPlaceHysteresis(t *testing.T) {
	p := Placement{Space: "S1", Frame: full}
	cases := []struct {
		name string
		q    Placement
		want bool
	}{
		{"identical", Placement{"S1", full}, true},
		{"three points off on every edge", Placement{"S1", layout.Rect{X: 3, Y: 27, W: 1280, H: 1410}}, true},
		{"width one point narrower", Placement{"S1", layout.Rect{X: 0, Y: 30, W: 1279, H: 1410}}, true},
		{"four points off", Placement{"S1", layout.Rect{X: 4, Y: 30, W: 1280, H: 1410}}, false},
		{"far edge four points off", Placement{"S1", layout.Rect{X: 0, Y: 30, W: 1284, H: 1410}}, false},
		{"shrunk", Placement{"S1", shrunk}, false},
		{"other space, same frame", Placement{"S2", full}, false},
	}
	for _, c := range cases {
		if got := p.InPlace(c.q); got != c.want {
			t.Errorf("%s: InPlace = %v, want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2:** create `arrangement.go` with the types and `InPlace` returning `false`; run `go test ./internal/arrangement/` and see the `true` cases fail.

- [ ] **Step 3: implement**

```go
// Package arrangement holds the rules of the arrangement model: what a look
// at the live session means for the recorded intent. It has no storage and
// no system calls; the store and the agent sit on either side of it.
package arrangement

import (
	"math"

	"github.com/cehbz/spacekit/internal/layout"
)

// Tolerance is the hysteresis band, in points per edge, inside which a frame
// counts as in place: a difference inside it is neither adopted nor repaired.
const Tolerance = 3.0

// Placement is where a window belongs: a space and a frame. Space is a space
// key: the space UUID, or display:<uuid>/<index> for the untitled desktop.
type Placement struct {
	Space string
	Frame layout.Rect
}

// InPlace reports whether q is on the same space as p and within Tolerance of
// it on every edge.
func (p Placement) InPlace(q Placement) bool {
	return p.Space == q.Space && sameFrame(p.Frame, q.Frame)
}

func sameFrame(a, b layout.Rect) bool {
	near := func(x, y float64) bool { return math.Abs(x-y) <= Tolerance }
	return near(a.X, b.X) && near(a.Y, b.Y) && near(a.X+a.W, b.X+b.W) && near(a.Y+a.H, b.Y+b.H)
}
```

- [ ] **Step 4:** `go test ./internal/arrangement/` passes; `go vet`, `gofmt -l` clean. Stage both files. Commit message 1.

---

### Task 2: Decide

**Files:** Modify `internal/arrangement/arrangement.go`, `internal/arrangement/arrangement_test.go`.

**Consumes:** `Placement`, `InPlace`, `sameFrame`.

**Produces:**

```go
type Progress int
const ( Untried Progress = iota; Moved; Attempted )

type Recorded struct {
	Window   int64  // store identity
	Binding  uint32 // window-server id in this boot
	Placed   bool   // has a placement in the current arrangement
	Placement
	Owed     bool
	Progress Progress
}
type Seen struct {
	Binding            uint32
	PID                int
	Bundle, App, Title string
	Placement
}
type Look struct {
	Windows []Seen
	Visible map[string]bool // space keys visible at this look or the previous one
}
type Kind int
const ( Adopt Kind = iota; Owe; Release; GiveUp; Repair )
type Decision struct {
	Kind         Kind
	Window       int64 // 0: a window never seen before
	Seen         Seen
	Want         Placement // Repair: where it belongs
	Move, Resize bool      // Repair: what to do at this look
}
func Decide(recorded []Recorded, look Look) []Decision
func (d Decision) Progress() (Progress, bool)
```

- [ ] **Step 1: failing tests** (append to `arrangement_test.go`)

```go
func rec(binding uint32, space string, f layout.Rect) Recorded {
	return Recorded{Window: int64(binding) + 100, Binding: binding, Placed: true, Placement: Placement{space, f}}
}

func saw(binding uint32, space string, f layout.Rect) Seen {
	return Seen{Binding: binding, PID: 1, App: "A", Placement: Placement{space, f}}
}

func vis(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func kinds(ds []Decision) []Kind {
	var ks []Kind
	for _, d := range ds {
		ks = append(ks, d.Kind)
	}
	return ks
}

func wantKinds(t *testing.T, ds []Decision, want ...Kind) {
	t.Helper()
	got := kinds(ds)
	if len(got) != len(want) {
		t.Fatalf("decisions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decisions = %v, want %v", got, want)
		}
	}
}

func TestDecideFirstSightingAdopts(t *testing.T) {
	ds := Decide(nil, Look{Windows: []Seen{saw(1, "S1", full)}, Visible: vis("S9")})
	wantKinds(t, ds, Adopt)
	if ds[0].Window != 0 {
		t.Fatalf("a never-seen window has no store identity yet, got %d", ds[0].Window)
	}
}

func TestDecideBoundWithoutPlacementAdoptsForTheSameWindow(t *testing.T) {
	r := Recorded{Window: 7, Binding: 1}
	ds := Decide([]Recorded{r}, Look{Windows: []Seen{saw(1, "S1", full)}, Visible: vis("S1")})
	wantKinds(t, ds, Adopt)
	if ds[0].Window != 7 {
		t.Fatalf("Window = %d, want 7", ds[0].Window)
	}
}

func TestDecideInPlaceIsSilent(t *testing.T) {
	jitter := layout.Rect{X: 1, Y: 30, W: 1279, H: 1410}
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Windows: []Seen{saw(1, "S1", jitter)}, Visible: vis("S1")})
	wantKinds(t, ds)
}

func TestDecideChangeOnAVisibleSpaceIsAdopted(t *testing.T) {
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Windows: []Seen{saw(1, "S1", shrunk)}, Visible: vis("S1")})
	wantKinds(t, ds, Adopt)
	if ds[0].Window != 101 {
		t.Fatalf("Window = %d, want 101", ds[0].Window)
	}
}

func TestDecideMoveOffAVisibleSpaceIsAdopted(t *testing.T) {
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Windows: []Seen{saw(1, "S2", full)}, Visible: vis("S1")})
	wantKinds(t, ds, Adopt)
}

func TestDecideChangeOnAnUnseenSpaceIsOwed(t *testing.T) {
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Windows: []Seen{saw(1, "S1", shrunk)}, Visible: vis("S9")})
	wantKinds(t, ds, Owe, Repair)
	if ds[1].Move || ds[1].Resize {
		t.Fatalf("its space is not visible and it is on the right space: nothing to do yet, got %+v", ds[1])
	}
	if _, ok := ds[1].Progress(); ok {
		t.Fatal("a repair that did nothing leaves no progress")
	}
}

func TestDecideUnseenSpaceChangeIsMovedBack(t *testing.T) {
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Windows: []Seen{saw(1, "S2", full)}, Visible: vis("S9")})
	wantKinds(t, ds, Owe, Repair)
	if !ds[1].Move || ds[1].Resize || ds[1].Want.Space != "S1" {
		t.Fatalf("want a move back to S1 only, got %+v", ds[1])
	}
	if p, ok := ds[1].Progress(); !ok || p != Attempted {
		t.Fatalf("frame already right, so the move completes the repair: got %v %v", p, ok)
	}
}

func owedRec(binding uint32, space string, f layout.Rect, p Progress) Recorded {
	r := rec(binding, space, f)
	r.Owed, r.Progress = true, p
	return r
}

func TestDecideOwedSeenInPlaceIsReleased(t *testing.T) {
	ds := Decide([]Recorded{owedRec(1, "S1", full, Untried)}, Look{Windows: []Seen{saw(1, "S1", full)}, Visible: vis("S9")})
	wantKinds(t, ds, Release)
}

func TestDecideOwedIsResizedWhenItsSpaceIsVisible(t *testing.T) {
	ds := Decide([]Recorded{owedRec(1, "S1", full, Untried)}, Look{Windows: []Seen{saw(1, "S1", shrunk)}, Visible: vis("S1")})
	wantKinds(t, ds, Repair)
	if ds[0].Move || !ds[0].Resize || ds[0].Want.Frame != full {
		t.Fatalf("want a resize to the recorded frame, got %+v", ds[0])
	}
	if p, _ := ds[0].Progress(); p != Attempted {
		t.Fatalf("Progress = %v, want Attempted", p)
	}
}

func TestDecideOwedWaitsWhileDriftingUnseen(t *testing.T) {
	drift := layout.Rect{X: 0, Y: 33, W: 735, H: 923}
	ds := Decide([]Recorded{owedRec(1, "S1", full, Untried)}, Look{Windows: []Seen{saw(1, "S1", drift)}, Visible: vis("S9")})
	wantKinds(t, ds, Repair)
	if ds[0].Move || ds[0].Resize {
		t.Fatalf("nothing can be done until S1 is visible, got %+v", ds[0])
	}
}

func TestDecideOwedOnWrongUnseenSpaceIsMovedOnly(t *testing.T) {
	ds := Decide([]Recorded{owedRec(1, "S1", full, Untried)}, Look{Windows: []Seen{saw(1, "S2", shrunk)}, Visible: vis("S9")})
	wantKinds(t, ds, Repair)
	if !ds[0].Move || ds[0].Resize {
		t.Fatalf("want move only, got %+v", ds[0])
	}
	if p, _ := ds[0].Progress(); p != Moved {
		t.Fatalf("Progress = %v, want Moved", p)
	}
}

func TestDecideMovedWindowIsResizedOnceVisible(t *testing.T) {
	ds := Decide([]Recorded{owedRec(1, "S1", full, Moved)}, Look{Windows: []Seen{saw(1, "S1", shrunk)}, Visible: vis("S1")})
	wantKinds(t, ds, Repair)
	if ds[0].Move || !ds[0].Resize {
		t.Fatalf("want resize only, got %+v", ds[0])
	}
}

func TestDecideGivesUpAfterAFullAttempt(t *testing.T) {
	ds := Decide([]Recorded{owedRec(1, "S1", full, Attempted)}, Look{Windows: []Seen{saw(1, "S1", shrunk)}, Visible: vis("S1")})
	wantKinds(t, ds, GiveUp)
}

func TestDecideGivesUpWhenTheMoveDidNotTake(t *testing.T) {
	ds := Decide([]Recorded{owedRec(1, "S1", full, Moved)}, Look{Windows: []Seen{saw(1, "S2", full)}, Visible: vis("S9")})
	wantKinds(t, ds, GiveUp)
}

func TestDecideIgnoresRecordedWindowsNotSeen(t *testing.T) {
	ds := Decide([]Recorded{rec(1, "S1", full)}, Look{Visible: vis("S1")})
	wantKinds(t, ds)
}
```

- [ ] **Step 2:** add the types, `Decide` returning `nil`, and `Progress` returning `Untried, false`; run the tests and see them fail on assertions.

- [ ] **Step 3: implement** (append to `arrangement.go`)

```go
// Progress records how far the last repair of an owed window got, for the
// next look to judge.
type Progress int

const (
	Untried   Progress = iota
	Moved              // moved to its space; its frame could not be reached yet
	Attempted          // everything it needed has been attempted
)

// Recorded is a window as the store knows it in the current arrangement.
type Recorded struct {
	Window  int64  // store identity
	Binding uint32 // window-server id in this boot
	Placed  bool   // has a placement in the current arrangement
	Placement
	Owed     bool
	Progress Progress
}

// Seen is a window as a look found it.
type Seen struct {
	Binding            uint32
	PID                int
	Bundle, App, Title string
	Placement
}

// Look is one observation of the live session, taken when no disturbance is
// in progress. Visible holds the keys of the spaces shown at this look or the
// previous one: the only spaces whose windows the user can have touched.
type Look struct {
	Windows []Seen
	Visible map[string]bool
}

type Kind int

const (
	Adopt   Kind = iota // the arrangement follows the screen
	Owe                 // a change nobody can have made by hand: put it back
	Release             // an owed window is back in place
	GiveUp              // a repair was attempted and did not take: adopt where it is
	Repair              // an owed window is out of place: act where possible
)

// Decision is what a look means for one window.
type Decision struct {
	Kind         Kind
	Window       int64 // 0: a window never seen before
	Seen         Seen
	Want         Placement // Repair: where it belongs
	Move, Resize bool      // Repair: what to do at this look
}

// Decide compares each seen window with its record. A window with no record
// or no placement is adopted where it is. A settled window that differs from
// its placement is adopted when its old or new space was visible, and owed
// otherwise. An owed window is released once seen in place, repaired where
// that is possible now, and given up on when a completed attempt did not
// take. Recorded windows the look did not see are left alone.
func Decide(recorded []Recorded, look Look) []Decision {
	byBinding := make(map[uint32]Recorded, len(recorded))
	for _, r := range recorded {
		byBinding[r.Binding] = r
	}
	var out []Decision
	for _, s := range look.Windows {
		r, known := byBinding[s.Binding]
		switch {
		case !known:
			out = append(out, Decision{Kind: Adopt, Seen: s})
		case !r.Placed:
			out = append(out, Decision{Kind: Adopt, Window: r.Window, Seen: s})
		case r.Owed:
			out = append(out, owed(r, s, look.Visible))
		case r.Placement.InPlace(s.Placement):
		case look.Visible[r.Space] || look.Visible[s.Space]:
			out = append(out, Decision{Kind: Adopt, Window: r.Window, Seen: s})
		default:
			out = append(out, Decision{Kind: Owe, Window: r.Window, Seen: s}, owed(r, s, look.Visible))
		}
	}
	return out
}

func owed(r Recorded, s Seen, visible map[string]bool) Decision {
	if r.Placement.InPlace(s.Placement) {
		return Decision{Kind: Release, Window: r.Window, Seen: s}
	}
	wrongSpace := s.Space != r.Space
	if r.Progress == Attempted || (r.Progress == Moved && wrongSpace) {
		return Decision{Kind: GiveUp, Window: r.Window, Seen: s}
	}
	return Decision{
		Kind: Repair, Window: r.Window, Seen: s, Want: r.Placement,
		Move:   wrongSpace,
		Resize: visible[r.Space] && !sameFrame(r.Frame, s.Frame),
	}
}

// Progress is what a repair leaves for the next look: Attempted when the
// frame was written or only the space was wrong, Moved when the window was
// moved and its frame is still to do. ok is false when nothing was done.
func (d Decision) Progress() (p Progress, ok bool) {
	switch {
	case d.Kind != Repair:
		return Untried, false
	case d.Resize, d.Move && sameFrame(d.Want.Frame, d.Seen.Frame):
		return Attempted, true
	case d.Move:
		return Moved, true
	}
	return Untried, false
}
```

- [ ] **Step 4:** all `internal/arrangement` tests pass; vet and gofmt clean. Stage. Commit message 2.

---

### Task 3: the store, placements

**Files:** Create `internal/store/schema.sql`, `internal/store/store.go`, `internal/store/store_test.go`. Modify `go.mod`, `go.sum` (`go get modernc.org/sqlite@latest`).

**Consumes:** `arrangement.Recorded`, `arrangement.Seen`, `arrangement.Decision`, `arrangement.Placement`, `layout.SavedSpace`, `layout.Rect`.

**Produces:** `Open(path string) (*Store, error)`, `Close()`, `Arrangement(displaySet string) (int64, error)`, `SetSpaces(arr int64, spaces []layout.SavedSpace) error`, `Spaces(arr int64) ([]layout.SavedSpace, error)`, `Recorded(arr int64, boot time.Time) ([]arrangement.Recorded, error)`, `Apply(arr int64, boot, at time.Time, cause string, ds []arrangement.Decision) error` (Adopt only in this task; the other kinds in Task 4).

- [ ] **Step 1: schema** (`schema.sql`)

```sql
-- One arrangement per display set (sorted display UUIDs, comma-joined).
CREATE TABLE IF NOT EXISTS arrangement (
  id          INTEGER PRIMARY KEY,
  display_set TEXT NOT NULL UNIQUE
);

-- The spaces an arrangement has had, by space key, with their position.
CREATE TABLE IF NOT EXISTS space (
  arrangement_id INTEGER NOT NULL REFERENCES arrangement(id),
  key            TEXT    NOT NULL,
  display        TEXT    NOT NULL,
  idx            INTEGER NOT NULL,
  PRIMARY KEY (arrangement_id, key)
);

-- A lasting window identity.
CREATE TABLE IF NOT EXISTS window (
  id         INTEGER PRIMARY KEY,
  bundle     TEXT    NOT NULL,
  app        TEXT    NOT NULL,
  title      TEXT    NOT NULL,
  first_seen INTEGER NOT NULL
);

-- The window-server id a window has in one boot (boot time, unix seconds).
CREATE TABLE IF NOT EXISTS binding (
  window_id INTEGER NOT NULL REFERENCES window(id),
  boot      INTEGER NOT NULL,
  wid       INTEGER NOT NULL,
  PRIMARY KEY (boot, wid)
);

-- One look or event that altered the record (at: unix milliseconds).
CREATE TABLE IF NOT EXISTS change (
  id    INTEGER PRIMARY KEY,
  at    INTEGER NOT NULL,
  cause TEXT    NOT NULL,
  note  TEXT    NOT NULL DEFAULT ''
);

-- What a change did to one window: adopted, owed, released, gave up, repaired.
CREATE TABLE IF NOT EXISTS event (
  id        INTEGER PRIMARY KEY,
  change_id INTEGER NOT NULL REFERENCES change(id),
  window_id INTEGER NOT NULL REFERENCES window(id),
  kind      TEXT    NOT NULL
);

-- System-versioned placements: a row is current while closed_by is NULL.
CREATE TABLE IF NOT EXISTS placement (
  id             INTEGER PRIMARY KEY,
  window_id      INTEGER NOT NULL REFERENCES window(id),
  arrangement_id INTEGER NOT NULL REFERENCES arrangement(id),
  space          TEXT    NOT NULL,
  x REAL NOT NULL, y REAL NOT NULL, w REAL NOT NULL, h REAL NOT NULL,
  opened_by      INTEGER NOT NULL REFERENCES change(id),
  closed_by      INTEGER REFERENCES change(id)
);
CREATE UNIQUE INDEX IF NOT EXISTS placement_current
  ON placement(window_id, arrangement_id) WHERE closed_by IS NULL;

-- Windows owed their placement, with how far the last repair got.
CREATE TABLE IF NOT EXISTS owed (
  window_id      INTEGER NOT NULL REFERENCES window(id),
  arrangement_id INTEGER NOT NULL REFERENCES arrangement(id),
  progress       INTEGER NOT NULL DEFAULT 0,
  since          INTEGER NOT NULL REFERENCES change(id),
  PRIMARY KEY (window_id, arrangement_id)
);

-- Periods during which the screen was not intent.
CREATE TABLE IF NOT EXISTS disturbance (
  id    INTEGER PRIMARY KEY,
  kind  TEXT    NOT NULL,
  began INTEGER NOT NULL,
  ended INTEGER
);
```

- [ ] **Step 2: failing tests** (`store_test.go`)

```go
package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/layout"
)

var (
	boot = time.Unix(1790000000, 0)
	t0   = time.UnixMilli(1790000100000)
	full = layout.Rect{X: 0, Y: 30, W: 1280, H: 1410}
	half = layout.Rect{X: 0, Y: 517, W: 735, H: 923}
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func seen(wid uint32, space string, f layout.Rect) arrangement.Seen {
	return arrangement.Seen{Binding: wid, PID: 9, Bundle: "com.example", App: "Example", Title: "t",
		Placement: arrangement.Placement{Space: space, Frame: f}}
}

func adoptNew(t *testing.T, s *Store, arr int64, wid uint32, space string, f layout.Rect) arrangement.Recorded {
	t.Helper()
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Adopt, Seen: seen(wid, space, f)}}); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Recorded(arr, boot)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Binding == wid {
			return r
		}
	}
	t.Fatalf("window %d not recorded", wid)
	return arrangement.Recorded{}
}

func TestArrangementIsOnePerDisplaySet(t *testing.T) {
	s := open(t)
	a, _ := s.Arrangement("D1,D2")
	b, _ := s.Arrangement("D1,D2")
	c, _ := s.Arrangement("D2")
	if a == 0 || a != b || c == a {
		t.Fatalf("ids: %d %d %d", a, b, c)
	}
}

func TestSpacesUpsert(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	if err := s.SetSpaces(arr, []layout.SavedSpace{{UUID: "A", DisplayUUID: "D1", Index: 0}, {UUID: "B", DisplayUUID: "D1", Index: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSpaces(arr, []layout.SavedSpace{{UUID: "B", DisplayUUID: "D1", Index: 0}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Spaces(arr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("a space no longer present keeps its row: got %+v", got)
	}
	for _, sp := range got {
		if sp.UUID == "B" && sp.Index != 0 {
			t.Fatalf("B's index should have been updated: %+v", sp)
		}
	}
}

func TestAdoptCreatesWindowBindingAndPlacement(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	if r.Window == 0 || !r.Placed || r.Space != "S1" || r.Frame != full || r.Owed {
		t.Fatalf("recorded = %+v", r)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM window`); n != 1 {
		t.Fatalf("windows = %d", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM event WHERE kind = 'adopted'`); n != 1 {
		t.Fatalf("adopted events = %d", n)
	}
}

func TestAdoptAgainClosesTheOldVersion(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	err := s.Apply(arr, boot, t0.Add(time.Minute), "look", []arrangement.Decision{{Kind: arrangement.Adopt, Window: r.Window, Seen: seen(42, "S2", half)}})
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM placement WHERE window_id = ?`, r.Window); n != 2 {
		t.Fatalf("versions = %d, want 2", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM placement WHERE window_id = ? AND closed_by IS NULL`, r.Window); n != 1 {
		t.Fatalf("open versions = %d, want 1", n)
	}
	rs, _ := s.Recorded(arr, boot)
	if len(rs) != 1 || rs[0].Space != "S2" || rs[0].Frame != half {
		t.Fatalf("recorded = %+v", rs)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM window`); n != 1 {
		t.Fatalf("adopting a known window must not create another: %d", n)
	}
}

func TestRecordedIsScopedToBootAndArrangement(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1,D2")
	adoptNew(t, s, arr, 42, "S1", full)
	if rs, _ := s.Recorded(arr, boot.Add(time.Hour)); len(rs) != 0 {
		t.Fatalf("another boot has no bindings: %+v", rs)
	}
	other, _ := s.Arrangement("D2")
	rs, _ := s.Recorded(other, boot)
	if len(rs) != 1 || rs[0].Placed {
		t.Fatalf("bound this boot but not placed in the other arrangement: %+v", rs)
	}
}
```

- [ ] **Step 3:** `go get modernc.org/sqlite@latest`; create `store.go` with stubs (`Open` working, the rest returning zero values) so the tests compile and fail on assertions.

- [ ] **Step 4: implement** (`store.go`)

```go
// Package store keeps the arrangement model in SQLite: windows and their
// bindings, one arrangement per display set, system-versioned placements,
// owed windows, and the journal of changes, events and disturbances.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/layout"
)

//go:embed schema.sql
var schema string

type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Arrangement returns the arrangement for a display set, creating it on first use.
func (s *Store) Arrangement(displaySet string) (int64, error) {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO arrangement(display_set) VALUES (?)`, displaySet); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM arrangement WHERE display_set = ?`, displaySet).Scan(&id)
	return id, err
}

// SetSpaces records the arrangement's spaces as seen now. A space no longer
// present keeps its row, so a placement on it still resolves by position.
func (s *Store) SetSpaces(arr int64, spaces []layout.SavedSpace) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, sp := range spaces {
		if _, err := tx.Exec(`INSERT INTO space(arrangement_id, key, display, idx) VALUES (?, ?, ?, ?)
			ON CONFLICT(arrangement_id, key) DO UPDATE SET display = excluded.display, idx = excluded.idx`,
			arr, sp.UUID, sp.DisplayUUID, sp.Index); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Spaces returns every space the arrangement has had.
func (s *Store) Spaces(arr int64) ([]layout.SavedSpace, error) {
	rows, err := s.db.Query(`SELECT key, display, idx FROM space WHERE arrangement_id = ? ORDER BY display, idx`, arr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []layout.SavedSpace
	for rows.Next() {
		var sp layout.SavedSpace
		if err := rows.Scan(&sp.UUID, &sp.DisplayUUID, &sp.Index); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// Recorded returns the windows bound in this boot with their current
// placement in the arrangement, if any, and whether they are owed.
func (s *Store) Recorded(arr int64, boot time.Time) ([]arrangement.Recorded, error) {
	rows, err := s.db.Query(`
		SELECT w.id, b.wid, p.space, p.x, p.y, p.w, p.h, o.progress
		FROM binding b
		JOIN window w ON w.id = b.window_id
		LEFT JOIN placement p ON p.window_id = w.id AND p.arrangement_id = ?1 AND p.closed_by IS NULL
		LEFT JOIN owed o ON o.window_id = w.id AND o.arrangement_id = ?1
		WHERE b.boot = ?2
		ORDER BY w.id`, arr, boot.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []arrangement.Recorded
	for rows.Next() {
		var r arrangement.Recorded
		var space sql.NullString
		var x, y, w, h sql.NullFloat64
		var progress sql.NullInt64
		if err := rows.Scan(&r.Window, &r.Binding, &space, &x, &y, &w, &h, &progress); err != nil {
			return nil, err
		}
		if space.Valid {
			r.Placed = true
			r.Placement = arrangement.Placement{Space: space.String,
				Frame: layout.Rect{X: x.Float64, Y: y.Float64, W: w.Float64, H: h.Float64}}
		}
		if progress.Valid {
			r.Owed, r.Progress = true, arrangement.Progress(progress.Int64)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Apply records what a look decided, in one transaction and under one
// change. Decisions that alter nothing write nothing.
func (s *Store) Apply(arr int64, boot, at time.Time, cause string, ds []arrangement.Decision) error {
	if !writes(ds) {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO change(at, cause) VALUES (?, ?)`, at.UnixMilli(), cause)
	if err != nil {
		return err
	}
	change, _ := res.LastInsertId()
	for _, d := range ds {
		if err := apply(tx, change, arr, boot, at, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func writes(ds []arrangement.Decision) bool {
	for _, d := range ds {
		if d.Kind != arrangement.Repair {
			return true
		}
		if _, ok := d.Progress(); ok {
			return true
		}
	}
	return false
}

func apply(tx *sql.Tx, change, arr int64, boot, at time.Time, d arrangement.Decision) error {
	win := d.Window
	switch d.Kind {
	case arrangement.Adopt:
		if win == 0 {
			var err error
			if win, err = newWindow(tx, boot, at, d.Seen); err != nil {
				return err
			}
		}
		if err := place(tx, change, arr, win, d.Seen.Placement); err != nil {
			return err
		}
		return event(tx, change, win, "adopted")
	}
	return nil
}

func newWindow(tx *sql.Tx, boot, at time.Time, s arrangement.Seen) (int64, error) {
	res, err := tx.Exec(`INSERT INTO window(bundle, app, title, first_seen) VALUES (?, ?, ?, ?)`,
		s.Bundle, s.App, s.Title, at.UnixMilli())
	if err != nil {
		return 0, err
	}
	win, _ := res.LastInsertId()
	_, err = tx.Exec(`INSERT INTO binding(window_id, boot, wid) VALUES (?, ?, ?)`, win, boot.Unix(), s.Binding)
	return win, err
}

// place closes the window's current placement in the arrangement, if any,
// and opens p.
func place(tx *sql.Tx, change, arr, win int64, p arrangement.Placement) error {
	if _, err := tx.Exec(`UPDATE placement SET closed_by = ? WHERE window_id = ? AND arrangement_id = ? AND closed_by IS NULL`,
		change, win, arr); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO placement(window_id, arrangement_id, space, x, y, w, h, opened_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		win, arr, p.Space, p.Frame.X, p.Frame.Y, p.Frame.W, p.Frame.H, change)
	return err
}

func event(tx *sql.Tx, change, win int64, kind string) error {
	_, err := tx.Exec(`INSERT INTO event(change_id, window_id, kind) VALUES (?, ?, ?)`, change, win, kind)
	return err
}
```

- [ ] **Step 5:** `go test ./internal/store/` passes; whole tree builds, vets, gofmt clean. Stage `internal/store`, `go.mod`, `go.sum`. Commit message 3.

---

### Task 4: the store, owed windows and disturbances

**Files:** Modify `internal/store/store.go`, `internal/store/store_test.go`.

**Produces:** `Apply` handles `Owe`, `Release`, `GiveUp`, `Repair`; `OweAll(arr int64, boot, at time.Time, cause string) (int, error)`; `RefreshTitles(boot time.Time, seen []arrangement.Seen) error`; `type Disturbance struct { ID int64; Kind string; Began time.Time }`; `BeginDisturbance(kind string, at time.Time) (int64, error)`; `EndDisturbance(id int64, at time.Time) error`; `OpenDisturbances() ([]Disturbance, error)`.

- [ ] **Step 1: failing tests** (append to `store_test.go`)

```go
func one(t *testing.T, s *Store, arr int64) arrangement.Recorded {
	t.Helper()
	rs, err := s.Recorded(arr, boot)
	if err != nil || len(rs) != 1 {
		t.Fatalf("recorded = %+v, %v", rs, err)
	}
	return rs[0]
}

func TestOweThenRelease(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Owe, Window: r.Window, Seen: seen(42, "S1", half)}}); err != nil {
		t.Fatal(err)
	}
	if got := one(t, s, arr); !got.Owed || got.Progress != arrangement.Untried || got.Frame != full {
		t.Fatalf("owed keeps its placement: %+v", got)
	}
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Release, Window: r.Window, Seen: seen(42, "S1", full)}}); err != nil {
		t.Fatal(err)
	}
	if got := one(t, s, arr); got.Owed {
		t.Fatalf("released: %+v", got)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM event WHERE kind IN ('owed', 'released')`); n != 2 {
		t.Fatalf("journal events = %d, want 2", n)
	}
}

func TestRepairRecordsProgress(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	want := arrangement.Placement{Space: "S1", Frame: full}
	ds := []arrangement.Decision{
		{Kind: arrangement.Owe, Window: r.Window, Seen: seen(42, "S2", half)},
		{Kind: arrangement.Repair, Window: r.Window, Seen: seen(42, "S2", half), Want: want, Move: true},
	}
	if err := s.Apply(arr, boot, t0, "look", ds); err != nil {
		t.Fatal(err)
	}
	if got := one(t, s, arr); got.Progress != arrangement.Moved {
		t.Fatalf("Progress = %v, want Moved", got.Progress)
	}
}

func TestIdleRepairWritesNothing(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	before := count(t, s, `SELECT COUNT(*) FROM change`)
	idle := arrangement.Decision{Kind: arrangement.Repair, Window: r.Window, Seen: seen(42, "S1", half), Want: arrangement.Placement{Space: "S1", Frame: full}}
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{idle}); err != nil {
		t.Fatal(err)
	}
	if after := count(t, s, `SELECT COUNT(*) FROM change`); after != before {
		t.Fatalf("a repair that did nothing must not write a change: %d -> %d", before, after)
	}
}

func TestGiveUpAdoptsAndClearsOwed(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	ds := []arrangement.Decision{{Kind: arrangement.Owe, Window: r.Window, Seen: seen(42, "S1", half)}}
	if err := s.Apply(arr, boot, t0, "look", ds); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.GiveUp, Window: r.Window, Seen: seen(42, "S1", half)}}); err != nil {
		t.Fatal(err)
	}
	got := one(t, s, arr)
	if got.Owed || got.Frame != half {
		t.Fatalf("given up: adopted where it is and no longer owed: %+v", got)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM event WHERE kind = 'gave up'`); n != 1 {
		t.Fatalf("gave-up events = %d", n)
	}
}

func TestOweAllOwesPlacedWindowsOfThisBootAndResetsProgress(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	a := adoptNew(t, s, arr, 1, "S1", full)
	adoptNew(t, s, arr, 2, "S1", full)
	moved := []arrangement.Decision{
		{Kind: arrangement.Owe, Window: a.Window, Seen: seen(1, "S2", half)},
		{Kind: arrangement.Repair, Window: a.Window, Seen: seen(1, "S2", half), Want: arrangement.Placement{Space: "S1", Frame: full}, Move: true},
	}
	if err := s.Apply(arr, boot, t0, "look", moved); err != nil {
		t.Fatal(err)
	}
	n, err := s.OweAll(arr, boot, t0, "display change")
	if err != nil || n != 2 {
		t.Fatalf("OweAll = %d, %v; want 2", n, err)
	}
	rs, _ := s.Recorded(arr, boot)
	for _, r := range rs {
		if !r.Owed || r.Progress != arrangement.Untried {
			t.Fatalf("every placed window owed afresh: %+v", r)
		}
	}
	if n, _ := s.OweAll(arr, boot.Add(time.Hour), t0, "display change"); n != 0 {
		t.Fatalf("another boot has nothing bound: %d", n)
	}
}

func TestRefreshTitles(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	sn := seen(42, "S1", full)
	sn.Title = "renamed"
	if err := s.RefreshTitles(boot, []arrangement.Seen{sn}); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := s.db.QueryRow(`SELECT title FROM window WHERE id = ?`, r.Window).Scan(&title); err != nil || title != "renamed" {
		t.Fatalf("title = %q, %v", title, err)
	}
}

func TestDisturbanceLifecycle(t *testing.T) {
	s := open(t)
	id, err := s.BeginDisturbance("display", t0)
	if err != nil {
		t.Fatal(err)
	}
	open1, _ := s.OpenDisturbances()
	if len(open1) != 1 || open1[0].ID != id || open1[0].Kind != "display" || !open1[0].Began.Equal(t0) {
		t.Fatalf("open = %+v", open1)
	}
	if err := s.EndDisturbance(id, t0.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if open2, _ := s.OpenDisturbances(); len(open2) != 0 {
		t.Fatalf("still open: %+v", open2)
	}
}
```

- [ ] **Step 2:** add stubs for the new methods (zero values); run and see assertion failures (the `Apply` cases fail because `apply` ignores the kinds).

- [ ] **Step 3: implement.** Replace `apply` and add the rest:

```go
func apply(tx *sql.Tx, change, arr int64, boot, at time.Time, d arrangement.Decision) error {
	win := d.Window
	switch d.Kind {
	case arrangement.Adopt:
		if win == 0 {
			var err error
			if win, err = newWindow(tx, boot, at, d.Seen); err != nil {
				return err
			}
		}
		if err := place(tx, change, arr, win, d.Seen.Placement); err != nil {
			return err
		}
		return event(tx, change, win, "adopted")
	case arrangement.Owe:
		if _, err := tx.Exec(`INSERT OR IGNORE INTO owed(window_id, arrangement_id, progress, since) VALUES (?, ?, 0, ?)`,
			win, arr, change); err != nil {
			return err
		}
		return event(tx, change, win, "owed")
	case arrangement.Release:
		if _, err := tx.Exec(`DELETE FROM owed WHERE window_id = ? AND arrangement_id = ?`, win, arr); err != nil {
			return err
		}
		return event(tx, change, win, "released")
	case arrangement.GiveUp:
		if _, err := tx.Exec(`DELETE FROM owed WHERE window_id = ? AND arrangement_id = ?`, win, arr); err != nil {
			return err
		}
		if err := place(tx, change, arr, win, d.Seen.Placement); err != nil {
			return err
		}
		return event(tx, change, win, "gave up")
	case arrangement.Repair:
		p, ok := d.Progress()
		if !ok {
			return nil
		}
		if _, err := tx.Exec(`UPDATE owed SET progress = ? WHERE window_id = ? AND arrangement_id = ?`, int(p), win, arr); err != nil {
			return err
		}
		return event(tx, change, win, "repaired")
	}
	return nil
}

// OweAll marks every window bound in this boot that has a placement in the
// arrangement as owed, with no repair progress. It returns how many are owed.
func (s *Store) OweAll(arr int64, boot, at time.Time, cause string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO change(at, cause) VALUES (?, ?)`, at.UnixMilli(), cause)
	if err != nil {
		return 0, err
	}
	change, _ := res.LastInsertId()
	if _, err := tx.Exec(`UPDATE owed SET progress = 0 WHERE arrangement_id = ?`, arr); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO owed(window_id, arrangement_id, progress, since)
		SELECT p.window_id, p.arrangement_id, 0, ?1
		FROM placement p JOIN binding b ON b.window_id = p.window_id AND b.boot = ?2
		WHERE p.arrangement_id = ?3 AND p.closed_by IS NULL`, change, boot.Unix(), arr); err != nil {
		return 0, err
	}
	var n int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM owed o JOIN binding b ON b.window_id = o.window_id AND b.boot = ?1
		WHERE o.arrangement_id = ?2`, boot.Unix(), arr).Scan(&n); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE change SET note = ? WHERE id = ?`, fmt.Sprintf("%d windows owed", n), change); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// RefreshTitles records the latest title of each seen window bound in this
// boot. A title is identity for later matching, not a placement, so this
// writes no change.
func (s *Store) RefreshTitles(boot time.Time, seen []arrangement.Seen) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, w := range seen {
		if _, err := tx.Exec(`UPDATE window SET title = ?1
			WHERE id = (SELECT window_id FROM binding WHERE boot = ?2 AND wid = ?3) AND title <> ?1`,
			w.Title, boot.Unix(), w.Binding); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Disturbance is a period during which the screen was not intent.
type Disturbance struct {
	ID    int64
	Kind  string
	Began time.Time
}

func (s *Store) BeginDisturbance(kind string, at time.Time) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO disturbance(kind, began) VALUES (?, ?)`, kind, at.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) EndDisturbance(id int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE disturbance SET ended = ? WHERE id = ?`, at.UnixMilli(), id)
	return err
}

// OpenDisturbances returns disturbances with no end: after a restart, ones
// the agent was in the middle of.
func (s *Store) OpenDisturbances() ([]Disturbance, error) {
	rows, err := s.db.Query(`SELECT id, kind, began FROM disturbance WHERE ended IS NULL ORDER BY began`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Disturbance
	for rows.Next() {
		var d Disturbance
		var began int64
		if err := rows.Scan(&d.ID, &d.Kind, &began); err != nil {
			return nil, err
		}
		d.Began = time.UnixMilli(began)
		out = append(out, d)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4:** all store tests pass; tree builds, vets, gofmt clean. Stage. Commit message 4.

---

### Task 5: the agent looks, adopts and repairs through the model

**Files:** Modify `cmd/spacekeeper/watch.go`, `cmd/spacekeeper/main.go` (`-interval` default and help text), `internal/skylight/skylight.go` (`SessionLocked`), `internal/layout/layout.go` and its tests (remove `FrameDebt`, `FrameDebts`, `Settle`, `ReferenceFor`, `LatestBefore`, `SameDisplays` and their tests; delete `internal/layout/framedebt_test.go`).

**Consumes:** `arrangement.Decide`, `arrangement.Look`, `arrangement.Seen`, `arrangement.Decision`, `store.*` from Tasks 3 and 4; existing `gather`, `snapshot`, `layout.ResolveSpaces`, `layout.OverviewOpen`, `skylight.MoveWindowsToSpace`, `skylight.SetWindowFrame`, `sysevents`, `settle`.

- [ ] **Step 1: `skylight.SessionLocked`.** In the cgo preamble of `skylight.go`, next to `sk_active_space`:

```c
static bool sk_session_locked(void) {
	CFDictionaryRef d = CGSessionCopyCurrentDictionary();
	if (!d) return false;
	CFTypeRef v = CFDictionaryGetValue(d, CFSTR("CGSSessionScreenIsLocked"));
	bool locked = v && CFGetTypeID(v) == CFBooleanGetTypeID() && CFBooleanGetValue((CFBooleanRef)v);
	CFRelease(d);
	return locked;
}
```

and in Go:

```go
// SessionLocked reports whether the login session's screen is locked.
func SessionLocked() bool { return bool(C.sk_session_locked()) }
```

(`#include <stdbool.h>` if the preamble lacks it.)

- [ ] **Step 2: remove the superseded layout code** listed under Files, with its tests. `layout.OverviewOpen`, `FirstStartOfBoot`, `DefaultRestoreIndex`, `HighWaterIndex`, `Match`, `ResolveSpaces`, `SpaceDeficits`, `Signature`, `Stats` stay (snapshots and login convergence use them).

- [ ] **Step 3: watcher.** In `watch.go`:

  - Imports gain `sort`, `strings`, `internal/arrangement`, `internal/store`; `os` goes if unused.
  - `watcher` fields: remove `debts`; add

    ```go
    store       *store.Store
    burst       int64           // open display disturbance in the store, 0 if none
    oweNext     bool            // a disturbance ended while the agent was down: owe at the next look
    prevVisible map[string]bool // space keys visible at the previous look
    asleep      bool            // screens or system asleep: the timer still fires in dark wake
    ticks       int
    ```

  - `watchCmd` opens the store before starting the loop:

    ```go
    st, err := store.Open(filepath.Join(dataDir(), "spacekeeper.db"))
    if err != nil {
    	return err
    }
    defer st.Close()
    w := &watcher{opt: o, store: st, displays: settle.New(displayQuiet), spaces: settle.New(spaceQuiet)}
    ```

  - `loop`: replace `w.recoverPending()` with `w.recoverDisturbances()`; after `w.startBoot()` call `w.look("startup")` then `w.save("startup")`. The tick case becomes

    ```go
    case <-tick.C:
    	w.look("interval")
    	if w.ticks++; w.ticks%savesEvery == 0 {
    		w.save("interval")
    	}
    ```

    with `const savesEvery = 3` beside the other timing constants (snapshots keep their 3-minute cadence while the look runs every minute). The space-change case becomes `w.spaces.Close(); w.look("space change"); w.save("space change")`.

  - `handle`:

    ```go
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
    case sysevents.ScreensWake, sysevents.SystemWake:
    	w.asleep = false
    	log.Printf("%s", e.Kind)
    ```

    (`SpaceChanged` and `AppLaunched` unchanged.)

  - Delete `pendingPath`, `writePending`, `recoverPending`, `debtNames`, `payDebts`, and the body of `displaysSettled`; replace with:

    ```go
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
    	sysevents.OnMain(func() {
    		if err := w.lookOnMain(trigger); err != nil {
    			log.Printf("look (%s) failed: %v", trigger, err)
    		}
    	})
    }

    func (w *watcher) lookOnMain(trigger string) error {
    	s, err := gather()
    	if err != nil {
    		return err
    	}
    	if layout.OverviewOpen(s.windows) || skylight.SessionLocked() {
    		return nil
    	}
    	boot, now := bootTime(), time.Now()
    	arr, err := w.store.Arrangement(displaySet(s.spaces))
    	if err != nil {
    		return err
    	}
    	if err := w.store.SetSpaces(arr, s.spaces); err != nil {
    		return err
    	}
    	if w.oweNext {
    		n, err := w.store.OweAll(arr, boot, now, trigger)
    		if err != nil {
    			return err
    		}
    		w.oweNext = false
    		log.Printf("%s: %d window(s) owed their placement", trigger, n)
    	}
    	recorded, err := w.store.Recorded(arr, boot)
    	if err != nil {
    		return err
    	}
    	visible := visibleKeys(s)
    	both := make(map[string]bool, len(visible)+len(w.prevVisible))
    	for k := range visible {
    		both[k] = true
    	}
    	for k := range w.prevVisible {
    		both[k] = true
    	}
    	seen := seenWindows(s)
    	ds := arrangement.Decide(recorded, arrangement.Look{Windows: seen, Visible: both})
    	if err := w.repair(s, arr, ds); err != nil {
    		return err
    	}
    	if err := w.store.Apply(arr, boot, now, trigger, ds); err != nil {
    		return err
    	}
    	if strings.Contains(trigger, "sleep") || strings.Contains(trigger, "power off") {
    		if err := w.store.RefreshTitles(boot, seen); err != nil {
    			return err
    		}
    	}
    	w.prevVisible = visible
    	logDecisions(trigger, ds)
    	return nil
    }

    // repair acts on Repair decisions: window-server moves first, then frame
    // writes for windows whose space is visible. A space that no longer
    // exists, or a frame write the app refused, clears that part of the
    // decision so it leaves no progress behind.
    func (w *watcher) repair(s *snapshot, arr int64, ds []arrangement.Decision) error {
    	saved, err := w.store.Spaces(arr)
    	if err != nil {
    		return err
    	}
    	resolved := layout.ResolveSpaces(saved, s.displays)
    	moves := make(map[uint64][]uint32)
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
    		if d.Move {
    			moves[target] = append(moves[target], d.Seen.Binding)
    		}
    	}
    	for target, wids := range moves {
    		if err := skylight.MoveWindowsToSpace(wids, target); err != nil {
    			log.Printf("moving %d window(s) to space %d: %v", len(wids), target, err)
    		}
    	}
    	if len(moves) > 0 {
    		time.Sleep(500 * time.Millisecond) // the move is asynchronous
    	}
    	for i := range ds {
    		d := &ds[i]
    		if d.Kind != arrangement.Repair || !d.Resize {
    			continue
    		}
    		f := d.Want.Frame
    		if err := skylight.SetWindowFrame(d.Seen.PID, d.Seen.Binding, f.X, f.Y, f.W, f.H); err != nil {
    			d.Resize = false
    		}
    	}
    	return nil
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
    	for _, d := range ds {
    		switch d.Kind {
    		case arrangement.Adopt:
    			adopted++
    		case arrangement.Owe:
    			owed++
    			touched = append(touched, name(d.Seen))
    		case arrangement.Release:
    			released++
    		case arrangement.Repair:
    			if _, ok := d.Progress(); ok {
    				repaired++
    				touched = append(touched, name(d.Seen))
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
    ```

    (`fmt` joins the imports.)

  - The first look after login convergence ends needs no code: `w.boot` becomes nil and the next tick looks.

- [ ] **Step 4: `main.go`.** `-interval` default `time.Minute`; help text `-interval D  watch: look interval (default 1m); snapshots are saved every third look`.

- [ ] **Step 5:** `go build ./... && go vet ./... && go test ./...`, gofmt clean.

- [ ] **Step 6: verify on this machine** (orchestrator; visible effects are announced to the user first: one display-change burst, one visible window resized for a few seconds, no space switches). `make install`, then with `sqlite3 ~/.config/spacekeeper/spacekeeper.db`:

  1. After the startup look: `SELECT COUNT(*) FROM window`, `SELECT COUNT(*) FROM placement WHERE closed_by IS NULL` equal the number of non-fullscreen windows in `spacekeeper inspect`; `watch.log` shows `look (startup): adopted N`.
  2. Calm adoption: resize one window on a visible space through the Accessibility one-shot (launchd job); after the next look `watch.log` shows `adopted 1` and that window has two placement versions.
  3. Unseen change: with a crafted layout file, `spacekeeper restore -f` moves one window on a space that is not visible to another space that is not visible; after the next look `watch.log` shows `owed 1, repaired 1: <app | title>`, and after the following look `released 1`; `inspect` shows it back on its space.
  4. Display disturbance: nudge the built-in display origin to open a burst, shrink a visible window through the one-shot during the burst; at settle `watch.log` shows `display change: N window(s) owed their placement` and `repaired 1`, the next look `released`; the window is back at its frame; `SELECT COUNT(*) FROM owed` returns 0 and `SELECT kind, ended IS NOT NULL FROM disturbance ORDER BY id DESC LIMIT 1` returns `display|1`.

- [ ] **Step 7:** stage `cmd/spacekeeper`, `internal/skylight/skylight.go`, `internal/layout`. Commit message 5.

---

### Task 6: docs

**Files:** `README.md`, `TODO.md`, this plan, the spec.

- [ ] README "The agent": replace the wake-restore and frame-debt bullets with the arrangement model in four sentences (arrangement per display set; looks every minute and at space changes adopt calm changes to windows on visible spaces; a display change owes every window its placement, moved at once and resized when its space is visible; one that cannot be repaired is released and logged). State that snapshots and login convergence remain the login path for now. Add the database path.
- [ ] TODO: remove "Verify frame debt on a real wake"; add "Slice 2: bind by matching at login and app relaunch, seed from the newest snapshot, remove snapshots and login convergence" and "Slice 3: `log`, `undo`, `owe`, 90-day pruning"; add "Verify on a real wake: `display change: N window(s) owed`, repairs as spaces are visited, no `released unrepaired`".
- [ ] Stage README, TODO, `docs/superpowers/specs/2026-10-01-arrangement-model.md`, `docs/superpowers/plans/2026-10-01-arrangement-slice1.md`. Commit message 6.

---

## Commit messages (verbatim, in order)

1. `arrangement: placements with a hysteresis band`
2. `arrangement: Decide turns a look into adopt, owe, repair, release and give-up decisions`
3. `store: SQLite arrangement store with versioned placements`
4. `store: owed windows, the event journal, and disturbances`
5. `spacekeeper watch: looks, adoption and repair through the arrangement model`
6. `docs: the arrangement model, slice 1`
