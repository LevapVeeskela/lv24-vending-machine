package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/gateflow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/store/sqlite"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// TestGateTestReplayPrintsHint: repeating a signalId replays the recorded
// verdict, says so on stdout and writes no new events.
func TestGateTestReplayPrintsHint(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	args := []string{"gate", "test",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", testdataRel("gate-request.json"),
		"--fixture", testdataRel("decision-response.json")}
	if code := runArgs(env, args...); code != 0 {
		t.Fatalf("first gate test = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "replayed: true") {
		t.Fatalf("fresh run must not say replayed: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, args...); code != 0 {
		t.Fatalf("replayed gate test = %d, stderr=%s", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "replayed: true") || !strings.Contains(got, "hint:") {
		t.Fatalf("replay stdout misses the replay notice: %q", got)
	}
	if got := stdout.String(); !strings.Contains(got, "ALLOW") {
		t.Fatalf("replay stdout misses the verdict: %q", got)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("replay stderr = %q, want empty (exit 0)", got)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "verify",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal verify = %d, stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "journal ok: 3 events" {
		t.Fatalf("journal stdout = %q, want no events from the replay", got)
	}
}

// TestGateTestReplayChangedFixtureConflicts: a replay must be the recorded
// decision; another fixture under the same signalId is an error, not a silent
// old verdict.
func TestGateTestReplayChangedFixtureConflicts(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	base := []string{"gate", "test", "--config", cfgPath, "--data-dir", dataDir,
		"--request", testdataRel("gate-request.json")}
	first := append(append([]string{}, base...), "--fixture", testdataRel("decision-response.json"))
	if code := runArgs(env, first...); code != 0 {
		t.Fatalf("first gate test = %d, stderr=%s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	changed := append(append([]string{}, base...), "--fixture", testdataRel("decision-response-reject.json"))
	if code := runArgs(env, changed...); code != exitConflict {
		t.Fatalf("changed-fixture replay = %d, want %d, stderr=%s", code, exitConflict, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "fixture differs from journaled decision; change signalId or use another --data-dir") {
		t.Fatalf("stderr misses the fixture mismatch message: %q", got)
	}
	// Nothing was written or re-decided.
	if code := runArgs(env, "journal", "verify",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal verify = %d, stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "journal ok: 3 events" {
		t.Fatalf("journal stdout = %q, want 3 events", got)
	}
}

// TestGateTestAcceptsBOM: Windows editors prepend a UTF-8 BOM; the request and
// fixture are still parsed.
func TestGateTestAcceptsBOM(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	dir := t.TempDir()
	req := withBOM(t, dir, "request.json", testdataRel("gate-request.json"))
	fix := withBOM(t, dir, "fixture.json", testdataRel("decision-response.json"))
	if code := runArgs(env, "gate", "test",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", req, "--fixture", fix); code != 0 {
		t.Fatalf("gate test with BOM = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "ALLOW") {
		t.Fatalf("BOM run stdout = %q, want ALLOW", got)
	}
}

// withBOM copies src to dir/name with a UTF-8 BOM in front.
func withBOM(t *testing.T, dir, name, src string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, raw...), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// raceJournal holds both callers after their first gate.request walk, so both
// reach Append with an empty journal like two processes starting together.
type raceJournal struct {
	ports.Journal
	barrier *sync.WaitGroup
	waited  atomic.Bool
}

func (j *raceJournal) Walk(ctx context.Context, from int64, kind string, fn func(domain.Event) error) error {
	err := j.Journal.Walk(ctx, from, kind, fn)
	if err == nil && kind == domain.EventGateRequest && j.waited.CompareAndSwap(false, true) {
		j.barrier.Done()
		j.barrier.Wait()
	}
	return err
}

// raceDecision counts provider calls and can hold them.
type raceDecision struct {
	calls   atomic.Int32
	release <-chan struct{}
	resp    domain.DecisionResponse
}

func (d *raceDecision) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "race-decision", Name: "race", Kind: ports.KindDecision, Version: "v0"}
}

func (d *raceDecision) Decide(ctx context.Context, _ domain.DecisionRequest) (domain.DecisionResponse, error) {
	d.calls.Add(1)
	if d.release != nil {
		select {
		case <-d.release:
		case <-ctx.Done():
			return domain.DecisionResponse{}, ctx.Err()
		}
	}
	return d.resp, nil
}

func raceDeps(prefix string) ports.Deps {
	var mu sync.Mutex
	next := 0
	return ports.Deps{
		Clock: func() time.Time { return time.Now().UTC() },
		NewID: func() string {
			mu.Lock()
			defer mu.Unlock()
			next++
			return prefix + string(rune('0'+next))
		},
	}
}

func raceRequest() domain.GateRequest {
	return domain.GateRequest{
		SignalID:   "sig-race-1",
		Instrument: domain.Instrument{Venue: "binance", Symbol: "BTCUSDT"},
		Side:       domain.SideBuy,
		IntentKind: domain.IntentEntry,
		Timeframe:  "1h",
		TS:         time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC),
		Price:      domain.MustParseDecimal("67432.50"),
		Source:     "test",
		State:      map[string]float64{"rsi": 55.5},
	}
}

// TestSignalRaceAcrossStoresPaysOnce proves the guard with real database
// files: two processes (two stores over one database) race on one signalId,
// exactly one records the request, calls the provider and writes the verdict.
func TestSignalRaceAcrossStoresPaysOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	st1, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}
	t.Cleanup(func() { _ = st1.Close() })
	st2, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	fixture, _, code, err := readDecisionFixture(testdataRel("decision-response.json"), "test-model")
	if err != nil || code != exitOK {
		t.Fatalf("read fixture = %v (code %d)", err, code)
	}
	release := make(chan struct{})
	prov := &raceDecision{release: release, resp: fixture}
	cfg := gateflow.Config{Model: "test-model", Policy: domain.DefaultGatePolicy(), CodeVersion: "v-test"}

	var barrier sync.WaitGroup
	barrier.Add(2)
	f1, err := gateflow.New(&raceJournal{Journal: st1, barrier: &barrier}, prov, raceDeps("dec-a-"), cfg)
	if err != nil {
		t.Fatalf("New flow 1: %v", err)
	}
	f2, err := gateflow.New(&raceJournal{Journal: st2, barrier: &barrier}, prov, raceDeps("dec-b-"), cfg)
	if err != nil {
		t.Fatalf("New flow 2: %v", err)
	}

	type out struct {
		res gateflow.Result
		err error
	}
	done := make(chan out, 2)
	go func() {
		res, err := f1.Evaluate(ctx, raceRequest())
		done <- out{res, err}
	}()
	go func() {
		res, err := f2.Evaluate(ctx, raceRequest())
		done <- out{res, err}
	}()

	// The winner is inside the provider, so only the loser can return.
	loser := <-done
	if domain.CodeOf(loser.err) != domain.CodeConflict {
		t.Fatalf("loser err = %v, want conflict", loser.err)
	}
	if got := prov.calls.Load(); got != 1 {
		t.Fatalf("provider calls before release = %d, want 1", got)
	}
	close(release)
	winner := <-done
	if winner.err != nil {
		t.Fatalf("winner Evaluate() = %v", winner.err)
	}
	if winner.res.Verdict.Verdict != domain.VerdictAllow || winner.res.Reused {
		t.Fatalf("winner result = %+v, want a fresh ALLOW", winner.res)
	}
	if got := prov.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	for kind, want := range map[string]int{
		domain.EventGateRequest:      1,
		domain.EventDecisionResponse: 1,
		domain.EventGateVerdict:      1,
	} {
		evs, err := st1.Query(ctx, 0, kind)
		if err != nil {
			t.Fatalf("query %s: %v", kind, err)
		}
		if len(evs) != want {
			t.Fatalf("%s events = %d, want %d", kind, len(evs), want)
		}
	}
	if res, err := st1.Verify(ctx); err != nil || !res.OK || res.Count != 3 {
		t.Fatalf("verify = %+v err=%v", res, err)
	}

	// A later caller on the same database replays instead of paying again.
	replay, err := f2.Evaluate(ctx, raceRequest())
	if err != nil {
		t.Fatalf("replay Evaluate() = %v", err)
	}
	if !replay.Reused || replay.Journaled == nil {
		t.Fatalf("replay = %+v, want Reused with the journaled response", replay)
	}
	if got := prov.calls.Load(); got != 1 {
		t.Fatalf("provider calls after replay = %d, want 1", got)
	}
}
