// Package sqlite is a SQLite-backed ports.Journal implementation.
//
// Events are stored append-only in a hash-chained table; Append assigns
// Seq/EventID/TS/PrevHash/Hash itself inside a single BEGIN IMMEDIATE
// transaction.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/journal"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

var _ ports.Journal = (*Store)(nil)

// Store is a file-backed journal.
type Store struct {
	db   *sql.DB
	path string
}

// Open creates the parent directory if needed, opens the database,
// applies the schema idempotently and enables WAL with a busy timeout.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("journal store: empty path")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("journal store: mkdir %s: %w", dir, err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(10000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("journal store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("journal store: ping %s: %w", path, err)
	}
	// Belt and suspenders: the _pragma DSN parameters above are honored by
	// modernc.org/sqlite, but enforce the two mandatory settings explicitly
	// so a platform-specific DSN quirk cannot silently disable them.
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("journal store: enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=10000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("journal store: set busy_timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("journal store: migrate %s: %w", path, err)
	}
	return &Store{db: db, path: path}, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("journal store: close: %w", err)
	}
	return nil
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Append stores ev assigning Seq, EventID (ULID), TS (UTC), PrevHash and
// Hash, and returns the stored event.
func (s *Store) Append(ctx context.Context, ev domain.Event) (domain.Event, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return domain.Event{}, fmt.Errorf("journal store: conn: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return domain.Event{}, fmt.Errorf("journal store: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
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
		ev.TS = time.Now().UTC()
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
	payload := sealed.Payload
	if len(payload) == 0 {
		payload = []byte("null")
	}
	if !json.Valid(payload) {
		return domain.Event{}, fmt.Errorf("journal store: invalid payload JSON at seq %d", sealed.Seq)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO events (seq, event_id, kind, ts, schema_version, payload,
			code_version, prev_hash, hash, run_id, decision_id, policy_hash, snapshot_hash)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sealed.Seq,
		sealed.EventID,
		sealed.Kind,
		sealed.TS.UTC().Format(time.RFC3339Nano),
		sealed.SchemaVersion,
		payload,
		sealed.CodeVersion,
		sealed.PrevHash,
		sealed.Hash,
		nullStr(sealed.RunID),
		nullStr(sealed.DecisionID),
		nullStr(sealed.PolicyHash),
		nullStr(sealed.SnapshotHash),
	); err != nil {
		return domain.Event{}, fmt.Errorf("journal store: insert seq %d: %w", sealed.Seq, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return domain.Event{}, fmt.Errorf("journal store: commit seq %d: %w", sealed.Seq, err)
	}
	committed = true
	sealed.Payload = payload
	return sealed, nil
}

// Query lists events with Seq > from, optionally filtered by kind, in
// ascending Seq order.
func (s *Store) Query(ctx context.Context, from int64, kind string) ([]domain.Event, error) {
	q := `SELECT seq, event_id, kind, ts, schema_version, payload, code_version,
		prev_hash, hash, run_id, decision_id, policy_hash, snapshot_hash
		FROM events WHERE seq > ?`
	args := []any{from}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY seq ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("journal store: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Event
	for rows.Next() {
		var ev domain.Event
		var ts string
		var payload []byte
		var runID, decisionID, policyHash, snapshotHash sql.NullString
		if err := rows.Scan(
			&ev.Seq, &ev.EventID, &ev.Kind, &ts, &ev.SchemaVersion, &payload,
			&ev.CodeVersion, &ev.PrevHash, &ev.Hash,
			&runID, &decisionID, &policyHash, &snapshotHash,
		); err != nil {
			return nil, fmt.Errorf("journal store: scan: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, fmt.Errorf("journal store: parse ts at seq %d: %w", ev.Seq, err)
		}
		ev.TS = parsed.UTC()
		ev.Payload = payload
		ev.RunID = runID.String
		ev.DecisionID = decisionID.String
		ev.PolicyHash = policyHash.String
		ev.SnapshotHash = snapshotHash.String
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("journal store: rows: %w", err)
	}
	return out, nil
}

// Verify reads the whole chain in order and checks its integrity.
func (s *Store) Verify(ctx context.Context) (domain.VerifyResult, error) {
	events, err := s.Query(ctx, 0, "")
	if err != nil {
		return domain.VerifyResult{}, err
	}
	return journal.Verify(events), nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
