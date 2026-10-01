# Staged repair — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An owed window is put right at once: moved to its space, and if its frame is wrong, brought to its display's visible space, resized there, and returned. No repair waits for the user to visit a space.

**Architecture:** `arrangement.Decide` stops making a resize conditional on visibility, and the "moved, frame still to do" progress state goes. The agent's `repair` batches: one window-server move brings every window that needs a resize onto the visible space, each is resized through Accessibility, and one move per home space returns them. Each step is awaited by polling the window server, not by a fixed sleep, and the durations are logged. The 0.6 s arrival look is removed.

**Tech Stack:** Go 1.26, existing packages.

**Spec:** `docs/superpowers/specs/2026-10-01-arrangement-model.md`, rule 4 (Repair), amended by Task 3. Measured basis: a window moved from a hidden space to the visible one, resized there and moved back keeps its new frame (2026-10-01, autobrr: 1410 to 1300 and back).

## Global Constraints

- Work on `main`. Commit messages are in "Commit messages" below, in `/tmp/cg-batch-spacekit-765a`; subagents stage and checkpoint with `git stash create`, the orchestrator commits.
- `go build ./... && go vet ./... && go test ./...` clean at every commit; `gofmt -l cmd internal` empty.
- TDD for the rules: failing test on an assertion first.
- The working tree starts with an uncommitted change in `cmd/spacekeeper/watch.go` and the spec (the 0.6 s arrival look). Task 2 removes that code; Task 3 rewrites that spec text. Do not commit it as it stands.
- All SkyLight and Accessibility calls stay on the main thread; `repair` already runs there.
- Stored progress values stay as they are: 0 untried, 2 attempted. A stored 1 from the removed state reads as untried.
- Subagents do not run `make install`, `launchctl`, or the built binaries.
- Comments state what exists, tersely; no "new", "now", "old".

---

### Task 1: the rule — repair at once

**Files:** Modify `internal/arrangement/arrangement.go`, `internal/arrangement/arrangement_test.go`, `internal/store/store_test.go`.

**Produces:** `Progress` with `Untried Progress = 0` and `Attempted Progress = 2` (no `Moved`); `Decision.Resize` true whenever the frame differs, whatever is visible; `Decision.Progress()` returns `Attempted, true` for any Repair that moves or resizes; exported `func SameFrame(a, b layout.Rect) bool` (the existing `sameFrame`, renamed, all uses updated including `bind.go`).

- [ ] **Step 1: change the tests first.** In `arrangement_test.go`:

  - `TestDecideChangeOnAnUnseenSpaceIsOwed`: the second decision is a repair that resizes:

    ```go
    	wantKinds(t, ds, Owe, Repair)
    	if ds[1].Move || !ds[1].Resize || ds[1].Want.Frame != full {
    		t.Fatalf("on the right space at the wrong frame: resize, wherever its space is: %+v", ds[1])
    	}
    	if p, ok := ds[1].Progress(); !ok || p != Attempted {
    		t.Fatalf("Progress = %v %v, want Attempted", p, ok)
    	}
    ```

  - Rename `TestDecideOwedIsResizedWhenItsSpaceIsVisible` to `TestDecideOwedIsResizedWhereverItsSpaceIs` and make its look invisible (`Visible: vis("S9")`); assertions unchanged (`!Move`, `Resize`, `Want.Frame == full`, `Attempted`).
  - Replace `TestDecideOwedWaitsWhileDriftingUnseen` with:

    ```go
    func TestDecideOwedDriftingUnseenIsResized(t *testing.T) {
    	drift := layout.Rect{X: 0, Y: 33, W: 735, H: 923}
    	ds := Decide([]Recorded{owedRec(1, "S1", full, Untried)}, Look{Windows: []Seen{saw(1, "S1", drift)}, Visible: vis("S9")})
    	wantKinds(t, ds, Repair)
    	if ds[0].Move || !ds[0].Resize {
    		t.Fatalf("want a resize, got %+v", ds[0])
    	}
    }
    ```

  - Replace `TestDecideOwedOnWrongUnseenSpaceIsMovedOnly` with:

    ```go
    func TestDecideOwedOnTheWrongSpaceAtTheWrongFrameIsMovedAndResized(t *testing.T) {
    	ds := Decide([]Recorded{owedRec(1, "S1", full, Untried)}, Look{Windows: []Seen{saw(1, "S2", shrunk)}, Visible: vis("S9")})
    	wantKinds(t, ds, Repair)
    	if !ds[0].Move || !ds[0].Resize {
    		t.Fatalf("want move and resize, got %+v", ds[0])
    	}
    	if p, _ := ds[0].Progress(); p != Attempted {
    		t.Fatalf("Progress = %v, want Attempted", p)
    	}
    }
    ```

  - Delete `TestDecideMovedWindowIsResizedOnceVisible`.
  - Replace `TestDecideGivesUpWhenTheMoveDidNotTake` with:

    ```go
    func TestDecideGivesUpWhenStillOnTheWrongSpaceAfterAnAttempt(t *testing.T) {
    	ds := Decide([]Recorded{owedRec(1, "S1", full, Attempted)}, Look{Windows: []Seen{saw(1, "S2", full)}, Visible: vis("S9")})
    	wantKinds(t, ds, GiveUp)
    }
    ```

  In `store_test.go`, `TestRepairRecordsProgress` expects `arrangement.Attempted` (its decision moves a window whose frame also differs; the message becomes `"Progress = %v, want Attempted"`), and its decision gains `Resize: true`. Any other reference to `arrangement.Moved` is removed the same way.

