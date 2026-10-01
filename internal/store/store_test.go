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
