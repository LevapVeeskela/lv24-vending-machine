package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/store/sqlite"
)

// tailFixtureEvent is one journal event appended directly by a tail test.
type tailFixtureEvent struct {
	kind       string
	decisionID string
	payload    string
}

// appendTailEvents seeds the journal of dataDir with events in order.
func appendTailEvents(t *testing.T, dataDir string, events []tailFixtureEvent) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, config.DBPath(dataDir))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer func() { _ = st.Close() }()
	for i, ev := range events {
		if _, err := st.Append(ctx, domain.Event{
			Kind:        ev.kind,
			DecisionID:  ev.decisionID,
			Payload:     json.RawMessage(ev.payload),
			CodeVersion: "test",
		}); err != nil {
			t.Fatalf("append event %d (%s): %v", i, ev.kind, err)
		}
	}
}

// tailLines returns the non-empty output lines.
func tailLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestJournalTailOrderAndLimit(t *testing.T) {
	env, stdout, stderr, _, dataDir := initTestEnv(t)
	appendTailEvents(t, dataDir, []tailFixtureEvent{
		{kind: "gate.request", decisionID: "dec-1", payload: `{"signalId":"sig-1"}`},
		{kind: "decision.response", decisionID: "dec-1", payload: `{"score":0.7}`},
		{kind: "gate.verdict", decisionID: "dec-1", payload: `{"verdict":"ALLOW"}`},
		{kind: "outcome.labeled", decisionID: "dec-1", payload: `{"netReturn":"0.01"}`},
		{kind: "notify.sent", payload: `{"kind":"weekly","ok":true}`},
	})

	if code := runArgs(env, "journal", "tail", "--limit", "2", "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal tail --limit 2 = %d, stderr=%s", code, stderr.String())
	}
	lines := tailLines(stdout.String())
	if len(lines) != 2 {
		t.Fatalf("tail lines = %d (%q), want 2", len(lines), stdout.String())
	}
	if !strings.HasPrefix(lines[0], "4 ") || !strings.Contains(lines[0], "outcome.labeled") {
		t.Fatalf("first line = %q, want seq 4 outcome.labeled", lines[0])
	}
	if !strings.HasPrefix(lines[1], "5 ") || !strings.Contains(lines[1], "notify.sent") {
		t.Fatalf("second line = %q, want seq 5 notify.sent", lines[1])
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "tail", "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal tail default = %d, stderr=%s", code, stderr.String())
	}
	lines = tailLines(stdout.String())
	if len(lines) != 5 {
		t.Fatalf("default tail lines = %d, want 5", len(lines))
	}
	for i, line := range lines {
		want := string(rune('1' + i))
		if !strings.HasPrefix(line, want+" ") {
			t.Fatalf("line %d = %q, want ascending seq %s", i, line, want)
		}
	}
}

