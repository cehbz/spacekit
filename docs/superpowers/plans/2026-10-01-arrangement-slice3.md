# Arrangement model, slice 3 (journal tooling and retention) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The journal the store already keeps becomes usable for diagnosis and repair: `spacekeeper log` shows what changed and why, `spacekeeper undo <change>` reverts one change and has the agent put the windows back, `spacekeeper owe` has the agent put everything back, and history older than 90 days is deleted.

**Architecture:** Three store methods (`Journal`, `Undo`, `Prune`) with tests against a temporary database; three thin CLI commands in `cmd/spacekeeper/main.go`; the agent calls `Prune` at startup and once a day. The commands only write records; the running agent acts on them at its next look.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, existing packages.

**Spec:** `docs/superpowers/specs/2026-10-01-arrangement-model.md`, "Repair tooling" and "Storage" (90-day retention).

## Global Constraints

- Work on `main`. Commit messages are in "Commit messages" below, in `/tmp/cg-batch-spacekit-765a`; subagents stage and checkpoint with `git stash create`, the orchestrator commits.
- `go build ./... && go vet ./... && go test ./...` clean at every commit; `gofmt -l cmd internal` empty.
- TDD for the store: failing test on an assertion first.
- `internal/store` opens the database with one connection: never call a store method or run a query while a `rows` cursor is open; read rows into a slice, close, then query again.
- `-n` is already the dry-run flag: the log length flag is `-last`.
- Subagents do not run `make install`, `launchctl`, or the built binaries.
- Comments state what exists, tersely; no "new", "now", "old".

---

### Task 1: Journal

**Files:** Modify `internal/store/store.go`, `internal/store/store_test.go`.

**Produces:**

```go
type Event struct {
	Kind       string
	App, Title string
	From, To   *arrangement.Placement // the versions the change closed and opened for this window, if any
}
type Entry struct {
	ID     int64
	At     time.Time
	Cause  string
	Note   string
	Events []Event
}
func (s *Store) Journal(last int) ([]Entry, error)            // the latest `last` changes, oldest first
func (s *Store) Disturbances(last int) ([]Disturbance, error) // the latest `last`, oldest first
```

and `Disturbance` gains `Ended time.Time` (zero while open).

- [ ] **Step 1: failing tests** (append to `store_test.go`)

```go
func TestJournalShowsWhatEachChangeDid(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	r := adoptNew(t, s, arr, 42, "S1", full) // change 1: first sighting
	if err := s.Apply(arr, boot, t0.Add(time.Minute), "look", []arrangement.Decision{{Kind: arrangement.Adopt, Window: r.Window, Seen: seen(42, "S2", half)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(arr, boot, t0.Add(2*time.Minute), "look", []arrangement.Decision{{Kind: arrangement.Owe, Window: r.Window, Seen: seen(42, "S2", full)}}); err != nil {
		t.Fatal(err)
	}
	j, err := s.Journal(10)
	if err != nil || len(j) != 3 {
		t.Fatalf("Journal = %d entries, %v", len(j), err)
	}
	if j[0].ID >= j[1].ID || j[1].ID >= j[2].ID {
		t.Fatalf("oldest first: %v %v %v", j[0].ID, j[1].ID, j[2].ID)
	}
	first := j[0].Events[0]
	if first.Kind != "adopted" || first.App != "Example" || first.From != nil || first.To == nil || first.To.Space != "S1" {
		t.Fatalf("first sighting has no From: %+v", first)
	}
	second := j[1].Events[0]
	if second.From == nil || second.From.Space != "S1" || second.From.Frame != full || second.To == nil || second.To.Space != "S2" || second.To.Frame != half {
		t.Fatalf("adoption shows from and to: %+v", second)
	}
	if !j[1].At.Equal(t0.Add(time.Minute)) || j[1].Cause != "look" {
		t.Fatalf("entry = %+v", j[1])
	}
	third := j[2].Events[0]
	if third.Kind != "owed" || third.From != nil || third.To != nil {
		t.Fatalf("owing changes no placement: %+v", third)
	}
	if last, _ := s.Journal(1); len(last) != 1 || last[0].ID != j[2].ID {
		t.Fatalf("Journal(1) = %+v", last)
	}
}

func TestDisturbancesListsEndedAndOpen(t *testing.T) {
	s := open(t)
	a, _ := s.BeginDisturbance("display", t0)
	s.EndDisturbance(a, t0.Add(12*time.Second))
	s.BeginDisturbance("display", t0.Add(time.Hour))
	ds, err := s.Disturbances(10)
	if err != nil || len(ds) != 2 {
		t.Fatalf("Disturbances = %+v, %v", ds, err)
	}
	if !ds[0].Ended.Equal(t0.Add(12*time.Second)) || !ds[1].Ended.IsZero() {
		t.Fatalf("ended times: %+v", ds)
	}
}
```

