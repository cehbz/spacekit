package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/cehbz/spacekit/internal/skylight"
)

func activeSpaceID() uint64 { return skylight.ActiveSpaceID() }

func usage() {
	fmt.Fprint(os.Stderr, `usage: spaceswitch <command> [flags]

commands:
  left | right   switch to the adjacent space (native slide animation)
  status         print the active space ID
  daemon         run the tilt-wheel event tap (tilt left/right -> switch space)

flags (after the command):
  -verify        left/right: poll the active space and report whether it changed
  -invert        daemon: reverse the tilt-to-direction mapping
`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	verify := fs.Bool("verify", false, "confirm the active space changed")
	invert := fs.Bool("invert", false, "reverse tilt direction mapping")
	fs.Parse(os.Args[2:])

	switch cmd {
	case "status":
		id := activeSpaceID()
		if id == 0 {
			fatal("could not resolve active space (SkyLight symbols unavailable)")
		}
		fmt.Println(id)
	case "left", "right":
		switchSpace(cmd == "right", *verify)
	case "daemon":
		// Prompt for Accessibility if missing, but don't gate on the answer:
		// AXIsProcessTrusted misreports in some spawn contexts, and tap
		// creation below is the ground truth (it fails without the grant).
		axTrusted(true)
		fmt.Fprintln(os.Stderr, "spaceswitch: daemon starting (tilt -> space switch)")
		if err := runDaemon(*invert); err != nil {
			fatal(err.Error() + "; grant Accessibility to this binary in System Settings > Privacy & Security > Accessibility")
		}
	default:
		usage()
	}
}

func switchSpace(right, verify bool) {
	if !axTrusted(false) {
		fmt.Fprintln(os.Stderr, "note: AXIsProcessTrusted reports false; the post may still land if the responsible process holds the Accessibility grant")
	}
	before := activeSpaceID()
	if err := postCtrlArrow(right); err != nil {
		fatal(err.Error())
	}
	// A process that exits right after CGEventPost can lose the event
	// (observed under launchd: no switch without -verify, a switch every time
	// with it), so wait for the space to change before returning. The slide
	// animation takes a few hundred ms; poll rather than fixed-sleep.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cur := activeSpaceID(); cur != before {
			if verify {
				fmt.Printf("switched: space %d -> %d\n", before, cur)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !verify {
		return
	}
	fatal(fmt.Sprintf("active space did not change (still %d) — event posted but ignored, or already at the end of the space list", before))
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "spaceswitch: "+msg)
	os.Exit(1)
}
