# TODO

## spacekeeper

- Subscribe to the window server's own events instead of sampling: yabai registers connection notifications 804 (window destroyed), 808 (window ordered) and 1327/1328 (space created/destroyed) via `SLSRegisterConnectionNotifyProc`. A window's death and rebirth then arrives as an event, binding happens at once rather than at the next look, and the minute timer becomes a safety net rather than the mechanism.
- Compare the drawn rect (`SLSGetScreenRectForWindow`, what `CGWindowListCopyWindowInfo` reports) with the window's bounds (`SLSGetWindowBounds`) at a look; when they disagree, a bridged move to another space and back re-syncs them, where position writes do not. Without it such a window reads as out of place at every repair, is released unrepaired and re-owed at the next disturbance.
- Slice 2b: login through the model (after a reboot every app is a restart), then remove snapshots, their commands and login convergence; add a command that moves or resizes one window by id for on-machine tests.
- Each look runs on the main thread, including a 500 ms wait after window-server moves; measure a look with many repairs and move the wait off the main thread if it stalls event delivery.
- Verify at the next login that `login convergence` runs from the power-off snapshot and the event passes report `verified` counts (moves now run on the main thread via sysevents.OnMain). Check `watch.log` for the `will power off` snapshot from the preceding shutdown.
- Tune convergence numbers (2m quiet-exit, 10m cap, 15s sweep, 1.5s post-launch delay) once a few real logins have logged pass timings.
- Restore by verdict: tail the unified log for WindowServer's `PKGWindowMoveOnMatchingDisplayChangedSeed window <hex> ... likely misplaced` lines and move just those window IDs back as they are flagged, instead of waiting for the burst to settle and re-matching every window. Precise and ~10 s faster; depends on a log string Apple can change, so keep the settle path as the fallback.
- Multi-display space creation assumes SLS display order matches the Mission Control AX `mc.display` order. Held on this 2-display setup; revisit if it ever creates desktops on the wrong display.
- AX-title fallback only covers the active space (AX enumerates ~current-space windows); full titles still need Screen Recording. Fine, just noting the ceiling.
