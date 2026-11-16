package gateflow

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// memJournal is an in-memory ports.Journal for offline tests.
type memJournal struct {
	mu     sync.Mutex
	events []domain.Event
}

func (m *memJournal) Append(_ context.Context, ev domain.Event) (domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev.Seq = int64(len(m.events)) + 1
	if ev.EventID == "" {
		ev.EventID = "evt-test-" + itoa(len(m.events)+1)
	}
	m.events = append(m.events, ev)
	return ev, nil
}

func (m *memJournal) Query(_ context.Context, from int64, kind string) ([]domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Event
	for _, ev := range m.events {
		if ev.Seq > from && (kind == "" || ev.Kind == kind) {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (m *memJournal) Walk(_ context.Context, from int64, kind string, fn func(domain.Event) error) error {
	evs, err := m.Query(context.Background(), from, kind)
	if err != nil {
		return err
	}
	for _, ev := range evs {
		if err := fn(ev); err != nil {
			return err
		}
	}
	return nil
}

func (m *memJournal) Head(_ context.Context) (domain.Event, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return domain.Event{}, false, nil
	}
	return m.events[len(m.events)-1], true, nil
}

func (m *memJournal) Verify(_ context.Context) (domain.VerifyResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return domain.VerifyResult{OK: true, Count: int64(len(m.events))}, nil
}

func (m *memJournal) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// fnDecision is a scripted ports.Decision for offline tests.
type fnDecision struct {
	calls int
	req   domain.DecisionRequest
	fn    func(ctx context.Context, req domain.DecisionRequest) (domain.DecisionResponse, error)
}

func (f *fnDecision) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "test-decision", Name: "test", Kind: ports.KindDecision, Version: "v0"}
}

func (f *fnDecision) Decide(ctx context.Context, req domain.DecisionRequest) (domain.DecisionResponse, error) {
	f.calls++
	f.req = req
	return f.fn(ctx, req)
}

var fixedTime = time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC)

func testDeps(ids ...string) ports.Deps {
	next := 0
	return ports.Deps{
		Clock: func() time.Time { return fixedTime },
		NewID: func() string {
			next++
			if next <= len(ids) {
				return ids[next-1]
			}
			return "dec-extra-" + itoa(next)
		},
	}
}

func entryReq() domain.GateRequest {
	return domain.GateRequest{
		SignalID:   "sig-1",
		Instrument: domain.Instrument{Venue: "binance", Symbol: "BTCUSDT"},
		Side:       domain.SideBuy,
		IntentKind: domain.IntentEntry,
		Timeframe:  "1h",
		TS:         fixedTime,
		Price:      domain.MustParseDecimal("67432.50"),
		Source:     "test",
		State:      map[string]float64{"rsi": 55.5},
	}
}

func allowResp() domain.DecisionResponse {
	return domain.DecisionResponse{
		Answers: map[string]domain.Answer{
			"data_quality":     {Type: domain.QuestionChoice, Choice: "ok", Probabilities: map[string]float64{"ok": 0.95, "insufficient": 0.05}},
			"signal_alignment": {Type: domain.QuestionChoice, Choice: "aligned", Probabilities: map[string]float64{"aligned": 0.8, "neutral": 0.15, "conflict": 0.05}},
			"market_regime":    {Type: domain.QuestionChoice, Choice: "favorable", Probabilities: map[string]float64{"favorable": 0.7, "neutral": 0.2, "adverse": 0.1}},
			"risk_check":       {Type: domain.QuestionChoice, Choice: "pass", Probabilities: map[string]float64{"pass": 0.9, "block": 0.1}},
		},
		Model:          "model-v1",
		RequestedModel: "test-model",
	}
}

func testConfig() Config {
	return Config{Model: "test-model", Policy: domain.DefaultGatePolicy(), CodeVersion: "v-test"}
}

func kinds(jr *memJournal) []string {
	evs, _ := jr.Query(context.Background(), 0, "")
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Kind)
	}
	return out
}

