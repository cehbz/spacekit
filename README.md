# spacekit

A macOS Spaces (Mission Control) toolkit: switch spaces from a mouse tilt wheel, and keep windows on their spaces across reboots and display changes. SIP stays enabled.

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

Keeps each window on its space and at its frame, and puts it back after a reboot or a display reconfiguration has scattered it. SIP stays enabled.

```
go build -o spacekeeper ./cmd/spacekeeper

spacekeeper watch             run resident (the launchd agent): keep the arrangement and put windows back
spacekeeper inspect           each window with its space, CGWindow bounds, and the frame its app reports via Accessibility
spacekeeper log [-last N] [-v]  recent changes to the arrangement and display disturbances; -v lists each window
spacekeeper undo N            revert change N from the log; the agent puts the windows back at its next look
spacekeeper owe               have the agent put every window on screen back at its recorded placement
```

Snapshots under `~/.config/spacekeeper/snapshots/`, written by earlier versions, are no longer read and may be deleted.

### The agent

`make agent-install` runs `spacekeeper watch` as a launchd agent (`bz.ceh.spacekeeper`, KeepAlive) that owns the whole lifecycle:

- **Keeps an arrangement.** For each set of displays the agent records where every window belongs: its space and frame. It keeps a picture of the session from the window server's own events and judges a window half a second after the events about it stop, every window at each space change and after a display change settles; once a minute it checks the picture against the window server and logs any difference. A window that comes back under a new id after its app relaunched, or after a reboot, is bound to its record by title or frame and put back, frames included: login is this rule and nothing else. A change to a window on a space you can see is adopted as the new intent; a change to a window on a space you cannot see is not yours, so the window is put back. Differences within 3 points are ignored. The record lives in `~/.config/spacekeeper/spacekeeper.db` (SQLite); each placement keeps the period during which it was true, so the history is in the same table. History older than 90 days is deleted.
- **Repairs after a disturbance.** A display change, a locked or sleeping session and a logout are disturbances: nothing seen during one is adopted. While Mission Control or Exposé is showing nothing is judged and the picture keeps its frames, since the window list then carries thumbnail frames; the picture is checked against the window server when the overview closes. On Apple Silicon an external display de-registers briefly whenever the screens wake, macOS moves and shrinks windows, and Chrome often fails to bring its own back. When a display change has been quiet for 10 seconds, every window is owed its recorded placement: it is moved to its space and resized at once; a window on a hidden space is brought to the visible one for the resize and returned, each step confirmed by the window server's own report of it, since Accessibility only reaches windows on a shown space. A window seen back in place is released. One that a completed repair could not put back is released where it is and logged as `released unrepaired`. With the external display really gone, the arrangement for the remaining display set applies, and the full one returns with the display. A window that comes back under a new window-server id, whether its app restarted or rebuilt it in place, is matched to its record by title or frame and put back; a window that matches nothing is treated as new.
- **Records everything it sees.** Every event and every change to the arrangement is journaled for seven days; `spacekeeper log -events` reads it back.

Log: `~/Library/Logs/spacekeeper/watch.log`. `make agent-uninstall` removes it. The earlier separate save and restore agents are unloaded by `agent-install`.

The read side is SkyLight introspection (`SLSCopyManagedDisplaySpaces`, `SLSCopySpacesForWindows`), callable from any process. The write side is `SLSBridgedMoveWindowsToManagedSpaceOperation`, the bridged operation that works with SIP enabled since macOS 26.4. It is private and may vanish in any update; spacekeeper resolves it at runtime and reports clearly when it is unavailable. Verified working on macOS 26.5.

Spaces are identified by UUID, with a (display, position) fallback when a UUID is gone. Window IDs do not survive reboots, so windows are re-matched by app, then title, then frame proximity. Titles come from `kCGWindowName` (needs Screen Recording, covers all spaces) with an Accessibility fallback that covers the active space only; without any titles, matching uses app and frame. See [Screen Recording](#screen-recording) for the permission's quirks.

Limitations, by design: all-spaces (sticky) windows, fullscreen windows and Split View pairs are not placed, and missing desktops are not recreated.

### Screen Recording

Cross-space window titles come from `kCGWindowName`, which needs Screen Recording permission. Without it, titles are limited to an Accessibility fallback that covers only the active space, and matching falls back to app plus frame — weaker, and more likely to put a window back on the wrong space when an app has several similar windows.

macOS attributes the permission to the **responsible process**, not necessarily to the binary:

- **Under the launchd agent** (`watch`), spacekeeper is its own responsible process and TCC subject, so grant Screen Recording to spacekeeper itself. This is the path that matters for matching quality.
- **Run by hand from a terminal** (`inspect`, `owe`), the responsible process is the terminal or shell, not spacekeeper, so titles on other spaces are missing unless the terminal app is granted Screen Recording.

Grant under System Settings → Privacy & Security → Screen & System Audio Recording. As with Accessibility, changing the signing certificate orphans the grant — remove the stale row and re-grant.

## Research

`macos-spaces-research.md` is a survey of the macOS Spaces control landscape as of mid-2026: public and private APIs, version-by-version breakage, switching techniques, window-to-space persistence, and existing tools (yabai, AeroSpace, InstantSpaceSwitcher, Hammerspoon, and others), with sources.
