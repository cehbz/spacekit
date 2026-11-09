// Package layout is the cgo-free domain core of spacekeeper: the window and
// space types read from the window server, and space resolution. The
// skylight package is kept out so this stays unit-testable.
package layout

type Rect struct {
	X, Y, W, H float64
}

// SavedSpace identifies a space durably: UUID first, position fallback.
// Index is the position within the display's user spaces when recorded.
type SavedSpace struct {
	UUID        string `json:"uuid"`
	DisplayUUID string `json:"displayUUID"`
	Index       int    `json:"index"`
}

// LiveWindow is a window present right now (CGWindowIDs are session-scoped).
type LiveWindow struct {
	ID        uint32
	OwnerPID  int
	BundleID  string
	OwnerName string
	Title     string
	Frame     Rect
}

// ListedWindow is one entry of the window server's whole list: every
// window of every owner and layer, on screen or not.
type ListedWindow struct {
	ID        uint32
	OwnerName string
	Title     string
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

// OverviewOpen reports whether Mission Control or App Exposé is showing.
// While either is, CGWindow bounds are the scaled thumbnails, so a read
// taken then is not one to validate against. WindowManager's ExposeShieldWindow is
// in the window list from entry to exit of both.
func OverviewOpen(all []ListedWindow) bool {
	for _, l := range all {
		if l.OwnerName == "WindowManager" && l.Title == "ExposeShieldWindow" {
			return true
		}
	}
	return false
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
