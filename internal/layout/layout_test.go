package layout

import "testing"

func TestOverviewOpen(t *testing.T) {
	shield := ListedWindow{ID: 7, OwnerName: "WindowManager", Title: "ExposeShieldWindow"}
	overlay := ListedWindow{ID: 8, OwnerName: "WindowManager", Title: "Window Highlight Overlay"}
	finder := ListedWindow{ID: 9, OwnerName: "Finder", Title: "Desktop"}
	for _, c := range []struct {
		name string
		all  []ListedWindow
		want bool
	}{
		{"shield window: showing", []ListedWindow{finder, shield}, true},
		{"highlight overlay alone: not showing", []ListedWindow{finder, overlay}, false},
		{"empty list: not showing", nil, false},
	} {
		if got := OverviewOpen(c.all); got != c.want {
			t.Errorf("%s: OverviewOpen = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestResolveSpacesByUUID(t *testing.T) {
	saved := []SavedSpace{{UUID: "AAA", DisplayUUID: "D1", Index: 0}}
	displays := []CurrentDisplay{
		{UUID: "D1", Spaces: []CurrentSpace{{ID: 101, UUID: "AAA"}, {ID: 102, UUID: "BBB"}}},
	}
	m := ResolveSpaces(saved, displays)
	if m["AAA"] != 101 {
		t.Fatalf("want AAA->101, got %v", m)
	}
}

func TestResolveSpacesUUIDFoundOnOtherDisplay(t *testing.T) {
	// Space migrated displays but kept its UUID: UUID match wins.
	saved := []SavedSpace{{UUID: "AAA", DisplayUUID: "D1", Index: 0}}
	displays := []CurrentDisplay{
		{UUID: "D2", Spaces: []CurrentSpace{{ID: 201, UUID: "AAA"}}},
	}
	if m := ResolveSpaces(saved, displays); m["AAA"] != 201 {
		t.Fatalf("want AAA->201, got %v", m)
	}
}

func TestResolveSpacesFallbackToIndex(t *testing.T) {
	saved := []SavedSpace{{UUID: "GONE", DisplayUUID: "D1", Index: 1}}
	displays := []CurrentDisplay{
		{UUID: "D1", Spaces: []CurrentSpace{{ID: 101, UUID: "AAA"}, {ID: 102, UUID: "BBB"}}},
	}
	if m := ResolveSpaces(saved, displays); m["GONE"] != 102 {
		t.Fatalf("want GONE->102 via (display,index), got %v", m)
	}
}

func TestResolveSpacesMissingDisplayOrIndexSkipped(t *testing.T) {
	saved := []SavedSpace{
		{UUID: "GONE1", DisplayUUID: "DX", Index: 0}, // display gone
		{UUID: "GONE2", DisplayUUID: "D1", Index: 5}, // index out of range
	}
	displays := []CurrentDisplay{
		{UUID: "D1", Spaces: []CurrentSpace{{ID: 101, UUID: "AAA"}}},
	}
	if m := ResolveSpaces(saved, displays); len(m) != 0 {
		t.Fatalf("want no resolution, got %v", m)
	}
}
