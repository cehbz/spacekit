package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/layout"
	"strings"
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

// arrangementWith returns the arrangement for a display set with the given
// space keys recorded, as a decide records them before placing windows.
func arrangementWith(t *testing.T, s *Store, displaySet string, keys ...string) int64 {
	t.Helper()
	arr, err := s.Arrangement(displaySet)
	if err != nil {
		t.Fatal(err)
	}
	spaces := make([]layout.SavedSpace, len(keys))
	for i, k := range keys {
		spaces[i] = layout.SavedSpace{UUID: k, DisplayUUID: "D1", Index: i}
	}
	if err := s.SetSpaces(arr, spaces); err != nil {
		t.Fatal(err)
	}
	return arr
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
	arr := arrangementWith(t, s, "D1", "S1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	if r.Window == 0 || !r.Placed || r.Space != "S1" || r.Frame != full || r.Dirty {
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
	arr := arrangementWith(t, s, "D1", "S1", "S2")
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
	arr := arrangementWith(t, s, "D1,D2", "S1")
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

func TestDirtyThenClean(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Dirty, Window: r.Window, Seen: seen(42, "S1", half)}}); err != nil {
		t.Fatal(err)
	}
	if got := one(t, s, arr); !got.Dirty || got.Progress != arrangement.Untried || got.Frame != full {
		t.Fatalf("dirty keeps its placement: %+v", got)
	}
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Clean, Window: r.Window, Seen: seen(42, "S1", full)}}); err != nil {
		t.Fatal(err)
	}
	if got := one(t, s, arr); got.Dirty {
		t.Fatalf("clean: %+v", got)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM event WHERE kind IN ('dirty', 'clean')`); n != 2 {
		t.Fatalf("journal events = %d, want 2", n)
	}
}

func TestPutRecordsProgress(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1", "S2")
	r := adoptNew(t, s, arr, 42, "S1", full)
	want := arrangement.Placement{Space: "S1", Frame: full}
	ds := []arrangement.Decision{
		{Kind: arrangement.Dirty, Window: r.Window, Seen: seen(42, "S2", half)},
		{Kind: arrangement.Put, Window: r.Window, Seen: seen(42, "S2", half), Want: want, Move: true, Resize: true},
	}
	if err := s.Apply(arr, boot, t0, "look", ds); err != nil {
		t.Fatal(err)
	}
	if got := one(t, s, arr); got.Progress != arrangement.Attempted {
		t.Fatalf("Progress = %v, want Attempted", got.Progress)
	}
}

func TestIdlePutWritesNothing(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	before := count(t, s, `SELECT COUNT(*) FROM change`)
	idle := arrangement.Decision{Kind: arrangement.Put, Window: r.Window, Seen: seen(42, "S1", half), Want: arrangement.Placement{Space: "S1", Frame: full}}
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{idle}); err != nil {
		t.Fatal(err)
	}
	if after := count(t, s, `SELECT COUNT(*) FROM change`); after != before {
		t.Fatalf("a put that did nothing must not write a change: %d -> %d", before, after)
	}
}

func TestFailedKeepsTheTarget(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	ds := []arrangement.Decision{{Kind: arrangement.Dirty, Window: r.Window, Seen: seen(42, "S1", half)}}
	if err := s.Apply(arr, boot, t0, "look", ds); err != nil {
		t.Fatal(err)
	}
	failed := arrangement.Decision{Kind: arrangement.Failed, Window: r.Window, Seen: seen(42, "S1", half),
		Want: arrangement.Placement{Space: "S1", Frame: full}}
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{failed}); err != nil {
		t.Fatal(err)
	}
	got := one(t, s, arr)
	if !got.Dirty || got.Space != "S1" || got.Frame != full {
		t.Fatalf("a failed put leaves the target and the dirty mark: %+v", got)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM event WHERE kind = 'failed'`); n != 1 {
		t.Fatalf("failed events = %d", n)
	}
}

