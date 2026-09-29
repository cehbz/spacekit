# TODO

## spacekeeper

- Verify the watch agent on a real event: after the next screens wake, `~/Library/Logs/spacekeeper/watch.log` should show `display reconfiguration began`, then `displays settled; restored from ...` with `moved` matching any Chrome windows that landed on the built-in display. Check `verified` equals `moved`.
- Verify at the next login that `login convergence` runs from the power-off snapshot and the event passes report `verified` counts (moves now run on the main thread via sysevents.OnMain). Check `watch.log` for the `will power off` snapshot from the preceding shutdown.
- Tune convergence numbers (2m quiet-exit, 10m cap, 15s sweep, 1.5s post-launch delay) once a few real logins have logged pass timings.
- Restore by verdict: tail the unified log for WindowServer's `PKGWindowMoveOnMatchingDisplayChangedSeed window <hex> ... likely misplaced` lines and move just those window IDs back as they are flagged, instead of waiting for the burst to settle and re-matching every window. Precise and ~10 s faster; depends on a log string Apple can change, so keep the settle path as the fallback.
- Multi-display space creation assumes SLS display order matches the Mission Control AX `mc.display` order. Held on this 2-display setup; revisit if it ever creates desktops on the wrong display.
- AX-title fallback only covers the active space (AX enumerates ~current-space windows); full titles still need Screen Recording. Fine, just noting the ceiling.
