# TODO

## spacekeeper

- Tune converge numbers (2m quiet-exit, 10m cap, 15s sweep, 1.5s post-launch delay) once a few real reboots have logged pass timings to `/tmp/spacekeeper-restore.log`.
- Confirm `SLSBridgedMoveWindowsToManagedSpaceOperation` works from converge's worker goroutine (off the main thread) at the next real reboot — only the main-thread initial pass has performed a move so far; check the log's `verified` counts on event-triggered passes.
- Multi-display space creation assumes SLS display order matches the Mission Control AX `mc.display` order. Held on this 2-display setup; revisit if it ever creates desktops on the wrong display.
- AX-title fallback only covers the active space (AX enumerates ~current-space windows); full titles still need Screen Recording. Fine, just noting the ceiling.