- [ ] **Step 2:** add the types, `Ended` on `Disturbance`, and stubs returning `nil, nil`; run and see assertion failures.

- [ ] **Step 3: implement**

```go
// Event is what a change did to one window. From and To are the placement
// versions the change closed and opened for it, when it did.
type Event struct {
	Kind       string
	App, Title string
	From, To   *arrangement.Placement
}

// Entry is one change in the journal.
type Entry struct {
	ID     int64
	At     time.Time
	Cause  string
	Note   string
	Events []Event
}

// Journal returns the latest changes, oldest first.
func (s *Store) Journal(last int) ([]Entry, error) {
	rows, err := s.db.Query(`SELECT id, at, cause, note FROM change ORDER BY id DESC LIMIT ?`, last)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for rows.Next() {
		var e Entry
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Cause, &e.Note); err != nil {
			rows.Close()
			return nil, err
		}
		e.At = time.UnixMilli(at)
		out = append(out, e)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	for i := range out {
		if out[i].Events, err = s.events(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) events(change int64) ([]Event, error) {
	rows, err := s.db.Query(`SELECT e.kind, w.app, w.title, e.window_id FROM event e JOIN window w ON w.id = e.window_id
		WHERE e.change_id = ? ORDER BY e.id`, change)
	if err != nil {
		return nil, err
	}
	var out []Event
	var wins []int64
	for rows.Next() {
		var e Event
		var win int64
		if err := rows.Scan(&e.Kind, &e.App, &e.Title, &win); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
		wins = append(wins, win)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].From, err = s.version(`closed_by`, change, wins[i]); err != nil {
			return nil, err
		}
		if out[i].To, err = s.version(`opened_by`, change, wins[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// version returns the placement of a window that a change closed or opened
// (column is closed_by or opened_by), or nil.
func (s *Store) version(column string, change, win int64) (*arrangement.Placement, error) {
	var p arrangement.Placement
	err := s.db.QueryRow(`SELECT space, x, y, w, h FROM placement WHERE window_id = ? AND `+column+` = ?`, win, change).
		Scan(&p.Space, &p.Frame.X, &p.Frame.Y, &p.Frame.W, &p.Frame.H)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Disturbances returns the latest disturbances, oldest first. Ended is zero
// for one still open.
func (s *Store) Disturbances(last int) ([]Disturbance, error) {
	rows, err := s.db.Query(`SELECT id, kind, began, COALESCE(ended, 0) FROM disturbance ORDER BY id DESC LIMIT ?`, last)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Disturbance
	for rows.Next() {
		var d Disturbance
		var began, ended int64
		if err := rows.Scan(&d.ID, &d.Kind, &began, &ended); err != nil {
			return nil, err
		}
		d.Began = time.UnixMilli(began)
		if ended != 0 {
			d.Ended = time.UnixMilli(ended)
		}
		out = append(out, d)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
```

- [ ] **Step 4:** tests pass; vet, gofmt clean. Stage, checkpoint. Commit message 1.

---

### Task 2: Undo

**Files:** Modify `internal/store/store.go`, `internal/store/store_test.go`.

**Produces:** `func (s *Store) Undo(change int64, at time.Time) (reverted, skipped int, err error)`.

- [ ] **Step 1: failing tests**

