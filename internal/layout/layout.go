// Package layout is the cgo-free domain core of spacekeeper: the saved-layout
// document, window matching, and space resolution. The skylight package is
// kept out so this stays unit-testable.
package layout

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

type Rect struct {
	X, Y, W, H float64
}

// SavedSpace identifies a space durably: UUID first, position fallback.
// Index is the position within the display's user spaces at save time.
type SavedSpace struct {
	UUID        string `json:"uuid"`
	DisplayUUID string `json:"displayUUID"`
	Index       int    `json:"index"`
}

type SavedWindow struct {
	BundleID  string `json:"bundleID,omitempty"`
	OwnerName string `json:"ownerName"`
	Title     string `json:"title,omitempty"`
	Frame     Rect   `json:"frame"`
	// SpaceUUID is the user desktop the window was on. Empty for a fullscreen
	// window, which has no user space.
	SpaceUUID string `json:"spaceUUID,omitempty"`
	// Fullscreen records that the window occupied its own fullscreen (type 4)
	// space; DisplayUUID is the display that space was on.
	Fullscreen  bool   `json:"fullscreen,omitempty"`
	DisplayUUID string `json:"displayUUID,omitempty"`
}

type Layout struct {
	SavedAt time.Time `json:"savedAt"`
	// BootedAt is the boot time of the session that saved this layout.
	// Zero for snapshots predating the field.
	BootedAt time.Time     `json:"bootedAt"`
	Spaces   []SavedSpace  `json:"spaces"`
	Windows  []SavedWindow `json:"windows"`
}

// DisplaySpaces is the count of user desktops on one display.
type DisplaySpaces struct {
	DisplayUUID string
	Spaces      int
}

// Stats summarizes a layout for listing and ranking.
type Stats struct {
	Windows  int
	Displays []DisplaySpaces // in the layout's display order
}

func (s Stats) DisplayCount() int { return len(s.Displays) }

// Stats counts windows and the spaces per display.
func (l Layout) Stats() Stats {
	perDisplay := map[string]int{}
	var order []string
	for _, sp := range l.Spaces {
		if _, seen := perDisplay[sp.DisplayUUID]; !seen {
			order = append(order, sp.DisplayUUID)
		}
		perDisplay[sp.DisplayUUID]++
	}
	st := Stats{Windows: len(l.Windows)}
	for _, d := range order {
		st.Displays = append(st.Displays, DisplaySpaces{DisplayUUID: d, Spaces: perDisplay[d]})
	}
	return st
}

// Richer reports whether a is a richer arrangement than b: more displays
// first, then more windows, then more recent. This is a transparent ordering
// used to pick the high-water-mark snapshot, never to gate what is saved.
func Richer(a, b Layout) bool {
	as, bs := a.Stats(), b.Stats()
	if ad, bd := as.DisplayCount(), bs.DisplayCount(); ad != bd {
		return ad > bd
	}
	if as.Windows != bs.Windows {
		return as.Windows > bs.Windows
	}
	return a.SavedAt.After(b.SavedAt)
}

// HighWaterIndex is the richest layout by Richer, -1 when ls is empty. It is
// pinned by pruning so a collapse never deletes the best arrangement.
func HighWaterIndex(ls []Layout) int {
	best := -1
	for i := range ls {
		if best == -1 || Richer(ls[i], ls[best]) {
			best = i
		}
	}
	return best
}

// DefaultRestoreIndex picks the snapshot restore should use by default: the
// newest snapshot saved in a previous boot session after that session had
// settled (uptime at save >= settle). Post-boot scrambles — snapshots taken
// right after login, before apps reopen — are thereby skipped, including ones
// from earlier boots in a rapid reboot cycle. Falls back to the newest
// previous-boot snapshot, then the newest overall. Legacy snapshots (zero
// BootedAt) can't prove uptime and are trusted as settled. A zero boot means
// the boot time is unknown: only the newest-overall tier applies.
// Returns -1 only when ls is empty.
func DefaultRestoreIndex(ls []Layout, boot time.Time, settle time.Duration) int {
	best := -1
	bestTier := 0 // higher wins; recency breaks ties within a tier
	for i, l := range ls {
		tier := 1
		if !boot.IsZero() && l.SavedAt.Before(boot) {
			tier = 2
			if l.BootedAt.IsZero() || l.SavedAt.Sub(l.BootedAt) >= settle {
				tier = 3
			}
		}
		if best == -1 || tier > bestTier ||
			(tier == bestTier && l.SavedAt.After(ls[best].SavedAt)) {
			best, bestTier = i, tier
		}
	}
	return best
}

// LatestBefore returns the index of the newest layout saved strictly before
// t, or -1. Used to pick the reference for a post-reconfiguration restore:
// the last snapshot from before the displays started changing.
func LatestBefore(ls []Layout, t time.Time) int {
	best := -1
	for i, l := range ls {
		if !l.SavedAt.Before(t) {
			continue
		}
		if best == -1 || l.SavedAt.After(ls[best].SavedAt) {
			best = i
		}
	}
	return best
}