- [ ] **Step 2:** make it compile without changing behaviour (keep `Moved` as a deprecated alias only if needed for the stub step, then remove it in Step 3); run and see the changed tests fail on assertions.

- [ ] **Step 3: implement.** In `arrangement.go`:

```go
// Progress records whether a repair of an owed window has been attempted,
// for the next look to judge.
type Progress int

const (
	Untried   Progress = 0
	Attempted Progress = 2 // the window was moved, resized, or both
)
```

```go
func owed(r Recorded, s Seen) Decision {
	if r.Placement.InPlace(s.Placement) {
		return Decision{Kind: Release, Window: r.Window, Seen: s}
	}
	if r.Progress == Attempted {
		return Decision{Kind: GiveUp, Window: r.Window, Seen: s}
	}
	return Decision{
		Kind: Repair, Window: r.Window, Seen: s, Want: r.Placement,
		Move:   s.Space != r.Space,
		Resize: !SameFrame(r.Frame, s.Frame),
	}
}

// Progress is what a repair leaves for the next look: Attempted once the
// window was moved or resized. ok is false when nothing was done.
func (d Decision) Progress() (p Progress, ok bool) {
	if d.Kind == Repair && (d.Move || d.Resize) {
		return Attempted, true
	}
	return Untried, false
}
```

`Decide` calls `owed(r, s)` (the `visible` parameter goes); its doc comment says an owed window is repaired at once and given up on when an attempt did not take. `Look.Visible` stays: adoption still depends on it. Rename `sameFrame` to `SameFrame` with the doc comment `// SameFrame reports whether two frames are within Tolerance on every edge.`

- [ ] **Step 4:** all tests pass; vet, gofmt clean. Stage, checkpoint. Commit message 1.

---

### Task 2: the agent stages a resize on the visible space

**Files:** Modify `cmd/spacekeeper/watch.go`, `cmd/spacekeeper/main.go` (`snapshot`, `gather`).

**Consumes:** `arrangement.Decision`, `arrangement.SameFrame`, `layout.ResolveSpaces`, `skylight.MoveWindowsToSpace`, `skylight.SetWindowFrame`, `skylight.SpacesForWindow`, `skylight.WindowList`.

- [ ] **Step 1: per-display current space.** In `main.go`, `snapshot` gains `currentOf map[string]uint64 // display UUID -> the space it is showing`, initialised in `gather` and filled beside `s.current`: `s.currentOf[d.UUID] = d.CurrentSpace.ID()`.

- [ ] **Step 2: remove the arrival look.** From `watch.go` remove the `arrivalQuiet` constant and its comment, the `arrive` and `owed` fields and `arrive: settle.New(arrivalQuiet)`, the `arriveC` channel and its `case`, the `if w.owed > 0 { w.arrive.Note(e.At) }` in `handle`, the `w.owed` branch in `look` (the put-off path returns to `w.retry = trigger; w.spaces.Note(time.Now())` alone), and the `w.owed` counting after `w.prevVisible = visible`.

- [ ] **Step 3: staged repair.** Replace `repair` and add the helpers:

