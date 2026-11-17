// Package sqlite is a SQLite-backed ports.Journal implementation.
//
// Events are stored append-only in a hash-chained table; Append assigns
// Seq/EventID/TS/PrevHash/Hash itself inside a single BEGIN IMMEDIATE
// transaction.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/journal"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
	"modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrations lists the schema migrations in application order.
var migrations = []struct {
	version int
	file    string
}{
	{version: 1, file: "migrations/001_v1.sql"},
	{version: 2, file: "migrations/002_signal_id.sql"},
}

// latestVersion is the newest schema version.
const latestVersion = 2

// The connection setup is retried while another process holds the database
// lock; the whole window is one second, far below the 10 s busy timeout used
// for ordinary statements.
const (
	openAttempts   = 50
	openRetryDelay = 20 * time.Millisecond
)

// sqliteBusy is the primary SQLITE_BUSY result code; extended codes carry it
// in the low byte.
const sqliteBusy = 5

var _ ports.Journal = (*Store)(nil)

// Store is a file-backed journal.
type Store struct {
	db    *sql.DB
	path  string
	clock func() time.Time
}

// Option configures a Store.
type Option func(*Store)

// WithClock sets the clock used for the zero-TS fallback in Append.
// A nil clock is ignored; the default is time.Now.
func WithClock(clock func() time.Time) Option {
	return func(s *Store) {
		if clock != nil {
			s.clock = clock
		}
	}
}

// Open creates the parent directory if needed, opens the database,
// applies pending migrations idempotently and enables WAL with a busy
// timeout.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("journal store: empty path")
	}
	// The DSN is built as "file:"+path+"?params"; a path containing ? or #
	// would be parsed as DSN parameters instead of a file name.
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("journal store: invalid path: %q", path)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("journal store: mkdir %s: %w", dir, err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		// busy_timeout must be set before journal_mode: switching to WAL
		// takes a database lock, and a second process opening the same file
		// at the same time must wait for it instead of failing.
		"?_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(FULL)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cannot open the journal database at %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	// Two processes may open one database at the same time. Switching a fresh
	// file to WAL takes a lock that SQLite does not route through
	// busy_timeout, so the loser of that race sees SQLITE_BUSY: retry the
	// setup briefly instead of failing the second process.
	var setupErr error
retry:
	for attempt := 0; attempt < openAttempts; attempt++ {
		setupErr = setupConnection(ctx, db, path)
		if setupErr == nil || !isBusy(setupErr) {
			break
		}
		select {
		case <-ctx.Done():
			setupErr = ctx.Err()
			break retry
		case <-time.After(openRetryDelay):
		}
	}
	if setupErr != nil {
		_ = db.Close()
		return nil, setupErr
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureConstraints(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Best effort: restrict the data file on Unix. Modes are ignored on
	// Windows and a failed chmod must not fail Open.
	_ = os.Chmod(path, 0o600)
	s := &Store{db: db, path: path, clock: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	return s, nil
}

// appendOnlyTriggers holds the DDL that guarantees the events table is
// append-only. It runs on every Open (after migrations), so a database
// whose triggers were dropped externally regains them on next open.
// The bodies must match migrations/001_v1.sql.
const appendOnlyTriggers = `
CREATE TRIGGER IF NOT EXISTS events_no_update BEFORE UPDATE ON events
BEGIN
  SELECT RAISE(ABORT, 'events is append-only');
END;
CREATE TRIGGER IF NOT EXISTS events_no_delete BEFORE DELETE ON events
BEGIN
  SELECT RAISE(ABORT, 'events is append-only');
END;
`

// ensureConstraints re-creates the append-only triggers if missing.
func ensureConstraints(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, appendOnlyTriggers); err != nil {
		return fmt.Errorf("journal store: ensure append-only triggers: %w", err)
	}
	return nil
}

// setupConnection pings the database and enforces the mandatory pragmas.
func setupConnection(ctx context.Context, db *sql.DB, path string) error {
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("cannot use the journal database at %s: %w", path, err)
	}
	// Belt and suspenders: the _pragma DSN parameters above are honored by
	// modernc.org/sqlite, but enforce the mandatory settings explicitly
	// so a platform-specific DSN quirk cannot silently disable them.
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("journal store: enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=10000"); err != nil {
		return fmt.Errorf("journal store: set busy_timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		return fmt.Errorf("journal store: set synchronous: %w", err)
	}
	var syncMode string
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&syncMode); err != nil {
		return fmt.Errorf("journal store: read synchronous: %w", err)
	}
	if syncMode != "2" && !strings.EqualFold(syncMode, "FULL") {
		return fmt.Errorf("journal store: synchronous is %q, want FULL", syncMode)
	}
	return nil
}

// isBusy reports whether err is SQLite's BUSY result, which clears once the
// other process finishes with the database.
func isBusy(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) || se == nil {
		return false
	}
	return se.Code()&0xff == sqliteBusy
}

