# spacekeeper: the arrangement model

Replaces snapshot history and its selection heuristics with an explicit record
of intent and one reconcile loop.

## Problem

spacekeeper records what the screen looks like and later guesses which record
was intended and whether a difference is the user's doing or the system's.
Every defect since September was that guess failing: logout, monitor-outage,
dark-wake and Mission Control snapshots chosen as references; windows judged
"in place" by space alone; a drifting frame mistaken for a user move. Windows
are also re-identified by title and frame after each wake, although the frame
is what a wake corrupts and window ids are stable for the whole boot.

## Model

**Arrangement.** The intended placement of every window for one display set.
There is one arrangement per display set: Dell plus Air, Air alone, and any
other set that occurs. Changing display set switches arrangement.

**Placement.** A window's space and frame within an arrangement, or its
fullscreen state on a display.

**Window.** A lasting identity: app, latest title, and a history of
placements. It outlives reboots and app relaunches.

**Binding.** The window-server id a window has in the current boot, together
with the run of the app it belongs to (the owning process's start time).
Within a run, windows are identified by binding alone, and a fresh id is a
window the user opened. A fresh id in a run the store has not seen is a
window recreated by a restart: it is bound to a stored window of the same app
only on unambiguous evidence, the same non-empty title or a frame within the
hysteresis band, with title and frame together settled first. Without such
evidence it is a window of its own. A wrong binding moves an unrelated
window; a missed one leaves a window where the app put it.

**Look.** An observation of the live session: every minute, at each space
change, and immediately before an announced disturbance (screen or system
sleep, power-off). A look is two samples 400 ms apart that agree; if anything
moved between them the screen is in motion and the look is put off and
retried. During a space-switch slide the window server reports every window
shifted sideways, and a single sample taken then reads as if every window
had been moved.

**Disturbance.** A period during which the screen is not intent: a display
change (until quiet for 10 s), a screen in motion (a space switch, a drag,
any animation), the Mission Control overview, logout, a locked
or sleeping session, and for one app's windows, that app's relaunch: from the
first look that sees a run of the app the store does not know until a look in
which none of that run's windows is fresh.

**Owed.** A window whose placement was put at risk by a disturbance and has
not yet been seen back in place.

## Rules

1. **Adoption.** At a look, a window whose live placement differs from its
   recorded one is adopted (the arrangement follows the screen) only if no
   disturbance is in progress, the window is not owed, and it was on a visible
   space at this look or the previous one, counting both its old and its new
   space. A change to a window on a space not visible in that time is not the
   user's: the window becomes owed.
2. **First sighting.** A window with no placement in the current arrangement
   is adopted where it is.
3. **Disturbance.** When a display change settles, every window with a
   placement in the now-current arrangement is owed. An app relaunch owes that
   app's windows. Login owes everything.
4. **Repair.** An owed window is repaired at once. If it is on the wrong
   space it is moved to its own. If its frame is wrong it is resized through
   Accessibility, which only reaches a window on a shown space: a window on a
   hidden space is moved, with the others, to the space its display is
   showing, resized there, and moved home (measured for two windows: 30 ms to
   stage, 104 ms to resize, 21 ms to return).
5. **Release.** An owed window seen in place is released. One still out of
   place at the look after an attempt is released where it is, adopted, and
   logged.
6. **In place** means the same space and a frame within 3 px on each edge.
   That band is hysteresis: a difference inside it is neither adopted nor
   repaired, so a pixel of drift never becomes a change.
7. **Vanished windows** keep their placements, unbound, for matching at the
   next relaunch or login, until retention expires.

## Storage

SQLite (`modernc.org/sqlite`, WAL, single writer) in
`~/.config/spacekeeper/spacekeeper.db`, behind `internal/store` with an
embedded schema. Placements are system-versioned rows: each carries the period
during which it was true; a change closes the open row and opens a new one in
one transaction. The current arrangement is the open rows; history is the
closed ones. Entities: window, binding, arrangement (display set and its
spaces), placement (versioned, with the owed flag), change (cause: adopted,
restored, released, bound, undone), disturbance (kind, start, end). The agent
holds no state between looks that is not in the database. Closed versions
older than 90 days are deleted.

## What is retired

Snapshot files and `layout.json`; `save`, `list`, `show`, `restore` and their
selection flags (`-latest`, `-from`, `-high-water`, `-settled`); the
previous-boot tiers, high-water pin, `ReferenceFor`, `LatestBefore`,
`FirstStartOfBoot`; frame debt with its baseline frames; the save guards as
special cases (they become disturbances); login convergence as a mode; the
pending-burst file.

Kept: gathering the session, the window-server move, Accessibility frame and
fullscreen writes, recreating missing desktops, `sysevents`, `settle`,
matching (now only for binding), `inspect`.

## Repair tooling (for diagnosis, not for browsing)

`spacekeeper log` lists changes and disturbances with their causes;
`spacekeeper undo <change>` reverts one change and owes the affected windows;
`spacekeeper owe` marks everything owed, the replacement for a manual restore.

## Delivery in slices

1. **In-session.** Store, looks, adoption, display disturbance, owed, repair,
   release, overview and lock as disturbances.
2. **Relaunch binding (2a).** App runs on bindings; fresh windows of a
   restarted app are bound on evidence and owed. Snapshot saving and login
   convergence stay as the login path.
3. **Login through the model (2b).** After a reboot every app is a restart,
   so the same binding serves login. Snapshots, their commands and login
   convergence are removed; a command that moves or resizes one window by id
   replaces `restore -f` for on-machine tests.
4. **Repair tooling and retention.** `log`, `undo`, `owe`; 90-day pruning.

Each slice ends installed and running, verified on the machine.

## Testing

Rules live in a cgo-free package and are unit-tested from tables of
(recorded arrangement, look, visibility, disturbance) to decisions. The store
is tested against a temporary database, asserting rows. The agent loop stays
thin glue over those two.

## Known limits

- A disturbance the agent fails to recognize can be adopted for windows on the
  visible spaces; windows elsewhere are protected by rule 1.
- AppKit's screen for a window is not observable; re-anchoring after a move
  stays an open item.
- Binding across a reboot or relaunch is matching by app, title and frame; a
  recreated window whose title changed and whose frame is shared with others
  stays unbound.
- Fullscreen windows have no placements and are not restored.
