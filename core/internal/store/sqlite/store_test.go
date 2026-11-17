package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/journal"
)

func openTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, ctx
}

// dropAppendOnlyTriggers removes the production append-only triggers so a
// test can rewrite history directly. The trigger guarantee itself is
// covered by TestAppendOnlyTriggersBlockWrites.
func dropAppendOnlyTriggers(t *testing.T, s *Store, ctx context.Context) {
	t.Helper()
	for _, trg := range []string{"events_no_update", "events_no_delete"} {
		if _, err := s.db.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+trg); err != nil {
			t.Fatalf("drop trigger %s: %v", trg, err)
		}
	}
}

func userVersion(t *testing.T, s *Store, ctx context.Context) int {
	t.Helper()
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	return v
}

func appendN(t *testing.T, s *Store, ctx context.Context, n int, kind string) []domain.Event {
	t.Helper()
	out := make([]domain.Event, 0, n)
	for i := 0; i < n; i++ {
		ev, err := s.Append(ctx, domain.Event{
			Kind:        kind,
			Payload:     json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)),
			CodeVersion: "test",
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		out = append(out, ev)
	}
	return out
}

func TestAppend1000AndVerify(t *testing.T) {
	s, ctx := openTestStore(t)
	appendN(t, s, ctx, 1000, "risk.verdict")
	res, err := s.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK || res.Count != 1000 {
		t.Fatalf("want OK count 1000, got %+v", res)
	}
}

func TestOpenRejectsQueryLikePath(t *testing.T) {
	ctx := context.Background()
	for _, p := range []string{
		filepath.Join(t.TempDir(), "journal.db?cache=shared"),
		filepath.Join(t.TempDir(), "dir#frag", "journal.db"),
	} {
		s, err := Open(ctx, p)
		if err == nil {
			_ = s.Close()
			t.Fatalf("path %q: want invalid path error, got nil", p)
		}
		if !strings.Contains(err.Error(), "journal store: invalid path:") {
			t.Fatalf("path %q: want invalid path error, got %v", p, err)
		}
	}
}

func TestOpenDirectoryPathMessage(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), dir)
	if err == nil {
		_ = s.Close()
		t.Fatal("Open(directory) succeeded")
	}
	got := err.Error()
	if !strings.Contains(got, "cannot open the journal database at") &&
		!strings.Contains(got, "cannot use the journal database at") {
		t.Fatalf("error %q misses the journal database wording", got)
	}
}

func TestTamperAnyFieldBreaksVerify(t *testing.T) {
	// All 13 event columns. wantBroken is the seq Verify reports: the
	// tampered row's own seq, except for the seq case itself: the row
	// moves to the end of the ascending order, so the first gap is
	// reported at seq 4 ("want 3 got 4").
	cases := []struct {
		field      string
		expr       string
		wantBroken int64
	}{
		{"kind", `'tampered'`, 3},
		{"payload", `'{"evil":true}'`, 3},
		{"ts", `'2020-01-01T00:00:00Z'`, 3},
		{"code_version", `'9.9.9'`, 3},
		{"prev_hash", `'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`, 3},
		{"event_id", `'01TAMPERED'`, 3},
		{"run_id", `'tampered'`, 3},
		{"schema_version", `2`, 3},
		{"hash", `'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'`, 3},
		{"decision_id", `'tampered'`, 3},
		{"policy_hash", `'tampered'`, 3},
		{"snapshot_hash", `'tampered'`, 3},
		{"seq", `seq + 100`, 4},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			s, ctx := openTestStore(t)
			for i := 0; i < 5; i++ {
				if _, err := s.Append(ctx, domain.Event{
					Kind:        "risk.verdict",
					Payload:     json.RawMessage(`{"i":1}`),
					CodeVersion: "test",
					RunID:       "run-1",
				}); err != nil {
					t.Fatalf("append: %v", err)
				}
			}
			dropAppendOnlyTriggers(t, s, ctx)
			if _, err := s.db.ExecContext(ctx,
				fmt.Sprintf("UPDATE events SET %s = %s WHERE seq = 3", tc.field, tc.expr)); err != nil {
				t.Fatalf("tamper: %v", err)
			}
			res, err := s.Verify(ctx)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if res.OK || res.BrokenSeq != tc.wantBroken {
				t.Fatalf("tampering %s: want break at seq %d, got %+v", tc.field, tc.wantBroken, res)
			}
		})
	}
}

