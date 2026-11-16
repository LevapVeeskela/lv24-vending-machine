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
