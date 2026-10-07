# Overview disturbance and provisional bindings — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task by task.

**Goal:** A look is never taken while Mission Control or Exposé is showing, and a window that comes back under a new window-server id, in a running app or a relaunched one, is bound to its record instead of becoming a second one.

**Architecture:** The overview is detected from the window list the look already fetches: a `WindowManager` window titled `ExposeShieldWindow` exists from entry to exit of Mission Control and App Exposé, so a sample that meets it puts the look off and the look is retried, as for a moving screen. The overlay-window check goes. A record's binding is provisional: it is dead as soon as its id is absent from the raw window list (minimized windows stay in it, off screen), and every live window no record claims is matched against its app's unbound records by the existing evidence rules, whatever the app's process did. App runs (process start times, known runs, the relaunching set) go. The agent's log stamps lines in the zone `/etc/localtime` points at now, not at start.

**Tech Stack:** Go 1.26, existing packages.

**Spec:** `docs/superpowers/specs/2026-10-01-arrangement-model.md`; Task 4 amends rules 1, 3 and 7 and the "Relaunch binding" slice.

## Measured basis

- 2026-10-07 12:27:47: a look during Mission Control adopted thumbnail frames for every window of the current and previous space (1470×923 → 492×309), owed and staged 14 hidden-space windows, and at the next look resized the hidden space's windows down to the thumbnails. `layout.OverviewOpen` saw no `Window Highlight Overlay` at either sample.
- Sampler at 0.4 s during three overviews: the overlay existed for 0.7 s of a keyboard Mission Control, 6 s of a second, 1.8 s of an App Exposé. It marks the highlight, not the overview.
- Sampler at 0.2 s over the on-screen list: `WindowManager` `ExposeShieldWindow` (layer 19) present for the whole of a Mission Control (13:30:01 to 13:30:09) and an App Exposé (13:30:16 to 13:30:27); `Spaces Bar` (layer 14) for Mission Control only; the overlay for under a second of each; a nameless layer-20 Dock window during both and also alone at Dock reveals. The gather keeps layer 0 only, so the marker must be read from the raw list.
- yabai (`src/mission_control.c`, `src/event_loop.c`) observes the Dock's AX element for `AXExposeShowAllWindows`, `AXExposeShowFrontWindows`, `AXExposeShowDesktop`, `AXExposeExit`; the shield marker gives the same span without an observer.
- Sampler at 0.3 s over the all-windows list: an iTerm window minimized for 19 s stayed listed under its id with `kCGWindowIsOnscreen` absent (13:34:29 to 13:34:48).
- WhatsApp 2026-10-07 12:48:47: same process (pid 1453 since Sep 29), window id 149 gone, 24389 present with the same title on the then-active space; adopted as a second record because `evidence` rejects a candidate bound in the fresh window's own run.
- A window staged through the visible space at 12:28:25, during or just after the overview's exit, kept a drawn rect (`SLSGetScreenRectForWindow`, `CGWindowListCopyWindowInfo`) frozen at x=-387 while its bounds (`SLSGetWindowBounds`, Chrome's AX frame) followed every write (0, 1, 200); a bridged move to another space and back re-synced it. Holding looks through the overview removes the path that produced it.

## Global Constraints

- Work on `main`. Commit messages are in "Commit messages" below, in `/tmp/cg-batch-spacekit-765a`; subagents stage and checkpoint with `git stash create`, the orchestrator commits after approval.
- `go build ./... && go vet ./... && go test ./...` clean at every commit; `gofmt -l cmd internal` empty.
- TDD: each rule change starts with a test that compiles and fails on an assertion.
- All SkyLight and Accessibility calls stay on the main thread.
- Subagents do not run `make install`, `launchctl`, or the built binaries.
- Comments state what exists, tersely; no "new", "now", "old".
- Decisions: dead records stay eligible for binding until pruning (user, 2026-10-07). Ties stay unmatched.

---

### Task 1: the overview marker

**Files:** Modify `internal/layout/layout.go`, `layout_test.go`, `cmd/spacekeeper/main.go` (`snapshot`, `gather`, `saveSnapshot`), `cmd/spacekeeper/watch.go`.

**Produces:** `layout.OverviewOpen(all []skylight.Window) bool` true when the list holds a `WindowManager` window titled `ExposeShieldWindow`; `snapshot` carries the raw list (`all`) beside the filtered `windows`, and the two callers pass it. A look whose first or second sample meets the overview is put off and retried like one that found the screen moving (`w.retry = trigger`, logged `look (%s) put off: the overview is showing`); `saveSnapshot` keeps its skip with the same predicate.

- [ ] **Step 1: tests first.** `layout_test.go`: `OverviewOpen` true for a list with the shield window, false for one with only the highlight overlay (the 2026-10-07 case), false for an empty list.
- [ ] **Step 2: implement**, thread `all` through `snapshot`, retry a put-off look.
- [ ] **Step 3: on-machine check (orchestrator).** `make agent-install`; open Mission Control from the keyboard with the pointer still and hold 10 s across an interval look (`watch.log` shows `put off: the overview is showing`, then the retried look after exit); same for App Exposé; `spacekeeper log` shows no thumbnail frames adopted.

