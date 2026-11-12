// spacekeeper keeps each window on its space and at its frame, and puts it
// back after a reboot or a display reconfiguration has scattered it.
//
// Read side is SIP-safe SkyLight introspection. Write side is the
// SIP-enabled bridged move operation (Tahoe 26.4+) and Accessibility for
// frames. Space identity is UUID + (display UUID, index) fallback; windows
// are re-identified across sessions by app + title + frame.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/cehbz/spacekit/internal/event"
	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/skylight"
	"github.com/cehbz/spacekit/internal/sysevents"
)

// The AppKit run loop used by watch must own the main OS thread.
func init() { runtime.LockOSThread() }

func usage() {
	fmt.Fprint(os.Stderr, `usage: spacekeeper <command> [flags]

commands:
  watch     run resident: keep the arrangement and put dirty windows back
            (the launchd agent)
  inspect [W]
            list every window with its space, the window server's frame and
            the frame its app reports via Accessibility (needs Accessibility);
            with W, a window id or a word of an app name or title, one
            window's record, bindings, and its decisions interleaved with the
            raw window-server events behind them
  frames    sample drawn rects against window bounds; run while switching
            spaces
  log       list recent changes to the arrangement and display disturbances
  undo N    revert change N from the log; the agent puts the windows back
            at its next decide
  owe       mark every window dirty; the agent puts the windows back at its
            next decide
  place [-space id] W X Y WIDTH HEIGHT
            move or resize window W (needs Accessibility; for on-machine tests)

flags (after the command):
  -interval D  watch: validation tick (default 1m)
  -for D    frames: how long to sample (default 20s)
  -every D  frames: sampling interval (default 200ms)
  -last N   log: how many changes to show (default 20)
  -v        log: one line per window under each change
  -events   log: the raw event stream instead of changes
  -since T  log -events: RFC3339 lower bound
  -record   inspect W: W is a record id rather than a window-server id
  -last N   inspect W: how many decisions to show (default 20)
  -space id place: also move the window to this space id
`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	interval := fs.Duration("interval", time.Minute, "watch: validation tick")
	last := fs.Int("last", 20, "log: changes to show")
	verbose := fs.Bool("v", false, "log: per-window lines")
	events := fs.Bool("events", false, "log: the raw event stream instead of changes")
	since := fs.String("since", "", "log -events: RFC3339 lower bound")
	byRecord := fs.Bool("record", false, "inspect: the argument is a record id")
	spaceID := fs.Uint64("space", 0, "place: also move the window to this space id")
	framesFor := fs.Duration("for", 20*time.Second, "frames: how long to sample")
	framesEvery := fs.Duration("every", 200*time.Millisecond, "frames: sampling interval")
	fs.Parse(os.Args[2:])

	var err error
	switch cmd {
	case "inspect":
		if fs.NArg() == 0 {
			err = inspectCmd()
		} else {
			err = inspectWindow(fs.Arg(0), *byRecord, *last)
		}
	case "frames":
		err = framesCmd(*framesFor, *framesEvery)
	case "log":
		if *events {
			err = eventsCmd(*last, *since)
		} else {
			err = logCmd(*last, *verbose)
		}
	case "undo":
		if fs.NArg() != 1 {
			usage()
		}
		err = undoCmd(fs.Arg(0))
	case "owe":
		err = oweCmd()
	case "place":
		// placeCmd works through sysevents.OnMain, which needs Run pumping
		// the main thread's run loop.
		// Start's error covers notification registrations, which place does not use.
		sysevents.Start(func(event.Event) {})
		errc := make(chan error, 1)
		go func() {
			errc <- placeCmd(fs.Args(), *spaceID)
			sysevents.Stop()
		}()
		sysevents.Run()
		err = <-errc
	case "watch":
		err = watchCmd(watchOptions{interval: *interval})
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spacekeeper: "+err.Error())
		os.Exit(1)
	}
}

func dataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".spacekeeper"
	}
	return filepath.Join(home, ".config", "spacekeeper")
}

func shortUUID(u string) string {
	if len(u) > 8 {
		return u[:8]
	}
	return u
}

// spaceKey returns a durable identity for a space. The first desktop
// historically has an empty UUID, so synthesize one from position.
func spaceKey(uuid, displayUUID string, index int) string {
	if uuid != "" {
		return uuid
	}
	return fmt.Sprintf("display:%s/%d", displayUUID, index)
}

