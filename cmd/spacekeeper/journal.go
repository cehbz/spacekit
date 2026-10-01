package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/store"
)

func openStore() (*store.Store, error) {
	return store.Open(filepath.Join(dataDir(), "spacekeeper.db"))
}

// logCmd prints the latest changes and disturbances, oldest first: one line
// each, and with verbose one line per window under each change.
func logCmd(last int, verbose bool) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	entries, err := st.Journal(last)
	if err != nil {
		return err
	}
	ds, err := st.Disturbances(last)
	if err != nil {
		return err
	}
	type line struct {
		at   time.Time
		text string
	}
	var lines []line
	for _, e := range entries {
		text := fmt.Sprintf("%s  #%-4d %-22s %s", e.At.Format("2006-01-02 15:04:05"), e.ID, e.Cause, summary(e))
		if e.Note != "" {
			text += "  (" + e.Note + ")"
		}
		if verbose {
			for _, ev := range e.Events {
				text += "\n      " + eventLine(ev)
			}
		}
		lines = append(lines, line{e.At, text})
	}
	for _, d := range ds {
		if len(entries) > 0 && d.Began.Before(entries[0].At) {
			continue
		}
		dur := "still open"
		if !d.Ended.IsZero() {
			dur = d.Ended.Sub(d.Began).Round(time.Second).String()
		}
		lines = append(lines, line{d.Began, fmt.Sprintf("%s  %s disturbance, %s", d.Began.Format("2006-01-02 15:04:05"), d.Kind, dur)})
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].at.Before(lines[j].at) })
	for _, l := range lines {
		fmt.Println(l.text)
	}
	return nil
}

// summary counts a change's events by kind, in a fixed order.
func summary(e store.Entry) string {
	counts := map[string]int{}
	for _, ev := range e.Events {
		counts[ev.Kind]++
	}
	var parts []string
	for _, k := range []string{"adopted", "bound", "owed", "repaired", "released", "gave up", "undone"} {
		if counts[k] > 0 {
			parts = append(parts, k+" "+strconv.Itoa(counts[k]))
		}
	}
	return strings.Join(parts, ", ")
}

func eventLine(ev store.Event) string {
	title := ev.Title
	if len(title) > 36 {
		title = title[:36]
	}
	text := fmt.Sprintf("%-9s %s | %s", ev.Kind, ev.App, title)
	if ev.From != nil || ev.To != nil {
		text += "  " + placement(ev.From) + " -> " + placement(ev.To)
	}
	return text
}

func placement(p *arrangement.Placement) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprintf("%.8s %.0f,%.0f %.0fx%.0f", p.Space, p.Frame.X, p.Frame.Y, p.Frame.W, p.Frame.H)
}

// undoCmd reverts one change; the running agent puts the windows back at its
// next look.
func undoCmd(arg string) error {
	id, err := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64)
	if err != nil {
		return fmt.Errorf("undo needs a change number from `spacekeeper log`")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	reverted, skipped, err := st.Undo(id, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("reverted %d window(s)", reverted)
	if skipped > 0 {
		fmt.Printf(", left %d that changed again since", skipped)
	}
	fmt.Println("; the agent puts them back at its next look")
	return nil
}

// oweCmd marks every window of the current arrangement owed; the running
// agent puts them back at its next look.
func oweCmd() error {
	s, err := gather()
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	arr, err := st.Arrangement(displaySet(s.spaces))
	if err != nil {
		return err
	}
	n, err := st.OweAll(arr, bootTime(), time.Now(), "manual owe")
	if err != nil {
		return err
	}
	fmt.Printf("%d window(s) owed their placement; the agent puts them back at its next look\n", n)
	return nil
}
