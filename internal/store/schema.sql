-- One arrangement per display set (sorted display UUIDs, comma-joined).
CREATE TABLE IF NOT EXISTS arrangement (
  id          INTEGER PRIMARY KEY,
  display_set TEXT NOT NULL UNIQUE
);

-- The spaces an arrangement has had, by space key, with their position.
CREATE TABLE IF NOT EXISTS space (
  arrangement_id INTEGER NOT NULL REFERENCES arrangement(id),
  key            TEXT    NOT NULL,
  display        TEXT    NOT NULL,
  idx            INTEGER NOT NULL,
  PRIMARY KEY (arrangement_id, key)
);

-- A lasting window identity.
CREATE TABLE IF NOT EXISTS window (
  id         INTEGER PRIMARY KEY,
  bundle     TEXT    NOT NULL,
  app        TEXT    NOT NULL,
  title      TEXT    NOT NULL,
  first_seen INTEGER NOT NULL
);

-- The window-server id a window has in one boot (boot time, unix seconds).
CREATE TABLE IF NOT EXISTS binding (
  window_id INTEGER NOT NULL REFERENCES window(id),
  boot      INTEGER NOT NULL,
  wid       INTEGER NOT NULL,
  PRIMARY KEY (boot, wid)
);

-- One look or event that altered the record (at: unix milliseconds).
CREATE TABLE IF NOT EXISTS change (
  id    INTEGER PRIMARY KEY,
  at    INTEGER NOT NULL,
  cause TEXT    NOT NULL,
  note  TEXT    NOT NULL DEFAULT ''
);

-- What a change did to one window: adopted, owed, released, gave up, repaired.
CREATE TABLE IF NOT EXISTS event (
  id        INTEGER PRIMARY KEY,
  change_id INTEGER NOT NULL REFERENCES change(id),
  window_id INTEGER NOT NULL REFERENCES window(id),
  kind      TEXT    NOT NULL
);

-- System-versioned placements: a row is current while closed_by is NULL.
CREATE TABLE IF NOT EXISTS placement (
  id             INTEGER PRIMARY KEY,
  window_id      INTEGER NOT NULL REFERENCES window(id),
  arrangement_id INTEGER NOT NULL REFERENCES arrangement(id),
  space          TEXT    NOT NULL,
  x REAL NOT NULL, y REAL NOT NULL, w REAL NOT NULL, h REAL NOT NULL,
  opened_by      INTEGER NOT NULL REFERENCES change(id),
  closed_by      INTEGER REFERENCES change(id)
);
CREATE UNIQUE INDEX IF NOT EXISTS placement_current
  ON placement(window_id, arrangement_id) WHERE closed_by IS NULL;

-- Windows owed their placement, with how far the last repair got.
CREATE TABLE IF NOT EXISTS owed (
  window_id      INTEGER NOT NULL REFERENCES window(id),
  arrangement_id INTEGER NOT NULL REFERENCES arrangement(id),
  progress       INTEGER NOT NULL DEFAULT 0,
  since          INTEGER NOT NULL REFERENCES change(id),
  PRIMARY KEY (window_id, arrangement_id)
);

-- Periods during which the screen was not intent.
CREATE TABLE IF NOT EXISTS disturbance (
  id    INTEGER PRIMARY KEY,
  kind  TEXT    NOT NULL,
  began INTEGER NOT NULL,
  ended INTEGER
);