// SameDisplays reports whether both stats describe the same displays with the
// same number of desktops each, regardless of order. A transient display drop
// ends in the same topology it started from; anything else is not one.
func (s Stats) SameDisplays(o Stats) bool {
	if len(s.Displays) != len(o.Displays) {
		return false
	}
	counts := make(map[string]int, len(s.Displays))
	for _, d := range s.Displays {
		counts[d.DisplayUUID] = d.Spaces
	}
	for _, d := range o.Displays {
		if n, ok := counts[d.DisplayUUID]; !ok || n != d.Spaces {
			return false
		}
	}
	return true
}

// FirstStartOfBoot reports whether no layout has been saved in the current
// boot session, i.e. the agent is starting for the first time since boot and
// login convergence is due. Uptime is not a usable signal: an OS upgrade or a
// slow login can start the agent well after boot. A zero boot time means the
// session is unknown, so no claim is made.
func FirstStartOfBoot(ls []Layout, boot time.Time) bool {
	if boot.IsZero() {
		return false
	}
	for _, l := range ls {
		if !l.SavedAt.Before(boot) {
			return false
		}
	}
	return true
}

// ReferenceFor picks the layout a settled display change should restore
// from: the newest one saved in this boot session before the change began
// whose display set equals the settled one. A snapshot taken during a
// monitor outage has the wrong display set and is skipped, so a return after
// hours restores the last layout that had that display.
func ReferenceFor(ls []Layout, before, boot time.Time, want Stats) int {
	best := -1
	for i, l := range ls {
		if !l.SavedAt.Before(before) || l.SavedAt.Before(boot) || !l.Stats().SameDisplays(want) {
			continue
		}
		if best == -1 || l.SavedAt.After(ls[best].SavedAt) {
			best = i
		}
	}
	return best
}

// Signature is a stable fingerprint of a layout's content (spaces and windows,
// ignoring the timestamp), used to skip saving snapshots identical to the
// previous one. Order-independent: equal arrangements produce equal signatures.
func (l Layout) Signature() string {
	spaces := append([]SavedSpace(nil), l.Spaces...)
	sort.Slice(spaces, func(i, j int) bool {
		if spaces[i].DisplayUUID != spaces[j].DisplayUUID {
			return spaces[i].DisplayUUID < spaces[j].DisplayUUID
		}
		return spaces[i].Index < spaces[j].Index
	})
	windows := append([]SavedWindow(nil), l.Windows...)
	sort.Slice(windows, func(i, j int) bool {
		a, b := windows[i], windows[j]
		ka := a.BundleID + "\x00" + a.OwnerName + "\x00" + a.Title + "\x00" + a.SpaceUUID
		kb := b.BundleID + "\x00" + b.OwnerName + "\x00" + b.Title + "\x00" + b.SpaceUUID
		return ka < kb
	})
	out, _ := json.Marshal(struct {
		S []SavedSpace
		W []SavedWindow
	}{spaces, windows})
	return string(out)
}

// LiveWindow is a window present right now (CGWindowIDs are session-scoped,
// so live IDs never appear in a Layout).
type LiveWindow struct {
	ID        uint32
	OwnerPID  int
	BundleID  string
	OwnerName string
	Title     string
	Frame     Rect
}

// CurrentSpace / CurrentDisplay mirror the current Mission Control state
// (user spaces only), translated from skylight types by the caller.
type CurrentSpace struct {
	ID   uint64
	UUID string
}

type CurrentDisplay struct {
	UUID   string
	Spaces []CurrentSpace
}

// FrameDebt is a window whose frame macOS changed during a display drop and
// that still needs putting back. Accessibility can only resize windows on an
// active space, so debts are paid as the user visits each space; a window
// the user has since moved or resized is left alone.
type FrameDebt struct {
	ID         uint32
	PID        int
	Want, Seen Rect
	// Tried is set once a payment has been attempted. Until then the window
	// may still be drifting under macOS, so Seen is not a baseline.
	Tried bool
}