// A failed put records an attempt, so the window leaves the untried marks.
func TestFailedMarksAnAttempt(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	failed := []arrangement.Decision{
		{Kind: arrangement.Dirty, Window: r.Window, Seen: seen(42, "S1", half)},
		{Kind: arrangement.Failed, Window: r.Window, Seen: seen(42, "S1", half), Want: arrangement.Placement{Space: "S1", Frame: full}},
	}
	if err := s.Apply(arr, boot, t0, "look", failed); err != nil {
		t.Fatal(err)
	}
	if got := one(t, s, arr); !got.Dirty || got.Progress != arrangement.Attempted {
		t.Fatalf("after a failed put: %+v, want dirty with an attempt", got)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM event WHERE kind = 'failed'`); n != 1 {
		t.Fatalf("failed events = %d, want 1", n)
	}
}

func TestAdoptOfADirtyWindowCleansIt(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1", "S2")
	r := adoptNew(t, s, arr, 42, "S1", full)
	ds := []arrangement.Decision{{Kind: arrangement.Dirty, Window: r.Window, Seen: seen(42, "S1", half)}}
	if err := s.Apply(arr, boot, t0, "look", ds); err != nil {
		t.Fatal(err)
	}
	ds = []arrangement.Decision{{Kind: arrangement.Adopt, Window: r.Window, Seen: seen(42, "S2", half)}}
	if err := s.Apply(arr, boot, t0, "look", ds); err != nil {
		t.Fatal(err)
	}
	got := one(t, s, arr)
	if got.Dirty {
		t.Fatalf("Dirty = true after an adopt: %+v", got)
	}
	if got.Space != "S2" || got.Frame != half {
		t.Fatalf("the target follows the window: %+v", got)
	}
}

func TestOweAllOwesPlacedWindowsOfThisBootAndResetsProgress(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1", "S2")
	a := adoptNew(t, s, arr, 1, "S1", full)
	adoptNew(t, s, arr, 2, "S1", full)
	moved := []arrangement.Decision{
		{Kind: arrangement.Dirty, Window: a.Window, Seen: seen(1, "S2", half)},
		{Kind: arrangement.Put, Window: a.Window, Seen: seen(1, "S2", half), Want: arrangement.Placement{Space: "S1", Frame: full}, Move: true},
	}
	if err := s.Apply(arr, boot, t0, "look", moved); err != nil {
		t.Fatal(err)
	}
	n, err := s.OweAll(arr, boot, t0, "display change", []uint32{1, 2})
	if err != nil || n != 2 {
		t.Fatalf("OweAll = %d, %v; want 2", n, err)
	}
	rs, _ := s.Recorded(arr, boot)
	for _, r := range rs {
		if !r.Dirty || r.Progress != arrangement.Untried {
			t.Fatalf("every placed window dirty afresh: %+v", r)
		}
	}
	if n, _ := s.OweAll(arr, boot.Add(time.Hour), t0, "display change", []uint32{1, 2}); n != 0 {
		t.Fatalf("another boot has nothing bound: %d", n)
	}
}

func TestDirtyUntriedListsThisBootsMarkedWindows(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1", "S2")
	adoptNew(t, s, arr, 1, "S1", full)
	b := adoptNew(t, s, arr, 2, "S1", full)
	adoptNew(t, s, arr, 3, "S1", full)
	if _, err := s.OweAll(arr, boot, t0, "owe", []uint32{1}); err != nil {
		t.Fatal(err)
	}
	tried := []arrangement.Decision{
		{Kind: arrangement.Dirty, Window: b.Window, Seen: seen(2, "S2", half)},
		{Kind: arrangement.Put, Window: b.Window, Seen: seen(2, "S2", half), Want: arrangement.Placement{Space: "S1", Frame: full}, Move: true, Resize: true},
	}
	if err := s.Apply(arr, boot, t0, "display settled", tried); err != nil {
		t.Fatal(err)
	}
	ids, err := s.DirtyUntried(arr, boot)
	if err != nil || len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("DirtyUntried = %v, %v; want [1]", ids, err)
	}
	if ids, _ := s.DirtyUntried(arr, boot.Add(time.Hour)); len(ids) != 0 {
		t.Fatalf("another boot has nothing bound: %v", ids)
	}
}

func TestRefreshTitles(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
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

func seenTitled(wid uint32, title, space string, f layout.Rect) arrangement.Seen {
	s := seen(wid, space, f)
	s.Title = title
	return s
}

func TestOpenMigratesAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM pragma_table_info('binding') WHERE name = 'run'`); n != 0 {
		t.Fatalf("binding.run present after Open")
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if n := count(t, s2, `PRAGMA user_version`); n != len(migrations) {
		t.Fatalf("user_version = %d, want %d", n, len(migrations))
	}
}

func TestOpenDropsTheRunColumnKeepingBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		schema,
		`ALTER TABLE binding ADD COLUMN run INTEGER NOT NULL DEFAULT 0`,
		`PRAGMA user_version = 1`,
		`INSERT INTO window(id, bundle, app, title, first_seen) VALUES (16, 'net.whatsapp.WhatsApp', 'WhatsApp', 'WhatsApp', 0)`,
		`INSERT INTO binding(window_id, boot, wid, run) VALUES (16, 1790000000, 149, 555)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := count(t, s, `SELECT COUNT(*) FROM pragma_table_info('binding') WHERE name = 'run'`); n != 0 {
		t.Fatalf("binding.run present after migration")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM binding WHERE window_id = 16 AND boot = 1790000000 AND wid = 149`); n != 1 {
		t.Fatalf("binding lost in migration")
	}
}

