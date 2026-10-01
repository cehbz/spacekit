package arrangement

// Still reports whether two observations of the session, taken a moment
// apart, show the same windows at the same placements. While spaces slide,
// the overview animates, or a window is dragged, the window server reports
// frames in motion; what it shows then is not intent, and a look is only
// taken when the screen is still.
func Still(a, b []Seen) bool {
	if len(a) != len(b) {
		return false
	}
	at := make(map[uint32]Placement, len(a))
	for _, s := range a {
		at[s.Binding] = s.Placement
	}
	for _, s := range b {
		if p, ok := at[s.Binding]; !ok || p != s.Placement {
			return false
		}
	}
	return true
}
