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
	Session int64  // login session (start, unix seconds) of its latest binding; 0 if it has none
	Binding uint32 // window-server id in that login session
}

// Evidence that a fresh window is a stored one recreated, strongest first.
// Frame outranks title: titles change with tabs, documents and loading
// pages, a frame is where the user keeps the window.
const (
	none          = iota
	sameAppOnly   // the same app and nothing else; counts only for the app's only fresh window
	sameTitle     // same non-empty title; no frame match
	sameFrameOnly // frame within the band; the title changed
	titleAndFrame
)

// Bind pairs fresh windows with the stored windows they recreate. A stored
// window is a candidate for a fresh one of the same app unless its binding in
// this login session is live. A pair is made only when the evidence is
// unambiguous at its strength: one candidate for the fresh window, and one
// fresh window for that candidate. Candidates tied at a level narrow to the
// one bound in the latest login session; twins of one login session stay
// ambiguous. The app alone is evidence only for a fresh window that is its
// app's only fresh one, so an app that relaunches with one window finds its
// record while an app with many never binds a window on the app alone.
// Stronger evidence is settled first. The result maps a fresh window's id to
// the stored window.
func Bind(fresh []Seen, stored []Stored, session int64, live map[uint32]bool) map[uint32]int64 {
	out := make(map[uint32]int64)
	taken := make(map[int64]bool)
	perApp := make(map[string]int, len(fresh))
	for _, f := range fresh {
		perApp[appKey(f.Bundle, f.App)]++
	}
	for level := titleAndFrame; level >= sameAppOnly; level-- {
		for changed := true; changed; {
			changed = false
			for _, f := range fresh {
				if _, done := out[f.Binding]; done {
					continue
				}
				if level == sameAppOnly && perApp[appKey(f.Bundle, f.App)] != 1 {
					continue
				}
				match := latest(f, stored, taken, level, session, live)
				if match == nil {
					continue
				}
				rivals := 0
				for _, g := range fresh {
					if _, done := out[g.Binding]; !done && evidence(g, *match, session, live) >= level {
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
// with weaker evidence than the strongest present are not considered, so an
// ambiguity at a stronger level blocks a weaker bind. Candidates tied at the
// strongest evidence narrow to the one bound in the latest login session:
// twins left by earlier failed binds and stale records share a title or a
// frame, and the latest is the window. Twins of the same login session stay
// ambiguous.
func latest(f Seen, stored []Stored, taken map[int64]bool, level int, session int64, live map[uint32]bool) *Stored {
	best := none
	for i := range stored {
		if !taken[stored[i].Window] {
			best = max(best, evidence(f, stored[i], session, live))
		}
	}
	if best < level {
		return nil
	}
	var match *Stored
	n := 0
	for i := range stored {
		o := &stored[i]
		if taken[o.Window] || evidence(f, *o, session, live) != best {
			continue
		}
		switch {
		case match == nil:
			match, n = o, 1
		case o.Session > match.Session:
			match, n = o, 1
		case o.Session == match.Session:
			n++
		}
	}
	if n != 1 {
		return nil
	}
	return match
}

// count is an unread count in a title, "(3) " or "(6/0) ", which apps change
// between login sessions.
var count = regexp.MustCompile(`\(\d+(/\d+)?\) ?`)

// evidence rates how strongly f recreates o. Titles are compared with their
// counts removed; an empty title matches nothing. A fullscreen window, fresh
// or stored, has no frame to compare. Two windows of the same app always rate
// at least sameAppOnly; Bind decides when that counts.
func evidence(f Seen, o Stored, session int64, live map[uint32]bool) int {
	if appKey(f.Bundle, f.App) != appKey(o.Bundle, o.App) {
		return none
	}
	if o.Session == session && live[o.Binding] {
		return none
	}
	ft := count.ReplaceAllString(f.Title, "")
	title := ft != "" && ft == count.ReplaceAllString(o.Title, "")
	frame := o.Placed && o.Fullscreen == "" && f.Fullscreen == "" && SameFrame(f.Frame, o.Frame)
	switch {
	case title && frame:
		return titleAndFrame
	case frame:
		return sameFrameOnly
	case title:
		return sameTitle
	}
	return sameAppOnly
}

// appKey identifies an app: its bundle id, or its name when it has none.
func appKey(bundle, app string) string {
	if bundle != "" {
		return "b:" + bundle
	}
	return "o:" + app
}
