package gateflow

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// blockingArena is a ports.Decision that signals when it starts and blocks
// until release is closed. A nil release makes it answer immediately.
type blockingArena struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	once    sync.Once
	resp    domain.DecisionResponse
	err     error
}

func (b *blockingArena) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "test-arena", Name: "arena", Kind: ports.KindDecision, Version: "v0"}
}

func (b *blockingArena) Decide(ctx context.Context, _ domain.DecisionRequest) (domain.DecisionResponse, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	b.once.Do(func() { close(b.started) })
	if b.release != nil {
		select {
		case <-b.release:
		case <-ctx.Done():
			return domain.DecisionResponse{}, ctx.Err()
		}
	}
	if b.err != nil {
		return domain.DecisionResponse{}, b.err
	}
	return b.resp, nil
}

func (b *blockingArena) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func arenaResp() domain.DecisionResponse {
	return domain.DecisionResponse{
		Answers: map[string]domain.Answer{
			"data_quality":     {Type: domain.QuestionChoice, Choice: "insufficient", Probabilities: map[string]float64{"ok": 0.2, "insufficient": 0.8}},
			"signal_alignment": {Type: domain.QuestionChoice, Choice: "conflict", Probabilities: map[string]float64{"aligned": 0.1, "neutral": 0.2, "conflict": 0.7}},
			"market_regime":    {Type: domain.QuestionChoice, Choice: "adverse", Probabilities: map[string]float64{"favorable": 0.1, "neutral": 0.2, "adverse": 0.7}},
			"risk_check":       {Type: domain.QuestionChoice, Choice: "block", Probabilities: map[string]float64{"pass": 0.05, "block": 0.95}},
		},
		Model:          "arena-model-v2",
		RequestedModel: "arena-model",
	}
}

func arenaConfig(provider ports.Decision) *ArenaConfig {
	return &ArenaConfig{Provider: provider, Model: "arena-model"}
}

// waitFor polls cond until it reports true or the test deadline passes.
func waitFor(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s: condition not met within deadline", msg)
}

func arenaEvents(t *testing.T, jr *memJournal) []domain.Event {
	t.Helper()
	evs, err := jr.Query(context.Background(), 0, domain.EventArenaDecision)
	if err != nil {
		t.Fatalf("Query(arena) = %v", err)
	}
	return evs
}

func TestArenaRunsDetachedAndRecordsOneEvent(t *testing.T) {
	jr := &memJournal{}
	primary := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		return allowResp(), nil
	}}
	arena := &blockingArena{started: make(chan struct{}), release: make(chan struct{}), resp: arenaResp()}
	cfg := testConfig()
	cfg.Arena = arenaConfig(arena)
	f, err := New(jr, primary, testDeps("dec-1"), cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	begin := time.Now()
	res, err := f.Evaluate(context.Background(), entryReq())
	elapsed := time.Since(begin)
	if err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("Evaluate took %v, want the arena not to block it", elapsed)
	}
	if res.Verdict.Verdict != domain.VerdictAllow {
		t.Fatalf("primary verdict = %v, want ALLOW", res.Verdict.Verdict)
	}
	if n := jr.count(); n != 3 {
		t.Fatalf("events before arena release = %d, want 3", n)
	}

	// The arena call is already running while the primary result is out.
	select {
	case <-arena.started:
	case <-time.After(2 * time.Second):
		t.Fatalf("arena provider never started")
	}
	if got := arenaEvents(t, jr); len(got) != 0 {
		t.Fatalf("arena event recorded while blocked: %d", len(got))
	}

	close(arena.release)
	waitFor(t, "arena event", func() bool { return len(arenaEvents(t, jr)) == 1 })

	evs, _ := jr.Query(context.Background(), 0, "")
	if len(evs) != 4 {
		t.Fatalf("events = %d, want 4", len(evs))
	}
	want := []string{domain.EventGateRequest, domain.EventDecisionResponse, domain.EventGateVerdict, domain.EventArenaDecision}
	for i, k := range want {
		if evs[i].Kind != k {
			t.Fatalf("event[%d] = %s, want %s (kinds: %v)", i, evs[i].Kind, k, kinds(jr))
		}
	}
	arenaEv := evs[3]
	if arenaEv.DecisionID != "dec-1" {
		t.Fatalf("arena decisionId = %q, want dec-1", arenaEv.DecisionID)
	}
	if arenaEv.CodeVersion != "v-test" || arenaEv.SchemaVersion != domain.EventSchemaVersion {
		t.Fatalf("arena envelope = %+v", arenaEv)
	}
	if !arenaEv.TS.Equal(fixedTime) {
		t.Fatalf("arena ts = %v, want fixed clock", arenaEv.TS)
	}

	var payload arenaPayload
	if err := json.Unmarshal(arenaEv.Payload, &payload); err != nil {
		t.Fatalf("decode arena payload = %v", err)
	}
	if payload.DecisionID != "dec-1" {
		t.Fatalf("payload decisionId = %q", payload.DecisionID)
	}
	if payload.Model != arenaResp().Model {
		t.Fatalf("payload model = %q, want actual model %q", payload.Model, arenaResp().Model)
	}
	if payload.Response.Model != arenaResp().Model || payload.Response.RequestedModel != "arena-model" {
		t.Fatalf("payload response = %+v", payload.Response)
	}
	if payload.Verdict.DecisionID != "dec-1" || payload.Verdict.DecidedAt.IsZero() {
		t.Fatalf("payload verdict = %+v", payload.Verdict)
	}
	if arenaEv.PolicyHash != payload.Verdict.PolicyHash || arenaEv.PolicyHash == "" {
		t.Fatalf("arena policyHash = %q, verdict %q", arenaEv.PolicyHash, payload.Verdict.PolicyHash)
	}
	// Payload carries exactly the contract fields, never a bare response.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(arenaEv.Payload, &fields); err != nil {
		t.Fatalf("decode arena payload fields = %v", err)
	}
	if len(fields) != 4 {
		t.Fatalf("arena payload fields = %v, want decisionId/model/response/verdict", fields)
	}
}

