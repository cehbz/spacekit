// spacekeeper saves and restores window-to-space assignments.
//
// Read side is SIP-safe SkyLight introspection. Write side is the
// SIP-enabled bridged move operation (Tahoe 26.4+). Space identity is
// persisted as UUID + (display UUID, index) fallback; windows are
// re-identified across sessions by app + title + frame.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/skylight"
)

func usage() {
	fmt.Fprint(os.Stderr, `usage: spacekeeper <command> [flags]

commands:
  save      snapshot current window-to-space assignments to history
  restore   move windows back to a snapshot's spaces (default: the high-water snapshot)
  list      list saved snapshots, newest first
  show      print a snapshot's raw layout

flags (after the command):
  -f path   use an explicit file instead of the snapshot history
  -keep N   save: snapshots to retain, plus the high-water (default 200)
  -from id  restore/show: snapshot to use (timestamp substring); default is high-water
  -latest   restore/show: use the newest snapshot instead of the high-water
  -n        restore: dry run, print the plan without changing anything
  -frames   restore: also restore each window's position and size (needs Accessibility)
  -create   restore: recreate missing desktops via Mission Control, default on (-create=false to skip)
  -fullscreen  restore: re-fullscreen windows that were fullscreen when saved
`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	file := fs.String("f", "", "explicit layout file (overrides snapshot history)")
	from := fs.String("from", "", "snapshot id/substring to use")
	latest := fs.Bool("latest", false, "use the newest snapshot instead of the high-water")
	keep := fs.Int("keep", 200, "snapshots to retain")
	dryRun := fs.Bool("n", false, "dry run")
	frames := fs.Bool("frames", false, "also restore window position/size, not just space")
	create := fs.Bool("create", true, "recreate missing spaces via Mission Control (flashy)")
	fullscreen := fs.Bool("fullscreen", false, "re-fullscreen windows that were fullscreen at save time")
	fs.Parse(os.Args[2:])

	var err error
	switch cmd {
	case "save":
		err = saveCmd(*file, *keep)
	case "restore":
		err = restoreCmd(*file, *from, *latest, *dryRun, *frames, *create, *fullscreen)
	case "list":
		err = listCmd()
	case "show":
		err = showCmd(*file, *from, *latest)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spacekeeper: "+err.Error())
		os.Exit(1)
	}
}

// --- snapshot history ---

func dataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".spacekeeper"
	}
	return filepath.Join(home, ".config", "spacekeeper")
}

func snapshotsDir() string { return filepath.Join(dataDir(), "snapshots") }

type snapRef struct {
	path string
	l    layout.Layout
}

func loadLayout(path string) (layout.Layout, error) {
	var l layout.Layout
	data, err := os.ReadFile(path)
	if err != nil {
		return l, err
	}
	err = json.Unmarshal(data, &l)
	return l, err
}

func writeLayout(path string, l layout.Layout) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// listSnapshots returns saved snapshots, newest first.
func listSnapshots() ([]snapRef, error) {
	entries, err := os.ReadDir(snapshotsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var refs []snapRef
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "layout-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		l, err := loadLayout(filepath.Join(snapshotsDir(), e.Name()))
		if err != nil {
			continue
		}
		refs = append(refs, snapRef{filepath.Join(snapshotsDir(), e.Name()), l})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].l.SavedAt.After(refs[j].l.SavedAt) })
	return refs, nil
}

func newestSnap(refs []snapRef) *snapRef {
	if len(refs) == 0 {
		return nil
	}
	return &refs[0]
}

// highWaterSnap is the richest retained snapshot (see layout.Richer).
func highWaterSnap(refs []snapRef) *snapRef {
	if len(refs) == 0 {
		return nil
	}
	best := &refs[0]
	for i := 1; i < len(refs); i++ {
		if layout.Richer(refs[i].l, best.l) {
			best = &refs[i]
		}
	}
	return best
}

