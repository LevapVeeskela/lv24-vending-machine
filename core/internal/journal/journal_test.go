package journal

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

func testEvent(seq int64, ts time.Time) domain.Event {
	return domain.Event{
		Seq:           seq,
		EventID:       "01JTEST" + string(rune('0'+seq)),
		Kind:          "risk.verdict",
		TS:            ts,
		SchemaVersion: 1,
		Payload:       json.RawMessage(`{"n":1,"s":"x"}`),
		CodeVersion:   "0.1.0",
		RunID:         "run-1",
		DecisionID:    "dec-1",
		PolicyHash:    "ph-1",
		SnapshotHash:  "sh-1",
	}
}

func sealChain(t *testing.T, n int) []domain.Event {
	t.Helper()
	ts := time.Date(2026, 11, 16, 10, 0, 0, 123456789, time.UTC)
	prev := ZeroHash
	out := make([]domain.Event, 0, n)
	for i := 1; i <= n; i++ {
		ev, err := Seal(testEvent(int64(i), ts.Add(time.Duration(i)*time.Second)), prev)
		if err != nil {
			t.Fatalf("seal %d: %v", i, err)
		}
		prev = ev.Hash
		out = append(out, ev)
	}
	return out
}

func TestChainOfThreeIsValid(t *testing.T) {
	events := sealChain(t, 3)
	res := Verify(events)
	if !res.OK || res.Count != 3 {
		t.Fatalf("want OK count 3, got %+v", res)
	}
	if len(ZeroHash) != 64 || strings.Trim(ZeroHash, "0") != "" {
		t.Fatalf("ZeroHash must be 64 zeros, got %q", ZeroHash)
	}
	if events[0].PrevHash != ZeroHash {
		t.Fatalf("first prevHash = %q, want ZeroHash", events[0].PrevHash)
	}
	for _, ev := range events {
		if len(ev.Hash) != 64 {
			t.Fatalf("hash %q is not 64 hex chars", ev.Hash)
		}
	}
}

func TestEmptyJournal(t *testing.T) {
	if res := Verify(nil); !res.OK || res.Count != 0 {
		t.Fatalf("want OK count 0, got %+v", res)
	}
	if res := Verify([]domain.Event{}); !res.OK || res.Count != 0 {
		t.Fatalf("want OK count 0, got %+v", res)
	}
}

func TestSeqGap(t *testing.T) {
	events := sealChain(t, 2)
	ts := time.Date(2026, 11, 16, 10, 0, 4, 0, time.UTC)
	last, err := Seal(testEvent(4, ts), events[1].Hash)
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, last)
	res := Verify(events)
	if res.OK || res.BrokenSeq != 4 || !strings.Contains(res.Reason, "seq gap: want 3 got 4") {
		t.Fatalf("want seq gap at 4, got %+v", res)
	}
}

func TestTamperEveryFieldBreaksVerify(t *testing.T) {
	ts := time.Date(2026, 11, 16, 10, 0, 2, 123456789, time.UTC)
	cases := map[string]func(ev *domain.Event){
		"seq":          func(ev *domain.Event) { ev.Seq = 999 },
		"eventId":      func(ev *domain.Event) { ev.EventID = "tampered" },
		"kind":         func(ev *domain.Event) { ev.Kind = "tampered" },
		"ts":           func(ev *domain.Event) { ev.TS = ts.Add(time.Hour) },
		"payload":      func(ev *domain.Event) { ev.Payload = json.RawMessage(`{"n":2}`) },
		"codeVersion":  func(ev *domain.Event) { ev.CodeVersion = "9.9.9" },
		"prevHash":     func(ev *domain.Event) { ev.PrevHash = strings.Repeat("a", 64) },
		"hash":         func(ev *domain.Event) { ev.Hash = strings.Repeat("b", 64) },
		"runId":        func(ev *domain.Event) { ev.RunID = "tampered" },
		"decisionId":   func(ev *domain.Event) { ev.DecisionID = "tampered" },
		"policyHash":   func(ev *domain.Event) { ev.PolicyHash = "tampered" },
		"snapshotHash": func(ev *domain.Event) { ev.SnapshotHash = "tampered" },
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			events := sealChain(t, 3)
			tampered := events[1]
			tamper(&tampered)
			events[1] = tampered
			res := Verify(events)
			if res.OK {
				t.Fatalf("tampering %s kept chain valid", name)
			}
			if res.BrokenSeq != tampered.Seq {
				t.Fatalf("tampering %s: BrokenSeq=%d want %d (%+v)", name, res.BrokenSeq, tampered.Seq, res)
			}
			if res.Count != 1 {
				t.Fatalf("tampering %s: Count=%d want 1 (%+v)", name, res.Count, res)
			}
			if res.Reason == "" {
				t.Fatalf("tampering %s: empty reason", name)
			}
		})
	}
}

func TestCanonicalKeysSortedNoSpaces(t *testing.T) {
	ts := time.Date(2026, 11, 16, 10, 0, 1, 0, time.UTC)
	ev := testEvent(1, ts)
	ev.PrevHash = ZeroHash
	b, err := CanonicalJSON(ev)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(string(b)))
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("canonical is not a JSON object: %v", err)
	}
	if _, ok := m["hash"]; ok {
		t.Fatalf("canonical must not contain hash: %s", b)
	}
	if strings.Contains(string(b), " ") {
		t.Fatalf("canonical must not contain spaces: %s", b)
	}
	// Keys must appear in sorted order.
	wantOrder := []string{"codeVersion", "decisionId", "eventId", "kind", "payload", "policyHash", "prevHash", "runId", "schemaVersion", "seq", "snapshotHash", "ts"}
	last := -1
	for _, k := range wantOrder {
		i := strings.Index(string(b), `"`+k+`"`)
		if i < 0 {
			t.Fatalf("canonical misses key %s: %s", k, b)
		}
		if i < last {
			t.Fatalf("keys not sorted: %s", b)
		}
		last = i
	}
	// Optional fields omitted when empty.
	ev.RunID, ev.DecisionID, ev.PolicyHash, ev.SnapshotHash = "", "", "", ""
	b2, err := CanonicalJSON(ev)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"runId", "decisionId", "policyHash", "snapshotHash"} {
		if strings.Contains(string(b2), `"`+k+`"`) {
			t.Fatalf("empty optional %s must be omitted: %s", k, b2)
		}
	}
}