// FrameDebts lists matched windows whose live frame differs from the saved
// one and is smaller: a display drop only shrinks or squeezes windows, so a
// window that grew resized itself and is left alone. Untitled windows
// (popups, find bars) are skipped; they resize on their own too.
func FrameDebts(saved []SavedWindow, matched map[int]uint32, live []LiveWindow) []FrameDebt {
	byID := make(map[uint32]LiveWindow, len(live))
	for _, l := range live {
		byID[l.ID] = l
	}
	var out []FrameDebt
	for si, wid := range matched {
		s := saved[si]
		l, ok := byID[wid]
		if !ok || s.Fullscreen || s.Title == "" || l.Frame == s.Frame || l.Frame.W*l.Frame.H >= s.Frame.W*s.Frame.H {
			continue
		}
		out = append(out, FrameDebt{ID: wid, PID: l.OwnerPID, Want: s.Frame, Seen: l.Frame})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Settle decides a debt against the current state of its window. It is
// dropped when the window is gone or already shows the wanted frame, waits
// while its space is inactive, and is paid the first time its space is
// active whatever its frame has become: macOS keeps moving windows for
// seconds after a drop, and nobody can have touched a window on a space not
// yet visited. After an attempt, a frame other than the one that attempt
// left means the user or the app took over, and the debt is dropped.
func (d FrameDebt) Settle(live *LiveWindow, activeSpaces map[uint64]bool, spaceOf map[uint32]uint64) (pay, drop bool) {
	if live == nil || live.Frame == d.Want {
		return false, true
	}
	if !activeSpaces[spaceOf[d.ID]] {
		return false, false
	}
	if d.Tried && live.Frame != d.Seen {
		return false, true
	}
	return true, false
}

// OverviewOpen reports whether Mission Control's overview is showing. While
// it is, CGWindow bounds are the scaled thumbnails, so a layout gathered
// then is not one to keep. The overview adds WindowManager's highlight
// overlay windows to the window list and nothing else does.
func OverviewOpen(live []LiveWindow) bool {
	for _, l := range live {
		if l.OwnerName == "WindowManager" && l.Title == "Window Highlight Overlay" {
			return true
		}
	}
	return false
}

// Match pairs saved windows with live windows. Windows only match within the
// same app (bundle ID, falling back to owner name). Among an app's windows,
// equal titles are the strongest signal, then frame proximity. Each live
// window is used at most once. Returns saved-slice index -> live window ID.
func Match(saved []SavedWindow, live []LiveWindow) map[int]uint32 {
	type pair struct {
		savedIdx, liveIdx int
		score             float64
	}
	var pairs []pair
	for si, s := range saved {
		for li, l := range live {
			if appKey(s.BundleID, s.OwnerName) != appKey(l.BundleID, l.OwnerName) {
				continue
			}
			pairs = append(pairs, pair{si, li, matchScore(s, l)})
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].score > pairs[j].score })

	matched := make(map[int]uint32)
	usedSaved := make(map[int]bool)
	usedLive := make(map[int]bool)
	for _, p := range pairs {
		if usedSaved[p.savedIdx] || usedLive[p.liveIdx] {
			continue
		}
		usedSaved[p.savedIdx] = true
		usedLive[p.liveIdx] = true
		matched[p.savedIdx] = live[p.liveIdx].ID
	}
	return matched
}

// appKey gives windows an app identity: bundle ID when known, otherwise the
// owner name (some processes have no bundle).
func appKey(bundleID, ownerName string) string {
	if bundleID != "" {
		return "b:" + bundleID
	}
	return "o:" + ownerName
}

// matchScore rates a saved/live pairing. An exact non-empty title is the
// strongest signal (titles outrank any frame evidence); frame proximity
// breaks ties, since titles drift between sessions (browser tabs, documents).
func matchScore(s SavedWindow, l LiveWindow) float64 {
	score := 0.0
	if s.Title != "" && s.Title == l.Title {
		score += 100
	}
	d := math.Abs(s.Frame.X-l.Frame.X) + math.Abs(s.Frame.Y-l.Frame.Y) +
		math.Abs(s.Frame.W-l.Frame.W) + math.Abs(s.Frame.H-l.Frame.H)
	score += math.Max(0, 50-d/10)
	return score
}

// SpaceDeficits returns, per current display (in the given order), how many
// user spaces the saved layout had beyond what the display has now. Entry i
// aligns with displays[i]; it is 0 when the display has enough (or more)
// spaces, or when the saved layout never referenced that display. Saved
// displays that are no longer present cannot be recreated and are ignored.
func SpaceDeficits(saved []SavedSpace, displays []CurrentDisplay) []int {
	savedPerDisplay := make(map[string]int)
	for _, s := range saved {
		savedPerDisplay[s.DisplayUUID]++
	}
	out := make([]int, len(displays))
	for i, d := range displays {
		if deficit := savedPerDisplay[d.UUID] - len(d.Spaces); deficit > 0 {
			out[i] = deficit
		}
	}
	return out
}

// ResolveSpaces maps each saved space UUID to a current space ID. A space
// whose UUID is gone resolves by (display UUID, index); a saved space whose
// display is gone or whose index is out of range gets no entry.
func ResolveSpaces(saved []SavedSpace, displays []CurrentDisplay) map[string]uint64 {
	byUUID := make(map[string]uint64)
	byDisplay := make(map[string][]CurrentSpace)
	for _, d := range displays {
		byDisplay[d.UUID] = d.Spaces
		for _, sp := range d.Spaces {
			if sp.UUID != "" {
				byUUID[sp.UUID] = sp.ID
			}
		}
	}
	resolved := make(map[string]uint64)
	for _, s := range saved {
		if s.UUID != "" {
			if id, ok := byUUID[s.UUID]; ok {
				resolved[s.UUID] = id
				continue
			}
		}
		if spaces := byDisplay[s.DisplayUUID]; s.Index >= 0 && s.Index < len(spaces) {
			resolved[s.UUID] = spaces[s.Index].ID
		}
	}
	return resolved
}
