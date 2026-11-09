# TODO

## spacekeeper

- A relaunched window can bind to a stale record: `Bind` ranks a same-title match above a same-frame one, and a record whose window vanished is never closed, so PocketSmith's relaunched login window (title `PocketSmith`, standing at the Transactions window's 0,30 1280×1410) bound to a Nov 1 record of a window closed days earlier and was put at that record's 115,320 1050×800 (Nov 7 07:48:20). Decide how a record's presence at the last look weighs against a title match, or when a vanished window's record closes.
- Compare the drawn rect (`SLSGetScreenRectForWindow`, what `CGWindowListCopyWindowInfo` reports) with the window's bounds (`SLSGetWindowBounds`) at a look; when they disagree, a bridged move to another space and back re-syncs them, where position writes do not. Without it such a window reads as off target after every put, the put fails, and the window is marked dirty again at the next display change.
- Chrome's untitled popup windows (813×89 at 99,55 on 972AEF80, 814×89 at 1379,55 on the Dell's unnamed desktop) are marked dirty at every wake and their put fails a decide later (Oct 23 15:07, Oct 25 13:55). Decide whether untitled Chrome windows of that shape are placed at all.
- Restore by verdict: tail the unified log for WindowServer's `PKGWindowMoveOnMatchingDisplayChangedSeed window <hex> ... likely misplaced` lines and move just those window IDs back as they are flagged, instead of waiting for the burst to settle and re-matching every window. Precise and ~10 s faster; depends on a log string Apple can change, so keep the settle path as the fallback.
- Multi-display space creation assumes SLS display order matches the Mission Control AX `mc.display` order. Held on this 2-display setup; revisit if it ever creates desktops on the wrong display.
- AX-title fallback only covers the active space (AX enumerates ~current-space windows); full titles still need Screen Recording. Fine, just noting the ceiling.
- Prompt for Screen Recording when `watch` starts without it (the TCC prompt is the one visible channel) and show the grant in `inspect`. Without it window titles come only from AX, which sees the current space.
- A window whose space id is no longer in the space list gets an empty space key, and since `placement.space` now references `space` the whole `Apply` fails instead of recording that one window. Skip windows with no space key in `seenWindows` (watch.go) so the rest of the look still lands.
- An app's display name is written once, when its row is created, and never updated. Refresh it on each look (or on `Prune`) so a renamed app reads right in `log` and `inspect`.
- The latest-binding lookup in `Stored` (`MAX(rowid) ... WHERE window_id = ?`) has no index on `binding(window_id)`; fine at 300 windows, add the index if `Stored` shows up in a profile.
- The migration runner should run `PRAGMA foreign_key_check` before `COMMIT` and roll back on a violation, so a bad rebuild can't commit.
- `TestModelLaptopBootBindsDualOnlyRecordAndDualReturnPutsItBack` starts the second boot without waiting for the first model to finish, and drops the error from `Recorded`. Await the first model and check the error.