```go
func TestUndoRestoresThePriorPlacementAndOwesTheWindow(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	if err := s.Apply(arr, boot, t0.Add(time.Minute), "look", []arrangement.Decision{{Kind: arrangement.Adopt, Window: r.Window, Seen: seen(42, "S2", half)}}); err != nil {
		t.Fatal(err)
	}
	bad := int64(count(t, s, `SELECT MAX(id) FROM change`))
	reverted, skipped, err := s.Undo(bad, t0.Add(2*time.Minute))
	if err != nil || reverted != 1 || skipped != 0 {
		t.Fatalf("Undo = %d, %d, %v", reverted, skipped, err)
	}
	got := one(t, s, arr)
	if got.Space != "S1" || got.Frame != full || !got.Owed || got.Progress != arrangement.Untried {
		t.Fatalf("back at the prior placement and owed: %+v", got)
	}
	j, _ := s.Journal(1)
	if j[0].Cause != "undo" || len(j[0].Events) != 1 || j[0].Events[0].Kind != "undone" {
		t.Fatalf("the undo is journaled: %+v", j[0])
	}
}

func TestUndoLeavesWindowsThatChangedAgain(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	s.Apply(arr, boot, t0.Add(time.Minute), "look", []arrangement.Decision{{Kind: arrangement.Adopt, Window: r.Window, Seen: seen(42, "S2", half)}})
	bad := int64(count(t, s, `SELECT MAX(id) FROM change`))
	s.Apply(arr, boot, t0.Add(2*time.Minute), "look", []arrangement.Decision{{Kind: arrangement.Adopt, Window: r.Window, Seen: seen(42, "S3", full)}})
	before := count(t, s, `SELECT COUNT(*) FROM change`)
	reverted, skipped, err := s.Undo(bad, t0.Add(3*time.Minute))
	if err != nil || reverted != 0 || skipped != 1 {
		t.Fatalf("Undo = %d, %d, %v", reverted, skipped, err)
	}
	if got := one(t, s, arr); got.Space != "S3" || got.Owed {
		t.Fatalf("a later placement stands: %+v", got)
	}
	if after := count(t, s, `SELECT COUNT(*) FROM change`); after != before {
		t.Fatalf("an undo that reverts nothing writes nothing: %d -> %d", before, after)
	}
}

func TestUndoOfAFirstSightingLeavesNoPlacement(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	adoptNew(t, s, arr, 42, "S1", full)
	reverted, _, err := s.Undo(1, t0.Add(time.Minute))
	if err != nil || reverted != 1 {
		t.Fatalf("Undo = %d, %v", reverted, err)
	}
	if got := one(t, s, arr); got.Placed {
		t.Fatalf("no earlier placement to return to: %+v", got)
	}
}

func TestUndoOfAnUnknownChangeIsAnError(t *testing.T) {
	s := open(t)
	if _, _, err := s.Undo(99, t0); err == nil {
		t.Fatal("want an error")
	}
}
```

- [ ] **Step 2:** stub returning `0, 0, nil`; run; assertion failures.

- [ ] **Step 3: implement**

