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

// OweAll marks every window bound in this boot that has a placement in the
// arrangement as owed, with no repair progress. It returns how many are owed.
func (s *Store) OweAll(arr int64, boot, at time.Time, cause string) (int, error) {
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
	if _, err := tx.Exec(`UPDATE owed SET progress = 0 WHERE arrangement_id = ?`, arr); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO owed(window_id, arrangement_id, progress, since)
		SELECT p.window_id, p.arrangement_id, 0, ?1
		FROM placement p JOIN binding b ON b.window_id = p.window_id AND b.boot = ?2
		WHERE p.arrangement_id = ?3 AND p.closed_by IS NULL`, change, boot.Unix(), arr); err != nil {
		return 0, err
	}
	var n int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM owed o JOIN binding b ON b.window_id = o.window_id AND b.boot = ?1
		WHERE o.arrangement_id = ?2`, boot.Unix(), arr).Scan(&n); err != nil {
		return 0, err
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