// A database from before the migration has the disturbance table; Open drops it.
func TestMigrationDropsDisturbance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		schema,
		`CREATE TABLE IF NOT EXISTS disturbance (id INTEGER PRIMARY KEY, kind TEXT NOT NULL, began INTEGER NOT NULL, ended INTEGER)`,
		`CREATE TABLE IF NOT EXISTS stream (id INTEGER PRIMARY KEY, at INTEGER NOT NULL, kind TEXT NOT NULL, payload TEXT NOT NULL)`,
		`PRAGMA user_version = 3`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'disturbance'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("disturbance table still present")
	}
}

func TestStoredCarriesTheLatestBinding(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S2")
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Adopt, Seen: seenTitled(42, "autobrr", "S2", full)}}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Stored(arr)
	if err != nil || len(st) != 1 {
		t.Fatalf("Stored = %+v, %v", st, err)
	}
	o := st[0]
	if o.Title != "autobrr" || o.Bundle != "com.example" || o.Space != "S2" || o.Frame != full || o.Boot != boot.Unix() || o.Binding != 42 {
		t.Fatalf("stored = %+v", o)
	}
}

func TestBindGivesAStoredWindowAFreshIdAndMarksItDirty(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S2", "S5")
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Adopt, Seen: seenTitled(42, "autobrr", "S2", full)}}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Stored(arr)
	f := seenTitled(900, "autobrr - renamed", "S5", full)
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
	if got == nil || got.Window != st[0].Window || !got.Dirty || got.Space != "S2" {
		t.Fatalf("the fresh id is the same window, dirty against its recorded placement: %+v", rs)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM window`); n != 1 {
		t.Fatalf("binding must not create a window: %d", n)
	}
	st2, _ := s.Stored(arr)
	if st2[0].Binding != 900 || st2[0].Title != "autobrr - renamed" {
		t.Fatalf("latest binding and title: %+v", st2[0])
	}
	if n := count(t, s, `SELECT COUNT(*) FROM event WHERE kind = 'bound'`); n != 1 {
		t.Fatalf("bound events = %d", n)
	}
}

func TestJournalShowsWhatEachChangeDid(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1", "S2")
	r := adoptNew(t, s, arr, 42, "S1", full) // change 1: first sighting
	if err := s.Apply(arr, boot, t0.Add(time.Minute), "look", []arrangement.Decision{{Kind: arrangement.Adopt, Window: r.Window, Seen: seen(42, "S2", half)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(arr, boot, t0.Add(2*time.Minute), "look", []arrangement.Decision{{Kind: arrangement.Dirty, Window: r.Window, Seen: seen(42, "S2", full)}}); err != nil {
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
	if third.Kind != "dirty" || third.From != nil || third.To != nil {
		t.Fatalf("a dirty mark changes no placement: %+v", third)
	}
	if last, _ := s.Journal(1); len(last) != 1 || last[0].ID != j[2].ID {
		t.Fatalf("Journal(1) = %+v", last)
	}
}

func TestUndoRestoresThePriorPlacementAndMarksTheWindowDirty(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1", "S2")
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
	if got.Space != "S1" || got.Frame != full || !got.Dirty || got.Progress != arrangement.Untried {
		t.Fatalf("back at the prior placement and dirty: %+v", got)
	}
	j, _ := s.Journal(1)
	if j[0].Cause != "undo" || len(j[0].Events) != 1 || j[0].Events[0].Kind != "undone" {
		t.Fatalf("the undo is journaled: %+v", j[0])
	}
}

func TestUndoLeavesWindowsThatChangedAgain(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1", "S2", "S3")
	r := adoptNew(t, s, arr, 42, "S1", full)
	s.Apply(arr, boot, t0.Add(time.Minute), "look", []arrangement.Decision{{Kind: arrangement.Adopt, Window: r.Window, Seen: seen(42, "S2", half)}})
	bad := int64(count(t, s, `SELECT MAX(id) FROM change`))
	s.Apply(arr, boot, t0.Add(2*time.Minute), "look", []arrangement.Decision{{Kind: arrangement.Adopt, Window: r.Window, Seen: seen(42, "S3", full)}})
	before := count(t, s, `SELECT COUNT(*) FROM change`)
	reverted, skipped, err := s.Undo(bad, t0.Add(3*time.Minute))
	if err != nil || reverted != 0 || skipped != 1 {
		t.Fatalf("Undo = %d, %d, %v", reverted, skipped, err)
	}
	if got := one(t, s, arr); got.Space != "S3" || got.Dirty {
		t.Fatalf("a later placement stands: %+v", got)
	}
	if after := count(t, s, `SELECT COUNT(*) FROM change`); after != before {
		t.Fatalf("an undo that reverts nothing writes nothing: %d -> %d", before, after)
	}
}

func TestUndoOfAFirstSightingLeavesNoPlacement(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
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

func TestPruneDropsOldHistoryAndKeepsWhatIsCurrent(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1", "S2")
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
	if again, _ := s.Prune(t0.Add(-90*24*time.Hour), boot); again != 0 {
		t.Fatalf("a second prune finds nothing: %d", again)
	}
}

// A window closed since it was recorded has nothing that could clean it,
// and a window recreated under a fresh id has two bindings in the boot: only
// windows on screen are marked dirty, each once.
func TestOweAllOwesOnlyWindowsOnScreenEachOnce(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
	a := adoptNew(t, s, arr, 1, "S1", full)
	adoptNew(t, s, arr, 2, "S1", full) // closed since: not among the live ids
	if err := s.Bind(arr, boot, t0, "relaunch", []Bound{{Window: a.Window, Seen: seen(9, "S1", full)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Clean, Window: a.Window, Seen: seen(9, "S1", full)}}); err != nil {
		t.Fatal(err)
	}
	n, err := s.OweAll(arr, boot, t0, "display change", []uint32{9})
	if err != nil || n != 1 {
		t.Fatalf("OweAll = %d, %v; want 1", n, err)
	}
	if got := count(t, s, `SELECT COUNT(*) FROM owed`); got != 1 {
		t.Fatalf("owed rows = %d, want 1", got)
	}
	if got := count(t, s, `SELECT window_id FROM owed`); int64(got) != a.Window {
		t.Fatalf("the window on screen is the one marked dirty: %d", got)
	}
}

func TestStreamRoundTrip(t *testing.T) {
	s := open(t)
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	rows := []Raw{
		{At: at, Kind: "window moved", Payload: `{"Window":7}`},
		{At: at.Add(time.Second), Kind: "window quiet", Payload: `{"Window":7}`},
	}
	if err := s.Record(rows); err != nil {
		t.Fatal(err)
	}
	got, err := s.Stream(0, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != "window moved" || !got[1].At.Equal(at.Add(time.Second)) {
		t.Fatalf("Stream = %+v", got)
	}
	if got, _ = s.Stream(1, time.Time{}); len(got) != 1 || got[0].Kind != "window quiet" {
		t.Fatalf("last 1 = %+v", got)
	}
	if got, _ = s.Stream(0, at.Add(500*time.Millisecond)); len(got) != 1 || got[0].Kind != "window quiet" {
		t.Fatalf("since = %+v", got)
	}
	if got, _ = s.Stream(0, time.Time{}, "window moved"); len(got) != 1 || got[0].Kind != "window moved" {
		t.Fatalf("kinds = %+v", got)
	}
}

func TestPruneStream(t *testing.T) {
	s := open(t)
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s.Record([]Raw{{At: at, Kind: "a"}, {At: at.Add(48 * time.Hour), Kind: "b"}})
	n, err := s.PruneStream(at.Add(24 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneStream = %d, %v; want 1", n, err)
	}
	if got, _ := s.Stream(0, time.Time{}); len(got) != 1 || got[0].Kind != "b" {
		t.Fatalf("after prune = %+v", got)
	}
}

// A window recorded only in another arrangement is a binding candidate here,
// unplaced.
func TestStoredReturnsWindowsPlacedElsewhereUnplaced(t *testing.T) {
	s := open(t)
	air, _ := s.Arrangement("D1")
	dual := arrangementWith(t, s, "D1,D2", "S9")
	if err := s.Apply(dual, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Adopt, Seen: seenTitled(42, "autobrr", "S9", full)}}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Stored(air)
	if err != nil || len(st) != 1 {
		t.Fatalf("Stored(air) = %+v, %v; want the dual-only window", st, err)
	}
	o := st[0]
	if o.Placed || o.Space != "" || o.Title != "autobrr" || o.Bundle != "com.example" || o.App != "Example" || o.Boot != boot.Unix() || o.Binding != 42 {
		t.Fatalf("stored = %+v; want unplaced, with title, app and latest binding", o)
	}
	st, _ = s.Stored(dual)
	if len(st) != 1 || !st[0].Placed || st[0].Space != "S9" || st[0].Frame != full {
		t.Fatalf("Stored(dual) = %+v; want placed on S9", st)
	}
}

// Binding a window that has no placement here does not mark it dirty: the
// next decide adopts it where it is.
func TestBindToAWindowUnplacedHereIsNotDirty(t *testing.T) {
	s := open(t)
	air, _ := s.Arrangement("D1")
	dual := arrangementWith(t, s, "D1,D2", "S9")
	if err := s.Apply(dual, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Adopt, Seen: seenTitled(42, "autobrr", "S9", full)}}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Stored(air)
	if len(st) != 1 {
		t.Fatalf("Stored(air) = %+v, want the dual-only window", st)
	}
	boot2 := boot.Add(24 * time.Hour)
	if err := s.Bind(air, boot2, t0.Add(24*time.Hour), "startup", []Bound{{Window: st[0].Window, Seen: seenTitled(900, "autobrr", "S1", half)}}); err != nil {
		t.Fatal(err)
	}
	rs, _ := s.Recorded(air, boot2)
	if len(rs) != 1 || rs[0].Window != st[0].Window || rs[0].Placed || rs[0].Dirty {
		t.Fatalf("Recorded(air) = %+v; want the same window, unplaced, not dirty", rs)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM owed`); n != 0 {
		t.Fatalf("owed rows = %d, want 0", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM window`); n != 1 {
		t.Fatalf("windows = %d, want 1", n)
	}
}

// Two windows of one bundle with different owner names share an app row;
// two bundle-less apps are told apart by name.
func TestAppsAreKeyedByBundleOrName(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
	a := seenTitled(1, "a", "S1", full)
	b := seenTitled(2, "b", "S1", half)
	b.App = "Example (Renamed)"
	c := seenTitled(3, "c", "S1", full)
	c.Bundle, c.App = "", "term"
	d := seenTitled(4, "d", "S1", half)
	d.Bundle, d.App = "", "sh"
	for _, w := range []arrangement.Seen{a, b, c, d} {
		if err := s.Apply(arr, boot, t0, "look", []arrangement.Decision{{Kind: arrangement.Adopt, Seen: w}}); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, s, `SELECT COUNT(*) FROM app`); n != 3 {
		t.Fatalf("apps = %d, want 3 (one bundle, two names)", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM window w JOIN app p ON p.id = w.app_id WHERE p.bundle = 'com.example'`); n != 2 {
		t.Fatalf("windows under com.example = %d, want 2", n)
	}
}