func TestEvaluateWritesThreeLinkedEvents(t *testing.T) {
	jr := &memJournal{}
	prov := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		return allowResp(), nil
	}}
	f, err := New(jr, prov, testDeps("dec-1"), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	res, err := f.Evaluate(context.Background(), entryReq())
	if err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	if res.Reused || res.ProviderErr != nil || res.Response == nil {
		t.Fatalf("result = %+v, want fresh success", res)
	}
	if res.Verdict.Verdict != domain.VerdictAllow {
		t.Fatalf("verdict = %v, want ALLOW", res.Verdict.Verdict)
	}
	if res.Verdict.DecisionID != "dec-1" {
		t.Fatalf("decisionId = %q, want dec-1", res.Verdict.DecisionID)
	}
	if got := kinds(jr); len(got) != 3 ||
		got[0] != domain.EventGateRequest || got[1] != domain.EventDecisionResponse || got[2] != domain.EventGateVerdict {
		t.Fatalf("kinds = %v, want [request response verdict]", got)
	}
	evs, _ := jr.Query(context.Background(), 0, "")
	for _, ev := range evs {
		if ev.DecisionID != "dec-1" {
			t.Fatalf("event %s has decisionId %q", ev.Kind, ev.DecisionID)
		}
		if ev.SchemaVersion != domain.EventSchemaVersion || ev.CodeVersion != "v-test" {
			t.Fatalf("event %s has bad envelope: %+v", ev.Kind, ev)
		}
		if !ev.TS.Equal(fixedTime) {
			t.Fatalf("event %s ts = %v, want fixed clock", ev.Kind, ev.TS)
		}
	}
	if evs[2].PolicyHash != res.Verdict.PolicyHash || evs[2].PolicyHash == "" {
		t.Fatalf("verdict envelope policyHash = %q, verdict %q", evs[2].PolicyHash, res.Verdict.PolicyHash)
	}
	// Composed decision request.
	if prov.req.Model != "test-model" || len(prov.req.Questions) != 4 {
		t.Fatalf("decision request = %+v", prov.req)
	}
	if prov.req.Timeout != defaultTimeout {
		t.Fatalf("decision timeout = %v, want %v", prov.req.Timeout, defaultTimeout)
	}
	for _, k := range []string{"symbol", "venue", "timeframe", "side", "intent_kind", "close", "timestamp", "rsi"} {
		if _, ok := prov.req.State[k]; !ok {
			t.Fatalf("decision state misses %q: %v", k, prov.req.State)
		}
	}
	if prov.req.State["symbol"] != "BTCUSDT" || prov.req.State["side"] != "buy" {
		t.Fatalf("decision state routing = %v", prov.req.State)
	}
	if ts, _ := prov.req.State["timestamp"].(string); ts != fixedTime.Format(time.RFC3339Nano) {
		t.Fatalf("decision state timestamp = %v", prov.req.State["timestamp"])
	}
}

func TestIdempotentReplay(t *testing.T) {
	jr := &memJournal{}
	prov := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		return allowResp(), nil
	}}
	f, _ := New(jr, prov, testDeps("dec-1", "dec-2"), testConfig())
	first, err := f.Evaluate(context.Background(), entryReq())
	if err != nil {
		t.Fatalf("first Evaluate() = %v", err)
	}
	second, err := f.Evaluate(context.Background(), entryReq())
	if err != nil {
		t.Fatalf("second Evaluate() = %v", err)
	}
	if !second.Reused || second.Response != nil || second.ProviderErr != nil {
		t.Fatalf("replay = %+v, want Reused with no response", second)
	}
	if second.Verdict.Verdict != first.Verdict.Verdict || second.Verdict.DecisionID != "dec-1" {
		t.Fatalf("replayed verdict = %+v, want %+v", second.Verdict, first.Verdict)
	}
	if prov.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.calls)
	}
	if n := jr.count(); n != 3 {
		t.Fatalf("events = %d, want 3", n)
	}
}

func TestCrashRetryReusesDecisionID(t *testing.T) {
	jr := &memJournal{}
	raw, _ := json.Marshal(entryReq())
	if _, err := jr.Append(context.Background(), domain.Event{
		Kind:          domain.EventGateRequest,
		TS:            fixedTime,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       raw,
		CodeVersion:   "v-test",
		DecisionID:    "dec-crash",
	}); err != nil {
		t.Fatalf("seed request = %v", err)
	}
	prov := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		return allowResp(), nil
	}}
	f, _ := New(jr, prov, testDeps("dec-unused"), testConfig())
	res, err := f.Evaluate(context.Background(), entryReq())
	if err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	if res.Reused || res.Verdict.DecisionID != "dec-crash" {
		t.Fatalf("result = %+v, want fresh verdict on dec-crash", res)
	}
	if got := kinds(jr); len(got) != 3 ||
		got[0] != domain.EventGateRequest || got[1] != domain.EventDecisionResponse || got[2] != domain.EventGateVerdict {
		t.Fatalf("kinds = %v, want single request plus response and verdict", got)
	}
}

