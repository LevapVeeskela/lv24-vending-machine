package report

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// fakeJournal is an in-memory ports.Journal for offline tests.
type fakeJournal struct {
	events []domain.Event
}

func (f *fakeJournal) Append(_ context.Context, ev domain.Event) (domain.Event, error) {
	ev.Seq = int64(len(f.events) + 1)
	if ev.EventID == "" {
		ev.EventID = "evt-test"
	}
	f.events = append(f.events, ev)
	return ev, nil
}

func (f *fakeJournal) Query(_ context.Context, from int64, kind string) ([]domain.Event, error) {
	var out []domain.Event
	for _, ev := range f.events {
		if ev.Seq > from && (kind == "" || ev.Kind == kind) {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (f *fakeJournal) Walk(_ context.Context, from int64, kind string, fn func(domain.Event) error) error {
	for _, ev := range f.events {
		if ev.Seq > from && (kind == "" || ev.Kind == kind) {
			if err := fn(ev); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeJournal) Head(_ context.Context) (domain.Event, bool, error) {
	if len(f.events) == 0 {
		return domain.Event{}, false, nil
	}
	return f.events[len(f.events)-1], true, nil
}

func (f *fakeJournal) Verify(_ context.Context) (domain.VerifyResult, error) {
	return domain.VerifyResult{OK: true, Count: int64(len(f.events))}, nil
}

var testClock = time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)

func testDeps() ports.Deps {
	return ports.Deps{Clock: func() time.Time { return testClock }}
}

func mustPayload(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return raw
}

func gateRequestPayload(t *testing.T, signalID string) json.RawMessage {
	t.Helper()
	return mustPayload(t, domain.GateRequest{
		SignalID:   signalID,
		Instrument: domain.Instrument{Venue: "test-venue", Symbol: "BTC/USDT"},
		Side:       domain.SideBuy,
		IntentKind: domain.IntentEntry,
		Timeframe:  "1h",
		TS:         testClock,
		Price:      domain.MustParseDecimal("100"),
		Source:     "test",
	})
}

func responsePayload(t *testing.T, model, cost string, latency int64, pPass *float64) json.RawMessage {
	t.Helper()
	answers := map[string]domain.Answer{}
	if pPass != nil {
		answers[riskQuestion] = domain.Answer{
			Type:          domain.QuestionChoice,
			Choice:        "pass",
			Probabilities: map[string]float64{"pass": *pPass, "block": 1 - *pPass},
		}
	} else {
		answers["data_quality"] = domain.Answer{
			Type:          domain.QuestionChoice,
			Choice:        "ok",
			Probabilities: map[string]float64{"ok": 1, "insufficient": 0},
		}
	}
	return mustPayload(t, domain.DecisionResponse{
		Answers: answers,
		Model:   model,
		Usage: domain.Usage{
			CostUSD:   domain.MustParseDecimal(cost),
			LatencyMS: latency,
		},
	})
}

func verdictPayload(t *testing.T, verdict domain.Verdict) json.RawMessage {
	t.Helper()
	return mustPayload(t, domain.GateVerdict{
		Verdict:    verdict,
		Code:       "test",
		Reasons:    []string{"test"},
		Checks:     []domain.Check{},
		PolicyHash: "test-policy",
	})
}

func outcomePayload(t *testing.T, signalID, decisionID, netReturn string) json.RawMessage {
	t.Helper()
	return mustPayload(t, domain.Outcome{
		SignalID:    signalID,
		DecisionID:  decisionID,
		Instrument:  domain.Instrument{Venue: "test-venue", Symbol: "BTC/USDT"},
		Side:        domain.SideBuy,
		Timeframe:   "1h",
		EntryPrice:  domain.MustParseDecimal("100"),
		ExitPrice:   domain.MustParseDecimal("101"),
		NetReturn:   domain.MustParseDecimal(netReturn),
		GrossReturn: domain.MustParseDecimal(netReturn),
		LabeledAt:   testClock,
	})
}

// seedFour builds a journal with 4 labeled decisions across 2 models plus
// one orphan outcome that must be ignored:
//   - dec-1/sig-1 alpha p=0.75 ALLOW net +0.05 (fact 1)
//   - dec-2/sig-2 alpha p=0.50 ALLOW net -0.02 (fact 0)
//   - dec-3/sig-3 beta  p=0.25 REJECT net +0.03 (fact 1)
//   - dec-4/sig-4 beta  no pPass REJECT net -0.04, outcome links via signalId
//   - orphan outcome sig-unknown/dec-unknown net +1.00 (ignored)
func seedFour(t *testing.T) *fakeJournal {
	t.Helper()
	fp := func(v float64) *float64 { return &v }
	jr := &fakeJournal{}
	type decision struct {
		id, sig, model, cost string
		latency              int64
		pPass                *float64
		verdict              domain.Verdict
		net                  string
		outcomeDecisionID    string
	}
	decs := []decision{
		{"dec-1", "sig-1", "alpha", "0.010", 100, fp(0.75), domain.VerdictAllow, "0.05", "dec-1"},
		{"dec-2", "sig-2", "alpha", "0.020", 200, fp(0.5), domain.VerdictAllow, "-0.02", "dec-2"},
		{"dec-3", "sig-3", "beta", "0.030", 300, fp(0.25), domain.VerdictReject, "0.03", "dec-3"},
		// No pPass on dec-4; the outcome carries no decisionId so the
		// signalId -> decisionId fallback through gate.request applies.
		{"dec-4", "sig-4", "beta", "0.040", 400, nil, domain.VerdictReject, "-0.04", ""},
	}
	ctx := context.Background()
	for _, d := range decs {
		for _, ev := range []domain.Event{
			{Kind: domain.EventGateRequest, DecisionID: d.id, Payload: gateRequestPayload(t, d.sig)},
			{Kind: domain.EventDecisionResponse, DecisionID: d.id, Payload: responsePayload(t, d.model, d.cost, d.latency, d.pPass)},
			{Kind: domain.EventGateVerdict, DecisionID: d.id, Payload: verdictPayload(t, d.verdict)},
			{Kind: domain.EventOutcomeLabeled, DecisionID: d.outcomeDecisionID, Payload: outcomePayload(t, d.sig, d.outcomeDecisionID, d.net)},
		} {
			if _, err := jr.Append(ctx, ev); err != nil {
				t.Fatalf("append: %v", err)
			}
		}
	}
	// Orphan outcome: no verdict for dec-unknown and no gate.request for
	// sig-unknown, so it must be ignored everywhere.
	if _, err := jr.Append(ctx, domain.Event{
		Kind:       domain.EventOutcomeLabeled,
		DecisionID: "dec-unknown",
		Payload:    outcomePayload(t, "sig-unknown", "dec-unknown", "1.00"),
	}); err != nil {
		t.Fatalf("append orphan: %v", err)
	}
	return jr
}

func almostEqual(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func TestBuildFull(t *testing.T) {
	jr := seedFour(t)
	b, err := New(jr, testDeps(), Config{MinLabeled: 3})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !rep.GeneratedAt.Equal(testClock) {
		t.Fatalf("GeneratedAt = %v, want %v", rep.GeneratedAt, testClock)
	}
	// Verdict counts over the 4 gate.verdict events.
	wantCounts := map[string]int{"ALLOW": 2, "REJECT": 2, "ABSTAIN": 0, "SKIP": 0}
	for k, want := range wantCounts {
		if rep.VerdictCounts[k] != want {
			t.Fatalf("VerdictCounts[%q] = %d, want %d", k, rep.VerdictCounts[k], want)
		}
	}
	// 4 labeled: dec-1..dec-4 (dec-4 via signalId fallback). The orphan
	// outcome (+1.00) is ignored.
	if rep.LabeledCount != 4 {
		t.Fatalf("LabeledCount = %d, want 4", rep.LabeledCount)
	}
	// Cost over ALL decision.response events: 0.010+0.020+0.030+0.040.
	if got := rep.DecisionCostUSD.String(); got != "0.100" {
		t.Fatalf("DecisionCostUSD = %q, want %q", got, "0.100")
	}
	// Latencies [100,200,300,400]: p50 rank ceil(0.5*4)=2 -> 200,
	// p95 rank ceil(0.95*4)=4 -> 400.
	if rep.LatencyP50MS != 200 || rep.LatencyP95MS != 400 {
		t.Fatalf("latency p50/p95 = %d/%d, want 200/400", rep.LatencyP50MS, rep.LatencyP95MS)
	}
	if !rep.Sufficient {
		t.Fatalf("Sufficient = false, want true (4 >= 3)")
	}
	if rep.Comparisons == nil {
		t.Fatalf("Comparisons is nil, want populated")
	}
	c := rep.Comparisons
	// All entries: 0.05 + (-0.02) + 0.03 + (-0.04) = 0.02.
	if got := c.AllEntriesReturn.String(); got != "0.02" {
		t.Fatalf("AllEntriesReturn = %q, want 0.02", got)
	}
	// ALLOW only: 0.05 + (-0.02) = 0.03.
	if got := c.AllowReturn.String(); got != "0.03" {
		t.Fatalf("AllowReturn = %q, want 0.03", got)
	}
	// REJECT avg: (0.03 + (-0.04)) / 2 = -0.01/2 = -0.005, scale 12.
	if got := c.RejectedAvgReturn.String(); got != "-0.005000000000" {
		t.Fatalf("RejectedAvgReturn = %q, want -0.005000000000", got)
	}
	// Brier over the 3 pairs with pPass:
	// (0.75-1)^2=0.0625, (0.5-0)^2=0.25, (0.25-1)^2=0.5625;
	// (0.0625+0.25+0.5625)/3 = 0.875/3.
	almostEqual(t, "Brier", c.Brier, 0.2916666666666667)
	// ECE over 3 singletons: buckets 7,5,2 each hold 1/3 weight with
	// gaps |0.75-1|=0.25, |0.5-0|=0.5, |0.25-1|=0.75;
	// (0.25+0.5+0.75)/3 = 1.5/3 = 0.5.
	almostEqual(t, "ECE", c.ECE, 0.5)
	if len(c.Reliability) != 10 {
		t.Fatalf("Reliability buckets = %d, want 10", len(c.Reliability))
	}
	type bucketWant struct {
		count int
		mean  float64
		obs   float64
	}
	wantBuckets := map[int]bucketWant{
		// int(0.75*10)=7, int(0.5*10)=5, int(0.25*10)=2.
		7: {1, 0.75, 1},
		5: {1, 0.5, 0},
		2: {1, 0.25, 1},
	}
	for i, bk := range c.Reliability {
		if got := bk.LowerBound; got != float64(i)/10 {
			t.Fatalf("bucket %d lowerBound = %v, want %v", i, got, float64(i)/10)
		}
		w := wantBuckets[i]
		if bk.Count != w.count {
			t.Fatalf("bucket %d count = %d, want %d", i, bk.Count, w.count)
		}
		almostEqual(t, "bucket mean", bk.MeanPredicted, w.mean)
		almostEqual(t, "bucket observed", bk.ObservedRate, w.obs)
	}
	if len(c.ByModel) != 2 {
		t.Fatalf("ByModel entries = %d, want 2", len(c.ByModel))
	}
	// Sorted by model: alpha first.
	a := c.ByModel[0]
	if a.Model != "alpha" || a.Count != 2 {
		t.Fatalf("ByModel[0] = %+v, want model alpha count 2", a)
	}
	// Alpha pairs (0.75,1),(0.5,0): Brier (0.0625+0.25)/2 = 0.15625;
	// ECE (1/2)*0.25 + (1/2)*0.5 = 0.375; net 0.05-0.02 = 0.03;
	// cost 0.010+0.020 = 0.030.
	almostEqual(t, "alpha Brier", a.Brier, 0.15625)
	almostEqual(t, "alpha ECE", a.ECE, 0.375)
	if got := a.NetReturnSum.String(); got != "0.03" {
		t.Fatalf("alpha NetReturnSum = %q, want 0.03", got)
	}
	if got := a.CostUSD.String(); got != "0.030" {
		t.Fatalf("alpha CostUSD = %q, want 0.030", got)
	}
	bm := c.ByModel[1]
	if bm.Model != "beta" || bm.Count != 2 {
		t.Fatalf("ByModel[1] = %+v, want model beta count 2", bm)
	}
	// Beta pairs: only (0.25,1) since dec-4 has no pPass:
	// Brier 0.5625; ECE |0.25-1| = 0.75; net 0.03-0.04 = -0.01;
	// cost 0.030+0.040 = 0.070.
	almostEqual(t, "beta Brier", bm.Brier, 0.5625)
	almostEqual(t, "beta ECE", bm.ECE, 0.75)
	if got := bm.NetReturnSum.String(); got != "-0.01" {
		t.Fatalf("beta NetReturnSum = %q, want -0.01", got)
	}
	if got := bm.CostUSD.String(); got != "0.070" {
		t.Fatalf("beta CostUSD = %q, want 0.070", got)
	}
}

func TestBuildDefaultMinLabeled(t *testing.T) {
	jr := seedFour(t)
	// Config{} means MinLabeled defaults to 100; 4 labeled is not enough.
	b, err := New(jr, testDeps(), Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rep.LabeledCount != 4 {
		t.Fatalf("LabeledCount = %d, want 4", rep.LabeledCount)
	}
	if rep.Sufficient {
		t.Fatalf("Sufficient = true, want false (4 < 100)")
	}
	if rep.Comparisons != nil {
		t.Fatalf("Comparisons is populated, want nil")
	}
	// Base stats are still populated.
	if rep.VerdictCounts["ALLOW"] != 2 || rep.VerdictCounts["REJECT"] != 2 {
		t.Fatalf("VerdictCounts = %v, want ALLOW=2 REJECT=2", rep.VerdictCounts)
	}
	if got := rep.DecisionCostUSD.String(); got != "0.100" {
		t.Fatalf("DecisionCostUSD = %q, want 0.100", got)
	}
	if rep.LatencyP50MS != 200 || rep.LatencyP95MS != 400 {
		t.Fatalf("latency p50/p95 = %d/%d, want 200/400", rep.LatencyP50MS, rep.LatencyP95MS)
	}
}

func TestBuildMissingPPass(t *testing.T) {
	jr := &fakeJournal{}
	ctx := context.Background()
	// One labeled decision whose response has no risk_check answer.
	for _, ev := range []domain.Event{
		{Kind: domain.EventGateRequest, DecisionID: "dec-1", Payload: gateRequestPayload(t, "sig-1")},
		{Kind: domain.EventDecisionResponse, DecisionID: "dec-1", Payload: responsePayload(t, "alpha", "0.010", 120, nil)},
		{Kind: domain.EventGateVerdict, DecisionID: "dec-1", Payload: verdictPayload(t, domain.VerdictAllow)},
		{Kind: domain.EventOutcomeLabeled, DecisionID: "dec-1", Payload: outcomePayload(t, "sig-1", "dec-1", "0.07")},
	} {
		if _, err := jr.Append(ctx, ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	b, err := New(jr, testDeps(), Config{MinLabeled: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rep.LabeledCount != 1 || !rep.Sufficient {
		t.Fatalf("LabeledCount/Sufficient = %d/%v, want 1/true", rep.LabeledCount, rep.Sufficient)
	}
	if rep.Comparisons == nil {
		t.Fatalf("Comparisons is nil, want populated")
	}
	c := rep.Comparisons
	// PnL counts the labeled outcome even without pPass.
	if got := c.AllEntriesReturn.String(); got != "0.07" {
		t.Fatalf("AllEntriesReturn = %q, want 0.07", got)
	}
	if got := c.AllowReturn.String(); got != "0.07" {
		t.Fatalf("AllowReturn = %q, want 0.07", got)
	}
	// No calibration pairs: Brier and ECE are 0, all buckets empty.
	almostEqual(t, "Brier", c.Brier, 0)
	almostEqual(t, "ECE", c.ECE, 0)
	for i, bk := range c.Reliability {
		if bk.Count != 0 || bk.MeanPredicted != 0 || bk.ObservedRate != 0 {
			t.Fatalf("bucket %d not empty: %+v", i, bk)
		}
	}
	if len(c.ByModel) != 1 || c.ByModel[0].Model != "alpha" || c.ByModel[0].Count != 1 {
		t.Fatalf("ByModel = %+v, want one alpha entry with count 1", c.ByModel)
	}
	almostEqual(t, "alpha Brier", c.ByModel[0].Brier, 0)
	almostEqual(t, "alpha ECE", c.ByModel[0].ECE, 0)
	if got := c.ByModel[0].NetReturnSum.String(); got != "0.07" {
		t.Fatalf("alpha NetReturnSum = %q, want 0.07", got)
	}
}

func TestBuildEmptyJournal(t *testing.T) {
	b, err := New(&fakeJournal{}, testDeps(), Config{MinLabeled: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build on empty journal: %v", err)
	}
	if rep.LabeledCount != 0 || rep.Sufficient || rep.Comparisons != nil {
		t.Fatalf("empty report = %+v, want zero counts and nil comparisons", rep)
	}
	for _, v := range []string{"ALLOW", "REJECT", "ABSTAIN", "SKIP"} {
		if rep.VerdictCounts[v] != 0 {
			t.Fatalf("VerdictCounts[%q] = %d, want 0", v, rep.VerdictCounts[v])
		}
	}
	if rep.LatencyP50MS != 0 || rep.LatencyP95MS != 0 {
		t.Fatalf("latency = %d/%d, want 0/0", rep.LatencyP50MS, rep.LatencyP95MS)
	}
	if got := rep.DecisionCostUSD.String(); got != "0" {
		t.Fatalf("DecisionCostUSD = %q, want 0", got)
	}
}

func TestNewNilJournal(t *testing.T) {
	if _, err := New(nil, testDeps(), Config{}); err == nil {
		t.Fatalf("New(nil journal) = nil error, want an error")
	}
}
