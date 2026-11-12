package arrangement

import "regexp"

// Stored is a window as the store last knew it bound: a candidate for
// binding to a fresh window-server id once that binding is dead. Placement is
// its open placement in the arrangement being decided when Placed; a candidate
// recorded only elsewhere, or fullscreen here, offers no frame evidence.
type Stored struct {
	Window             int64
	Bundle, App, Title string
	Placement
	Placed  bool   // has an open placement in the arrangement being decided
	Boot    int64  // boot (unix seconds) of its latest binding; 0 if it has none
	Binding uint32 // window-server id in that boot
}

// Evidence that a fresh window is a stored one recreated, strongest first.
const (
	none          = iota
	sameFrameOnly // frame within the band; the title changed
	sameTitle     // same non-empty title
	titleAndFrame
)

// Bind pairs fresh windows with the stored windows they recreate. A stored
// window is a candidate for a fresh one of the same app unless its binding in
// this boot is live. A pair is made only when the evidence is
// unambiguous at its strength: one candidate for the fresh window, and one
// fresh window for that candidate. Candidates tied on title and frame are narrowed
// to those bound in the latest boot. Stronger evidence is settled first. The
// result maps a fresh window's id to the stored window.
func Bind(fresh []Seen, stored []Stored, boot int64, live map[uint32]bool) map[uint32]int64 {
	out := make(map[uint32]int64)
	taken := make(map[int64]bool)
	for level := titleAndFrame; level >= sameFrameOnly; level-- {
		for changed := true; changed; {
			changed = false
			for _, f := range fresh {
				if _, done := out[f.Binding]; done {
					continue
				}
				match := latest(f, stored, taken, level, boot, live)
				if match == nil {
					continue
				}
				rivals := 0
				for _, g := range fresh {
					if _, done := out[g.Binding]; !done && evidence(g, *match, boot, live) >= level {
						rivals++
					}
				}
				if rivals == 1 {
					out[f.Binding] = match.Window
					taken[match.Window] = true
					changed = true
				}
			}
		}
	}
	return out
}

// latest returns the one untaken candidate for f at level, or nil. Candidates
// matching on title and frame are narrowed to those bound in the latest boot:
// twins left by earlier failed binds share both, and the latest is the window.
// Twins of the same boot, and ties on title or frame alone, stay ambiguous.
func latest(f Seen, stored []Stored, taken map[int64]bool, level int, boot int64, live map[uint32]bool) *Stored {
	var match *Stored
	n := 0
	for i := range stored {
		o := &stored[i]
		if taken[o.Window] || evidence(f, *o, boot, live) < level {
			continue
		}
		switch {
		case match == nil:
			match, n = o, 1
		case level != titleAndFrame:
			n++
		case o.Boot > match.Boot:
			match, n = o, 1
		case o.Boot == match.Boot:
			n++
		}
	}
	if n != 1 {
		return nil
	}
	return match
}

// count is an unread count in a title, "(3) " or "(6/0) ", which apps change
// between boots.
var count = regexp.MustCompile(`\(\d+(/\d+)?\) ?`)

// evidence rates how strongly f recreates o. Titles are compared with their
// counts removed; an empty title matches nothing. A fullscreen window, fresh
// or stored, has no frame to compare.
func evidence(f Seen, o Stored, boot int64, live map[uint32]bool) int {
	if appKey(f.Bundle, f.App) != appKey(o.Bundle, o.App) {
		return none
	}
	if o.Boot == boot && live[o.Binding] {
		return none
	}
	ft := count.ReplaceAllString(f.Title, "")
	title := ft != "" && ft == count.ReplaceAllString(o.Title, "")
	frame := o.Placed && o.Fullscreen == "" && f.Fullscreen == "" && SameFrame(f.Frame, o.Frame)
	switch {
	case title && frame:
		return titleAndFrame
	case title:
		return sameTitle
	case frame:
		return sameFrameOnly
	}
	return none
}

// appKey identifies an app: its bundle id, or its name when it has none.
func appKey(bundle, app string) string {
	if bundle != "" {
		return "b:" + bundle
	}
	return "o:" + app
}
