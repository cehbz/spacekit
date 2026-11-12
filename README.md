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

- Ctrl+Arrow switches the space on the display under the pointer; a shortcut posted from a process acts the same as one typed.
- The Mission Control shortcuts "Move left/right a space" must be enabled (they are by default).
- Mapping the tilt to space switching costs you tilt-driven horizontal scrolling. Shift+wheel still scrolls horizontally.

## spacekeeper

Keeps each window on its space and at its frame, and puts it back after a reboot or a display reconfiguration has scattered it. SIP stays enabled.

```
go build -o spacekeeper ./cmd/spacekeeper

spacekeeper watch             run resident (the launchd agent): keep the arrangement and put windows back
spacekeeper inspect [W]       each window with its space, the window server's frame, and the frame its app reports via Accessibility;
                              with W (a window id or a word of an app name or title) one window's record, bindings and history
spacekeeper log [-last N] [-v]  recent changes to the arrangement and display changes; -v lists each window
spacekeeper undo N            revert change N from the log; the agent puts the windows back within a minute
spacekeeper owe               mark every window on screen dirty; the agent puts each back at its recorded placement within a minute
spacekeeper frames [-for 20s] [-every 200ms]  sample each window's drawn rect against its frame; run while switching spaces
```

Snapshots under `~/.config/spacekeeper/snapshots/`, written by earlier versions, are no longer read and may be deleted.

### The agent

`make agent-install` runs `spacekeeper watch` as a launchd agent (`bz.ceh.spacekeeper`, KeepAlive) that owns the whole lifecycle:

- **Keeps an arrangement.** For each set of displays the agent records where every window belongs, its space and frame or fullscreen on a display: the target. It keeps its own picture of the session, Current, seeded once at startup and then maintained from the window server's events, each followed by a read of that window's frame (`SLSGetWindowBounds`, which a space slide and Mission Control leave alone, unlike the window list's drawn rect). Once a minute it validates Current against a full read: every difference is logged, and one is taken into Current only when two consecutive reads agree on it, so a read taken while the screen is in motion changes nothing. A read taken while Mission Control or Exposé is showing compares nothing. The record lives in `~/.config/spacekeeper/spacekeeper.db` (SQLite); each placement keeps the period during which it was true, so the history is in the same table. History older than 90 days is deleted.
- **Decides by cause.** The agent decides a window half a second after the events about it stop, every window when a display change has settled for 10 seconds, at startup, and when a put ends; it decides on Current, never on a fresh read. A window off its target changed either for a system cause or for you. System causes: a display change (and the 20 seconds after it settles, during which the window server and Chrome keep moving windows), a window coming back under a new window-server id after its app relaunched or after a reboot (bound to its record by title or frame), and the agent's own puts. For those the window is dirty and is put back: moved to its space and resized at once, a window on a hidden space brought to the visible one for the resize and returned, each step confirmed by the window server's own report of it. Anything else is your doing and the target follows the window. Differences within 3 points are ignored. A put that does not take is logged as `put failed`; the target stays and the window is tried again at the next display change. A window marked dirty by `owe` or `undo` is put back at the next tick, whatever else is happening. On Apple Silicon an external display de-registers briefly whenever the screens wake, macOS moves and shrinks windows, and Chrome often fails to bring its own back; that display change is the cause the agent exists for. With the external display really gone, the arrangement for the remaining display set applies, and the full one returns with the display.
- **Restores fullscreen.** A window fullscreen on a display is recorded as fullscreen there. One that comes back as a normal window after a reboot or a relaunch is staged to the space that display shows and made fullscreen through Accessibility (`AXFullScreen`); once the window server reports it on a fullscreen space of that display, the display is stepped back with Ctrl+Arrow to the space it was showing, each step confirmed by the switch. Each step moves the pointer to that display for about a tenth of a second and puts it back. macOS puts the fullscreen space last on its display. Entering or leaving fullscreen by hand moves the target; no put takes a window out of fullscreen. A failed return leaves the display on the fullscreen space and the put is reported in the log; you switch back by hand.
- **Records everything it sees.** Every event and every change to the arrangement is journaled for seven days; `spacekeeper log -events` reads it back.

Log: `~/Library/Logs/spacekeeper/watch.log`. `make agent-uninstall` removes it. The earlier separate save and restore agents are unloaded by `agent-install`.

The read side is SkyLight introspection (`SLSCopyManagedDisplaySpaces`, `SLSCopySpacesForWindows`), callable from any process. The write side is `SLSBridgedMoveWindowsToManagedSpaceOperation`, the bridged operation that works with SIP enabled since macOS 26.4. It is private and may vanish in any update; spacekeeper resolves it at runtime and reports clearly when it is unavailable. Verified working on macOS 26.5.

Spaces are identified by UUID, with a (display, position) fallback when a UUID is gone. Window IDs do not survive reboots, so windows are re-matched by app, then title, then frame proximity. Titles come from `kCGWindowName` (needs Screen Recording, covers all spaces) with an Accessibility fallback that covers the active space only; without any titles, matching uses app and frame. See [Screen Recording](#screen-recording) for the permission's quirks.

Limitations, by design: all-spaces (sticky) windows and Split View pairs are not placed, missing desktops are not recreated, and a restored fullscreen space is not moved back to its old position among its display's spaces.

### Screen Recording

Cross-space window titles come from `kCGWindowName`, which needs Screen Recording permission. Without it, titles are limited to an Accessibility fallback that covers only the active space, and matching falls back to app plus frame — weaker, and more likely to put a window back on the wrong space when an app has several similar windows.

macOS attributes the permission to the **responsible process**, not necessarily to the binary:

- **Under the launchd agent** (`watch`), spacekeeper is its own responsible process and TCC subject, so grant Screen Recording to spacekeeper itself. This is the path that matters for matching quality.
- **Run by hand from a terminal** (`inspect`, `owe`), the responsible process is the terminal or shell, not spacekeeper, so titles on other spaces are missing unless the terminal app is granted Screen Recording.

Grant under System Settings → Privacy & Security → Screen & System Audio Recording. As with Accessibility, changing the signing certificate orphans the grant — remove the stale row and re-grant.

## Research

`macos-spaces-research.md` is a survey of the macOS Spaces control landscape as of mid-2026: public and private APIs, version-by-version breakage, switching techniques, window-to-space persistence, and existing tools (yabai, AeroSpace, InstantSpaceSwitcher, Hammerspoon, and others), with sources.