// snapshot is the shared read side: current user spaces per display, and
// every normal window with its single user-space assignment.
type snapshot struct {
	spaces    []layout.SavedSpace
	displays  []layout.CurrentDisplay
	windows   []layout.LiveWindow
	all       []layout.ListedWindow // the whole window list, unfiltered
	winSpace  map[uint32]uint64     // window ID -> current space ID
	idToKey   map[uint64]string     // space ID -> spaceKey
	current   map[uint64]bool       // each display's current space ID
	currentOf map[string]uint64     // display UUID -> the space it is showing
	// fsSpace maps a fullscreen/tiled (type 4) space ID to its display UUID;
	// fsWindow maps a window living in one to that display UUID.
	fsSpace  map[uint64]string
	fsWindow map[uint32]string
	// order maps a display UUID to all its spaces, fullscreen ones included,
	// in Mission Control order: the order Ctrl+Arrow steps through.
	order map[string][]uint64
}

// windowManagerBundle is the system process whose windows (the Mission
// Control highlight overlay, tiling handles) are never an app's.
const windowManagerBundle = "com.apple.WindowManager"

// placeableWindow is the window-list test for a window the agent places: an
// ordinary visible window of usable size, attached to no parent (a sheet,
// save panel or bubble goes where its parent goes, and a move of it moves
// the parent) and not the window manager's own. Membership of one user space
// is checked separately, since it needs the space list.
func placeableWindow(w skylight.WindowInfo, parent uint32, bundle string) bool {
	return w.Layer == 0 && w.Alpha != 0 && w.Bounds.Width >= 50 && w.Bounds.Height >= 50 &&
		parent == 0 && bundle != windowManagerBundle
}

// parentOf is the window's parent id; a failed query reads as no parent,
// logged once, so the agent still places windows when the symbol is gone.
func parentOf(wid uint32) uint32 {
	parent, err := skylight.WindowParent(wid)
	if err != nil && !parentWarned {
		parentWarned = true
		log.Printf("window parent unavailable, treating windows as unattached: %v", err)
	}
	return parent
}

var parentWarned bool

// setSpaces rebuilds the space-list fields from the window server's display
// list; the window fields are left alone.
func (s *snapshot) setSpaces(displays []skylight.Display) {
	s.spaces, s.displays = nil, nil
	s.idToKey = make(map[uint64]string)
	s.current = make(map[uint64]bool)
	s.currentOf = make(map[string]uint64)
	s.fsSpace = make(map[uint64]string)
	s.order = make(map[string][]uint64)
	for _, d := range displays {
		cur := layout.CurrentDisplay{UUID: d.UUID}
		s.current[d.CurrentSpace.ID()] = true
		s.currentOf[d.UUID] = d.CurrentSpace.ID()
		idx := 0
		for _, sp := range d.Spaces {
			s.order[d.UUID] = append(s.order[d.UUID], sp.ID())
			if sp.UserSpace() {
				key := spaceKey(sp.UUID, d.UUID, idx)
				s.spaces = append(s.spaces, layout.SavedSpace{UUID: key, DisplayUUID: d.UUID, Index: idx})
				cur.Spaces = append(cur.Spaces, layout.CurrentSpace{ID: sp.ID(), UUID: sp.UUID})
				s.idToKey[sp.ID()] = key
				idx++
			} else if sp.Type == 4 {
				s.fsSpace[sp.ID()] = d.UUID
			}
		}
		s.displays = append(s.displays, cur)
	}
}

