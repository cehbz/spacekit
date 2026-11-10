package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cehbz/spacekit/internal/layout"
	"github.com/cehbz/spacekit/internal/store"
)

// inspectWindow prints one window's record, its bindings, and its decisions
// interleaved with the raw window-server events behind them, newest last.
// arg is a window-server id of this boot, a word of a live window's app name
// or title that picks out one window, or a record id with record set.
// Several matches are listed to choose from. Main thread only.
func inspectWindow(arg string, record bool, last int) error {
	id, err := strconv.ParseUint(strings.TrimSpace(arg), 10, 64)
	if err != nil && record {
		return fmt.Errorf("-record needs a record id")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if err != nil || id == 0 {
		// Text: match the live windows' app and title and their records' title,
		// which the agent keeps with Screen Recording while a terminal run
		// sees titles only on the shown space.
		s, err := readAll()
		if err != nil {
			return err
		}
		titles, err := recordTitles(st, s.windows)
		if err != nil {
			return err
		}
		matches := matchWindows(s.windows, titles, arg)
		switch len(matches) {
		case 0:
			fmt.Printf("no window's app or title contains %q\n", arg)
			return nil
		case 1:
			id = uint64(matches[0].ID)
		default:
			for _, w := range matches {
				if w.Title == "" {
					w.Title = titles[w.ID]
				}
				fmt.Println(inspectLine(s, w))
			}
			return nil
		}
	}
	win := int64(id)
	if !record {
		if win, err = st.WindowByBinding(bootTime(), uint32(id)); err != nil {
			return err
		}
		if win == 0 {
			fmt.Printf("window %d has no record in this boot; a record id works with -record\n", id)
			return nil
		}
	}
	rec, err := st.Window(win)
	if err != nil {
		return err
	}
	fmt.Printf("record #%d  %s | %s  (%s)  first seen %s\n", rec.Window, rec.App, rec.Title, rec.Bundle, rec.FirstSeen.Format("2006-01-02 15:04"))
	for _, t := range rec.Targets {
		state := "clean"
		if t.Dirty {
			state = "dirty"
			if t.Progress != 0 {
				state = "dirty, put tried"
			}
		}
		fmt.Printf("  target on %-18s %-8.8s %.0f,%.0f %.0fx%.0f  since %s (#%d)  %s\n",
			shortDisplaySet(t.DisplaySet), t.Space, t.Frame.X, t.Frame.Y, t.Frame.W, t.Frame.H, t.Since.Format("2006-01-02 15:04:05"), t.Change, state)
	}
	bs, err := st.Bindings(win)
	if err != nil {
		return err
	}
	var wids []uint32
	boot := bootTime()
	for _, b := range bs {
		fmt.Printf("  bound in boot %s as window %d\n", b.Boot.Format("2006-01-02 15:04:05"), b.WID)
		if b.Boot.Unix() == boot.Unix() {
			wids = append(wids, b.WID)
		}
	}
	steps, err := st.History(win, last)
	if err != nil {
		return err
	}
	var since time.Time
	if len(steps) > 0 {
		since = steps[0].At
	} else {
		since = time.Now().Add(-time.Hour)
	}
	var raws []store.Raw
	if len(wids) > 0 {
		if raws, err = st.Stream(0, since); err != nil {
			return err
		}
	}
	fmt.Println()
	var lines []string
	for _, wid := range wids {
		lines = append(lines, explainLines(nil, raws, wid)...)
	}
	lines = append(lines, explainLines(steps, nil, 0)...)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i][:12] < lines[j][:12] })
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

// recordTitles is the recorded title of each live window bound in this boot.
func recordTitles(st *store.Store, windows []layout.LiveWindow) (map[uint32]string, error) {
	boot := bootTime()
	out := map[uint32]string{}
	for _, w := range windows {
		win, err := st.WindowByBinding(boot, w.ID)
		if err != nil {
			return nil, err
		}
		if win == 0 {
			continue
		}
		rec, err := st.Window(win)
		if err != nil {
			return nil, err
		}
		out[w.ID] = rec.Title
	}
	return out, nil
}

// matchWindows is the live windows whose app name, title or recorded title
// contains text, case-insensitively.
func matchWindows(windows []layout.LiveWindow, titles map[uint32]string, text string) []layout.LiveWindow {
	text = strings.ToLower(text)
	has := func(s string) bool { return strings.Contains(strings.ToLower(s), text) }
	var out []layout.LiveWindow
	for _, w := range windows {
		if has(w.OwnerName) || has(w.Title) || has(titles[w.ID]) {
			out = append(out, w)
		}
	}
	return out
}

// shortDisplaySet abbreviates a display set (sorted display UUIDs, comma-joined)
// to the first eight characters of each, joined by +.
func shortDisplaySet(set string) string {
	parts := strings.Split(set, ",")
	for i, p := range parts {
		if len(p) > 8 {
			parts[i] = p[:8]
		}
	}
	return strings.Join(parts, "+")
}

// explainLines renders a window's decisions and the raw window-server events
// that name it (as Window or as a decision's Binding), interleaved by time.
func explainLines(steps []store.Step, raws []store.Raw, wid uint32) []string {
	type line struct {
		at   time.Time
		text string
	}
	var ls []line
	for _, s := range steps {
		text := fmt.Sprintf("%s  %-20s #%d %s", s.At.Format("15:04:05.000"), s.Kind, s.Change, s.Cause)
		if s.Opened != nil {
			f := s.Opened.Frame
			text += fmt.Sprintf(" → %.8s %.0f,%.0f %.0fx%.0f", s.Opened.Space, f.X, f.Y, f.W, f.H)
		}
		ls = append(ls, line{s.At, text})
	}
	names := regexp.MustCompile(`"(Window|Binding)":` + strconv.FormatUint(uint64(wid), 10) + `\b`)
	for _, r := range raws {
		if duplicated[r.Kind] || !names.MatchString(r.Payload) {
			continue
		}
		text := fmt.Sprintf("%s  %-20s", r.At.Format("15:04:05.000"), r.Kind)
		if d := rawDetail(r); d != "" {
			text += " " + d
		}
		ls = append(ls, line{r.At, strings.TrimRight(text, " ")})
	}
	sort.SliceStable(ls, func(i, j int) bool { return ls[i].at.Before(ls[j].at) })
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.text
	}
	return out
}

// duplicated are stream kinds another line already shows: the window
// server's bare moved and resized notices (the screen's "window changed"
// carries the frame) and the model's target changes (the decision lines).
var duplicated = map[string]bool{"window moved": true, "window resized": true, "target changed": true}

// rawDetail is the part of a window event worth a glance: its space and,
// for a change, its frame.
func rawDetail(r store.Raw) string {
	var p struct {
		Space uint64
		Gone  bool
		Frame *struct{ X, Y, W, H float64 }
	}
	if json.Unmarshal([]byte(r.Payload), &p) != nil || p.Space == 0 {
		return ""
	}
	s := fmt.Sprintf("space %d", p.Space)
	if p.Gone {
		return s + " gone"
	}
	if p.Frame != nil {
		s += fmt.Sprintf(" at %.0f,%.0f %.0fx%.0f", p.Frame.X, p.Frame.Y, p.Frame.W, p.Frame.H)
	}
	return s
}
