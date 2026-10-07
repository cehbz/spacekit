package arrangement

// Stored is a placed window as the store last knew it bound: a candidate for
// binding to a fresh window-server id once that binding is dead.
type Stored struct {
	Window             int64
	Bundle, App, Title string
	Placement
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
// fresh window for that candidate. Stronger evidence is settled first. The
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
				var match *Stored
				n := 0
				for i := range stored {
					o := &stored[i]
					if !taken[o.Window] && evidence(f, *o, boot, live) >= level {
						match = o
						n++
					}
				}
				if n != 1 {
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

func evidence(f Seen, o Stored, boot int64, live map[uint32]bool) int {
	if appKey(f.Bundle, f.App) != appKey(o.Bundle, o.App) {
		return none
	}
	if o.Boot == boot && live[o.Binding] {
		return none
	}
	title := f.Title != "" && f.Title == o.Title
	frame := SameFrame(f.Frame, o.Frame)
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