func TestProviderFailureWritesAbstain(t *testing.T) {
	jr := &memJournal{}
	boom := errors.New("boom")
	prov := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		return domain.DecisionResponse{}, boom
	}}
	f, _ := New(jr, prov, testDeps("dec-1"), testConfig())
	res, err := f.Evaluate(context.Background(), entryReq())
	if err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	if res.ProviderErr == nil || res.Response != nil {
		t.Fatalf("result = %+v, want ProviderErr with nil response", res)
	}
	if res.Verdict.Verdict != domain.VerdictAbstain {
		t.Fatalf("verdict = %v, want ABSTAIN", res.Verdict.Verdict)
	}
	if got := kinds(jr); len(got) != 2 ||
		got[0] != domain.EventGateRequest || got[1] != domain.EventGateVerdict {
		t.Fatalf("kinds = %v, want [request verdict]", got)
	}
}

func TestExitWritesTwoEventsWithoutProvider(t *testing.T) {
	jr := &memJournal{}
	f, _ := New(jr, nil, testDeps("dec-1"), testConfig())
	req := entryReq()
	req.IntentKind = domain.IntentExit
	req.Side = domain.SideSell
	res, err := f.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	if res.Verdict.Verdict != domain.VerdictSkip || res.Response != nil || res.ProviderErr != nil {
		t.Fatalf("result = %+v, want SKIP with no provider contact", res)
	}
	if got := kinds(jr); len(got) != 2 ||
		got[0] != domain.EventGateRequest || got[1] != domain.EventGateVerdict {
		t.Fatalf("kinds = %v, want [request verdict]", got)
	}
}

func seedResponse(t *testing.T, jr *memJournal, decisionID, requested, actual string) {
	t.Helper()
	resp := allowResp()
	resp.RequestedModel = requested
	resp.Model = actual
	raw, _ := json.Marshal(resp)
	if _, err := jr.Append(context.Background(), domain.Event{
		Kind:          domain.EventDecisionResponse,
		TS:            fixedTime,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       raw,
		CodeVersion:   "v-test",
		DecisionID:    decisionID,
	}); err != nil {
		t.Fatalf("seed response = %v", err)
	}
}

func TestModelChangeRecorded(t *testing.T) {
	jr := &memJournal{}
	seedResponse(t, jr, "dec-old", "test-model", "model-v1")
	prov := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		r := allowResp()
		r.Model = "model-v2"
		return r, nil
	}}
	f, _ := New(jr, prov, testDeps("dec-new"), testConfig())
	if _, err := f.Evaluate(context.Background(), entryReq()); err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	evs, _ := jr.Query(context.Background(), 0, "")
	var mc *domain.ModelChanged
	for _, ev := range evs {
		if ev.Kind == domain.EventDecisionModelChanged && ev.DecisionID == "dec-new" {
			var v domain.ModelChanged
			if err := json.Unmarshal(ev.Payload, &v); err != nil {
				t.Fatalf("decode model_changed = %v", err)
			}
			mc = &v
		}
	}
	if mc == nil {
		t.Fatalf("no model_changed event in %v", kinds(jr))
	}
	if mc.RequestedModel != "test-model" || mc.PreviousModel != "model-v1" || mc.ActualModel != "model-v2" {
		t.Fatalf("model_changed = %+v", mc)
	}
}

func TestCrashRetryReplaysRecordedResponse(t *testing.T) {
	jr := &memJournal{}
	req := entryReq()
	reqRaw, _ := json.Marshal(req)
	if _, err := jr.Append(context.Background(), domain.Event{
		Kind:          domain.EventGateRequest,
		TS:            fixedTime,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       reqRaw,
		CodeVersion:   "v-test",
		DecisionID:    "dec-crash",
	}); err != nil {
		t.Fatalf("seed request = %v", err)
	}
	seedResponse(t, jr, "dec-crash", "test-model", "model-a")
	called := false
	prov := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		called = true
		r := allowResp()
		r.Model = "model-b"
		r.Answers["risk_check"] = domain.Answer{Type: domain.QuestionChoice, Choice: "block",
			Probabilities: map[string]float64{"pass": 0.1, "block": 0.9}}
		return r, nil
	}}
	f, _ := New(jr, prov, testDeps("dec-unused"), testConfig())
	res, err := f.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	if called {
		t.Fatalf("provider was called on crash retry with a recorded response")
	}
	if res.Reused || res.Verdict.Verdict != domain.VerdictAllow {
		t.Fatalf("result = %+v, want fresh ALLOW from the recorded answer", res)
	}
	if res.Response == nil || res.Response.Model != "model-a" {
		t.Fatalf("response = %+v, want recorded model-a", res.Response)
	}
	if got := kinds(jr); len(got) != 3 ||
		got[0] != domain.EventGateRequest || got[1] != domain.EventDecisionResponse || got[2] != domain.EventGateVerdict {
		t.Fatalf("kinds = %v, want only the missing verdict appended", got)
	}
}

