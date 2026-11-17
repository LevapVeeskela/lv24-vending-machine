-- v2: one journaled gate.request per signalId, even across processes.
--
-- Append fills the column from the payload for kind = 'gate.request'. The
-- partial index is the cross-process idempotency guard: two processes on one
-- database cannot both record the same signal id, so only one of them calls
-- the paid decision provider.
--
-- Rows written by earlier versions keep signal_id NULL on purpose: they are
-- outside the index (and their signalId is still read from the payload), and
-- backfilling could fail on duplicates produced by the race this migration
-- fixes.
ALTER TABLE events ADD COLUMN signal_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_events_gate_request_signal_id
  ON events(signal_id) WHERE kind = 'gate.request' AND signal_id IS NOT NULL;
