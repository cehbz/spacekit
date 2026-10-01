# spacekit

A macOS Spaces (Mission Control) toolkit: switch spaces from a mouse tilt wheel, and save/restore window-to-space layouts. SIP stays enabled.

## spaceswitch

Switches Mission Control spaces with the native slide animation. No SIP changes, no private write APIs. The mouse tilt wheel (horizontal scroll click) becomes previous/next space.

macOS ignores synthetic Ctrl+Arrow shortcuts posted from ordinary session-level event sources, which is why Hammerspoon's `keyStroke` and similar approaches silently fail. spaceswitch builds the keystroke from the HID system state event source and posts it to the HID event tap (`kCGEventSourceStateHIDSystemState`, `kCGHIDEventTap`). Mission Control accepts that and performs a real switch.

Daemon mode runs an event tap on scroll events. A tilt click arrives as a single discrete horizontal-only scroll delta. The daemon consumes it and posts Ctrl+Left/Right. Vertical scrolling, continuous (trackpad and Magic Mouse) scrolling, and shift+wheel synthesized horizontal scroll all pass through untouched.

Tested on macOS 26.5 (Tahoe), Apple Silicon, with a Logitech M720. The only private API use is read-only SkyLight introspection for `status` and `-verify`; the switch itself is plain CGEvent posting.

### Build

```
go build -o spaceswitch ./cmd/spaceswitch
```

### Usage

```
spaceswitch left|right [-verify]   one-shot switch; -verify confirms the active space changed
spaceswitch status                 print the active space ID
spaceswitch daemon [-invert]       tilt wheel -> space switch
```

### Run at login

Save as `~/Library/LaunchAgents/spaceswitch.plist` (adjust the binary path), then `launchctl bootstrap gui/$UID ~/Library/LaunchAgents/spaceswitch.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>spaceswitch</string>
	<key>ProgramArguments</key>
	<array>
		<string>/Users/you/bin/spaceswitch</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardErrorPath</key>
	<string>/tmp/spaceswitch.log</string>
</dict>
</plist>
```

### Permissions

The daemon needs Accessibility (System Settings, Privacy & Security, Accessibility). Under launchd the binary is its own TCC subject, so grant it to the binary itself, not your terminal.

### Stable signing

macOS binds an Accessibility grant to a binary's code signature. An ad-hoc signature (the default) changes its cdhash on every rebuild, so each rebuild orphans the grant, makes macOS re-prompt, and leaves a stale duplicate entry in the Accessibility list. Signing with a stable self-signed identity fixes this for good: the grant then keys on the identifier plus certificate (`identifier "bz.ceh.spaceswitch" and certificate leaf = H"…"`) and survives rebuilds.

The signing identity lives in 1Password, not in any keychain. The build pulls it into a throwaway keychain that is deleted when signing finishes — nothing is written to the login keychain.

One-time setup:

1. `./scripts/gen-signing-cert.sh` — generates a self-signed Code Signing certificate (via `openssl`, no Keychain Access) as a password-protected `.p12` and prints the password plus a ready-to-run `op item create`.
2. Run the printed `op item create` to store the `.p12` and its password in 1Password, then `rm` the local `.p12`.
3. `make install` — pulls the identity from 1Password into a temporary keychain, signs both binaries, installs to `~/bin`. Falls back to ad-hoc with a warning if the secret or `op` is unavailable.
4. Remove any existing `spaceswitch`/`spacekeeper` entries from the Accessibility list, then grant once. Future rebuilds keep the grant.

Overrides: `CODESIGN_IDENTITY` (certificate CN, default `mac-knob`), `OP_P12_REF` / `OP_P12PW_REF` (the `op://` secret references). Per-machine references can go in a gitignored `signing.local.mk` that the Makefile includes, e.g.:

```make
OP_P12_REF   = op://Private/spacekit signing/p12
OP_P12PW_REF = op://Private/spacekit signing/p12password
```

Changing a binary's signature (ad-hoc → cert, or a new cert) changes its Designated Requirement, so the existing Accessibility grant stops matching and you must remove the stale row and grant once more. After that, rebuilds signed with the same cert keep the grant.

