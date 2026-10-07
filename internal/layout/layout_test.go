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
