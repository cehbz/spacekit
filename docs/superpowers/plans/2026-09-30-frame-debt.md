# Display-change reference, frame debt, and save guards — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After a display change the agent restores from the newest snapshot with the same display set, repairs window frames that macOS shrank (paying the repair per space as the user visits it), never records Mission Control thumbnails as a layout, and `spaceswitch` one-shots land their event.

**Architecture:** Domain rules in `internal/layout` (reference selection, frame-debt computation and due/drop decisions, overview detection), driven by `watch.go`; AX write order fixed in `internal/skylight/ax.m`; `spaceswitch` waits for the space change before exiting.

**Tech Stack:** Go 1.26 + cgo/ObjC; existing packages.

**Spec:** Session findings 2026-09-30 (observations in the KB): Sep 28 monitor outages left "not a transient drop" because the pre-burst reference was saved during the outage; the 21:52 wake shrank Chrome windows to built-in geometry while their spaces stayed correct; AX frame writes are ignored off the active space and clamp height when size is set before position; Mission Control's overview appears in the window list as `WindowManager` / `Window Highlight Overlay`; a one-shot `spaceswitch left` without `-verify` never switches under launchd.

## Global Constraints

- Branch `watch-agent`, in place. Batch file `/tmp/cg-batch-spacekit-765a`; messages listed below verbatim; `go test ./...` and `go vet ./...` clean at every commit.
- All SkyLight/AX calls in the watcher go through `sysevents.OnMain`.
- Frame debt is paid only when the window's live frame still equals the frame recorded at settle time; any user change cancels the debt for that window.
- Reference selection stays within the current boot session; login convergence covers reboots.

---

### Task 1: layout.ReferenceFor and its use after a display change

**Files:** `internal/layout/layout.go`, `internal/layout/history_test.go`, `cmd/spacekeeper/watch.go` (`displaysSettled`)

**Produces:** `func ReferenceFor(ls []Layout, before, boot time.Time, want Stats) int` — index of the newest layout with `SavedAt` in `[boot, before)` whose `Stats().SameDisplays(want)`, else -1.

- [ ] Test (append to history_test.go):

```go
func TestReferenceForPicksNewestMatchingDisplaySetThisBoot(t *testing.T) {
	boot := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	two := []SavedSpace{{UUID: "a", DisplayUUID: "D1"}, {UUID: "b", DisplayUUID: "D2"}}
	one := []SavedSpace{{UUID: "b", DisplayUUID: "D2"}}
	burst := boot.Add(10 * time.Hour)
	ls := []Layout{
		{SavedAt: boot.Add(9 * time.Hour), Spaces: one},                       // during the outage
		{SavedAt: boot.Add(6 * time.Hour), Spaces: two},                       // last good, this boot
		{SavedAt: boot.Add(5 * time.Hour), Spaces: two},
		{SavedAt: boot.Add(-1 * time.Hour), Spaces: two},                      // previous boot
		{SavedAt: boot.Add(11 * time.Hour), Spaces: two},                      // after the burst
	}
	want := Layout{Spaces: two}.Stats()
	if got := ReferenceFor(ls, burst, boot, want); got != 1 {
		t.Fatalf("ReferenceFor = %d, want 1", got)
	}
	if got := ReferenceFor(ls, burst, boot, Layout{Spaces: one}.Stats()); got != 0 {
		t.Fatalf("one-display want: got %d, want 0", got)
	}
	if got := ReferenceFor(ls[:1], burst, boot, want); got != -1 {
		t.Fatalf("no match: got %d, want -1", got)
	}
}
```

- [ ] Run `go test ./internal/layout/ -run ReferenceFor` → compile error; stub returning -1; assertion failure.
- [ ] Implement:

```go
// ReferenceFor picks the layout a settled display change should restore
// from: the newest one saved in this boot session before the change began
// whose display set equals the settled one. A snapshot taken during a
// monitor outage has the wrong display set and is skipped, so a return after
// hours restores the last layout that had that display.
func ReferenceFor(ls []Layout, before, boot time.Time, want Stats) int {
	best := -1
	for i, l := range ls {
		if !l.SavedAt.Before(before) || l.SavedAt.Before(boot) || !l.Stats().SameDisplays(want) {
			continue
		}
		if best == -1 || l.SavedAt.After(ls[best].SavedAt) {
			best = i
		}
	}
	return best
}
```

