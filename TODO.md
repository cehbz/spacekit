# TODO

## spacekeeper

- Verify the watch agent on a real event: after the next screens wake, `~/Library/Logs/spacekeeper/watch.log` should show `display reconfiguration began`, then `displays settled; restored from ...` with `moved` matching any Chrome windows that landed on the built-in display. Check `verified` equals `moved`.
- Verify at the next login that `login convergence` runs from the previous-boot snapshot and the event passes report `verified` counts (moves now run on the main thread via sysevents.OnMain).
- Tune convergence numbers (2m quiet-exit, 10m cap, 15s sweep, 1.5s post-launch delay) once a few real logins have logged pass timings.
- Dell U2725QE "TBT Switch when PC Sleep" was set to OFF on 2026-09-02. At the next system wake, check `log show` for `Processing hotplug` on display 2 at the wake time; if absent, the hotplug cycle is gone for system sleeps.
- Multi-display space creation assumes SLS display order matches the Mission Control AX `mc.display` order. Held on this 2-display setup; revisit if it ever creates desktops on the wrong display.
- AX-title fallback only covers the active space (AX enumerates ~current-space windows); full titles still need Screen Recording. Fine, just noting the ceiling.
