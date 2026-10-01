# TODO

## spacekeeper

- Verify the arrangement model on a real wake: `watch.log` should show `display change: N window(s) owed their placement`, `repaired` lines as spaces are visited, `released`, and no `released unrepaired`.
- Confirm in `watch.log` that looks are put off during space switches in normal use (`put off: the screen is in motion`) and that no placement is adopted with a frame outside its display.
- Verify relaunch binding on a Chrome quit and reopen: `watch.log` should show `relaunch: bound N of M`, repairs and releases, and the count of Chrome rows in `window` should grow only by windows with no match.
- Slice 2b: login through the model (after a reboot every app is a restart), then remove snapshots, their commands and login convergence; add a command that moves or resizes one window by id for on-machine tests.
- Slice 3: `log`, `undo` and `owe` commands over the journal; delete closed placement versions older than 90 days.
- Each look runs on the main thread, including a 500 ms wait after window-server moves; measure a look with many repairs and move the wait off the main thread if it stalls event delivery.
- Verify at the next login that `login convergence` runs from the power-off snapshot and the event passes report `verified` counts (moves now run on the main thread via sysevents.OnMain). Check `watch.log` for the `will power off` snapshot from the preceding shutdown.
- Tune convergence numbers (2m quiet-exit, 10m cap, 15s sweep, 1.5s post-launch delay) once a few real logins have logged pass timings.
- Re-anchor after bridged moves: a window the agent moved back to its space can be left with AppKit believing it is on the other display (green-button menu and tiling options acted on the built-in display for a chat-space Chrome window). It was cured after a display reconfiguration and a 1 px AX position write; find which one does it, then have the settle pass apply the cheap one to every moved window on an active space.
- Restore by verdict: tail the unified log for WindowServer's `PKGWindowMoveOnMatchingDisplayChangedSeed window <hex> ... likely misplaced` lines and move just those window IDs back as they are flagged, instead of waiting for the burst to settle and re-matching every window. Precise and ~10 s faster; depends on a log string Apple can change, so keep the settle path as the fallback.
- Multi-display space creation assumes SLS display order matches the Mission Control AX `mc.display` order. Held on this 2-display setup; revisit if it ever creates desktops on the wrong display.
- AX-title fallback only covers the active space (AX enumerates ~current-space windows); full titles still need Screen Recording. Fine, just noting the ceiling.
