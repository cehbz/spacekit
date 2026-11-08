package main

import (
	"fmt"
	"strconv"

	"github.com/cehbz/spacekit/internal/skylight"
	"github.com/cehbz/spacekit/internal/sysevents"
)

// placeCmd moves or resizes one window by id, for on-machine tests:
// spacekeeper place [-space id] <wid> <x> <y> <w> <h>. Accessibility
// frame writes need the Accessibility grant of the responsible process, so
// run it as a launchd one-shot like inspect.
func placeCmd(args []string, space uint64) error {
	if len(args) != 5 {
		return fmt.Errorf("usage: place [-space id] <wid> <x> <y> <w> <h>")
	}
	n := make([]float64, 5)
	for i, a := range args {
		v, err := strconv.ParseFloat(a, 64)
		if err != nil {
			return fmt.Errorf("place: %q: %w", a, err)
		}
		n[i] = v
	}
	wid := uint32(n[0])
	var err error
	sysevents.OnMain(func() {
		var w skylight.WindowInfo
		if w, err = skylight.WindowInfoFor(wid); err != nil {
			return
		}
		if space != 0 {
			if err = skylight.MoveWindowsToSpace([]uint32{wid}, space); err != nil {
				return
			}
		}
		err = skylight.SetWindowFrame(w.OwnerPID, wid, n[1], n[2], n[3], n[4])
	})
	return err
}