### Caveats

- Ctrl+Arrow switches the space on the display that has keyboard focus, not the display under the cursor. That matches what the physical shortcut does.
- The Mission Control shortcuts "Move left/right a space" must be enabled (they are by default).
- Mapping the tilt to space switching costs you tilt-driven horizontal scrolling. Shift+wheel still scrolls horizontally.

## spacekeeper

Saves window-to-space assignments and moves windows back later, after a reboot or a display reconfiguration has scattered them. SIP stays enabled.

```
go build -o spacekeeper ./cmd/spacekeeper

spacekeeper save              snapshot the current layout into history
spacekeeper list              list snapshots, newest first
spacekeeper restore [-n]      restore the last pre-shutdown layout; -n prints the plan
spacekeeper restore -latest   restore the newest snapshot instead
spacekeeper restore -from ID  restore a specific snapshot (timestamp substring from `list`)
spacekeeper restore -high-water   restore the richest retained snapshot
spacekeeper restore -frames   also restore each window's position and size
spacekeeper restore -create=false   skip recreating missing desktops
spacekeeper restore -fullscreen   also re-fullscreen windows that were fullscreen
spacekeeper show [-from ID]   print a snapshot's raw layout
spacekeeper watch             run resident (the launchd agent): saves, wake restore, login convergence
spacekeeper inspect           each window with its space, CGWindow bounds, and the frame its app reports via Accessibility
```

### Snapshot history

`save` writes a timestamped snapshot under `~/.config/spacekeeper/snapshots/`, deduped (nothing is written when the layout is unchanged) and pruned to the last `-keep` (default 200). It captures everything, with no judgment about whether a state is "good" — so an accidental window-nuke, a disconnected monitor, or an update closing windows all get recorded, but none of them destroy earlier snapshots.

`restore` defaults to the newest **settled previous-boot snapshot** — the layout as it was at the last shutdown. Snapshots taken shortly after a boot (uptime at save below `-settled`, default 10m) are skipped, so a login-time snapshot of the not-yet-reopened session — or a whole cycle of quick reboots — never becomes the thing that gets restored. `-high-water` selects the richest retained arrangement instead (ranked by display count, then window count, then recency); the richest snapshot is still pinned so pruning never deletes it. If the default pick looks wrong, `list` shows every snapshot with descriptive facts and boot tags (`[this-boot]`, `[unsettled]`) so you choose, and `restore -from <id>` or `-latest` overrides.

`-frames` repositions and resizes via the Accessibility API. It only affects windows on the **currently active space** — macOS won't let AX resize a window that lives on another space, so frame restore is partial unless you run it per-space. Space assignment (the default) has no such limit. Grant the running process Accessibility for `-frames` to do anything.

### The agent

`make agent-install` runs `spacekeeper watch` as a launchd agent (`bz.ceh.spacekeeper`, KeepAlive) that owns the whole lifecycle:

- **Keeps an arrangement.** For each set of displays the agent records where every window belongs: its space and frame. It looks at the session every minute, at each space change, and just before sleep or power-off. A change to a window on a space you can see is adopted as the new intent; a change to a window on a space you cannot see is not yours, so the window is put back. Differences within 3 points are ignored. The record lives in `~/.config/spacekeeper/spacekeeper.db` (SQLite); each placement keeps the period during which it was true, so the history is in the same table.
- **Repairs after a disturbance.** A display change, the Mission Control overview, a locked or sleeping session and a logout are disturbances: nothing seen during one is adopted. On Apple Silicon an external display de-registers briefly whenever the screens wake, macOS moves and shrinks windows, and Chrome often fails to bring its own back. When a display change has been quiet for 10 seconds, every window is owed its recorded placement: it is moved to its space at once and resized when its space is visible, since Accessibility only reaches windows on a visible space. A window seen back in place is released. One that a completed repair could not put back is released where it is and logged as `released unrepaired`. With the external display really gone, the arrangement for the remaining display set applies, and the full one returns with the display. An app that restarts has its recreated windows matched to their records by title or frame and put back; a window that matches nothing is treated as new.
- **Snapshots** are still saved every third look and at sleep and power-off, deduped, and skipped while Mission Control is open. They are the reference for login only.
- **Converges at login.** On its first start in a boot session (no snapshot from this boot exists yet; uptime is not used, since an OS upgrade or a slow login can start the agent long after boot), it restores the newest settled previous-boot snapshot and keeps reconciling as apps launch (a pass 1.5 s after each launch plus a 15 s sweep) until every saved window is handled, two quiet minutes pass, or the 10-minute cap. Windows already handled are never touched again, so rearranging by hand during convergence is safe. `spacekeeper watch -boot` forces this for testing.

