package layout

import "testing"

func TestFrameDebtsOnlyForDifferingFrames(t *testing.T) {
	saved := []SavedWindow{
		{OwnerName: "A", Title: "shrunk", Frame: Rect{0, 30, 1280, 1410}},
		{OwnerName: "B", Title: "unchanged", Frame: Rect{0, 30, 1280, 1410}},
		{OwnerName: "C", Title: "fullscreen", Frame: Rect{0, 0, 100, 100}, Fullscreen: true},
		{OwnerName: "D", Title: "grew", Frame: Rect{0, 0, 800, 600}},
		{OwnerName: "E", Title: "", Frame: Rect{99, 55, 814, 458}},
	}
	live := []LiveWindow{
		{ID: 1, OwnerPID: 10, Frame: Rect{0, 517, 735, 923}},
		{ID: 2, OwnerPID: 11, Frame: Rect{0, 30, 1280, 1410}},
		{ID: 3, OwnerPID: 12, Frame: Rect{5, 5, 50, 50}},
		{ID: 4, OwnerPID: 13, Frame: Rect{0, 0, 1916, 1076}}, // an app sizing itself larger
		{ID: 5, OwnerPID: 14, Frame: Rect{99, 55, 814, 89}},  // untitled popup
	}
	debts := FrameDebts(saved, map[int]uint32{0: 1, 1: 2, 2: 3, 3: 4, 4: 5}, live)
	if len(debts) != 1 || debts[0].ID != 1 || debts[0].PID != 10 || debts[0].Want != saved[0].Frame || debts[0].Seen != live[0].Frame {
		t.Fatalf("debts = %+v", debts)
	}
}

func TestFrameDebtSettle(t *testing.T) {
	d := FrameDebt{ID: 1, PID: 10, Want: Rect{0, 30, 1280, 1410}, Seen: Rect{0, 517, 735, 923}}
	unchanged := &LiveWindow{ID: 1, Frame: d.Seen}
	spaceOf := map[uint32]uint64{1: 4}
	if pay, drop := d.Settle(unchanged, map[uint64]bool{4: true}, spaceOf); !pay || drop {
		t.Fatal("active space, unchanged frame: should pay")
	}
	if pay, drop := d.Settle(unchanged, map[uint64]bool{3: true}, spaceOf); pay || drop {
		t.Fatal("inactive space: should wait")
	}
	moved := &LiveWindow{ID: 1, Frame: Rect{100, 100, 800, 600}}
	if pay, drop := d.Settle(moved, map[uint64]bool{4: true}, spaceOf); pay || !drop {
		t.Fatal("user changed the frame: should drop")
	}
	if pay, drop := d.Settle(nil, map[uint64]bool{4: true}, spaceOf); pay || !drop {
		t.Fatal("window gone: should drop")
	}
	done := &LiveWindow{ID: 1, Frame: d.Want}
	if pay, drop := d.Settle(done, map[uint64]bool{3: true}, spaceOf); pay || !drop {
		t.Fatal("already at the wanted frame: satisfied, drop")
	}
}
