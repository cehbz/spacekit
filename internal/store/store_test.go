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