// A version-4 database: window carries bundle and app, placement has no
// foreign key to space. Open splits app out and keeps every row.
func TestMigrationSplitsAppFromWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		schema,
		`CREATE TABLE IF NOT EXISTS stream (id INTEGER PRIMARY KEY, at INTEGER NOT NULL, kind TEXT NOT NULL, payload TEXT NOT NULL)`,
		`PRAGMA user_version = 4`,
		`INSERT INTO arrangement(id, display_set) VALUES (1, 'D1'), (2, 'D1,D2')`,
		`INSERT INTO space(arrangement_id, key, display, idx) VALUES (1, 'S1', 'D1', 0), (2, 'S1', 'D1', 0), (2, 'S9', 'D2', 0)`,
		`INSERT INTO window(id, bundle, app, title, first_seen) VALUES
		   (16, 'com.google.Chrome', 'Google Chrome', 'autobrr', 0),
		   (17, 'com.google.Chrome', 'Google Chrome', 'Review', 0),
		   (18, '', 'term', 'zsh', 0),
		   (19, 'com.google.Chrome', 'Chrome', 'Gmail', 0)`,
		`INSERT INTO binding(window_id, boot, wid) VALUES (16, 1790000000, 149), (17, 1790000000, 150), (18, 1790000000, 151)`,
		`INSERT INTO change(id, at, cause) VALUES (1, 1790000100000, 'look'), (2, 1790000200000, 'look')`,
		`INSERT INTO placement(id, window_id, arrangement_id, space, x, y, w, h, opened_by, closed_by) VALUES
		   (1, 16, 2, 'S9', 0, 30, 1280, 1410, 1, NULL),
		   (2, 17, 2, 'S1', 0, 30, 1280, 1410, 1, 2),
		   (3, 17, 2, 'S9', 0, 30, 640, 700, 2, NULL),
		   (4, 18, 1, 'S1', 0, 517, 735, 923, 1, NULL)`,
		`INSERT INTO owed(window_id, arrangement_id, progress, since) VALUES (16, 2, 1, 2)`,
		`INSERT INTO event(change_id, window_id, kind) VALUES (1, 16, 'adopted'), (2, 17, 'adopted')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v := count(t, s, `PRAGMA user_version`); v != len(migrations) {
		t.Fatalf("user_version = %d, want %d", v, len(migrations))
	}
	if n := count(t, s, `SELECT COUNT(*) FROM app`); n != 2 {
		t.Fatalf("apps = %d, want 2", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM pragma_table_info('window') WHERE name IN ('bundle', 'app')`); n != 0 {
		t.Fatal("window still carries bundle or app")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM window w JOIN app a ON a.id = w.app_id WHERE a.bundle = 'com.google.Chrome' AND a.name = 'Chrome'`); n != 3 {
		t.Fatalf("Chrome windows under the latest window's name = %d, want 3", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM window w JOIN app a ON a.id = w.app_id WHERE a.bundle = '' AND a.name = 'term' AND w.title = 'zsh'`); n != 1 {
		t.Fatalf("bundle-less window = %d, want 1", n)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM placement`:                                                         4,
		`SELECT COUNT(*) FROM placement WHERE closed_by IS NULL`:                                 3,
		`SELECT COUNT(*) FROM binding`:                                                           3,
		`SELECT COUNT(*) FROM owed WHERE window_id = 16 AND progress = 1`:                        1,
		`SELECT COUNT(*) FROM event`:                                                             2,
		`SELECT COUNT(*) FROM pragma_foreign_key_list('placement') WHERE "table" = 'space'`:      2,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'placement_current'`: 1,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'window_app'`:        1,
		`PRAGMA foreign_keys`: 1,
	} {
		if n := count(t, s, q); n != want {
			t.Fatalf("%s = %d, want %d", q, n, want)
		}
	}
	var violations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if violations != 0 {
		t.Fatalf("foreign_key_check: %d violation(s)", violations)
	}
	st, err := s.Stored(1)
	if err != nil || len(st) != 4 {
		t.Fatalf("Stored(air) = %+v, %v; want all four windows", st, err)
	}
	// Reopening is a no-op at the latest version.
	s.Close()
	if s2, err := Open(path); err != nil {
		t.Fatalf("reopen: %v", err)
	} else {
		s2.Close()
	}
}

// A window's history lists its decisions oldest first, each with its
// change's cause, and the placement a change opened for it.
func TestHistoryListsAWindowsDecisionsInOrder(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	steps := []struct {
		cause string
		ds    []arrangement.Decision
	}{
		{"display settled", []arrangement.Decision{{Kind: arrangement.Dirty, Window: r.Window, Seen: seen(42, "S1", half)},
			{Kind: arrangement.Put, Window: r.Window, Seen: seen(42, "S1", half), Want: arrangement.Placement{Space: "S1", Frame: full}, Resize: true}}},
		{"put ended", []arrangement.Decision{{Kind: arrangement.Clean, Window: r.Window, Seen: seen(42, "S1", full)}}},
	}
	for i, st := range steps {
		if err := s.Apply(arr, boot, t0.Add(time.Duration(i+1)*time.Second), st.cause, st.ds); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.History(r.Window, 10)
	if err != nil {
		t.Fatal(err)
	}
	var kinds, causes []string
	for _, h := range got {
		kinds = append(kinds, h.Kind)
		causes = append(causes, h.Cause)
	}
	if strings.Join(kinds, " ") != "adopted dirty put clean" {
		t.Fatalf("kinds = %v, want adopted dirty put clean", kinds)
	}
	if strings.Join(causes, " ") != "look display settled display settled put ended" {
		t.Fatalf("causes = %v", causes)
	}
	if got[0].Opened == nil || got[0].Opened.Frame != full || got[1].Opened != nil {
		t.Fatalf("the adopt opened the placement, the dirty mark opened none: %+v %+v", got[0].Opened, got[1].Opened)
	}
	if !got[1].At.After(got[0].At) || !got[3].At.After(got[2].At) {
		t.Fatalf("history is oldest first: %v", got)
	}
	if last, err := s.History(r.Window, 2); err != nil || len(last) != 2 || last[0].Kind != "put" {
		t.Fatalf("the last two: %+v %v", last, err)
	}
}

// A window record carries its app, title, current targets with their dirty
// marks, and the boots and ids it has been bound under.
func TestWindowRecordCarriesTargetsAndBindings(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	if err := s.Apply(arr, boot, t0, "display settled", []arrangement.Decision{{Kind: arrangement.Dirty, Window: r.Window, Seen: seen(42, "S1", half)}}); err != nil {
		t.Fatal(err)
	}
	win, err := s.WindowByBinding(boot, 42)
	if err != nil || win != r.Window {
		t.Fatalf("WindowByBinding = %d, %v; want %d", win, err, r.Window)
	}
	if none, err := s.WindowByBinding(boot, 43); err != nil || none != 0 {
		t.Fatalf("an unbound id yields 0: %d, %v", none, err)
	}
	rec, err := s.Window(r.Window)
	if err != nil {
		t.Fatal(err)
	}
	if rec.App != "Example" || rec.Bundle != "com.example" || rec.Title != "t" || len(rec.Targets) != 1 {
		t.Fatalf("record %+v", rec)
	}
	tg := rec.Targets[0]
	if tg.DisplaySet != "D1" || tg.Space != "S1" || tg.Frame != full || !tg.Dirty || tg.Since.IsZero() {
		t.Fatalf("target %+v", tg)
	}
	bs, err := s.Bindings(r.Window)
	if err != nil || len(bs) != 1 || bs[0].WID != 42 || !bs[0].Boot.Equal(boot) {
		t.Fatalf("bindings %+v %v", bs, err)
	}
}

// seenAs is window wid seen at placement p.
func seenAs(wid uint32, p arrangement.Placement) arrangement.Seen {
	s := seen(wid, "", layout.Rect{})
	s.Placement = p
	return s
}

// adoptAt records that window win (wid in this boot) is now at p.
func adoptAt(t *testing.T, s *Store, arr int64, at time.Time, win int64, wid uint32, p arrangement.Placement) {
	t.Helper()
	if err := s.Apply(arr, boot, at, "window settled", []arrangement.Decision{{Kind: arrangement.Adopt, Window: win, Seen: seenAs(wid, p)}}); err != nil {
		t.Fatalf("recording %+v: %v", p, err)
	}
}

// Entering fullscreen by hand replaces the window's placement with the
// fullscreen target, and leaving replaces that with the landing placement,
// each as a new version. The fullscreen target is not one of the spaces.
func TestFullscreenTargetReplacesAPlacementAndBack(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	fs := arrangement.FullscreenOn("D1")
	adoptAt(t, s, arr, t0.Add(time.Minute), r.Window, 42, fs)
	if got := one(t, s, arr); !got.Placed || got.Placement != fs {
		t.Fatalf("after entering fullscreen: %+v, want the target fullscreen on D1", got)
	}
	back := arrangement.Placement{Space: "S1", Frame: half}
	adoptAt(t, s, arr, t0.Add(2*time.Minute), r.Window, 42, back)
	if got := one(t, s, arr); got.Placement != back {
		t.Fatalf("after leaving fullscreen: %+v, want S1 at the landing frame", got)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM placement WHERE window_id = ?`, r.Window); n != 3 {
		t.Fatalf("versions = %d, want 3", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM placement WHERE window_id = ? AND closed_by IS NULL`, r.Window); n != 1 {
		t.Fatalf("open versions = %d, want 1", n)
	}
	spaces, err := s.Spaces(arr)
	if err != nil || len(spaces) != 1 || spaces[0].UUID != "S1" {
		t.Fatalf("Spaces = %+v, %v; want S1 only", spaces, err)
	}
}

// Every reader of targets returns the fullscreen form: binding candidates,
// the window record, its history and the journal.
func TestFullscreenTargetReadsBackEverywhere(t *testing.T) {
	s := open(t)
	arr := arrangementWith(t, s, "D1", "S1")
	r := adoptNew(t, s, arr, 42, "S1", full)
	fs := arrangement.FullscreenOn("D1")
	adoptAt(t, s, arr, t0.Add(time.Minute), r.Window, 42, fs)
	st, err := s.Stored(arr)
	if err != nil || len(st) != 1 || !st[0].Placed || st[0].Placement != fs {
		t.Fatalf("Stored = %+v, %v; want the candidate fullscreen on D1", st, err)
	}
	rec, err := s.Window(r.Window)
	if err != nil || len(rec.Targets) != 1 || rec.Targets[0].Placement != fs {
		t.Fatalf("Window targets = %+v, %v; want fullscreen on D1", rec.Targets, err)
	}
	hist, err := s.History(r.Window, 10)
	if err != nil || len(hist) != 2 || hist[1].Opened == nil || *hist[1].Opened != fs {
		t.Fatalf("History = %+v, %v; want the second adopt to open fullscreen on D1", hist, err)
	}
	js, err := s.Journal(10)
	if err != nil || len(js) == 0 {
		t.Fatalf("Journal = %+v, %v", js, err)
	}
	ev := js[len(js)-1].Events
	if len(ev) != 1 || ev[0].From == nil || *ev[0].From != (arrangement.Placement{Space: "S1", Frame: full}) || ev[0].To == nil || *ev[0].To != fs {
		t.Fatalf("the last change's events %+v, want S1 -> fullscreen on D1", ev)
	}
}

func TestMigrationGivesSpacesAFullscreenColumn(t *testing.T) {
	s := open(t)
	if n := count(t, s, `SELECT COUNT(*) FROM pragma_table_info('space') WHERE name = 'fullscreen'`); n != 1 {
		t.Fatalf("space.fullscreen columns = %d, want 1", n)
	}
}