func TestJournalTailKindFilter(t *testing.T) {
	env, stdout, stderr, _, dataDir := initTestEnv(t)
	appendTailEvents(t, dataDir, []tailFixtureEvent{
		{kind: "gate.request", decisionID: "dec-1", payload: `{"signalId":"sig-1"}`},
		{kind: "notify.sent", payload: `{"kind":"provider_unavailable","ok":true}`},
		{kind: "gate.verdict", decisionID: "dec-1", payload: `{"verdict":"ALLOW"}`},
		{kind: "notify.sent", payload: `{"kind":"weekly","ok":false}`},
	})

	if code := runArgs(env, "journal", "tail", "--kind", "notify.sent", "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal tail --kind = %d, stderr=%s", code, stderr.String())
	}
	lines := tailLines(stdout.String())
	if len(lines) != 2 {
		t.Fatalf("kind-filtered lines = %d (%q), want 2", len(lines), stdout.String())
	}
	if !strings.HasPrefix(lines[0], "2 ") || !strings.HasPrefix(lines[1], "4 ") {
		t.Fatalf("kind-filtered seqs = %q, want 2 and 4", lines)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "tail", "--kind", "notify", "--data-dir", dataDir); code != exitUsage {
		t.Fatalf("journal tail unknown kind = %d, want %d, stderr=%s",
			code, exitUsage, stderr.String())
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("unknown kind output = %q, want empty", got)
	}
	for _, want := range []string{"invalid --kind", "gate.request", "notify.sent"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("unknown kind stderr misses %q: %q", want, stderr.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "tail", "--kind", "notify", "--json", "--data-dir", dataDir); code != exitUsage {
		t.Fatalf("journal tail unknown kind --json = %d, want %d, stderr=%s",
			code, exitUsage, stderr.String())
	}
	var envelope struct {
		Code    string `json:"code"`
		Details struct {
			Allowed []string `json:"allowed"`
		} `json:"details"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatalf("decode unknown kind JSON: %v (%q)", err, stderr.String())
	}
	if envelope.Code != codeUsage {
		t.Fatalf("unknown kind JSON code = %q, want %q", envelope.Code, codeUsage)
	}
	if len(envelope.Details.Allowed) != len(journalTailKinds) {
		t.Fatalf("unknown kind JSON allowed = %v, want all known kinds", envelope.Details.Allowed)
	}
}

func TestJournalTailEmptyJournal(t *testing.T) {
	env, stdout, stderr, _, dataDir := initTestEnv(t)

	if code := runArgs(env, "journal", "tail", "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal tail empty = %d, stderr=%s", code, stderr.String())
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("empty journal output = %q, want empty", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "tail", "--json", "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal tail empty --json = %d, stderr=%s", code, stderr.String())
	}
	var entries []journalTailEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("decode empty JSON: %v (%q)", err, stdout.String())
	}
	if len(entries) != 0 {
		t.Fatalf("empty JSON entries = %d, want 0", len(entries))
	}
}

func TestJournalTailJSON(t *testing.T) {
	env, stdout, stderr, _, dataDir := initTestEnv(t)
	appendTailEvents(t, dataDir, []tailFixtureEvent{
		{kind: "gate.request", decisionID: "dec-1", payload: `{"signalId":"sig-1","price":"100000"}`},
		{kind: "notify.sent", payload: `{"kind":"weekly","ok":false}`},
	})

	if code := runArgs(env, "journal", "tail", "--json", "--limit", "1", "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal tail --json = %d, stderr=%s", code, stderr.String())
	}
	var entries []struct {
		Seq        int64           `json:"seq"`
		TS         string          `json:"ts"`
		Kind       string          `json:"kind"`
		DecisionID string          `json:"decisionId"`
		Payload    json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("decode JSON: %v (%q)", err, stdout.String())
	}
	if len(entries) != 1 {
		t.Fatalf("JSON entries = %d, want 1", len(entries))
	}
	ev := entries[0]
	if ev.Seq != 2 || ev.Kind != "notify.sent" || ev.DecisionID != "" || ev.TS == "" {
		t.Fatalf("JSON entry = %+v, want seq 2 notify.sent", ev)
	}
	var payload struct {
		Kind string `json:"kind"`
		OK   bool   `json:"ok"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil || payload.Kind != "weekly" || payload.OK {
		t.Fatalf("JSON payload = %s, err=%v", ev.Payload, err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "tail", "--json", "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal tail --json all = %d, stderr=%s", code, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("decode all JSON: %v (%q)", err, stdout.String())
	}
	if len(entries) != 2 || entries[0].DecisionID != "dec-1" {
		t.Fatalf("JSON entries = %+v, want two with decisionId dec-1 on the first", entries)
	}
	if !strings.Contains(string(entries[0].Payload), `"price":"100000"`) {
		t.Fatalf("JSON payload = %s, want the journal price", entries[0].Payload)
	}
}

func TestJournalTailHumanOutputHasNoPayload(t *testing.T) {
	env, stdout, stderr, _, dataDir := initTestEnv(t)
	appendTailEvents(t, dataDir, []tailFixtureEvent{
		{kind: "gate.request", decisionID: "dec-visible", payload: `{"signalId":"sig-secret","price":"123456.789","state":{"rsi":55.5}}`},
	})

	if code := runArgs(env, "journal", "tail", "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal tail = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, forbidden := range []string{"signalId", "sig-secret", "price", "123456.789", "state", "rsi", "55.5"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("human output leaks payload field %q: %q", forbidden, out)
		}
	}
	if !strings.Contains(out, "gate.request") || !strings.Contains(out, "dec-visible") {
		t.Fatalf("human output = %q, want kind and decisionId", out)
	}
	lines := tailLines(out)
	if len(lines) != 1 {
		t.Fatalf("human lines = %d, want 1", len(lines))
	}
	if fields := strings.Fields(lines[0]); len(fields) != 4 {
		t.Fatalf("human line fields = %v, want seq ts kind decisionId", fields)
	}
}

func TestJournalTailDecisionIDPlaceholder(t *testing.T) {
	env, stdout, stderr, _, dataDir := initTestEnv(t)
	appendTailEvents(t, dataDir, []tailFixtureEvent{
		{kind: "notify.sent", payload: `{"ok":true}`},
	})

	if code := runArgs(env, "journal", "tail", "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal tail = %d, stderr=%s", code, stderr.String())
	}
	if fields := strings.Fields(stdout.String()); len(fields) != 4 || fields[3] != "-" {
		t.Fatalf("line = %q, want a '-' decisionId placeholder", stdout.String())
	}
}

func TestJournalTailInvalidLimit(t *testing.T) {
	env, _, stderr, _, dataDir := initTestEnv(t)
	for _, limit := range []string{"0", "-1", "1001", "abc"} {
		if code := runArgs(env, "journal", "tail", "--limit", limit, "--data-dir", dataDir); code != exitUsage {
			t.Fatalf("journal tail --limit %s = %d, want %d, stderr=%s",
				limit, code, exitUsage, stderr.String())
		}
		stderr.Reset()
	}
}

func TestJournalTailMissingDatabase(t *testing.T) {
	env, _, _, _, dataDir := initTestEnv(t)
	if code := runArgs(env, "journal", "tail", "--data-dir", filepath.Join(dataDir, "missing")); code != exitNotFound {
		t.Fatalf("journal tail missing db = %d, want %d", code, exitNotFound)
	}
}
