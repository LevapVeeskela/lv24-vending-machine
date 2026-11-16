package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
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
