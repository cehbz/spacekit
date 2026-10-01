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

**Binding.** The window-server id a window has in the current boot. Within a
boot, windows are identified by binding alone. Matching by app, title and
frame creates bindings only where ids are new: at login and after an app
relaunch.

**Look.** An observation of the live session: every minute, at each space
change, and immediately before an announced disturbance (screen or system
sleep, power-off).

**Disturbance.** A period during which the screen is not intent: a display
change (until quiet for 10 s), the Mission Control overview, logout, a locked
or sleeping session, and for one app's windows, that app's relaunch.

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
4. **Repair.** An owed window is moved to its space at once (the window-server
   move works for any space) and resized when its space is visible
   (Accessibility reaches only visible spaces; position, then size, then
   position). Repairs are attempted at every look.
5. **Release.** An owed window seen in place is released. One whose repair was
   attempted while visible and is still out of place at the following look is
   released where it is, adopted, and logged.
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
   release, overview and lock as disturbances. Identity by binding only. The
   old wake restore and frame debt are removed; snapshot saving and login
   convergence stay, unchanged, as the only login path.
2. **Binding by matching.** Login and app relaunch bind through matching and
   owe; the arrangement is seeded from the newest snapshot once. Snapshots,
   their commands and login convergence are removed.
3. **Repair tooling and retention.** `log`, `undo`, `owe`; 90-day pruning;
   README, TODO and KB.

Each slice ends installed and running, verified by an induced display burst
and, where it applies, a real wake.

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
- Binding across a reboot or relaunch is still matching by app, title and
  frame.