// pruneSnapshots keeps the newest `keep` snapshots plus the high-water one,
// deleting the rest. Returns the number removed.
func pruneSnapshots(keep int) int {
	refs, err := listSnapshots()
	if err != nil || len(refs) <= keep {
		return 0
	}
	hw := highWaterSnap(refs)
	deleted := 0
	for i, r := range refs {
		if i < keep || (hw != nil && r.path == hw.path) {
			continue
		}
		if os.Remove(r.path) == nil {
			deleted++
		}
	}
	return deleted
}

func shortUUID(u string) string {
	if len(u) > 8 {
		return u[:8]
	}
	return u
}

func displaySummary(st layout.Stats) string {
	if len(st.Displays) == 0 {
		return "no displays"
	}
	parts := make([]string, 0, len(st.Displays))
	for _, d := range st.Displays {
		parts = append(parts, fmt.Sprintf("%s=%d", shortUUID(d.DisplayUUID), d.Spaces))
	}
	return "displays: " + strings.Join(parts, ", ")
}

// resolveSnapshot selects which layout to act on: an explicit file, a -from
// match, the newest (-latest), or the high-water default.
func resolveSnapshot(explicit, from string, latest bool) (layout.Layout, string, error) {
	if explicit != "" {
		l, err := loadLayout(explicit)
		return l, explicit, err
	}
	refs, err := listSnapshots()
	if err != nil {
		return layout.Layout{}, "", err
	}
	if len(refs) == 0 {
		return layout.Layout{}, "", errors.New("no snapshots yet — run `spacekeeper save`")
	}
	if from != "" {
		for _, r := range refs {
			if strings.Contains(filepath.Base(r.path), from) {
				return r.l, r.path, nil
			}
		}
		return layout.Layout{}, "", fmt.Errorf("no snapshot matching %q", from)
	}
	if latest {
		return refs[0].l, refs[0].path, nil
	}
	hw := highWaterSnap(refs)
	return hw.l, hw.path, nil
}

func listCmd() error {
	refs, err := listSnapshots()
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		fmt.Println("no snapshots yet — run `spacekeeper save`")
		return nil
	}
	hw := highWaterSnap(refs)
	for i, r := range refs {
		st := r.l.Stats()
		tags := ""
		if i == 0 {
			tags += " [latest]"
		}
		if hw != nil && r.path == hw.path {
			tags += " [high-water]"
		}
		fmt.Printf("%s  %2d windows  %s%s\n",
			r.l.SavedAt.Format("2006-01-02 15:04:05"), st.Windows, displaySummary(st), tags)
	}
	return nil
}

func showCmd(explicit, from string, latest bool) error {
	l, path, err := resolveSnapshot(explicit, from, latest)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "# "+path)
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(data, '\n'))
	return err
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
	spaces   []layout.SavedSpace
	displays []layout.CurrentDisplay
	windows  []layout.LiveWindow
	winSpace map[uint32]uint64 // window ID -> current space ID
	idToKey  map[uint64]string // space ID -> spaceKey
	// fsSpace maps a fullscreen/tiled (type 4) space ID to its display UUID;
	// fsWindow maps a window living in one to that display UUID.
	fsSpace  map[uint64]string
	fsWindow map[uint32]string
}

