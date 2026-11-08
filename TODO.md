# TODO

## spacekeeper

- A relaunched window can bind to a stale record: `Bind` ranks a same-title match above a same-frame one, and a record whose window vanished is never closed, so PocketSmith's relaunched login window (title `PocketSmith`, standing at the Transactions window's 0,30 1280×1410) bound to a Nov 1 record of a window closed days earlier and was repaired to that record's 115,320 1050×800 (Nov 7 07:48:20). Decide how a record's presence at the last look weighs against a title match, or when a vanished window's record closes.
- Compare the drawn rect (`SLSGetScreenRectForWindow`, what `CGWindowListCopyWindowInfo` reports) with the window's bounds (`SLSGetWindowBounds`) at a look; when they disagree, a bridged move to another space and back re-syncs them, where position writes do not. Without it such a window reads as out of place at every repair, is released unrepaired and re-owed at the next disturbance.
- Chrome's untitled popup windows (813×89 at 99,55 on 972AEF80, 814×89 at 1379,55 on the Dell's unnamed desktop) are owed at every wake and `released unrepaired` a look later (Oct 23 15:07, Oct 25 13:55). Decide whether untitled Chrome windows of that shape are placed at all.
- Restore by verdict: tail the unified log for WindowServer's `PKGWindowMoveOnMatchingDisplayChangedSeed window <hex> ... likely misplaced` lines and move just those window IDs back as they are flagged, instead of waiting for the burst to settle and re-matching every window. Precise and ~10 s faster; depends on a log string Apple can change, so keep the settle path as the fallback.
- Multi-display space creation assumes SLS display order matches the Mission Control AX `mc.display` order. Held on this 2-display setup; revisit if it ever creates desktops on the wrong display.
- AX-title fallback only covers the active space (AX enumerates ~current-space windows); full titles still need Screen Recording. Fine, just noting the ceiling.
