# TODO

## spacekeeper

- Restore-at-login picks stale layouts (2026-07-09: restored a 3-week-old high-water snapshot instead of the pre-shutdown layout; `Richer` has no age bound). Also the save agent's `RunAtLoad` snapshots the post-boot scramble before the restore agent's settle expires, so `-latest` is unsafe at login. Fix: record boot session (`kern.boottime`) + uptime in each snapshot; login restore defaults to the newest snapshot from a previous boot session; high-water becomes explicit fallback/opt-in.
- Multi-display space creation assumes SLS display order matches the Mission Control AX `mc.display` order. Held on this 2-display setup; revisit if it ever creates desktops on the wrong display.
- AX-title fallback only covers the active space (AX enumerates ~current-space windows); full titles still need Screen Recording. Fine, just noting the ceiling.