func gather() (*snapshot, error) {
	displays, err := skylight.ManagedDisplaySpaces()
	if err != nil {
		return nil, err
	}
	s := &snapshot{
		winSpace: make(map[uint32]uint64),
		idToKey:  make(map[uint64]string),
		fsSpace:  make(map[uint64]string),
		fsWindow: make(map[uint32]string),
	}
	for _, d := range displays {
		cur := layout.CurrentDisplay{UUID: d.UUID}
		idx := 0
		for _, sp := range d.Spaces {
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
		if w.Layer != 0 || w.Alpha == 0 || w.Bounds.Width < 50 || w.Bounds.Height < 50 {
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
		s.windows = append(s.windows, layout.LiveWindow{
			ID:        w.Number,
			OwnerPID:  w.OwnerPID,
			BundleID:  skylight.BundleIDForPID(w.OwnerPID),
			OwnerName: w.OwnerName,
			Title:     title,
			Frame:     layout.Rect{X: w.Bounds.X, Y: w.Bounds.Y, W: w.Bounds.Width, H: w.Bounds.Height},
		})
	}
	return s, nil
}

// buildLayout turns a gathered snapshot into a saveable layout.
func buildLayout(s *snapshot) layout.Layout {
	l := layout.Layout{SavedAt: time.Now(), Spaces: s.spaces}
	for _, w := range s.windows {
		sw := layout.SavedWindow{
			BundleID:  w.BundleID,
			OwnerName: w.OwnerName,
			Title:     w.Title,
			Frame:     w.Frame,
		}
		if disp, ok := s.fsWindow[w.ID]; ok {
			sw.Fullscreen = true
			sw.DisplayUUID = disp
		} else {
			sw.SpaceUUID = s.idToKey[s.winSpace[w.ID]]
		}
		l.Windows = append(l.Windows, sw)
	}
	return l
}

func saveCmd(explicit string, keep int) error {
	if !skylight.ScreenRecordingGranted() {
		skylight.RequestScreenRecording() // register the binary; silent, not narrated
		fmt.Fprintln(os.Stderr, `note: Screen Recording is unavailable to this process; `+
			`window titles are limited to the active space, weakening cross-space matching. `+
			`See the README "Screen Recording" section.`)
	}
	s, err := gather()
	if err != nil {
		return err
	}
	l := buildLayout(s)

	if explicit != "" {
		if err := writeLayout(explicit, l); err != nil {
			return err
		}
		fmt.Printf("saved %d windows to %s\n", len(l.Windows), explicit)
		return nil
	}

	refs, _ := listSnapshots()
	if n := newestSnap(refs); n != nil && n.l.Signature() == l.Signature() {
		fmt.Println("unchanged since the last snapshot; nothing saved")
		return nil
	}
	path := filepath.Join(snapshotsDir(), "layout-"+l.SavedAt.Format("20060102-150405")+".json")
	if err := writeLayout(path, l); err != nil {
		return err
	}
	st := l.Stats()
	fmt.Printf("snapshot %s: %d windows, %s\n", filepath.Base(path), st.Windows, displaySummary(st))
	if pruned := pruneSnapshots(keep); pruned > 0 {
		fmt.Printf("pruned %d old snapshot(s) (kept newest %d + high-water)\n", pruned, keep)
	}
	return nil
}

func restoreCmd(explicit, from string, latest, dryRun, frames, create, fullscreen bool) error {
	l, path, err := resolveSnapshot(explicit, from, latest)
	if err != nil {
		return err
	}
	which := "high-water"
	switch {
	case explicit != "":
		which = "file"
	case from != "":
		which = "selected"
	case latest:
		which = "latest"
	}
	st := l.Stats()
	fmt.Printf("restoring %s snapshot %s (saved %s): %d windows, %s\n",
		which, filepath.Base(path), l.SavedAt.Format("2006-01-02 15:04"), st.Windows, displaySummary(st))
	return restoreLayout(l, dryRun, frames, create, fullscreen)
}

func restoreLayout(l layout.Layout, dryRun, frames, create, fullscreen bool) error {

	s, err := gather()
	if err != nil {
		return err
	}

	// Recreate missing spaces first, so windows have somewhere to land.
	if create {
		deficits := layout.SpaceDeficits(l.Spaces, s.displays)
		want := 0
		for _, d := range deficits {
			want += d
		}
		if want > 0 {
			if dryRun {
				fmt.Printf("would create %d missing desktop(s) via Mission Control\n", want)
			} else {
				fmt.Printf("creating %d missing desktop(s) via Mission Control...\n", want)
				added, err := skylight.AddSpaces(deficits)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: space creation incomplete (%d/%d): %v\n", added, want, err)
				}
				// Re-read state so the new spaces are resolvable by index.
				if s, err = gather(); err != nil {
					return err
				}
			}
		}
	}

	resolved := layout.ResolveSpaces(l.Spaces, s.displays)
	matched := layout.Match(l.Windows, s.windows)

	pidByWindow := make(map[uint32]int, len(s.windows))
	for _, w := range s.windows {
		pidByWindow[w.ID] = w.OwnerPID
	}

	moves := make(map[uint64][]uint32) // target space ID -> window IDs
	skipped, unresolved := 0, 0
	for si, wid := range matched {
		if l.Windows[si].Fullscreen {
			continue // handled by the fullscreen pass, not a space move
		}
		target, ok := resolved[l.Windows[si].SpaceUUID]
		if !ok {
			unresolved++
			continue
		}
		if s.winSpace[wid] == target {
			skipped++
			continue
		}
		if dryRun {
			fmt.Printf("would move %q %q (window %d) -> space %d\n",
				l.Windows[si].OwnerName, l.Windows[si].Title, wid, target)
			continue
		}
		moves[target] = append(moves[target], wid)
	}

	moveCount := 0
	for target, wids := range moves {
		if err := skylight.MoveWindowsToSpace(wids, target); err != nil {
			return fmt.Errorf("moving %d windows to space %d: %w", len(wids), target, err)
		}
		moveCount += len(wids)
	}

	verified := 0
	if moveCount > 0 {
		// The bridged operation is asynchronous; give it a beat, then check.
		time.Sleep(500 * time.Millisecond)
		for target, wids := range moves {
			for _, wid := range wids {
				ids, err := skylight.SpacesForWindow(wid)
				if err == nil && len(ids) == 1 && ids[0] == target {
					verified++
				}
			}
		}
	}

	framed, frameErr := 0, 0
	if frames && !dryRun {
		for si, wid := range matched {
			if l.Windows[si].Fullscreen {
				continue // these get fullscreened, not framed
			}
			f := l.Windows[si].Frame
			if err := skylight.SetWindowFrame(pidByWindow[wid], wid, f.X, f.Y, f.W, f.H); err != nil {
				frameErr++
				continue
			}
			framed++
		}
	}

	// Re-fullscreen windows that were fullscreen at save time. Each transition
	// creates a fullscreen space and animates, so this runs last.
	fsDone, fsSkip, fsFail := 0, 0, 0
	wantFS := 0
	for si := range matched {
		if l.Windows[si].Fullscreen {
			wantFS++
		}
	}
	if fullscreen && wantFS > 0 {
		if dryRun {
			fmt.Printf("would restore %d fullscreen window(s)\n", wantFS)
		} else {
			for si, wid := range matched {
				if !l.Windows[si].Fullscreen {
					continue
				}
				switch skylight.SetFullscreen(pidByWindow[wid], wid, true) {
				case skylight.FullscreenChanged:
					fsDone++
				case skylight.FullscreenAlready:
					fsSkip++
				default:
					fsFail++
				}
			}
		}
	}

	fmt.Printf("matched %d/%d saved windows; moved %d (%d verified), %d already in place",
		len(matched), len(l.Windows), moveCount, verified, skipped)
	if unresolved > 0 {
		fmt.Printf(", %d on spaces that no longer exist", unresolved)
	}
	if frames && !dryRun {
		fmt.Printf("; restored %d frames", framed)
		if frameErr > 0 {
			fmt.Printf(" (%d failed — apps that refuse AX resize, or Accessibility not granted)", frameErr)
		}
	}
	if fullscreen && !dryRun && wantFS > 0 {
		fmt.Printf("; fullscreened %d (%d already, %d unsupported/failed)", fsDone, fsSkip, fsFail)
	} else if !fullscreen && wantFS > 0 {
		fmt.Printf("; %d fullscreen window(s) skipped (use -fullscreen)", wantFS)
	}
	fmt.Println()
	if moveCount > 0 && verified < moveCount {
		fmt.Fprintln(os.Stderr, "warning: some moves did not verify — the bridged-move API may be restricted on this macOS build")
	}
	return nil
}