func TestNoModelChangeOnFirstUse(t *testing.T) {
	jr := &memJournal{}
	prov := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		return allowResp(), nil
	}}
	f, _ := New(jr, prov, testDeps("dec-1"), testConfig())
	if _, err := f.Evaluate(context.Background(), entryReq()); err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	for _, k := range kinds(jr) {
		if k == domain.EventDecisionModelChanged {
			t.Fatalf("unexpected model_changed event")
		}
	}
}

func TestCommonDeadline(t *testing.T) {
	jr := &memJournal{}
	prov := &fnDecision{fn: func(ctx context.Context, _ domain.DecisionRequest) (domain.DecisionResponse, error) {
		<-ctx.Done()
		return domain.DecisionResponse{}, ctx.Err()
	}}
	cfg := testConfig()
	cfg.Timeout = 50 * time.Millisecond
	f, _ := New(jr, prov, testDeps("dec-1"), cfg)
	start := time.Now()
	res, err := f.Evaluate(context.Background(), entryReq())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	if res.Verdict.Verdict != domain.VerdictAbstain || res.ProviderErr == nil {
		t.Fatalf("result = %+v, want ABSTAIN with provider error", res)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Evaluate took %v, want prompt deadline", elapsed)
	}
	if n := jr.count(); n != 2 {
		t.Fatalf("events = %d, want request plus verdict", n)
	}
}

func TestInvalidRequestRejectedBeforeIO(t *testing.T) {
	bad := entryReq()
	cases := map[string]func(*domain.GateRequest){
		"signal":    func(r *domain.GateRequest) { r.SignalID = "" },
		"venue":     func(r *domain.GateRequest) { r.Instrument.Venue = "" },
		"symbol":    func(r *domain.GateRequest) { r.Instrument.Symbol = "" },
		"timeframe": func(r *domain.GateRequest) { r.Timeframe = "" },
		"source":    func(r *domain.GateRequest) { r.Source = "" },
		"side":      func(r *domain.GateRequest) { r.Side = "hold" },
		"intent":    func(r *domain.GateRequest) { r.IntentKind = "amend" },
		"ts":        func(r *domain.GateRequest) { r.TS = time.Time{} },
		"price":     func(r *domain.GateRequest) { r.Price = domain.MustParseDecimal("0") },
		"state":     func(r *domain.GateRequest) { r.State = map[string]float64{"x": math.Inf(1)} },
	}
	_ = bad
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			jr := &memJournal{}
			prov := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
				return allowResp(), nil
			}}
			f, _ := New(jr, prov, testDeps("dec-1"), testConfig())
			req := entryReq()
			mut(&req)
			if _, err := f.Evaluate(context.Background(), req); err == nil {
				t.Fatalf("Evaluate() = nil, want invalid request")
			} else if domain.CodeOf(err) != domain.CodeInvalidRequest {
				t.Fatalf("Evaluate() code = %v, want invalid_request", domain.CodeOf(err))
			}
			if prov.calls != 0 || jr.count() != 0 {
				t.Fatalf("calls = %d events = %d, want no I/O", prov.calls, jr.count())
			}
		})
	}
	// NaN state is rejected too.
	jr := &memJournal{}
	prov := &fnDecision{fn: func(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
		return allowResp(), nil
	}}
	f, _ := New(jr, prov, testDeps("dec-1"), testConfig())
	req := entryReq()
	req.State = map[string]float64{"x": nan()}
	if _, err := f.Evaluate(context.Background(), req); domain.CodeOf(err) != domain.CodeInvalidRequest {
		t.Fatalf("NaN state err = %v, want invalid_request", err)
	}
}

func nan() float64 { return math.NaN() }

func TestNewRejectsBadConfig(t *testing.T) {
	if _, err := New(nil, nil, testDeps(), testConfig()); err == nil {
		t.Fatalf("New(nil journal) = nil, want error")
	}
	if _, err := New(&memJournal{}, nil, testDeps(), Config{}); err == nil {
		t.Fatalf("New(empty model) = nil, want error")
	}
	// Empty NewID is an error at Evaluate time.
	f, _ := New(&memJournal{}, nil, ports.Deps{Clock: func() time.Time { return fixedTime }}, testConfig())
	if _, err := f.Evaluate(context.Background(), entryReq()); err == nil {
		t.Fatalf("Evaluate without NewID = nil, want error")
	}
}