// readAll is the full read: every space, and every window with the space it
// is on and its frame.
func readAll() (*snapshot, error) {
	displays, err := skylight.ManagedDisplaySpaces()
	if err != nil {
		return nil, err
	}
	s := &snapshot{
		winSpace:  make(map[uint32]uint64),
		idToKey:   make(map[uint64]string),
		current:   make(map[uint64]bool),
		currentOf: make(map[string]uint64),
		fsSpace:   make(map[uint64]string),
		fsWindow:  make(map[uint32]string),
	}
	s.setSpaces(displays)

	wins, err := skylight.WindowList()
	if err != nil {
		return nil, err
	}
	// Titles via Accessibility (no Screen Recording needed), one lookup per
	// app, filled in below when the CGWindow name is empty.
	titlesByPID := map[int]map[uint32]string{}
	axTitle := func(pid int, wid uint32) string {
		t, ok := titlesByPID[pid]
		if !ok {
			t = skylight.WindowTitlesForPID(pid)
			titlesByPID[pid] = t
		}
		return t[wid]
	}
	for _, w := range wins {
		s.all = append(s.all, layout.ListedWindow{ID: w.Number, OwnerName: w.OwnerName, Title: w.Name})
		bundle := skylight.BundleIDForPID(w.OwnerPID)
		if !placeableWindow(w, parentOf(w.Number), bundle) {
			continue
		}
		ids, err := skylight.SpacesForWindow(w.Number)
		if err != nil {
			continue
		}
		var userSpaces []uint64
		var fsDisplay string
		for _, id := range ids {
			if _, ok := s.idToKey[id]; ok {
				userSpaces = append(userSpaces, id)
			} else if disp, ok := s.fsSpace[id]; ok {
				fsDisplay = disp
			}
		}
		// Keep windows with a single home: one user desktop, or one
		// fullscreen/tiled space. Sticky windows (several user spaces) are
		// skipped.
		switch {
		case len(userSpaces) == 1:
			s.winSpace[w.Number] = userSpaces[0]
		case len(userSpaces) == 0 && fsDisplay != "":
			s.fsWindow[w.Number] = fsDisplay
		default:
			continue
		}
		title := w.Name
		if title == "" {
			title = axTitle(w.OwnerPID, w.Number)
		}
		frame := layout.Rect{X: w.Bounds.X, Y: w.Bounds.Y, W: w.Bounds.Width, H: w.Bounds.Height}
		if x, y, bw, bh, err := skylight.WindowFrame(w.Number); err == nil {
			frame = layout.Rect{X: x, Y: y, W: bw, H: bh}
		} else {
			boundsUnavailable(err)
		}
		s.windows = append(s.windows, layout.LiveWindow{
			ID:        w.Number,
			OwnerPID:  w.OwnerPID,
			BundleID:  bundle,
			OwnerName: w.OwnerName,
			Title:     title,
			Frame:     frame,
		})
	}
	return s, nil
}

// inspectCmd is a diagnostic: for each window, the space it is on, its
// window server frame, and the frame its app reports through Accessibility.
// "MISMATCH" marks a window whose app disagrees with the window server
// about where it is, the state a window can be left in after a display drop.
func inspectCmd() error {
	s, err := readAll()
	if err != nil {
		return err
	}
	for _, w := range s.windows {
		fmt.Println(inspectLine(s, w))
	}
	return nil
}

// inspectLine is one window of the listing: id, app, title, space, the
// window server's frame and the app's AX frame, with MISMATCH when they differ.
func inspectLine(s *snapshot, w layout.LiveWindow) string {
	space := s.idToKey[s.winSpace[w.ID]]
	if d, ok := s.fsWindow[w.ID]; ok {
		space = "fullscreen@" + shortUUID(d)
	}
	f := w.Frame
	line := fmt.Sprintf("%-6d %-16.16s %-36.36s %-8.8s ws=%.0f,%.0f %.0fx%.0f", w.ID, w.OwnerName, w.Title, space, f.X, f.Y, f.W, f.H)
	if x, y, aw, ah, ok := skylight.WindowAXFrame(w.OwnerPID, w.ID); ok {
		line += fmt.Sprintf(" ax=%.0f,%.0f %.0fx%.0f", x, y, aw, ah)
		if math.Abs(x-f.X) > 2 || math.Abs(y-f.Y) > 2 || math.Abs(aw-f.W) > 2 || math.Abs(ah-f.H) > 2 {
			line += " MISMATCH"
		}
	} else {
		line += " ax=-"
	}
	return line
}

// window returns the window as the snapshot holds it, and whether it does.
func (s *snapshot) window(wid uint32) (layout.LiveWindow, bool) {
	for _, w := range s.windows {
		if w.ID == wid {
			return w, true
		}
	}
	return layout.LiveWindow{}, false
}

// lists reports whether the snapshot's whole window list holds the window.
func (s *snapshot) lists(wid uint32) bool {
	for _, a := range s.all {
		if a.ID == wid {
			return true
		}
	}
	return false
}