func TestArenaSkippedForExitIntent(t *testing.T) {
	jr := &memJournal{}
	primary := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		return allowResp(), nil
	}}
	arena := &blockingArena{started: make(chan struct{}), resp: arenaResp()}
	cfg := testConfig()
	cfg.Arena = arenaConfig(arena)
	f, err := New(jr, primary, testDeps("dec-1"), cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	req := entryReq()
	req.IntentKind = domain.IntentExit
	if _, err := f.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := arena.callCount(); n != 0 {
		t.Fatalf("arena calls = %d, want 0 for exit intent", n)
	}
	if got := kinds(jr); len(got) != 2 {
		t.Fatalf("kinds = %v, want [request verdict]", got)
	}
}

func TestArenaSkippedWhenNotConfigured(t *testing.T) {
	jr := &memJournal{}
	primary := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		return allowResp(), nil
	}}
	f, err := New(jr, primary, testDeps("dec-1"), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := f.Evaluate(context.Background(), entryReq()); err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := arenaEvents(t, jr); len(got) != 0 {
		t.Fatalf("arena events without config = %d, want 0", len(got))
	}
	if n := jr.count(); n != 3 {
		t.Fatalf("events = %d, want 3", n)
	}
}

func TestArenaFailureKeepsPrimaryResult(t *testing.T) {
	jr := &memJournal{}
	primary := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		return allowResp(), nil
	}}
	arena := &blockingArena{started: make(chan struct{}),
		err: domain.NewError(domain.CodeInvalidRequest, "arena rejected")}
	cfg := testConfig()
	cfg.Arena = arenaConfig(arena)
	f, err := New(jr, primary, testDeps("dec-1"), cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	res, err := f.Evaluate(context.Background(), entryReq())
	if err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	if res.Verdict.Verdict != domain.VerdictAllow || res.ProviderErr != nil {
		t.Fatalf("primary result = %+v, want unchanged ALLOW", res)
	}
	waitFor(t, "arena attempt", func() bool { return arena.callCount() > 0 })
	time.Sleep(50 * time.Millisecond)
	if got := arenaEvents(t, jr); len(got) != 0 {
		t.Fatalf("arena events after failure = %d, want 0", len(got))
	}
	if n := jr.count(); n != 3 {
		t.Fatalf("events = %d, want 3", n)
	}
}

func TestNewRejectsBadArenaConfig(t *testing.T) {
	if _, err := New(&memJournal{}, nil, testDeps(), Config{
		Model: "test-model", Policy: domain.DefaultGatePolicy(), CodeVersion: "v-test",
		Arena: &ArenaConfig{Model: "arena-model"},
	}); err == nil {
		t.Fatalf("New(nil arena provider) = nil, want error")
	}
	if _, err := New(&memJournal{}, nil, testDeps(), Config{
		Model: "test-model", Policy: domain.DefaultGatePolicy(), CodeVersion: "v-test",
		Arena: &ArenaConfig{Provider: &blockingArena{}, Model: "  "},
	}); err == nil {
		t.Fatalf("New(empty arena model) = nil, want error")
	}
}