Log: `~/Library/Logs/spacekeeper/watch.log`. `make agent-uninstall` removes it. The earlier separate save and restore agents are unloaded by `agent-install`.

The read side is SkyLight introspection (`SLSCopyManagedDisplaySpaces`, `SLSCopySpacesForWindows`), callable from any process. The write side is `SLSBridgedMoveWindowsToManagedSpaceOperation`, the bridged operation that works with SIP enabled since macOS 26.4. It is private and may vanish in any update; spacekeeper resolves it at runtime and reports clearly when it is unavailable. Verified working on macOS 26.5.

Spaces are identified by UUID, with a (display, position) fallback when a UUID is gone. Window IDs do not survive reboots, so windows are re-matched by app, then title, then frame proximity. Titles come from `kCGWindowName` (needs Screen Recording, covers all spaces) with an Accessibility fallback that covers the active space only; without any titles, matching uses app and frame. See [Screen Recording](#screen-recording) for the permission's quirks.

If a display has fewer desktops than the saved layout (e.g. after a display was disconnected and reconnected), restore recreates the missing ones before moving windows. This is the one operation that drives Mission Control — it animates in and out, unavoidably, because there is no non-flashy, no-SIP way to create a Dock-managed space. It runs only when desktops are actually missing; disable with `-create=false`. Recreated desktops get new UUIDs, so windows resolve to them by display position.

Fullscreen windows are recorded at save time and, with `-fullscreen`, restored by toggling each window's `AXFullScreen` (which recreates its dedicated space). Caveats: it works only for apps that expose a settable `AXFullScreen` (most do); macOS places the recreated fullscreen space by creation order, not at a precise slot; and the window is fullscreened on whatever display it is currently on. Run after windows have reopened. Without `-fullscreen`, such windows are matched but left alone and reported.

Limitations, by design: all-spaces (sticky) windows and Split View pairs are not reconstructed (a Split View window becomes a solo fullscreen at best), and recreating a desktop on a display that is entirely gone is not possible (those windows are reported instead).

### Screen Recording

Cross-space window titles come from `kCGWindowName`, which needs Screen Recording permission. Without it, titles are limited to an Accessibility fallback that covers only the active space, and matching falls back to app plus frame — weaker, and more likely to put a window back on the wrong space when an app has several similar windows.

`save` prints a one-line note when the Screen Recording check returns false. Whether that matters depends on how `save` runs, because macOS attributes the check to the **responsible process**, not necessarily to the binary:

- **Under the launchd agent** (`watch`), spacekeeper is its own responsible process and TCC subject, so grant Screen Recording to spacekeeper itself. This is the path that matters for restore quality, and it works.
- **Run by hand from a terminal**, the responsible process is the terminal or shell, not spacekeeper — so the note can appear even when spacekeeper is granted. Either grant Screen Recording to the terminal app, or ignore the note and rely on the agent.

Grant under System Settings → Privacy & Security → Screen & System Audio Recording. As with Accessibility, changing the signing certificate orphans the grant — remove the stale row and re-grant.

## Research

`macos-spaces-research.md` is a survey of the macOS Spaces control landscape as of mid-2026: public and private APIs, version-by-version breakage, switching techniques, window-to-space persistence, and existing tools (yabai, AeroSpace, InstantSpaceSwitcher, Hammerspoon, and others), with sources.