// migrate applies pending embedded migrations in one immediate transaction.
//
// BEGIN IMMEDIATE takes the write lock before the version is read, so two
// processes opening one database at the same time serialize here instead of
// both applying the same ALTER TABLE. DDL and the user_version bump commit
// together: an interrupted migration cannot leave a half-applied schema
// behind a stale version, and the DDL stays idempotent because the version
// only advances on success.
func migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("journal store: conn: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("journal store: begin migration: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var version int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("journal store: read user_version: %w", err)
	}
	for _, m := range migrations {
		if m.version <= version {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile(m.file)
		if err != nil {
			return fmt.Errorf("journal store: read migration %s: %w", m.file, err)
		}
		if _, err := conn.ExecContext(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("journal store: apply migration %d: %w", m.version, err)
		}
		version = m.version
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
		return fmt.Errorf("journal store: set user_version %d: %w", version, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("journal store: commit migrations: %w", err)
	}
	finished = true
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("journal store: close: %w", err)
	}
	return nil
}

// now returns the current time from the store clock.
func (s *Store) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock().UTC()
}

// Append stores ev assigning Seq, EventID (ULID), TS (UTC), PrevHash and
// Hash, and returns the stored event. The payload is canonicalized before
// sealing and the exact canonical bytes are stored; a payload that fails
// canonicalization is rejected and no row is written.
//
// A gate.request also records its signalId in the signal_id column, which
// carries the UNIQUE partial index. A duplicate signal id is reported as a
// domain.CodeConflict and writes nothing.
func (s *Store) Append(ctx context.Context, ev domain.Event) (domain.Event, error) {
	canon, err := journal.CanonicalizePayload(ev.Payload)
	if err != nil {
		return domain.Event{}, fmt.Errorf("journal store: canonicalize payload: %w", err)
	}
	ev.Payload = canon

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return domain.Event{}, fmt.Errorf("journal store: conn: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return domain.Event{}, fmt.Errorf("journal store: begin: %w", err)
	}
	// finished marks the manual transaction as committed or rolled back so
	// the deferred cleanup does not touch it again.
	finished := false
	defer func() {
		if !finished {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var maxSeq int64
	if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM events").Scan(&maxSeq); err != nil {
		return domain.Event{}, fmt.Errorf("journal store: max seq: %w", err)
	}
	prevHash := journal.ZeroHash
	if maxSeq > 0 {
		var h sql.NullString
		if err := conn.QueryRowContext(ctx, "SELECT hash FROM events WHERE seq = ?", maxSeq).Scan(&h); err != nil {
			return domain.Event{}, fmt.Errorf("journal store: last hash: %w", err)
		}
		prevHash = h.String
	}

	ev.Seq = maxSeq + 1
	if ev.EventID == "" {
		ev.EventID = ulid.Make().String()
	}
	if ev.TS.IsZero() {
		ev.TS = s.now()
	} else {
		ev.TS = ev.TS.UTC()
	}
	if ev.SchemaVersion == 0 {
		ev.SchemaVersion = 1
	}
	sealed, err := journal.Seal(ev, prevHash)
	if err != nil {
		return domain.Event{}, fmt.Errorf("journal store: seal: %w", err)
	}
	signalID := signalIDOf(sealed.Kind, sealed.Payload)
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO events (seq, event_id, kind, ts, schema_version, payload,
			code_version, prev_hash, hash, run_id, decision_id, policy_hash,
			snapshot_hash, signal_id)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sealed.Seq,
		sealed.EventID,
		sealed.Kind,
		sealed.TS.UTC().Format(time.RFC3339Nano),
		sealed.SchemaVersion,
		sealed.Payload,
		sealed.CodeVersion,
		sealed.PrevHash,
		sealed.Hash,
		nullStr(sealed.RunID),
		nullStr(sealed.DecisionID),
		nullStr(sealed.PolicyHash),
		nullStr(sealed.SnapshotHash),
		nullStr(signalID),
	); err != nil {
		// Roll back before reading the table: the failed insert may only have
		// hit the UNIQUE(signal_id) index because another process recorded
		// the same signal id first. The caller needs a typed conflict so it
		// can replay the winner instead of failing on a raw driver error.
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		finished = true
		if signalID != "" {
			if recorded, qerr := signalRecorded(ctx, conn, signalID); qerr == nil && recorded {
				return domain.Event{}, domain.WrapError(domain.CodeConflict,
					fmt.Sprintf("journal store: signalId %q is already recorded", signalID), err)
			}
		}
		return domain.Event{}, fmt.Errorf("journal store: insert seq %d: %w", sealed.Seq, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return domain.Event{}, fmt.Errorf("journal store: commit seq %d: %w", sealed.Seq, err)
	}
	finished = true
	return sealed, nil
}

// signalIDOf returns the signal id recorded in the signal_id column: the
// payload field of a gate.request, empty for every other kind.
//
// The column backs the UNIQUE index that makes one signal id resolve to one
// recorded request even when independent processes race. The hash chain does
// not cover the column, so it is derived from the sealed payload instead of
// adding an envelope field that older records would not have.
func signalIDOf(kind string, payload []byte) string {
	if kind != domain.EventGateRequest {
		return ""
	}
	var v struct {
		SignalID string `json:"signalId"`
	}
	if err := json.Unmarshal(payload, &v); err != nil {
		return ""
	}
	return strings.TrimSpace(v.SignalID)
}

// signalRecorded reports whether a gate.request with signalID is stored.
func signalRecorded(ctx context.Context, conn *sql.Conn, signalID string) (bool, error) {
	var one int
	err := conn.QueryRowContext(ctx,
		`SELECT 1 FROM events WHERE kind = ? AND signal_id = ? LIMIT 1`,
		domain.EventGateRequest, signalID).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	default:
		return true, nil
	}
}

