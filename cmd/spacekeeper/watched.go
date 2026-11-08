package main

import "sort"

// watchedCap is how many windows SLSRequestNotificationsForWindows accepts.
const watchedCap = 1024

// watched is the window server's per-window request set, owned by the screen.
// The zero value is an empty set.
type watched struct {
	ids map[uint32]bool
}

// Set replaces the set with these windows.
func (s *watched) Set(wids []uint32) {
	s.ids = make(map[uint32]bool, len(wids))
	for _, w := range wids {
		s.ids[w] = true
	}
}

// Attached adds a window and reports whether the set changed.
func (s *watched) Attached(wid uint32) bool {
	if s.ids == nil {
		s.ids = map[uint32]bool{}
	}
	if s.ids[wid] {
		return false
	}
	s.ids[wid] = true
	return true
}

// Destroyed removes a window and reports whether the set changed.
func (s *watched) Destroyed(wid uint32) bool {
	if !s.ids[wid] {
		return false
	}
	delete(s.ids, wid)
	return true
}

// IDs is the set in ascending order, cut at watchedCap.
func (s *watched) IDs() []uint32 {
	out := make([]uint32, 0, len(s.ids))
	for w := range s.ids {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) > watchedCap {
		out = out[:watchedCap]
	}
	return out
}

// Overflow is how many windows IDs leaves out.
func (s *watched) Overflow() int {
	if n := len(s.ids) - watchedCap; n > 0 {
		return n
	}
	return 0
}