```go
// repair acts on Repair decisions. A window on the wrong space whose frame is
// right is moved home. A window whose frame is wrong is resized through
// Accessibility, which only reaches a window on a shown space: one on a
// hidden space is first moved, with all the others, to the space its display
// is showing, resized there, and moved home. Each step is awaited by polling
// the window server. A window whose space no longer exists is skipped and
// keeps no progress.
func (w *watcher) repair(s *snapshot, arr int64, ds []arrangement.Decision) error {
	saved, err := w.store.Spaces(arr)
	if err != nil {
		return err
	}
	resolved := layout.ResolveSpaces(saved, s.displays)
	displayOf := make(map[string]string, len(saved)) // space key -> display UUID
	for _, sp := range saved {
		displayOf[sp.UUID] = sp.DisplayUUID
	}
	home := make(map[uint64][]uint32)  // home space -> windows to send there
	stage := make(map[uint64][]uint32) // shown space -> windows to bring to it
	var resize []*arrangement.Decision
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
		wid := d.Seen.Binding
		if !d.Resize {
			if d.Move {
				home[target] = append(home[target], wid)
			}
			continue
		}
		resize = append(resize, d)
		shown := s.currentOf[displayOf[d.Want.Space]]
		at := s.winSpace[wid]
		if shown == 0 || at == shown && target == shown {
			continue // already on its shown home space
		}
		if at != shown {
			stage[shown] = append(stage[shown], wid)
		}
		if target != shown {
			home[target] = append(home[target], wid)
		}
	}
	if len(home) == 0 && len(resize) == 0 {
		return nil
	}
	staged, tStage := moveAndWait(stage)
	t0 := time.Now()
	want := make(map[uint32]layout.Rect, len(resize))
	for _, d := range resize {
		f := d.Want.Frame
		if err := skylight.SetWindowFrame(d.Seen.PID, d.Seen.Binding, f.X, f.Y, f.W, f.H); err == nil {
			want[d.Seen.Binding] = f
		}
	}
	_, framed := waitFor(stepTimeout, func() bool { return framesAre(want) })
	tResize := time.Since(t0)
	returned, tHome := moveAndWait(home)
	if len(resize) > 0 {
		log.Printf("repair: %d window(s) resized (%d staged in %s, resize %s settled=%v, %d moved home in %s)",
			len(resize), staged, tStage.Round(time.Millisecond), tResize.Round(time.Millisecond), framed, returned, tHome.Round(time.Millisecond))
	}
	return nil
}

// stepTimeout bounds each awaited step of a repair.
const stepTimeout = 2 * time.Second

// moveAndWait moves each group of windows to its space and waits until the
// window server reports every one there. It returns how many it moved and
// how long the wait took.
func moveAndWait(groups map[uint64][]uint32) (int, time.Duration) {
	n := 0
	for space, wids := range groups {
		if err := skylight.MoveWindowsToSpace(wids, space); err != nil {
			log.Printf("moving %d window(s) to space %d: %v", len(wids), space, err)
			continue
		}
		n += len(wids)
	}
	if n == 0 {
		return 0, 0
	}
	took, _ := waitFor(stepTimeout, func() bool {
		for space, wids := range groups {
			for _, wid := range wids {
				ids, err := skylight.SpacesForWindow(wid)
				if err != nil || len(ids) != 1 || ids[0] != space {
					return false
				}
			}
		}
		return true
	})
	return n, took
}

// framesAre reports whether the window server shows each window at its
// wanted frame.
func framesAre(want map[uint32]layout.Rect) bool {
	if len(want) == 0 {
		return true
	}
	wins, err := skylight.WindowList()
	if err != nil {
		return false
	}
	seen := 0
	for _, w := range wins {
		f, ok := want[w.Number]
		if !ok {
			continue
		}
		seen++
		if !arrangement.SameFrame(f, layout.Rect{X: w.Bounds.X, Y: w.Bounds.Y, W: w.Bounds.Width, H: w.Bounds.Height}) {
			return false
		}
	}
	return seen == len(want)
}

// waitFor polls cond every 20 ms until it holds or the timeout passes.
func waitFor(timeout time.Duration, cond func() bool) (time.Duration, bool) {
	start := time.Now()
	for {
		if cond() {
			return time.Since(start), true
		}
		if time.Since(start) >= timeout {
			return time.Since(start), false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
```

  The fixed `time.Sleep(500 * time.Millisecond)` after moves goes with the old `repair`. A refused Accessibility write still counts as an attempt (the decision keeps `Resize`), so the next look gives up on the window and logs it, and the agent does not stage it again every minute.

- [ ] **Step 4:** build, vet, test, gofmt clean. Stage, checkpoint.

- [ ] **Step 5: verify on this machine** (orchestrator, at a time the user approves; visible effect: two windows from another space appear on the visible space and vanish, twice). After `make install`:

  1. With the staging harness (window-server move, Accessibility one-shot, move back), shorten two windows that live on a hidden space; let a look adopt nothing (they are on a hidden space, so the look owes and repairs them): `watch.log` shows `owed 2` and a `repair: 2 window(s) resized (2 staged in …, resize …, 2 moved home in …)` line; the next look logs `released 2`; both windows are on their space at their recorded frames.
  2. Record the three durations from the `repair:` line as the measured cost.
  3. A window on the wrong space with the right frame is moved home with no staging (`repaired 1`, no `repair:` resize line).

- [ ] **Step 6:** Commit message 2.

---

### Task 3: docs

- [ ] Spec, **Look**: back to "every minute, at each space change, and immediately before an announced disturbance", keeping the two-sample sentence. Rule 4 (Repair): "An owed window is repaired at once. If it is on the wrong space it is moved to its own. If its frame is wrong it is resized through Accessibility, which only reaches a window on a shown space: a window on a hidden space is moved with the others to the space its display is showing, resized, and moved home." Rule 5 (Release): an owed window seen in place is released; one still out of place at the look after an attempt is released where it is, adopted, and logged.
- [ ] README "The agent": replace "moved to its space at once and resized when its space is visible, since Accessibility only reaches windows on a visible space" with "moved to its space and resized at once; a window on a hidden space is brought to the visible one for the resize and returned, since Accessibility only reaches windows on a shown space".
- [ ] TODO: no item to add or remove unless verification leaves one.
- [ ] Commit message 3 (with this plan).

---

## Commit messages (verbatim, in order)

1. `arrangement: an owed window is repaired at once, wherever its space is`
2. `spacekeeper watch: resize owed windows by staging them on the visible space`
3. `docs: repairs no longer wait for a visit`
