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
	return &Store{db}, nil
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
	_, err = tx.Exec(`INSERT INTO binding(window_id, boot, wid) VALUES (?, ?, ?)`, win, boot.Unix(), s.Binding)
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
