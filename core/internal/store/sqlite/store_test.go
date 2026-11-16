package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
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

func TestTamperAnyFieldBreaksVerify(t *testing.T) {
	// field -> SQL expression producing a different but valid-typed value.
	cases := map[string]string{
		"kind":         `'tampered'`,
		"payload":      `'{"evil":true}'`,
		"ts":           `'2020-01-01T00:00:00Z'`,
		"code_version": `'9.9.9'`,
		"prev_hash":    `'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`,
		"event_id":     `'01TAMPERED'`,
		"run_id":       `'tampered'`,
	}
	for field, expr := range cases {
		t.Run(field, func(t *testing.T) {
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
				fmt.Sprintf("UPDATE events SET %s = %s WHERE seq = 3", field, expr)); err != nil {
				t.Fatalf("tamper: %v", err)
			}
			res, err := s.Verify(ctx)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if res.OK || res.BrokenSeq != 3 {
				t.Fatalf("tampering %s: want break at seq 3, got %+v", field, res)
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