func TestAppendOnlyTriggersBlockWrites(t *testing.T) {
	s, ctx := openTestStore(t)
	if _, err := s.Append(ctx, domain.Event{
		Kind:        "risk.verdict",
		Payload:     json.RawMessage(`{"i":1}`),
		CodeVersion: "test",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE events SET kind = 'tampered' WHERE seq = 1`); err == nil {
		t.Fatalf("UPDATE: want append-only error, got nil")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE: want append-only error, got %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE seq = 1`); err == nil {
		t.Fatalf("DELETE: want append-only error, got nil")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE: want append-only error, got %v", err)
	}
	events, err := s.Query(ctx, 0, "")
	if err != nil || len(events) != 1 {
		t.Fatalf("row must survive blocked writes: %v len=%d", err, len(events))
	}
	if res, err := s.Verify(ctx); err != nil || !res.OK || res.Count != 1 {
		t.Fatalf("verify after blocked writes: %+v err=%v", res, err)
	}
}

func TestVerifyRejectsDuplicateKeyPayload(t *testing.T) {
	s, ctx := openTestStore(t)
	appended := appendN(t, s, ctx, 2, "risk.verdict")
	last := appended[len(appended)-1]
	// INSERT is allowed (triggers block only UPDATE/DELETE); the
	// duplicate-key payload must break Verify at seq 3 even though the
	// row hash below is arbitrary.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO events (seq, event_id, kind, ts, schema_version, payload,
			code_version, prev_hash, hash) VALUES (?,?,?,?,?,?,?,?,?)`,
		3,
		"01TESTDUPKEY00000000000001",
		"risk.verdict",
		time.Now().UTC().Format(time.RFC3339Nano),
		1,
		[]byte(`{"a":1,"a":2}`),
		"test",
		last.Hash,
		strings.Repeat("c", 64),
	); err != nil {
		t.Fatalf("insert duplicate-key row: %v", err)
	}
	res, err := s.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK || res.BrokenSeq != 3 || !strings.Contains(res.Reason, "payload") {
		t.Fatalf("want payload break at seq 3, got %+v", res)
	}
}

func TestVerifyRejectsNonCanonicalPayload(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"whitespace", `{"b": 1, "a": 2}`},
		{"key order", `{"b":1,"a":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ctx := openTestStore(t)
			if _, err := s.Append(ctx, domain.Event{
				Kind:        "risk.verdict",
				Payload:     json.RawMessage(`{"a":2,"b":1}`),
				CodeVersion: "test",
			}); err != nil {
				t.Fatalf("append: %v", err)
			}
			dropAppendOnlyTriggers(t, s, ctx)
			if _, err := s.db.ExecContext(ctx, `UPDATE events SET payload = ? WHERE seq = 1`, tc.payload); err != nil {
				t.Fatalf("tamper: %v", err)
			}
			res, err := s.Verify(ctx)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if res.OK || res.BrokenSeq != 1 || !strings.Contains(res.Reason, "payload") {
				t.Fatalf("want payload break at seq 1, got %+v", res)
			}
		})
	}
}