// Query lists events with Seq > from, optionally filtered by kind, in
// ascending Seq order.
func (s *Store) Query(ctx context.Context, from int64, kind string) ([]domain.Event, error) {
	q, args := selectEvents(from, kind)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("journal store: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("journal store: rows: %w", err)
	}
	return out, nil
}

// Walk streams events with Seq > from in ascending Seq order, calling fn
// for each event without loading all rows into memory. An error returned
// by fn stops the walk and is returned.
func (s *Store) Walk(ctx context.Context, from int64, kind string, fn func(domain.Event) error) error {
	q, args := selectEvents(from, kind)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("journal store: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return err
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("journal store: rows: %w", err)
	}
	return nil
}

// Head returns the highest-seq event, or false when the store is empty.
func (s *Store) Head(ctx context.Context) (domain.Event, bool, error) {
	q := `SELECT seq, event_id, kind, ts, schema_version, payload, code_version,
		prev_hash, hash, run_id, decision_id, policy_hash, snapshot_hash
		FROM events ORDER BY seq DESC LIMIT 1`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return domain.Event{}, false, fmt.Errorf("journal store: head: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.Event{}, false, fmt.Errorf("journal store: rows: %w", err)
		}
		return domain.Event{}, false, nil
	}
	ev, err := scanEvent(rows)
	if err != nil {
		return domain.Event{}, false, err
	}
	if err := rows.Err(); err != nil {
		return domain.Event{}, false, fmt.Errorf("journal store: rows: %w", err)
	}
	return ev, true, nil
}

// Verify streams the whole chain in order and checks its integrity: seq
// continuity, prevHash linkage, byte-for-byte canonical payload equality,
// then the event hash.
func (s *Store) Verify(ctx context.Context) (domain.VerifyResult, error) {
	return journal.VerifyStream(ctx, func(ctx context.Context, fn func(domain.Event) error) error {
		rows, err := s.db.QueryContext(ctx, `SELECT seq, event_id, kind, ts, schema_version, payload, code_version,
			prev_hash, hash, run_id, decision_id, policy_hash, snapshot_hash
			FROM events ORDER BY seq ASC`)
		if err != nil {
			return fmt.Errorf("journal store: query: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			ev, err := scanEvent(rows)
			if err != nil {
				return err
			}
			if err := fn(ev); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("journal store: rows: %w", err)
		}
		return nil
	})
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// selectEvents builds the ascending-seq event listing query.
func selectEvents(from int64, kind string) (string, []any) {
	q := `SELECT seq, event_id, kind, ts, schema_version, payload, code_version,
		prev_hash, hash, run_id, decision_id, policy_hash, snapshot_hash
		FROM events WHERE seq > ?`
	args := []any{from}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY seq ASC`
	return q, args
}

// scanEvent scans one event row into a domain.Event.
func scanEvent(rows *sql.Rows) (domain.Event, error) {
	var ev domain.Event
	var ts string
	var payload []byte
	var runID, decisionID, policyHash, snapshotHash sql.NullString
	if err := rows.Scan(
		&ev.Seq, &ev.EventID, &ev.Kind, &ts, &ev.SchemaVersion, &payload,
		&ev.CodeVersion, &ev.PrevHash, &ev.Hash,
		&runID, &decisionID, &policyHash, &snapshotHash,
	); err != nil {
		return domain.Event{}, fmt.Errorf("journal store: scan: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return domain.Event{}, fmt.Errorf("journal store: parse ts at seq %d: %w", ev.Seq, err)
	}
	ev.TS = parsed.UTC()
	ev.Payload = payload
	ev.RunID = runID.String
	ev.DecisionID = decisionID.String
	ev.PolicyHash = policyHash.String
	ev.SnapshotHash = snapshotHash.String
	return ev, nil
}
