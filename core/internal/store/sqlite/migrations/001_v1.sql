CREATE TABLE IF NOT EXISTS events (
  seq            INTEGER PRIMARY KEY,
  event_id       TEXT NOT NULL UNIQUE,
  kind           TEXT NOT NULL,
  ts             TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  payload        BLOB NOT NULL,
  code_version   TEXT NOT NULL,
  prev_hash      TEXT NOT NULL,
  hash           TEXT NOT NULL,
  run_id         TEXT,
  decision_id    TEXT,
  policy_hash    TEXT,
  snapshot_hash  TEXT
);
CREATE INDEX IF NOT EXISTS idx_events_kind_seq ON events(kind, seq);
DROP TRIGGER IF EXISTS events_no_update;
CREATE TRIGGER events_no_update BEFORE UPDATE ON events
BEGIN
  SELECT RAISE(ABORT, 'events is append-only');
END;
DROP TRIGGER IF EXISTS events_no_delete;
CREATE TRIGGER events_no_delete BEFORE DELETE ON events
BEGIN
  SELECT RAISE(ABORT, 'events is append-only');
END;
