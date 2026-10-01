// Package store keeps the arrangement model in SQLite: windows and their
// bindings, one arrangement per display set, system-versioned placements,
// owed windows, and the journal of changes, events and disturbances.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/cehbz/spacekit/internal/arrangement"
	"github.com/cehbz/spacekit/internal/layout"
)

//go:embed schema.sql
var schema string

type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}

// migrations bring a database from PRAGMA user_version n to n+1. schema.sql
// is version 0.
var migrations = []string{
	`ALTER TABLE binding ADD COLUMN run INTEGER NOT NULL DEFAULT 0`,
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for ; v < len(migrations); v++ {
		if _, err := db.Exec(migrations[v]); err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Arrangement returns the arrangement for a display set, creating it on first use.
func (s *Store) Arrangement(displaySet string) (int64, error) {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO arrangement(display_set) VALUES (?)`, displaySet); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM arrangement WHERE display_set = ?`, displaySet).Scan(&id)
	return id, err
}

// SetSpaces records the arrangement's spaces as seen now. A space no longer
// present keeps its row, so a placement on it still resolves by position.
func (s *Store) SetSpaces(arr int64, spaces []layout.SavedSpace) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, sp := range spaces {
		if _, err := tx.Exec(`INSERT INTO space(arrangement_id, key, display, idx) VALUES (?, ?, ?, ?)
			ON CONFLICT(arrangement_id, key) DO UPDATE SET display = excluded.display, idx = excluded.idx`,
			arr, sp.UUID, sp.DisplayUUID, sp.Index); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Spaces returns every space the arrangement has had.
func (s *Store) Spaces(arr int64) ([]layout.SavedSpace, error) {
	rows, err := s.db.Query(`SELECT key, display, idx FROM space WHERE arrangement_id = ? ORDER BY display, idx`, arr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []layout.SavedSpace
	for rows.Next() {
		var sp layout.SavedSpace
		if err := rows.Scan(&sp.UUID, &sp.DisplayUUID, &sp.Index); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// Recorded returns the windows bound in this boot with their current
// placement in the arrangement, if any, and whether they are owed.
func (s *Store) Recorded(arr int64, boot time.Time) ([]arrangement.Recorded, error) {
	rows, err := s.db.Query(`
		SELECT w.id, b.wid, p.space, p.x, p.y, p.w, p.h, o.progress
		FROM binding b
		JOIN window w ON w.id = b.window_id
		LEFT JOIN placement p ON p.window_id = w.id AND p.arrangement_id = ?1 AND p.closed_by IS NULL
		LEFT JOIN owed o ON o.window_id = w.id AND o.arrangement_id = ?1
		WHERE b.boot = ?2
		ORDER BY w.id`, arr, boot.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []arrangement.Recorded
	for rows.Next() {
		var r arrangement.Recorded
		var space sql.NullString
		var x, y, w, h sql.NullFloat64
		var progress sql.NullInt64
		if err := rows.Scan(&r.Window, &r.Binding, &space, &x, &y, &w, &h, &progress); err != nil {
			return nil, err
		}
		if space.Valid {
			r.Placed = true
			r.Placement = arrangement.Placement{Space: space.String,
				Frame: layout.Rect{X: x.Float64, Y: y.Float64, W: w.Float64, H: h.Float64}}
		}
		if progress.Valid {
			r.Owed, r.Progress = true, arrangement.Progress(progress.Int64)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Apply records what a look decided, in one transaction and under one
// change. Decisions that alter nothing write nothing.
func (s *Store) Apply(arr int64, boot, at time.Time, cause string, ds []arrangement.Decision) error {
	if !writes(ds) {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO change(at, cause) VALUES (?, ?)`, at.UnixMilli(), cause)
	if err != nil {
		return err
	}
	change, _ := res.LastInsertId()
	for _, d := range ds {
		if err := apply(tx, change, arr, boot, at, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func writes(ds []arrangement.Decision) bool {
	for _, d := range ds {
		if d.Kind != arrangement.Repair {
			return true
		}
		if _, ok := d.Progress(); ok {
			return true
		}
	}
	return false
}

func apply(tx *sql.Tx, change, arr int64, boot, at time.Time, d arrangement.Decision) error {
	win := d.Window
	switch d.Kind {
	case arrangement.Adopt:
		if win == 0 {
			var err error
			if win, err = newWindow(tx, boot, at, d.Seen); err != nil {
				return err
			}
		}
		if err := place(tx, change, arr, win, d.Seen.Placement); err != nil {
			return err
		}
		return event(tx, change, win, "adopted")
	case arrangement.Owe:
		if _, err := tx.Exec(`INSERT OR IGNORE INTO owed(window_id, arrangement_id, progress, since) VALUES (?, ?, 0, ?)`,
			win, arr, change); err != nil {
			return err
		}
		return event(tx, change, win, "owed")
	case arrangement.Release:
		if _, err := tx.Exec(`DELETE FROM owed WHERE window_id = ? AND arrangement_id = ?`, win, arr); err != nil {
			return err
		}
		return event(tx, change, win, "released")
	case arrangement.GiveUp:
		if _, err := tx.Exec(`DELETE FROM owed WHERE window_id = ? AND arrangement_id = ?`, win, arr); err != nil {
			return err
		}
		if err := place(tx, change, arr, win, d.Seen.Placement); err != nil {
			return err
		}
		return event(tx, change, win, "gave up")
	case arrangement.Repair:
		p, ok := d.Progress()
		if !ok {
			return nil
		}
		if _, err := tx.Exec(`UPDATE owed SET progress = ? WHERE window_id = ? AND arrangement_id = ?`, int(p), win, arr); err != nil {
			return err
		}
		return event(tx, change, win, "repaired")
	}
	return nil
}

func newWindow(tx *sql.Tx, boot, at time.Time, s arrangement.Seen) (int64, error) {
	res, err := tx.Exec(`INSERT INTO window(bundle, app, title, first_seen) VALUES (?, ?, ?, ?)`,
		s.Bundle, s.App, s.Title, at.UnixMilli())
	if err != nil {
		return 0, err
	}
	win, _ := res.LastInsertId()
	_, err = tx.Exec(`INSERT INTO binding(window_id, boot, wid, run) VALUES (?, ?, ?, ?)`, win, boot.Unix(), s.Binding, s.Run)
	return win, err
}

// place closes the window's current placement in the arrangement, if any,
// and opens p.
func place(tx *sql.Tx, change, arr, win int64, p arrangement.Placement) error {
	if _, err := tx.Exec(`UPDATE placement SET closed_by = ? WHERE window_id = ? AND arrangement_id = ? AND closed_by IS NULL`,
		change, win, arr); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO placement(window_id, arrangement_id, space, x, y, w, h, opened_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		win, arr, p.Space, p.Frame.X, p.Frame.Y, p.Frame.W, p.Frame.H, change)
	return err
}

func event(tx *sql.Tx, change, win int64, kind string) error {
	_, err := tx.Exec(`INSERT INTO event(change_id, window_id, kind) VALUES (?, ?, ?)`, change, win, kind)
	return err
}

// OweAll marks owed, with no repair progress, every window on screen (its
// id in this boot is among live) that has a placement in the arrangement. A
// window that is gone is not owed: nothing could release it. It returns how
// many windows are owed.
func (s *Store) OweAll(arr int64, boot, at time.Time, cause string, live []uint32) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO change(at, cause) VALUES (?, ?)`, at.UnixMilli(), cause)
	if err != nil {
		return 0, err
	}
	change, _ := res.LastInsertId()
	n := 0
	for _, wid := range live {
		res, err := tx.Exec(`
			INSERT INTO owed(window_id, arrangement_id, progress, since)
			SELECT p.window_id, p.arrangement_id, 0, ?1
			FROM placement p JOIN binding b ON b.window_id = p.window_id
			WHERE b.boot = ?2 AND b.wid = ?3 AND p.arrangement_id = ?4 AND p.closed_by IS NULL
			ON CONFLICT(window_id, arrangement_id) DO UPDATE SET progress = 0`, change, boot.Unix(), wid, arr)
		if err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	if _, err := tx.Exec(`UPDATE change SET note = ? WHERE id = ?`, fmt.Sprintf("%d windows owed", n), change); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// RefreshTitles records the latest title of each seen window bound in this
// boot. A title is identity for later matching, not a placement, so this
// writes no change.
func (s *Store) RefreshTitles(boot time.Time, seen []arrangement.Seen) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, w := range seen {
		if _, err := tx.Exec(`UPDATE window SET title = ?1
			WHERE id = (SELECT window_id FROM binding WHERE boot = ?2 AND wid = ?3) AND title <> ?1`,
			w.Title, boot.Unix(), w.Binding); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Disturbance is a period during which the screen was not intent.
type Disturbance struct {
	ID    int64
	Kind  string
	Began time.Time
	Ended time.Time // zero while open
}

func (s *Store) BeginDisturbance(kind string, at time.Time) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO disturbance(kind, began) VALUES (?, ?)`, kind, at.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) EndDisturbance(id int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE disturbance SET ended = ? WHERE id = ?`, at.UnixMilli(), id)
	return err
}

// OpenDisturbances returns disturbances with no end: after a restart, ones
// the agent was in the middle of.
func (s *Store) OpenDisturbances() ([]Disturbance, error) {
	rows, err := s.db.Query(`SELECT id, kind, began FROM disturbance WHERE ended IS NULL ORDER BY began`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Disturbance
	for rows.Next() {
		var d Disturbance
		var began int64
		if err := rows.Scan(&d.ID, &d.Kind, &began); err != nil {
			return nil, err
		}
		d.Began = time.UnixMilli(began)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Stored returns every window with a placement in the arrangement, with its
// latest binding: the candidates for binding fresh ids after a relaunch.
func (s *Store) Stored(arr int64) ([]arrangement.Stored, error) {
	rows, err := s.db.Query(`
		SELECT w.id, w.bundle, w.app, w.title, p.space, p.x, p.y, p.w, p.h,
		       COALESCE(b.boot, 0), COALESCE(b.wid, 0), COALESCE(b.run, 0)
		FROM placement p
		JOIN window w ON w.id = p.window_id
		LEFT JOIN binding b ON b.rowid = (SELECT MAX(rowid) FROM binding WHERE window_id = w.id)
		WHERE p.arrangement_id = ? AND p.closed_by IS NULL
		ORDER BY w.id`, arr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []arrangement.Stored
	for rows.Next() {
		var o arrangement.Stored
		if err := rows.Scan(&o.Window, &o.Bundle, &o.App, &o.Title, &o.Space, &o.Frame.X, &o.Frame.Y, &o.Frame.W, &o.Frame.H,
			&o.Boot, &o.Binding, &o.Run); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// KnownRuns returns the app runs that have bindings in this boot.
func (s *Store) KnownRuns(boot time.Time) (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT run FROM binding WHERE boot = ? AND run <> 0`, boot.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var run int64
		if err := rows.Scan(&run); err != nil {
			return nil, err
		}
		out[run] = true
	}
	return out, rows.Err()
}

// NoteRuns records the app run of bindings that lack one.
func (s *Store) NoteRuns(boot time.Time, seen []arrangement.Seen) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, w := range seen {
		if w.Run == 0 {
			continue
		}
		if _, err := tx.Exec(`UPDATE binding SET run = ? WHERE boot = ? AND wid = ? AND run = 0`, w.Run, boot.Unix(), w.Binding); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Bound pairs a stored window with the fresh window that recreates it.
type Bound struct {
	Window int64
	Seen   arrangement.Seen
}

// Bind gives stored windows their fresh ids and owes each its placement.
func (s *Store) Bind(arr int64, boot, at time.Time, cause string, bound []Bound) error {
	if len(bound) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO change(at, cause, note) VALUES (?, ?, ?)`, at.UnixMilli(), cause, fmt.Sprintf("%d windows bound", len(bound)))
	if err != nil {
		return err
	}
	change, _ := res.LastInsertId()
	for _, b := range bound {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO binding(window_id, boot, wid, run) VALUES (?, ?, ?, ?)`,
			b.Window, boot.Unix(), b.Seen.Binding, b.Seen.Run); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE window SET title = ?, app = ? WHERE id = ?`, b.Seen.Title, b.Seen.App, b.Window); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO owed(window_id, arrangement_id, progress, since) VALUES (?, ?, 0, ?)
			ON CONFLICT(window_id, arrangement_id) DO UPDATE SET progress = 0`, b.Window, arr, change); err != nil {
			return err
		}
		if err := event(tx, change, b.Window, "bound"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Event is what a change did to one window. From and To are the placement
// versions the change closed and opened for it, when it did.
type Event struct {
	Kind       string
	App, Title string
	From, To   *arrangement.Placement
}

// Entry is one change in the journal.
type Entry struct {
	ID     int64
	At     time.Time
	Cause  string
	Note   string
	Events []Event
}

// Journal returns the latest changes, oldest first.
func (s *Store) Journal(last int) ([]Entry, error) {
	rows, err := s.db.Query(`SELECT id, at, cause, note FROM change ORDER BY id DESC LIMIT ?`, last)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for rows.Next() {
		var e Entry
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Cause, &e.Note); err != nil {
			rows.Close()
			return nil, err
		}
		e.At = time.UnixMilli(at)
		out = append(out, e)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	for i := range out {
		if out[i].Events, err = s.events(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) events(change int64) ([]Event, error) {
	rows, err := s.db.Query(`SELECT e.kind, w.app, w.title, e.window_id FROM event e JOIN window w ON w.id = e.window_id
		WHERE e.change_id = ? ORDER BY e.id`, change)
	if err != nil {
		return nil, err
	}
	var out []Event
	var wins []int64
	for rows.Next() {
		var e Event
		var win int64
		if err := rows.Scan(&e.Kind, &e.App, &e.Title, &win); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
		wins = append(wins, win)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].From, err = s.version(`closed_by`, change, wins[i]); err != nil {
			return nil, err
		}
		if out[i].To, err = s.version(`opened_by`, change, wins[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// version returns the placement of a window that a change closed or opened
// (column is closed_by or opened_by), or nil.
func (s *Store) version(column string, change, win int64) (*arrangement.Placement, error) {
	var p arrangement.Placement
	err := s.db.QueryRow(`SELECT space, x, y, w, h FROM placement WHERE window_id = ? AND `+column+` = ?`, win, change).
		Scan(&p.Space, &p.Frame.X, &p.Frame.Y, &p.Frame.W, &p.Frame.H)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Disturbances returns the latest disturbances, oldest first. Ended is zero
// for one still open.
func (s *Store) Disturbances(last int) ([]Disturbance, error) {
	rows, err := s.db.Query(`SELECT id, kind, began, COALESCE(ended, 0) FROM disturbance ORDER BY id DESC LIMIT ?`, last)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Disturbance
	for rows.Next() {
		var d Disturbance
		var began, ended int64
		if err := rows.Scan(&d.ID, &d.Kind, &began, &ended); err != nil {
			return nil, err
		}
		d.Began = time.UnixMilli(began)
		if ended != 0 {
			d.Ended = time.UnixMilli(ended)
		}
		out = append(out, d)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// Undo reverts the placements a change opened. A window whose placement from
// that change is still current gets back the one the change closed, or none
// if it had none, and is owed so the agent puts it there. A window whose
// placement has changed again since is skipped. An undo that reverts nothing
// writes nothing.
func (s *Store) Undo(change int64, at time.Time) (reverted, skipped int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM change WHERE id = ?`, change).Scan(&exists); err != nil {
		return 0, 0, err
	}
	if exists == 0 {
		return 0, 0, fmt.Errorf("no change %d", change)
	}
	type version struct {
		id, win, arr int64
		current      bool
	}
	rows, err := tx.Query(`SELECT id, window_id, arrangement_id, closed_by IS NULL FROM placement WHERE opened_by = ?`, change)
	if err != nil {
		return 0, 0, err
	}
	var vs []version
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.id, &v.win, &v.arr, &v.current); err != nil {
			rows.Close()
			return 0, 0, err
		}
		vs = append(vs, v)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	var undo int64
	for _, v := range vs {
		if !v.current {
			skipped++
			continue
		}
		if undo == 0 {
			res, err := tx.Exec(`INSERT INTO change(at, cause, note) VALUES (?, 'undo', ?)`, at.UnixMilli(), fmt.Sprintf("of change %d", change))
			if err != nil {
				return 0, 0, err
			}
			undo, _ = res.LastInsertId()
		}
		if _, err := tx.Exec(`UPDATE placement SET closed_by = ? WHERE id = ?`, undo, v.id); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(`INSERT INTO placement(window_id, arrangement_id, space, x, y, w, h, opened_by)
			SELECT window_id, arrangement_id, space, x, y, w, h, ? FROM placement
			WHERE window_id = ? AND arrangement_id = ? AND closed_by = ?`, undo, v.win, v.arr, change); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(`INSERT INTO owed(window_id, arrangement_id, progress, since) VALUES (?, ?, 0, ?)
			ON CONFLICT(window_id, arrangement_id) DO UPDATE SET progress = 0`, v.win, v.arr, undo); err != nil {
			return 0, 0, err
		}
		if err := event(tx, undo, v.win, "undone"); err != nil {
			return 0, 0, err
		}
		reverted++
	}
	if reverted == 0 {
		return 0, skipped, nil
	}
	return reverted, skipped, tx.Commit()
}

// Prune deletes history older than the cutoff: windows last bound in a boot
// that began before it and not bound in the current boot, closed placement
// versions whose closing change predates it, the events of changes that old,
// changes nothing refers to any more, and disturbances that ended before it.
// It returns the number of closed placement versions deleted.
func (s *Store) Prune(cutoff, boot time.Time) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ms, sec := cutoff.UnixMilli(), cutoff.Unix()
	gone := `SELECT w.id FROM window w WHERE w.first_seen < ?1 AND NOT EXISTS
		(SELECT 1 FROM binding b WHERE b.window_id = w.id AND (b.boot = ?2 OR b.boot >= ?3))`
	for _, q := range []string{
		`DELETE FROM owed WHERE window_id IN (` + gone + `)`,
		`DELETE FROM event WHERE window_id IN (` + gone + `)`,
		`DELETE FROM placement WHERE window_id IN (` + gone + `)`,
		`DELETE FROM binding WHERE window_id IN (` + gone + `)`,
	} {
		if _, err := tx.Exec(q, ms, boot.Unix(), sec); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM window WHERE first_seen < ?1 AND NOT EXISTS (SELECT 1 FROM binding b WHERE b.window_id = window.id)`, ms); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM placement WHERE closed_by IN (SELECT id FROM change WHERE at < ?)`, ms)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.Exec(`DELETE FROM event WHERE change_id IN (SELECT id FROM change WHERE at < ?)`, ms); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM change WHERE at < ?
		AND id NOT IN (SELECT opened_by FROM placement)
		AND id NOT IN (SELECT closed_by FROM placement WHERE closed_by IS NOT NULL)
		AND id NOT IN (SELECT since FROM owed)
		AND id NOT IN (SELECT change_id FROM event)`, ms); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM disturbance WHERE ended IS NOT NULL AND ended < ?`, ms); err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}