### Task 2: provisional bindings

**Files:** Modify `internal/arrangement/bind.go`, `bind_test.go`, `arrangement.go`, `internal/store/store.go`, `schema.sql` (migration), `store_test.go`, `cmd/spacekeeper/watch.go`; delete `cmd/spacekeeper/boottime_darwin.go`'s `processStart`.

**Produces:** `evidence` rejects a candidate only while its id is live; `Seen.Run`, `Stored.Run`, the `binding.run` column, `KnownRuns`, `NoteRuns`, `watcher.relaunching`, `processStart` go. `rebind` takes every seen window whose id no record of this boot claims as fresh, every record whose id is absent from the raw list (`snapshot.all`, not the filtered `windows`: a minimized or sticky window is alive but unplaced) as a candidate, and binds by `arrangement.Bind` as today; the journal event stays `bound`, the log line reads `bound N of M unclaimed window(s) to their records`. The first look of a boot is the same path (every record unbound, every window unclaimed), which is slice 2b's login case; 2b itself (removing snapshots and login convergence) stays a separate plan.

- [ ] **Step 1: tests first.** `bind_test.go`: a fresh window whose app's process is unchanged binds to a same-titled record whose id is absent (the WhatsApp case); a record whose id is live is never a candidate; two same-titled dead records leave the fresh window unbound. `store_test.go`: `Bind` and `Stored` without `run`; migration from a schema with the column.
- [ ] **Step 2: arrangement.** Drop the run clause from `evidence`; drop `Run` from `Seen` and `Stored`.
- [ ] **Step 3: store.** Migration `user_version` +1: rebuild `binding` without `run` (`ALTER TABLE ... DROP COLUMN` under modernc; verify it runs on a copy of the live store). Remove `KnownRuns`, `NoteRuns`.
- [ ] **Step 4: watcher.** `rebind` as above; remove `relaunching`, `NoteRuns` call, `processStart`; the `relaunch` look trigger stays for the app-launch event.
- [ ] **Step 5: on-machine check (orchestrator).** Quit and reopen Activity Monitor: one window row, two bindings, moved home. Close and reopen a WhatsApp window from the Dock on another space: bound to record 16, moved home, no record added. Minimize a window across a look: no `bound` or adopted event for it, its binding intact.

### Task 3: log lines in the current zone

**Files:** Add `cmd/spacekeeper/zone.go`, `zone_test.go`; modify `cmd/spacekeeper/watch.go`.

**Produces:** `log.SetFlags(0)` and a `log.SetOutput` writer that prefixes each line with `2006/01/02 15:04:05` in a `*time.Location` loaded from the target of the `/etc/localtime` link, reloaded whenever `os.Readlink` returns a different target. The journal is unaffected (unix ms, formatted at read).

- [ ] **Step 1: test first.** `zone_test.go`: with a link in a temp dir pointing at `Asia/Bangkok` then re-pointed at `Europe/London`, consecutive stamps differ by the offset.
- [ ] **Step 2: implement**, install, confirm `watch.log` stamps agree with `date`.

### Task 4: spec, TODO, README

**Files:** Modify `docs/superpowers/specs/2026-10-01-arrangement-model.md`, `TODO.md`, `README.md`.

- Rule 1 names the overview among disturbances; rule 3 drops "An app relaunch owes that app's windows" in favour of "A window bound to a record is owed that record's placement"; rule 7 reads "Vanished windows keep their placements, unbound, and bind to the next unclaimed window of their app the evidence picks, until retention expires."
- Slice "Relaunch binding (2a)" is rewritten as "Provisional bindings".
- TODO.md: remove "Re-anchor after bridged moves" (its symptom is the frozen drawn rect, produced by staging during the overview, which Task 1 closes) and "relaunch binding displaced case" (subsumed). README's agent section: one sentence each on the overview hold and on binding.

## Commit messages

```
spacekeeper watch: put a look off while Mission Control or Exposé is showing

WindowManager's ExposeShieldWindow is in the window list for the whole of
either overview, so a sample that meets it puts the look off and the look
is retried, as for a moving screen. The Window Highlight Overlay check
goes: that window marks the highlighted thumbnail, not the overview, and
a look taken through its gaps adopted thumbnail frames and shrank a
space's windows to them.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0134rzKMitheKoKhmp9dP5gc
@@COMMIT-GATE-SEP@@
arrangement, store: a binding is provisional; app runs go

A record's binding is dead as soon as its window id is absent from the
window list, and any unclaimed live window of the app can bind to it on
the evidence rules, whether the process restarted or rebuilt its window.
Process start times, known runs and the relaunching set are removed, and
the binding table loses its run column.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0134rzKMitheKoKhmp9dP5gc
@@COMMIT-GATE-SEP@@
spacekeeper watch: log lines in the zone /etc/localtime points at now

Go loads the local zone once at start; after a zone change the agent's
log drifted from the journal by the difference.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0134rzKMitheKoKhmp9dP5gc
@@COMMIT-GATE-SEP@@
docs: the overview disturbance and provisional bindings

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0134rzKMitheKoKhmp9dP5gc
```