- [ ] `displaysSettled`: gather first (on main), compute `now := layout.Layout{Spaces: s.spaces}.Stats()`, then `i := layout.ReferenceFor(layouts(refs), start, bootTime(), now)`; if -1 log `displays settled to %s; no snapshot with that display set this boot; leaving windows alone`. Remove the `SameDisplays` check (the reference matches by construction) and `LatestBefore` use here. Keep `LatestBefore` in layout (still tested).
- [ ] `go test ./... && go vet ./...` clean. Commit message 1.

### Task 2: AX frame write order

**Files:** `internal/skylight/ax.m` (`sk_set_window_frame`)

- [ ] Replace the size-then-position block with position, size, position:

```objc
		// Position first: a window near the bottom of a display has its
		// height clamped to the remaining screen if the size lands first
		// (observed: y=517 clamped 1410 to 923). Then size, then position
		// again for apps that shift the origin when resizing.
		AXError e0 = AXUIElementSetAttributeValue(win, kAXPositionAttribute, posVal);
		AXError e1 = AXUIElementSetAttributeValue(win, kAXSizeAttribute, sizeVal);
		AXError e2 = AXUIElementSetAttributeValue(win, kAXPositionAttribute, posVal);
		CFRelease(posVal);
		CFRelease(sizeVal);
		rc = (e0 == kAXErrorSuccess && e1 == kAXErrorSuccess && e2 == kAXErrorSuccess) ? 0 : 3;
```

- [ ] Manual check (no unit test possible): with a window on the active space, run a launchd one-shot `restore -f <ref> -frames -create=false` where `<ref>` gives that window frame `y=517 h=923`, then a second one-shot with `y=30 h=1410`; the second must produce `h=1410` in one pass (`spacekeeper save -f` to read). Commit message 2.

### Task 3: frame debt after a transient drop

**Files:** `internal/layout/layout.go`, `internal/layout/framedebt_test.go` (new), `cmd/spacekeeper/watch.go`, `cmd/spacekeeper/main.go` (`snapshot` gains `current map[uint64]bool` filled in `gather` from `d.CurrentSpace.ID()`)

**Produces:**

```go
type FrameDebt struct {
	ID   uint32
	PID  int
	Want Rect // saved frame
	Seen Rect // live frame when the debt was recorded
}
// FrameDebts lists matched windows whose live frame differs from the saved one.
func FrameDebts(saved []SavedWindow, matched map[int]uint32, live []LiveWindow) []FrameDebt
// Settle decides a debt against the current state of its window: pay when the
// window is on an active space and unchanged since the debt was recorded;
// drop when the window is gone or its frame changed (the user moved it).
func (d FrameDebt) Settle(live *LiveWindow, activeSpaces map[uint64]bool, spaceOf map[uint32]uint64) (pay, drop bool)
```

- [ ] Tests:

```go
package layout

import "testing"

func TestFrameDebtsOnlyForDifferingFrames(t *testing.T) {
	saved := []SavedWindow{
		{OwnerName: "A", Frame: Rect{0, 30, 1280, 1410}},
		{OwnerName: "B", Frame: Rect{0, 30, 1280, 1410}},
		{OwnerName: "C", Frame: Rect{0, 0, 100, 100}, Fullscreen: true},
	}
	live := []LiveWindow{
		{ID: 1, OwnerPID: 10, Frame: Rect{0, 517, 735, 923}},
		{ID: 2, OwnerPID: 11, Frame: Rect{0, 30, 1280, 1410}},
		{ID: 3, OwnerPID: 12, Frame: Rect{5, 5, 50, 50}},
	}
	debts := FrameDebts(saved, map[int]uint32{0: 1, 1: 2, 2: 3}, live)
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
}
```

- [ ] Run → compile error; stubs; assertion failures.
- [ ] Implement in layout.go:

```go
// FrameDebt is a window whose frame macOS changed during a display drop and
// that still needs putting back. Accessibility can only resize windows on an
// active space, so debts are paid as the user visits each space; a window
// the user has since moved or resized is left alone.
type FrameDebt struct {
	ID         uint32
	PID        int
	Want, Seen Rect
}

func FrameDebts(saved []SavedWindow, matched map[int]uint32, live []LiveWindow) []FrameDebt {
	byID := make(map[uint32]LiveWindow, len(live))
	for _, l := range live {
		byID[l.ID] = l
	}
	var out []FrameDebt
	for si, wid := range matched {
		s := saved[si]
		l, ok := byID[wid]
		if !ok || s.Fullscreen || l.Frame == s.Frame {
			continue
		}
		out = append(out, FrameDebt{ID: wid, PID: l.OwnerPID, Want: s.Frame, Seen: l.Frame})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (d FrameDebt) Settle(live *LiveWindow, activeSpaces map[uint64]bool, spaceOf map[uint32]uint64) (pay, drop bool) {
	if live == nil || live.Frame != d.Seen {
		return false, true
	}
	return activeSpaces[spaceOf[d.ID]], false
}
```