// clone is a deep copy: the screen publishes Current by value.
func (s *snapshot) clone() *snapshot {
	c := &snapshot{
		spaces:    append([]layout.SavedSpace(nil), s.spaces...),
		displays:  make([]layout.CurrentDisplay, len(s.displays)),
		windows:   append([]layout.LiveWindow(nil), s.windows...),
		all:       append([]layout.ListedWindow(nil), s.all...),
		winSpace:  make(map[uint32]uint64, len(s.winSpace)),
		idToKey:   make(map[uint64]string, len(s.idToKey)),
		current:   make(map[uint64]bool, len(s.current)),
		currentOf: make(map[string]uint64, len(s.currentOf)),
		fsSpace:   make(map[uint64]string, len(s.fsSpace)),
		fsWindow:  make(map[uint32]string, len(s.fsWindow)),
		order:     make(map[string][]uint64, len(s.order)),
	}
	for i, d := range s.displays {
		c.displays[i] = layout.CurrentDisplay{UUID: d.UUID, Spaces: append([]layout.CurrentSpace(nil), d.Spaces...)}
	}
	for k, v := range s.winSpace {
		c.winSpace[k] = v
	}
	for k, v := range s.idToKey {
		c.idToKey[k] = v
	}
	for k, v := range s.current {
		c.current[k] = v
	}
	for k, v := range s.currentOf {
		c.currentOf[k] = v
	}
	for k, v := range s.fsSpace {
		c.fsSpace[k] = v
	}
	for k, v := range s.fsWindow {
		c.fsWindow[k] = v
	}
	for k, v := range s.order {
		c.order[k] = append([]uint64(nil), v...)
	}
	return c
}

// errNotPlaceable is a window the server lists but the agent does not place.
var errNotPlaceable = errors.New("window not placeable")

// readWindow lists one window with its title and spaces. Main thread only.
// A window that is not placeable reads as errNotPlaceable; one the list
// omits reads as readUnlisted does.
func readWindow(wid uint32) (layout.LiveWindow, []uint64, error) {
	w, err := skylight.WindowInfoFor(wid)
	if errors.Is(err, skylight.ErrNotListed) {
		return readUnlisted(wid)
	}
	if err != nil {
		return layout.LiveWindow{}, nil, err
	}
	bundle := skylight.BundleIDForPID(w.OwnerPID)
	if !placeableWindow(w, parentOf(wid), bundle) {
		return layout.LiveWindow{}, nil, errNotPlaceable
	}
	ids, err := skylight.SpacesForWindow(wid)
	if err != nil {
		return layout.LiveWindow{}, nil, err
	}
	title := w.Name
	if title == "" {
		title = skylight.WindowTitlesForPID(w.OwnerPID)[wid]
	}
	frame := layout.Rect{X: w.Bounds.X, Y: w.Bounds.Y, W: w.Bounds.Width, H: w.Bounds.Height}
	if x, y, bw, bh, err := skylight.WindowFrame(wid); err == nil {
		frame = layout.Rect{X: x, Y: y, W: bw, H: bh}
	} else {
		boundsUnavailable(err)
	}
	return layout.LiveWindow{ID: wid, OwnerPID: w.OwnerPID, BundleID: bundle,
		OwnerName: w.OwnerName, Title: title, Frame: frame}, ids, nil
}

// readUnlisted reads what the window server still answers for a window its
// list omits, as it does for up to 200 ms after an attach: ErrNotListed with
// the window's spaces and, when the bounds read succeeds, its ID and frame.
func readUnlisted(wid uint32) (layout.LiveWindow, []uint64, error) {
	ids, _ := skylight.SpacesForWindow(wid) // none: a window on no space
	var w layout.LiveWindow
	if x, y, bw, bh, err := skylight.WindowFrame(wid); err == nil {
		w = layout.LiveWindow{ID: wid, Frame: layout.Rect{X: x, Y: y, W: bw, H: bh}}
	}
	return w, ids, skylight.ErrNotListed
}

var boundsWarned bool

// boundsUnavailable logs the first failure to read window bounds; the drawn
// rect from the window list stands in, which a space slide can displace.
func boundsUnavailable(err error) {
	if !boundsWarned {
		boundsWarned = true
		log.Printf("window bounds unavailable, reading drawn rects: %v", err)
	}
}