func TestAppendCanonicalizesPayload(t *testing.T) {
	s, ctx := openTestStore(t)
	ev, err := s.Append(ctx, domain.Event{
		Kind:        "risk.verdict",
		Payload:     json.RawMessage(" \n" + `{"b":1,"a":2}` + " "),
		CodeVersion: "test",
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if string(ev.Payload) != `{"a":2,"b":1}` {
		t.Fatalf("returned payload not canonical: %s", ev.Payload)
	}
	got, err := s.Query(ctx, 0, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("query: %v len=%d", err, len(got))
	}
	if string(got[0].Payload) != `{"a":2,"b":1}` {
		t.Fatalf("stored payload not canonical: %s", got[0].Payload)
	}
	// Empty payload is stored as null.
	ev2, err := s.Append(ctx, domain.Event{Kind: "k", CodeVersion: "c"})
	if err != nil {
		t.Fatalf("append empty payload: %v", err)
	}
	if string(ev2.Payload) != `null` {
		t.Fatalf("empty payload must be stored as null, got %s", ev2.Payload)
	}
	if res, err := s.Verify(ctx); err != nil || !res.OK || res.Count != 2 {
		t.Fatalf("verify: %+v err=%v", res, err)
	}
}

func TestAppendRejectsInvalidPayload(t *testing.T) {
	s, ctx := openTestStore(t)
	for _, raw := range []string{
		`{"a":1,"a":2}`,
		`{"a":{"b":1,"b":2}}`,
		`{"a":1} {"b":2}`,
		`{"a":`,
	} {
		if _, err := s.Append(ctx, domain.Event{
			Kind:        "risk.verdict",
			Payload:     json.RawMessage(raw),
			CodeVersion: "test",
		}); err == nil {
			t.Fatalf("append %q: want error, got nil", raw)
		}
	}
	events, err := s.Query(ctx, 0, "")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("rejected appends must write no rows, got %d", len(events))
	}
}

func TestUserVersionFreshAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if v := userVersion(t, s, ctx); v != latestVersion {
		t.Fatalf("fresh user_version = %d, want %d", v, latestVersion)
	}
	if _, err := s.Append(ctx, domain.Event{Kind: "a", Payload: json.RawMessage(`{}`), CodeVersion: "t"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Second Open must be a no-op migration: version and data unchanged.
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if v := userVersion(t, s2, ctx); v != latestVersion {
		t.Fatalf("reopened user_version = %d, want %d", v, latestVersion)
	}
	events, err := s2.Query(ctx, 0, "")
	if err != nil || len(events) != 1 {
		t.Fatalf("after reopen: %v len=%d", err, len(events))
	}
	if res, err := s2.Verify(ctx); err != nil || !res.OK || res.Count != 1 {
		t.Fatalf("after reopen verify: %+v err=%v", res, err)
	}
}

func TestOpenLegacyDatabaseMigrates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	// Build a pre-migration database: events table, no triggers, no
	// index, user_version 0, two chained rows.
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	legacyDDL := `CREATE TABLE events (
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
	);`
	if _, err := raw.ExecContext(ctx, legacyDDL); err != nil {
		_ = raw.Close()
		t.Fatalf("legacy ddl: %v", err)
	}
	ts := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	prev := journal.ZeroHash
	var lastHash string
	for i := int64(1); i <= 2; i++ {
		ev := domain.Event{
			Seq:           i,
			EventID:       fmt.Sprintf("01LEGACY000000000000000%d", i),
			Kind:          "risk.verdict",
			TS:            ts.Add(time.Duration(i) * time.Second),
			SchemaVersion: 1,
			Payload:       json.RawMessage(`{"i":1}`),
			CodeVersion:   "test",
		}
		sealed, err := journal.Seal(ev, prev)
		if err != nil {
			_ = raw.Close()
			t.Fatalf("seal legacy %d: %v", i, err)
		}
		if _, err := raw.ExecContext(ctx,
			`INSERT INTO events (seq, event_id, kind, ts, schema_version, payload,
				code_version, prev_hash, hash) VALUES (?,?,?,?,?,?,?,?,?)`,
			sealed.Seq, sealed.EventID, sealed.Kind,
			sealed.TS.UTC().Format(time.RFC3339Nano), sealed.SchemaVersion,
			[]byte(`{"i":1}`), sealed.CodeVersion, sealed.PrevHash, sealed.Hash,
		); err != nil {
			_ = raw.Close()
			t.Fatalf("insert legacy %d: %v", i, err)
		}
		prev = sealed.Hash
		lastHash = sealed.Hash
	}
	var v0 int
	if err := raw.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v0); err != nil || v0 != 0 {
		_ = raw.Close()
		t.Fatalf("legacy user_version = %d, err %v; want 0", v0, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}
	// Open through the store: must migrate cleanly and preserve the chain.
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	defer func() { _ = s.Close() }()
	if v := userVersion(t, s, ctx); v != latestVersion {
		t.Fatalf("migrated user_version = %d, want %d", v, latestVersion)
	}
	events, err := s.Query(ctx, 0, "")
	if err != nil || len(events) != 2 {
		t.Fatalf("legacy rows after migrate: %v len=%d", err, len(events))
	}
	if res, err := s.Verify(ctx); err != nil || !res.OK || res.Count != 2 {
		t.Fatalf("legacy verify: %+v err=%v", res, err)
	}
	ev3, err := s.Append(ctx, domain.Event{Kind: "risk.verdict", Payload: json.RawMessage(`{"i":3}`), CodeVersion: "test"})
	if err != nil {
		t.Fatalf("append after migrate: %v", err)
	}
	if ev3.Seq != 3 || ev3.PrevHash != lastHash {
		t.Fatalf("chain did not continue: %+v", ev3)
	}
	// Triggers must exist after migration.
	if _, err := s.db.ExecContext(ctx, `UPDATE events SET kind = 'x' WHERE seq = 1`); err == nil {
		t.Fatalf("legacy UPDATE: want append-only error, got nil")
	}
}

func TestWithClock(t *testing.T) {
	ctx := context.Background()
	fixed := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	s, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ev, err := s.Append(ctx, domain.Event{Kind: "k", Payload: json.RawMessage(`{}`), CodeVersion: "c"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !ev.TS.Equal(fixed) {
		t.Fatalf("zero TS must use store clock: got %v want %v", ev.TS, fixed)
	}
	explicit := fixed.Add(-time.Hour)
	ev2, err := s.Append(ctx, domain.Event{Kind: "k", TS: explicit, Payload: json.RawMessage(`{}`), CodeVersion: "c"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !ev2.TS.Equal(explicit) {
		t.Fatalf("explicit TS must be kept: got %v want %v", ev2.TS, explicit)
	}
}

func TestReopenRestoresDroppedTriggers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.Append(ctx, domain.Event{Kind: "k", Payload: json.RawMessage(`{}`), CodeVersion: "c"}); err != nil {
		_ = s.Close()
		t.Fatalf("append: %v", err)
	}
	dropAppendOnlyTriggers(t, s, ctx)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if _, err := s2.db.ExecContext(ctx, `UPDATE events SET kind = 'x' WHERE seq = 1`); err == nil {
		t.Fatalf("UPDATE after reopen: want append-only error, got nil")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE after reopen: want append-only error, got %v", err)
	}
	if _, err := s2.db.ExecContext(ctx, `DELETE FROM events WHERE seq = 1`); err == nil {
		t.Fatalf("DELETE after reopen: want append-only error, got nil")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE after reopen: want append-only error, got %v", err)
	}
	if res, err := s2.Verify(ctx); err != nil || !res.OK || res.Count != 1 {
		t.Fatalf("verify after reopen: %+v err=%v", res, err)
	}
}

func TestDbFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "journal.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = s.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("db file mode = %o, want 600", perm)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("data dir mode = %o, want no group/other access", perm)
	}
}

func TestHead(t *testing.T) {
	s, ctx := openTestStore(t)
	if _, found, err := s.Head(ctx); err != nil || found {
		t.Fatalf("empty head: found=%v err=%v", found, err)
	}
	appended := appendN(t, s, ctx, 3, "risk.verdict")
	head, found, err := s.Head(ctx)
	if err != nil || !found {
		t.Fatalf("head: found=%v err=%v", found, err)
	}
	last := appended[len(appended)-1]
	if head.Seq != 3 || head.Hash != last.Hash || !reflect.DeepEqual(head, last) {
		t.Fatalf("head mismatch:\n%+v\n%+v", head, last)
	}
}

func TestConcurrentAppendNoDupSeq(t *testing.T) {
	s, ctx := openTestStore(t)
	const workers = 8
	const perWorker = 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				_, err := s.Append(ctx, domain.Event{
					Kind:        "risk.verdict",
					Payload:     json.RawMessage(fmt.Sprintf(`{"w":%d,"i":%d}`, w, i)),
					CodeVersion: "test",
				})
				if err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("append: %v", err)
	}
	events, err := s.Query(ctx, 0, "")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(events) != workers*perWorker {
		t.Fatalf("want %d events, got %d", workers*perWorker, len(events))
	}
	seen := make(map[int64]bool, len(events))
	for i, ev := range events {
		if want := int64(i + 1); ev.Seq != want {
			t.Fatalf("position %d: seq=%d want %d", i, ev.Seq, want)
		}
		if seen[ev.Seq] {
			t.Fatalf("duplicate seq %d", ev.Seq)
		}
		seen[ev.Seq] = true
	}
	res, err := s.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK || res.Count != workers*perWorker {
		t.Fatalf("want OK count %d, got %+v", workers*perWorker, res)
	}
}

func TestQueryFiltersAndSorts(t *testing.T) {
	s, ctx := openTestStore(t)
	for i := 0; i < 6; i++ {
		kind := "a"
		if i%2 == 0 {
			kind = "b"
		}
		if _, err := s.Append(ctx, domain.Event{
			Kind:        kind,
			Payload:     json.RawMessage(`{}`),
			CodeVersion: "test",
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	all, err := s.Query(ctx, 0, "")
	if err != nil || len(all) != 6 {
		t.Fatalf("query all: %v len=%d", err, len(all))
	}
	b, err := s.Query(ctx, 0, "b")
	if err != nil || len(b) != 3 {
		t.Fatalf("query kind=b: %v len=%d", err, len(b))
	}
	for i, ev := range b {
		if ev.Kind != "b" {
			t.Fatalf("query kind=b returned %q", ev.Kind)
		}
		if i > 0 && b[i-1].Seq >= ev.Seq {
			t.Fatalf("not ascending: %+v", b)
		}
	}
	after, err := s.Query(ctx, 4, "")
	if err != nil || len(after) != 2 || after[0].Seq != 5 || after[1].Seq != 6 {
		t.Fatalf("query from=4: %v %+v", err, after)
	}
}

func TestReopenSeesEvents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "journal.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.Append(ctx, domain.Event{Kind: "a", Payload: json.RawMessage(`{}`), CodeVersion: "t"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	events, err := s2.Query(ctx, 0, "")
	if err != nil || len(events) != 1 {
		t.Fatalf("after reopen: %v len=%d", err, len(events))
	}
	res, err := s2.Verify(ctx)
	if err != nil || !res.OK || res.Count != 1 {
		t.Fatalf("after reopen verify: %v %+v", err, res)
	}
}

func TestAppendDefaultsAndRoundTrip(t *testing.T) {
	s, ctx := openTestStore(t)
	before := time.Now().Add(-time.Second)
	ev, err := s.Append(ctx, domain.Event{Kind: "k", Payload: json.RawMessage(`{"a":1}`), CodeVersion: "c"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if ev.Seq != 1 || ev.EventID == "" || ev.SchemaVersion != 1 {
		t.Fatalf("defaults not assigned: %+v", ev)
	}
	if ev.TS.Before(before) || ev.TS.Location() != time.UTC {
		t.Fatalf("bad TS: %v", ev.TS)
	}
	if ev.PrevHash == "" || ev.Hash == "" {
		t.Fatalf("chain fields missing: %+v", ev)
	}
	got, err := s.Query(ctx, 0, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("query: %v len=%d", err, len(got))
	}
	if !reflect.DeepEqual(got[0], ev) {
		t.Fatalf("round trip mismatch:\n%+v\n%+v", got[0], ev)
	}
}
