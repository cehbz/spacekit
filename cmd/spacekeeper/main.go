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
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/skylight"
)

// The AppKit run loop used by watch must own the main OS thread.
func init() { runtime.LockOSThread() }

func usage() {
	fmt.Fprint(os.Stderr, `usage: spacekeeper <command> [flags]

commands:
  save      snapshot current window-to-space assignments to history
  restore   move windows back to a snapshot's spaces
            (default: newest settled snapshot from a previous boot — the
            layout as of the last shutdown)
  list      list saved snapshots, newest first
  show      print a snapshot's raw layout
  watch     run resident: periodic and event-driven saves, restore after a
            transient display drop, and login convergence (the launchd agent)
  inspect   print every recorded window with its space, CGWindow bounds, and
            the frame its app reports via Accessibility (needs Accessibility)

flags (after the command):
  -f path   use an explicit file instead of the snapshot history
  -keep N   save: snapshots to retain, plus the high-water (default 200)
  -from id  restore/show: snapshot to use (timestamp substring)
  -latest   restore/show: use the newest snapshot
  -high-water  restore/show: use the richest snapshot (the old default)
  -settled D   minimum uptime at save for the default pick (default 10m)
  -n        restore: dry run, print the plan without changing anything
  -frames   restore: also restore each window's position and size (needs Accessibility)
  -create   restore: recreate missing desktops via Mission Control, default on (-create=false to skip)
  -fullscreen  restore: re-fullscreen windows that were fullscreen when saved
  -interval D  watch: periodic save interval (default 3m)
  -boot        watch: run login convergence even if this boot already has snapshots
  -boot-cap D  watch: hard cap on login convergence (default 10m)
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
	latest := fs.Bool("latest", false, "use the newest snapshot")
	highWater := fs.Bool("high-water", false, "use the high-water (richest) snapshot, the old default")
	settled := fs.Duration("settled", 10*time.Minute, "minimum uptime at save time for a snapshot to count as settled")
	keep := fs.Int("keep", 200, "snapshots to retain")
	dryRun := fs.Bool("n", false, "dry run")
	frames := fs.Bool("frames", false, "also restore window position/size, not just space")
	create := fs.Bool("create", true, "recreate missing spaces via Mission Control (flashy)")
	fullscreen := fs.Bool("fullscreen", false, "re-fullscreen windows that were fullscreen at save time")
	interval := fs.Duration("interval", 3*time.Minute, "watch: periodic save interval")
	forceBoot := fs.Bool("boot", false, "watch: run login convergence even if this boot already has snapshots")
	bootCap := fs.Duration("boot-cap", 10*time.Minute, "watch: hard cap on login convergence")
	fs.Parse(os.Args[2:])

	var err error
	switch cmd {
	case "save":
		err = saveCmd(*file, *keep, *settled)
	case "restore":
		err = restoreCmd(*file, *from, *latest, *highWater, *settled, *dryRun, *frames, *create, *fullscreen)
	case "list":
		err = listCmd()
	case "show":
		err = showCmd(*file, *from, *latest, *highWater, *settled)
	case "inspect":
		err = inspectCmd()
	case "watch":
		err = watchCmd(watchOptions{
			interval: *interval, settle: *settled, bootCap: *bootCap, forceBoot: *forceBoot,
			keep: *keep, frames: *frames, create: *create, fullscreen: *fullscreen,
		})
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

func layouts(refs []snapRef) []layout.Layout {
	ls := make([]layout.Layout, len(refs))
	for i, r := range refs {
		ls[i] = r.l
	}
	return ls
}

// highWaterSnap is the richest retained snapshot (see layout.Richer).
func highWaterSnap(refs []snapRef) *snapRef {
	if i := layout.HighWaterIndex(layouts(refs)); i >= 0 {
		return &refs[i]
	}
	return nil
}

// pruneSnapshots keeps the newest `keep` snapshots plus two pinned ones: the
// high-water arrangement and the snapshot restore would pick by default (the
// layout as of the last shutdown). Without the second pin, a few days of
// interval saves push every previous-boot snapshot out, and a bare `restore`
// would fall through to the high-water snapshot from weeks earlier.
func pruneSnapshots(keep int, settle time.Duration) int {
	refs, err := listSnapshots()
	if err != nil || len(refs) <= keep {
		return 0
	}
	ls := layouts(refs)
	pinned := map[int]bool{
		layout.HighWaterIndex(ls):                          true,
		layout.DefaultRestoreIndex(ls, bootTime(), settle): true,
	}
	deleted := 0
	for i, r := range refs {
		if i < keep || pinned[i] {
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
// match, the newest (-latest), the richest (-high-water), or the default —
// the newest settled snapshot from a previous boot session, i.e. the layout
// as of the last shutdown (see layout.DefaultRestoreIndex).
func resolveSnapshot(explicit, from string, latest, highWater bool, settle time.Duration) (layout.Layout, string, error) {
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
	if highWater {
		hw := highWaterSnap(refs)
		return hw.l, hw.path, nil
	}
	i := layout.DefaultRestoreIndex(layouts(refs), bootTime(), settle)
	return refs[i].l, refs[i].path, nil
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
	boot := bootTime()
	for i, r := range refs {
		st := r.l.Stats()
		tags := ""
		if i == 0 {
			tags += " [latest]"
		}
		if hw != nil && r.path == hw.path {
			tags += " [high-water]"
		}
		if !boot.IsZero() && !r.l.SavedAt.Before(boot) {
			tags += " [this-boot]"
		} else if !r.l.BootedAt.IsZero() && r.l.SavedAt.Sub(r.l.BootedAt) < 10*time.Minute {
			tags += " [unsettled]"
		}
		fmt.Printf("%s  %2d windows  %s%s\n",
			r.l.SavedAt.Format("2006-01-02 15:04:05"), st.Windows, displaySummary(st), tags)
	}
	return nil
}

func showCmd(explicit, from string, latest, highWater bool, settle time.Duration) error {
	l, path, err := resolveSnapshot(explicit, from, latest, highWater, settle)
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
	current  map[uint64]bool   // each display's current space ID
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
		current:  make(map[uint64]bool),
		fsSpace:  make(map[uint64]string),
		fsWindow: make(map[uint32]string),
	}
	for _, d := range displays {
		cur := layout.CurrentDisplay{UUID: d.UUID}
		s.current[d.CurrentSpace.ID()] = true
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

// inspectCmd is a diagnostic: for each window, the space it is on, its
// CGWindow bounds, and the frame its app reports through Accessibility.
// "MISMATCH" marks a window whose app disagrees with the window server
// about where it is, the state a window can be left in after a display drop.
func inspectCmd() error {
	s, err := gather()
	if err != nil {
		return err
	}
	for _, w := range s.windows {
		space := s.idToKey[s.winSpace[w.ID]]
		if d, ok := s.fsWindow[w.ID]; ok {
			space = "fullscreen@" + shortUUID(d)
		}
		f := w.Frame
		line := fmt.Sprintf("%-6d %-16.16s %-36.36s %-8.8s cg=%.0f,%.0f %.0fx%.0f", w.ID, w.OwnerName, w.Title, space, f.X, f.Y, f.W, f.H)
		if x, y, aw, ah, ok := skylight.WindowAXFrame(w.OwnerPID, w.ID); ok {
			line += fmt.Sprintf(" ax=%.0f,%.0f %.0fx%.0f", x, y, aw, ah)
			if math.Abs(x-f.X) > 2 || math.Abs(y-f.Y) > 2 || math.Abs(aw-f.W) > 2 || math.Abs(ah-f.H) > 2 {
				line += " MISMATCH"
			}
		} else {
			line += " ax=-"
		}
		fmt.Println(line)
	}
	return nil
}

// buildLayout turns a gathered snapshot into a saveable layout.
func buildLayout(s *snapshot) layout.Layout {
	l := layout.Layout{SavedAt: time.Now(), BootedAt: bootTime(), Spaces: s.spaces}
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

// errOverviewOpen is returned by saveSnapshot while Mission Control is
// showing: the window list then holds thumbnail bounds, not a layout.
var errOverviewOpen = errors.New("Mission Control is open")

// saveSnapshot writes the current layout into history unless it is identical
// to the newest snapshot. It returns the written path, or "" when unchanged.
func saveSnapshot(keep int, settle time.Duration) (string, error) {
	s, err := gather()
	if err != nil {
		return "", err
	}
	if layout.OverviewOpen(s.windows) {
		return "", errOverviewOpen
	}
	l := buildLayout(s)
	refs, _ := listSnapshots()
	if n := newestSnap(refs); n != nil && n.l.Signature() == l.Signature() {
		return "", nil
	}
	path := filepath.Join(snapshotsDir(), "layout-"+l.SavedAt.Format("20060102-150405")+".json")
	if err := writeLayout(path, l); err != nil {
		return "", err
	}
	pruneSnapshots(keep, settle)
	return path, nil
}

func saveCmd(explicit string, keep int, settle time.Duration) error {
	if !skylight.ScreenRecordingGranted() {
		skylight.RequestScreenRecording() // register the binary; silent, not narrated
		fmt.Fprintln(os.Stderr, `note: Screen Recording is unavailable to this process; `+
			`window titles are limited to the active space, weakening cross-space matching. `+
			`See the README "Screen Recording" section.`)
	}
	if explicit != "" {
		s, err := gather()
		if err != nil {
			return err
		}
		l := buildLayout(s)
		if err := writeLayout(explicit, l); err != nil {
			return err
		}
		fmt.Printf("saved %d windows to %s\n", len(l.Windows), explicit)
		return nil
	}
	path, err := saveSnapshot(keep, settle)
	if err != nil {
		return err
	}
	if path == "" {
		fmt.Println("unchanged since the last snapshot; nothing saved")
		return nil
	}
	l, err := loadLayout(path)
	if err != nil {
		return err
	}
	st := l.Stats()
	fmt.Printf("snapshot %s: %d windows, %s\n", filepath.Base(path), st.Windows, displaySummary(st))
	return nil
}

func restoreCmd(explicit, from string, latest, highWater bool, settle time.Duration, dryRun, frames, create, fullscreen bool) error {
	l, path, err := resolveSnapshot(explicit, from, latest, highWater, settle)
	if err != nil {
		return err
	}
	which := "previous-boot"
	switch {
	case explicit != "":
		which = "file"
	case from != "":
		which = "selected"
	case latest:
		which = "latest"
	case highWater:
		which = "high-water"
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
	if create {
		if s, err = createMissingSpaces(l, s, dryRun); err != nil {
			return err
		}
	}
	if dryRun {
		return dryRunReport(l, s, frames, fullscreen)
	}
	st, err := applyPass(l, s, make(map[int]bool), frames, fullscreen)
	if err != nil {
		return err
	}
	printSummary(l, st, frames, fullscreen, false)
	return nil
}

// createMissingSpaces recreates desktops the layout needs that no longer
// exist, then re-reads state so the new spaces are resolvable by index. In a
// dry run it only reports what would be created.
func createMissingSpaces(l layout.Layout, s *snapshot, dryRun bool) (*snapshot, error) {
	deficits := layout.SpaceDeficits(l.Spaces, s.displays)
	want := 0
	for _, d := range deficits {
		want += d
	}
	if want == 0 {
		return s, nil
	}
	if dryRun {
		fmt.Printf("would create %d missing desktop(s) via Mission Control\n", want)
		return s, nil
	}
	fmt.Printf("creating %d missing desktop(s) via Mission Control...\n", want)
	added, err := skylight.AddSpaces(deficits)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: space creation incomplete (%d/%d): %v\n", added, want, err)
	}
	return gather()
}

// passStats reports what one reconcile pass did.
type passStats struct {
	matched, moved, verified, inPlace, unresolved int
	framed, frameErr                              int
	fsDone, fsSkip, fsFail, fsWant                int
}

// applyPass matches l.Windows against the gathered state s and acts on the
// matches whose saved index is not yet in handled: moves them to their saved
// space (and optionally frames/fullscreens them), then marks them handled.
// Windows handled in an earlier pass are never touched again, so convergence
// cannot fight the user's own rearranging.
func applyPass(l layout.Layout, s *snapshot, handled map[int]bool, frames, fullscreen bool) (passStats, error) {
	var st passStats
	resolved := layout.ResolveSpaces(l.Spaces, s.displays)
	matched := layout.Match(l.Windows, s.windows)

	pidByWindow := make(map[uint32]int, len(s.windows))
	for _, w := range s.windows {
		pidByWindow[w.ID] = w.OwnerPID
	}

	fresh := make(map[int]uint32) // this pass's new matches
	for si, wid := range matched {
		if handled[si] {
			continue
		}
		fresh[si] = wid
		handled[si] = true
	}
	st.matched = len(fresh)

	moves := make(map[uint64][]uint32) // target space ID -> window IDs
	for si, wid := range fresh {
		if l.Windows[si].Fullscreen {
			st.fsWant++
			continue // handled by the fullscreen pass, not a space move
		}
		target, ok := resolved[l.Windows[si].SpaceUUID]
		if !ok {
			st.unresolved++
			continue
		}
		if s.winSpace[wid] == target {
			st.inPlace++
			continue
		}
		moves[target] = append(moves[target], wid)
	}

	for target, wids := range moves {
		if err := skylight.MoveWindowsToSpace(wids, target); err != nil {
			return st, fmt.Errorf("moving %d windows to space %d: %w", len(wids), target, err)
		}
		st.moved += len(wids)
	}
	if st.moved > 0 {
		// The bridged operation is asynchronous; give it a beat, then check.
		time.Sleep(500 * time.Millisecond)
		for target, wids := range moves {
			for _, wid := range wids {
				ids, err := skylight.SpacesForWindow(wid)
				if err == nil && len(ids) == 1 && ids[0] == target {
					st.verified++
				}
			}
		}
	}

	if frames {
		for si, wid := range fresh {
			if l.Windows[si].Fullscreen {
				continue // these get fullscreened, not framed
			}
			f := l.Windows[si].Frame
			if err := skylight.SetWindowFrame(pidByWindow[wid], wid, f.X, f.Y, f.W, f.H); err != nil {
				st.frameErr++
				continue
			}
			st.framed++
		}
	}

	// Re-fullscreen windows that were fullscreen at save time. Each transition
	// creates a fullscreen space and animates, so this runs last.
	if fullscreen {
		for si, wid := range fresh {
			if !l.Windows[si].Fullscreen {
				continue
			}
			switch skylight.SetFullscreen(pidByWindow[wid], wid, true) {
			case skylight.FullscreenChanged:
				st.fsDone++
			case skylight.FullscreenAlready:
				st.fsSkip++
			default:
				st.fsFail++
			}
		}
	}
	return st, nil
}

// dryRunReport prints what a restore pass would do, in the same shape as the
// real pass's summary.
func dryRunReport(l layout.Layout, s *snapshot, frames, fullscreen bool) error {
	resolved := layout.ResolveSpaces(l.Spaces, s.displays)
	matched := layout.Match(l.Windows, s.windows)
	var st passStats
	st.matched = len(matched)
	for si, wid := range matched {
		if l.Windows[si].Fullscreen {
			st.fsWant++
			continue
		}
		target, ok := resolved[l.Windows[si].SpaceUUID]
		if !ok {
			st.unresolved++
			continue
		}
		if s.winSpace[wid] == target {
			st.inPlace++
			continue
		}
		fmt.Printf("would move %q %q (window %d) -> space %d\n",
			l.Windows[si].OwnerName, l.Windows[si].Title, wid, target)
	}
	if fullscreen && st.fsWant > 0 {
		fmt.Printf("would restore %d fullscreen window(s)\n", st.fsWant)
	}
	printSummary(l, st, frames, fullscreen, true)
	return nil
}

// printSummary prints the one-line result a restore has always printed.
func printSummary(l layout.Layout, st passStats, frames, fullscreen, dryRun bool) {
	fmt.Printf("matched %d/%d saved windows; moved %d (%d verified), %d already in place",
		st.matched, len(l.Windows), st.moved, st.verified, st.inPlace)
	if st.unresolved > 0 {
		fmt.Printf(", %d on spaces that no longer exist", st.unresolved)
	}
	if frames && !dryRun {
		fmt.Printf("; restored %d frames", st.framed)
		if st.frameErr > 0 {
			fmt.Printf(" (%d failed — apps that refuse AX resize, or Accessibility not granted)", st.frameErr)
		}
	}
	if fullscreen && !dryRun && st.fsWant > 0 {
		fmt.Printf("; fullscreened %d (%d already, %d unsupported/failed)", st.fsDone, st.fsSkip, st.fsFail)
	} else if !fullscreen && st.fsWant > 0 {
		fmt.Printf("; %d fullscreen window(s) skipped (use -fullscreen)", st.fsWant)
	}
	fmt.Println()
	if st.moved > 0 && st.verified < st.moved {
		fmt.Fprintln(os.Stderr, "warning: some moves did not verify — the bridged-move API may be restricted on this macOS build")
	}
}