- [ ] `gather`: record `s.current[d.CurrentSpace.ID()] = true` per display (new field `current map[uint64]bool`).
- [ ] `watch.go`: field `debts []layout.FrameDebt`. In `displaysSettled`, after a successful `passOn`, `w.debts = layout.FrameDebts(ref.l.Windows, layout.Match(ref.l.Windows, s.windows), s.windows)` (the match is recomputed; windows just moved keep their IDs), log `frame debt: %d window(s)`, then `w.payDebts(s)` immediately for the active spaces. On the space-change quiet timer (before the save): if `len(w.debts) > 0`, gather on main and `payDebts`. `payDebts(s *snapshot)` runs on main: for each debt, look up the live window, `Settle`; on pay call `skylight.SetWindowFrame(d.PID, d.ID, Want...)`, keep the debt if it errors, log `frame debt: paid %d, dropped %d, %d outstanding`.
- [ ] Manual check: displace via the one-shot launchd frames job (set two windows on a non-active Dell space to `y=517 h=923`), then simulate the drop with the origin nudge used on 2026-09-02; watch.log must show `frame debt: 2 window(s)`; switch to that space with the tilt wheel; within 3 s the log shows `paid 2` and `save -f` shows full frames. Commit message 3.

### Task 4: spaceswitch one-shot waits for the switch

**Files:** `cmd/spaceswitch/main.go` (`switchSpace`)

- [ ] Replace `if !verify { return }` with a silent poll: the same loop as `-verify` but with a 300 ms deadline and no output; `-verify` keeps the 1500 ms loop and its messages. Comment: `A process that exits right after CGEventPost can lose the event (observed under launchd: no switch without -verify, switch every time with it); wait for the space to change before returning.`
- [ ] Manual check: launchd one-shot `spaceswitch left` (no `-verify`) changes `spaceswitch status`; then `right` to return. Commit message 4.

### Task 5: skip saves while Mission Control is open

**Files:** `internal/layout/layout.go`, `internal/layout/history_test.go`, `cmd/spacekeeper/main.go` (`saveSnapshot`), `cmd/spacekeeper/watch.go` (`save`)

- [ ] Test:

```go
func TestOverviewOpen(t *testing.T) {
	if OverviewOpen([]LiveWindow{{OwnerName: "Finder", Title: "Desktop"}}) {
		t.Fatal("no overlay: closed")
	}
	if !OverviewOpen([]LiveWindow{{OwnerName: "WindowManager", Title: "Window Highlight Overlay"}}) {
		t.Fatal("overlay present: open")
	}
}
```

- [ ] Implement:

```go
// OverviewOpen reports whether Mission Control's overview is showing. While
// it is, CGWindow bounds are the scaled thumbnails, so a layout gathered
// then is not one to keep. The overview adds WindowManager's highlight
// overlay windows to the window list and nothing else does.
func OverviewOpen(live []LiveWindow) bool {
	for _, l := range live {
		if l.OwnerName == "WindowManager" && l.Title == "Window Highlight Overlay" {
			return true
		}
	}
	return false
}
```

- [ ] `saveSnapshot`: after `gather`, `if layout.OverviewOpen(s.windows) { return "", errOverviewOpen }` with `var errOverviewOpen = errors.New("Mission Control is open")`; `watcher.save` logs `save (%s) skipped: Mission Control is open` for that error instead of `failed`. `gather` must not filter those windows before the check (they pass the layer/size filter today, as the 11:10:11 snapshot shows). Commit message 5.

### Task 6: docs

- [ ] README "The agent": reference = newest same-display-set snapshot this boot; frame debt paid per space on visit; Mission Control guard. TODO: drop the "verify on a real event" item (done: 21:53 and 10:17 events), add "verify frame debt on a real wake". Commit message 6 (includes this plan file).

## Commit messages (verbatim, in order)

1. `layout: ReferenceFor picks the newest same-display-set snapshot; watcher uses it after a display change`
2. `skylight: set position before size in AX frame restore`
3. `spacekeeper watch: frame debt after a transient drop, paid as each space becomes active`
4. `spaceswitch: wait for the switch before exiting so a one-shot post is not dropped`
5. `spacekeeper: skip saves while Mission Control is open`
6. `docs: display-change reference, frame debt, Mission Control guard`