```go
// Undo reverts the placements a change opened. A window whose placement from
// that change is still current gets back the one the change closed, or none
// if it had none, and is owed so the agent puts it there. A window whose
// placement has changed again since is skipped. An undo that reverts nothing
// writes nothing.
func (s *Store) Undo(change int64, at time.Time) (reverted, skipped int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM change WHERE id = ?`, change).Scan(&exists); err != nil {
		return 0, 0, err
	}
	if exists == 0 {
		return 0, 0, fmt.Errorf("no change %d", change)
	}
	type version struct {
		id, win, arr int64
		current      bool
	}
	rows, err := tx.Query(`SELECT id, window_id, arrangement_id, closed_by IS NULL FROM placement WHERE opened_by = ?`, change)
	if err != nil {
		return 0, 0, err
	}
	var vs []version
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.id, &v.win, &v.arr, &v.current); err != nil {
			rows.Close()
			return 0, 0, err
		}
		vs = append(vs, v)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	var undo int64
	for _, v := range vs {
		if !v.current {
			skipped++
			continue
		}
		if undo == 0 {
			res, err := tx.Exec(`INSERT INTO change(at, cause, note) VALUES (?, 'undo', ?)`, at.UnixMilli(), fmt.Sprintf("of change %d", change))
			if err != nil {
				return 0, 0, err
			}
			undo, _ = res.LastInsertId()
		}
		if _, err := tx.Exec(`UPDATE placement SET closed_by = ? WHERE id = ?`, undo, v.id); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(`INSERT INTO placement(window_id, arrangement_id, space, x, y, w, h, opened_by)
			SELECT window_id, arrangement_id, space, x, y, w, h, ? FROM placement
			WHERE window_id = ? AND arrangement_id = ? AND closed_by = ?`, undo, v.win, v.arr, change); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(`INSERT INTO owed(window_id, arrangement_id, progress, since) VALUES (?, ?, 0, ?)
			ON CONFLICT(window_id, arrangement_id) DO UPDATE SET progress = 0`, v.win, v.arr, undo); err != nil {
			return 0, 0, err
		}
		if err := event(tx, undo, v.win, "undone"); err != nil {
			return 0, 0, err
		}
		reverted++
	}
	if reverted == 0 {
		return 0, skipped, nil
	}
	return reverted, skipped, tx.Commit()
}
```

Note for the first-sighting test: `Recorded` reports a window bound in this boot with no open placement as `Placed == false`, and the `owed` row is harmless there.

- [ ] **Step 4:** tests pass; vet, gofmt clean. Stage, checkpoint. Commit message 2.

---

### Task 3: Prune

**Files:** Modify `internal/store/store.go`, `internal/store/store_test.go`.

**Produces:** `func (s *Store) Prune(cutoff, boot time.Time) (int, error)` — returns the closed placement versions deleted.

- [ ] **Step 1: failing test**

```go
func TestPruneDropsOldHistoryAndKeepsWhatIsCurrent(t *testing.T) {
	s := open(t)
	arr, _ := s.Arrangement("D1")
	old := t0.Add(-100 * 24 * time.Hour)
	oldBoot := boot.Add(-100 * 24 * time.Hour)

	// A window of this boot with one stale version and a current one.
	if err := s.Apply(arr, boot, old, "look", []arrangement.Decision{{Kind: arrangement.Adopt, Seen: seen(1, "S1", full)}}); err != nil {
		t.Fatal(err)
	}
	live := one(t, s, arr)
	if err := s.Apply(arr, boot, old.Add(time.Hour), "look", []arrangement.Decision{{Kind: arrangement.Adopt, Window: live.Window, Seen: seen(1, "S2", half)}}); err != nil {
		t.Fatal(err)
	}
	// A window last bound in a boot long ago.
	if err := s.Apply(arr, oldBoot, old, "look", []arrangement.Decision{{Kind: arrangement.Adopt, Seen: seen(2, "S1", full)}}); err != nil {
		t.Fatal(err)
	}
	d, _ := s.BeginDisturbance("display", old)
	s.EndDisturbance(d, old.Add(time.Minute))
	s.BeginDisturbance("display", t0)

	n, err := s.Prune(t0.Add(-90*24*time.Hour), boot)
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v; want the one closed stale version", n, err)
	}
	if got := one(t, s, arr); got.Space != "S2" || got.Frame != half {
		t.Fatalf("the current placement survives: %+v", got)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM window`); n != 1 {
		t.Fatalf("the long-gone window is deleted: %d windows", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM placement`); n != 1 {
		t.Fatalf("placements = %d, want 1", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM change`); n != 1 {
		t.Fatalf("only the change the current placement was opened by remains: %d", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM event`); n != 0 {
		t.Fatalf("events of pruned changes are deleted: %d", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM disturbance`); n != 1 {
		t.Fatalf("the ended stale disturbance is deleted, the open one kept: %d", n)
	}
	if again, _ := s.Prune(t0.Add(-90*24*time.Hour), boot); again != 0 {
		t.Fatalf("a second prune finds nothing: %d", again)
	}
}
```

- [ ] **Step 2:** stub returning `0, nil`; run; assertion failure.

- [ ] **Step 3: implement**

```go
// Prune deletes history older than the cutoff: windows last bound in a boot
// that began before it and not bound in the current boot, closed placement
// versions whose closing change predates it, the events of changes that old,
// changes nothing refers to any more, and disturbances that ended before it.
// It returns the number of closed placement versions deleted.
func (s *Store) Prune(cutoff, boot time.Time) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ms, sec := cutoff.UnixMilli(), cutoff.Unix()
	gone := `SELECT w.id FROM window w WHERE w.first_seen < ?1 AND NOT EXISTS
		(SELECT 1 FROM binding b WHERE b.window_id = w.id AND (b.boot = ?2 OR b.boot >= ?3))`
	for _, q := range []string{
		`DELETE FROM owed WHERE window_id IN (` + gone + `)`,
		`DELETE FROM event WHERE window_id IN (` + gone + `)`,
		`DELETE FROM placement WHERE window_id IN (` + gone + `)`,
		`DELETE FROM binding WHERE window_id IN (` + gone + `)`,
	} {
		if _, err := tx.Exec(q, ms, boot.Unix(), sec); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM window WHERE first_seen < ?1 AND NOT EXISTS (SELECT 1 FROM binding b WHERE b.window_id = window.id)`, ms); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM placement WHERE closed_by IN (SELECT id FROM change WHERE at < ?)`, ms)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.Exec(`DELETE FROM event WHERE change_id IN (SELECT id FROM change WHERE at < ?)`, ms); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM change WHERE at < ?
		AND id NOT IN (SELECT opened_by FROM placement)
		AND id NOT IN (SELECT closed_by FROM placement WHERE closed_by IS NOT NULL)
		AND id NOT IN (SELECT since FROM owed)
		AND id NOT IN (SELECT change_id FROM event)`, ms); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM disturbance WHERE ended IS NOT NULL AND ended < ?`, ms); err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}
```

In the test the long-gone window's placement is open (never closed), so it is removed by the window deletion and is not counted in `n`; `n` counts the live window's stale closed version.

- [ ] **Step 4:** tests pass; vet, gofmt clean. Stage, checkpoint. Commit message 3.

---

### Task 4: commands and daily pruning

**Files:** Modify `cmd/spacekeeper/main.go`, `cmd/spacekeeper/watch.go`. Create `cmd/spacekeeper/journal.go`.

**Consumes:** `store.Journal`, `store.Disturbances`, `store.Undo`, `store.Prune`, `store.OweAll`, `store.Arrangement`; `gather`, `displaySet`, `bootTime`, `dataDir`.

- [ ] **Step 1: `journal.go`**

```go
package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/store"
)

func openStore() (*store.Store, error) {
	return store.Open(filepath.Join(dataDir(), "spacekeeper.db"))
}

// logCmd prints the latest changes and disturbances, oldest first: one line
// each, and with verbose one line per window under each change.
func logCmd(last int, verbose bool) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	entries, err := st.Journal(last)
	if err != nil {
		return err
	}
	ds, err := st.Disturbances(last)
	if err != nil {
		return err
	}
	type line struct {
		at   time.Time
		text string
	}
	var lines []line
	for _, e := range entries {
		text := fmt.Sprintf("%s  #%-4d %-22s %s", e.At.Format("2006-01-02 15:04:05"), e.ID, e.Cause, summary(e))
		if e.Note != "" {
			text += "  (" + e.Note + ")"
		}
		if verbose {
			for _, ev := range e.Events {
				text += "\n      " + eventLine(ev)
			}
		}
		lines = append(lines, line{e.At, text})
	}
	for _, d := range ds {
		if len(entries) > 0 && d.Began.Before(entries[0].At) {
			continue
		}
		dur := "still open"
		if !d.Ended.IsZero() {
			dur = d.Ended.Sub(d.Began).Round(time.Second).String()
		}
		lines = append(lines, line{d.Began, fmt.Sprintf("%s  %s disturbance, %s", d.Began.Format("2006-01-02 15:04:05"), d.Kind, dur)})
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].at.Before(lines[j].at) })
	for _, l := range lines {
		fmt.Println(l.text)
	}
	return nil
}

// summary counts a change's events by kind, in a fixed order.
func summary(e store.Entry) string {
	counts := map[string]int{}
	for _, ev := range e.Events {
		counts[ev.Kind]++
	}
	var parts []string
	for _, k := range []string{"adopted", "bound", "owed", "repaired", "released", "gave up", "undone"} {
		if counts[k] > 0 {
			parts = append(parts, k+" "+strconv.Itoa(counts[k]))
		}
	}
	return strings.Join(parts, ", ")
}

func eventLine(ev store.Event) string {
	title := ev.Title
	if len(title) > 36 {
		title = title[:36]
	}
	text := fmt.Sprintf("%-9s %s | %s", ev.Kind, ev.App, title)
	if ev.From != nil || ev.To != nil {
		text += "  " + placement(ev.From) + " -> " + placement(ev.To)
	}
	return text
}

func placement(p *arrangement.Placement) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprintf("%.8s %.0f,%.0f %.0fx%.0f", p.Space, p.Frame.X, p.Frame.Y, p.Frame.W, p.Frame.H)
}

// undoCmd reverts one change; the running agent puts the windows back at its
// next look.
func undoCmd(arg string) error {
	id, err := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64)
	if err != nil {
		return fmt.Errorf("undo needs a change number from `spacekeeper log`")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	reverted, skipped, err := st.Undo(id, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("reverted %d window(s)", reverted)
	if skipped > 0 {
		fmt.Printf(", left %d that changed again since", skipped)
	}
	fmt.Println("; the agent puts them back at its next look")
	return nil
}

// oweCmd marks every window of the current arrangement owed; the running
// agent puts them back at its next look.
func oweCmd() error {
	s, err := gather()
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	arr, err := st.Arrangement(displaySet(s.spaces))
	if err != nil {
		return err
	}
	n, err := st.OweAll(arr, bootTime(), time.Now(), "manual owe")
	if err != nil {
		return err
	}
	fmt.Printf("%d window(s) owed their placement; the agent puts them back at its next look\n", n)
	return nil
}
```

- [ ] **Step 2: `main.go`.** Usage text gains, after the `inspect` lines:

```
  log       list recent changes to the arrangement and disturbances
  undo N    revert change N from the log; the agent puts the windows back
  owe       have the agent put every window back at its recorded placement
```

and under flags:

```
  -last N   log: how many changes to show (default 20)
  -v        log: one line per window under each change
```

Flags: `last := fs.Int("last", 20, "log: changes to show")`, `verbose := fs.Bool("v", false, "log: per-window lines")`. Cases:

```go
	case "log":
		err = logCmd(*last, *verbose)
	case "undo":
		if fs.NArg() != 1 {
			usage()
		}
		err = undoCmd(fs.Arg(0))
	case "owe":
		err = oweCmd()
```

- [ ] **Step 3: daily pruning in `watch.go`.** Constant `retention = 90 * 24 * time.Hour` with the other timing constants. Method:

```go
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
```

Call `w.prune()` in `loop` before `w.recoverDisturbances()`, and in the tick case once a day: `if w.ticks%1440 == 0 { w.prune() }` after the look (1440 one-minute ticks).

- [ ] **Step 4:** build, vet, test, gofmt clean. Stage, checkpoint.

- [ ] **Step 5: verify on this machine** (orchestrator, at a time the user approves; visible effect: one window on the visible space is shortened and comes back). After `make install`:

  1. `spacekeeper log -last 30` lists today's changes, including the display disturbance at 16:27:59, the space-switch adoptions (#16 to #18), the manual undo, and the Chrome relaunch binding; `spacekeeper log -last 3 -v` shows per-window lines with from and to.
  2. Shorten a window on the visible space with the Accessibility one-shot and wait for the look that adopts it; `spacekeeper log -last 1 -v` shows it with from and to. Run `spacekeeper undo <that change>`; the next look repairs the window and the one after releases it; the window is back at its frame.
  3. `spacekeeper owe` prints the number owed; the next look logs `released N` with nothing repaired.
  4. `watch.log` shows no prune line (nothing is 90 days old).

- [ ] **Step 6:** Commit message 4.

---

### Task 5: docs

- [ ] README: add `log`, `undo N`, `owe` to the command block with one line each, and a sentence in "The agent" that history older than 90 days is deleted. TODO: remove the slice 3 item. Spec: no change.
- [ ] Commit message 5 (with this plan).

---

## Commit messages (verbatim, in order)

1. `store: the journal read back as changes with their events, and disturbances`
2. `store: undo one change and owe the windows it touched`
3. `store: prune history older than a cutoff`
4. `spacekeeper: log, undo and owe commands; the agent prunes history past 90 days`
5. `docs: the journal commands`
