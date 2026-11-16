package report

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// decSpec describes one decision for the extended report tests.
type decSpec struct {
	id, sig, model, cost, net string
	latency                   int64
	p                         *float64
	verdict                   domain.Verdict
	policy                    string
	entry                     time.Time
	backfill                  bool
	feeBps                    int
	horizon                   int
	priceSource               string
}

func fp(v float64) *float64 { return &v }

func appendEv(t *testing.T, jr *fakeJournal, ev domain.Event) {
	t.Helper()
	if _, err := jr.Append(context.Background(), ev); err != nil {
		t.Fatalf("append %s: %v", ev.Kind, err)
	}
}

func verdictPolicyPayload(t *testing.T, v domain.Verdict, policy string) json.RawMessage {
	t.Helper()
	return mustPayload(t, domain.GateVerdict{
		Verdict:    v,
		Code:       "test",
		Reasons:    []string{"test"},
		Checks:     []domain.Check{},
		PolicyHash: policy,
	})
}

func outcomeOptsPayload(t *testing.T, sig, did, net string, d decSpec) json.RawMessage {
	t.Helper()
	return mustPayload(t, domain.Outcome{
		SignalID:    sig,
		DecisionID:  did,
		Instrument:  domain.Instrument{Venue: "test-venue", Symbol: "BTC/USDT"},
		Side:        domain.SideBuy,
		Timeframe:   "1h",
		EntryTS:     d.entry,
		EntryPrice:  domain.MustParseDecimal("100"),
		ExitPrice:   domain.MustParseDecimal("101"),
		HorizonBars: d.horizon,
		FeeBps:      d.feeBps,
		NetReturn:   domain.MustParseDecimal(net),
		GrossReturn: domain.MustParseDecimal(net),
		LabeledAt:   testClock,
		Backfill:    d.backfill,
		PriceSource: d.priceSource,
	})
}

// appendSpec writes gate.request, decision.response, gate.verdict and
// outcome.labeled for one decision.
func appendSpec(t *testing.T, jr *fakeJournal, d decSpec) {
	t.Helper()
	appendEv(t, jr, domain.Event{
		Kind:       domain.EventGateRequest,
		DecisionID: d.id,
		Payload:    gateRequestPayload(t, d.sig),
	})
	appendEv(t, jr, domain.Event{
		Kind:       domain.EventDecisionResponse,
		DecisionID: d.id,
		Payload:    responsePayload(t, d.model, d.cost, d.latency, d.p),
	})
	appendEv(t, jr, domain.Event{
		Kind:       domain.EventGateVerdict,
		DecisionID: d.id,
		Payload:    verdictPolicyPayload(t, d.verdict, d.policy),
	})
	appendEv(t, jr, domain.Event{
		Kind:       domain.EventOutcomeLabeled,
		DecisionID: d.id,
		Payload:    outcomeOptsPayload(t, d.sig, d.id, d.net, d),
	})
}

func decimalIs(t *testing.T, name string, got domain.Decimal, want string) {
	t.Helper()
	if got.String() != want {
		t.Fatalf("%s = %q, want %q", name, got.String(), want)
	}
}

func buildReport(t *testing.T, jr *fakeJournal, cfg Config) domain.Report {
	t.Helper()
	b, err := New(jr, testDeps(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return rep
}

// TestGroupsCalibrationExact pins hand-computed group, calibration and
// bootstrap values on a six-decision journal.
func TestGroupsCalibrationExact(t *testing.T) {
	day1 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	specs := []decSpec{
		{id: "d1", sig: "s1", model: "alpha", cost: "0.010", net: "0.10", latency: 100, p: fp(0.9), verdict: domain.VerdictAllow, policy: "p1", entry: day1, feeBps: 10, horizon: 12, priceSource: "md"},
		{id: "d2", sig: "s2", model: "alpha", cost: "0.020", net: "-0.02", latency: 200, p: fp(0.6), verdict: domain.VerdictAllow, policy: "p1", entry: day1, feeBps: 10, horizon: 12, priceSource: "md"},
		{id: "d3", sig: "s3", model: "beta", cost: "0.030", net: "0.04", latency: 300, p: fp(0.5), verdict: domain.VerdictAllow, policy: "p1", entry: day2, feeBps: 10, horizon: 12, priceSource: "md"},
		{id: "d4", sig: "s4", model: "beta", cost: "0.040", net: "0.02", latency: 400, p: fp(0.2), verdict: domain.VerdictReject, policy: "p1", entry: day2, feeBps: 10, horizon: 12, priceSource: "md"},
		{id: "d5", sig: "s5", model: "gamma", cost: "0.050", net: "-0.03", latency: 500, p: fp(0.1), verdict: domain.VerdictReject, policy: "p1", entry: day3, feeBps: 10, horizon: 12, priceSource: "md"},
		{id: "d6", sig: "s6", model: "gamma", cost: "0.060", net: "-0.01", latency: 600, p: fp(0.4), verdict: domain.VerdictAbstain, policy: "p1", entry: day3, feeBps: 10, horizon: 12, priceSource: "md"},
	}
	jr := &fakeJournal{}
	for _, s := range specs {
		appendSpec(t, jr, s)
	}
	rep := buildReport(t, jr, Config{MinLabeled: 1})
	if rep.LabeledCount != 6 || !rep.Sufficient {
		t.Fatalf("labeled/sufficient = %d/%v, want 6/true", rep.LabeledCount, rep.Sufficient)
	}
	if rep.PolicyHash != "p1" {
		t.Fatalf("PolicyHash = %q, want p1", rep.PolicyHash)
	}
	// Assumptions come from the included outcomes.
	a := rep.Assumptions
	if a == nil || a.HorizonBars != 12 || a.FeeBpsRoundTrip != 20 || a.PriceSource != "md" ||
		!a.EqualSize || !a.NoSlippage || !a.NoFunding {
		t.Fatalf("Assumptions = %+v, want horizon 12, fees 20, md, true/true/true", a)
	}
	c := rep.Comparisons
	if c == nil {
		t.Fatalf("Comparisons is nil")
	}
	type groupWant struct {
		count int
		sum   string
		avg   string
	}
	wantGroups := map[string]groupWant{
		"all":     {6, "0.10", "0.016666666667"},
		"ALLOW":   {3, "0.12", "0.040000000000"},
		"REJECT":  {2, "-0.01", "-0.005000000000"},
		"ABSTAIN": {1, "-0.01", "-0.010000000000"},
	}
	for name, w := range wantGroups {
		g := c.Groups[name]
		if g.Count != w.count {
			t.Fatalf("group %s count = %d, want %d", name, g.Count, w.count)
		}
		decimalIs(t, "group "+name+" sum", g.SumNetReturn, w.sum)
		decimalIs(t, "group "+name+" avg", g.AvgNetReturn, w.avg)
	}
	decimalIs(t, "RejectedAvgReturn", c.RejectedAvgReturn, "-0.005000000000")
	// Brier = (0.01+0.36+0.25+0.64+0.01+0.16)/6; base rate 3/6.
	almostEqual(t, "BaseRate", c.BaseRate, 0.5)
	almostEqual(t, "Brier", c.Brier, 1.43/6)
	almostEqual(t, "ConstantBrier", c.ConstantBrier, 0.25)
	almostEqual(t, "BrierSkillScore", c.BrierSkillScore, 1-(1.43/6)/0.25)
	// Positives at ranks 2,4,6: (12-6)/9.
	almostEqual(t, "AUC", c.AUC, 2.0/3.0)
	almostEqual(t, "ECE", c.ECE, 2.5/6)
	if c.CalibrationCount != 6 {
		t.Fatalf("CalibrationCount = %d, want 6", c.CalibrationCount)
	}
	d := c.Difference
	if d == nil {
		t.Fatalf("Difference is nil")
	}
	decimalIs(t, "difference mean", d.Mean, "0.045000000000")
	if d.Days != 3 {
		t.Fatalf("difference days = %d, want 3", d.Days)
	}
	if d.BootstrapReps <= 0 || d.BootstrapReps > DefaultBootstrapReps {
		t.Fatalf("bootstrap reps = %d, want (0,2000]", d.BootstrapReps)
	}
	if d.ContainsZero || d.Low.Sign() <= 0 {
		t.Fatalf("difference CI = [%s,%s] zero=%v, want strictly positive",
			d.Low.String(), d.High.String(), d.ContainsZero)
	}
}

// TestBootstrapDeterministicAndNoise contains a reproducible CI and one
// that spans zero for symmetric noise.
func TestBootstrapDeterministicAndNoise(t *testing.T) {
	day1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	jr := &fakeJournal{}
	for _, s := range []decSpec{
		{id: "n1", sig: "n1", model: "m", cost: "0.01", net: "0.05", p: fp(0.6), verdict: domain.VerdictAllow, policy: "p1", entry: day1},
		{id: "n2", sig: "n2", model: "m", cost: "0.01", net: "-0.05", p: fp(0.6), verdict: domain.VerdictAllow, policy: "p1", entry: day2},
		{id: "n3", sig: "n3", model: "m", cost: "0.01", net: "-0.05", p: fp(0.4), verdict: domain.VerdictReject, policy: "p1", entry: day1},
		{id: "n4", sig: "n4", model: "m", cost: "0.01", net: "0.05", p: fp(0.4), verdict: domain.VerdictReject, policy: "p1", entry: day2},
	} {
		appendSpec(t, jr, s)
	}
	first := buildReport(t, jr, Config{MinLabeled: 1})
	second := buildReport(t, jr, Config{MinLabeled: 1})
	if first.Comparisons == nil || first.Comparisons.Difference == nil {
		t.Fatalf("Difference is nil")
	}
	d1 := first.Comparisons.Difference
	d2 := second.Comparisons.Difference
	if d1.Mean.String() != d2.Mean.String() || d1.Low.String() != d2.Low.String() ||
		d1.High.String() != d2.High.String() || d1.BootstrapReps != d2.BootstrapReps {
		t.Fatalf("bootstrap not reproducible: %+v vs %+v", d1, d2)
	}
	if !d1.ContainsZero {
		t.Fatalf("noise CI = [%s,%s], want it to contain zero",
			d1.Low.String(), d1.High.String())
	}
}

// TestPolicyFilter selects a policy explicitly and by highest-seq verdict.
func TestPolicyFilter(t *testing.T) {
	polA := strings.Repeat("a", 64)
	polB := strings.Repeat("b", 64)
	polC := strings.Repeat("c", 64)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	jr := &fakeJournal{}
	for _, s := range []decSpec{
		{id: "p1", sig: "p1", model: "m", cost: "0.01", net: "0.10", verdict: domain.VerdictAllow, policy: polA, entry: now},
		{id: "p2", sig: "p2", model: "m", cost: "0.01", net: "-0.02", verdict: domain.VerdictAllow, policy: polA, entry: now},
		{id: "p3", sig: "p3", model: "m", cost: "0.01", net: "0.50", verdict: domain.VerdictReject, policy: polB, entry: now},
	} {
		appendSpec(t, jr, s)
	}
	// Highest-seq verdict is p3 under polB.
	auto := buildReport(t, jr, Config{MinLabeled: 1})
	if auto.PolicyHash != polB || auto.LabeledCount != 1 || auto.OtherPolicyDecisions != 2 {
		t.Fatalf("auto policy = %q labeled %d other %d, want polB/1/2",
			auto.PolicyHash, auto.LabeledCount, auto.OtherPolicyDecisions)
	}
	if got := auto.Comparisons.Groups["REJECT"].Count; got != 1 {
		t.Fatalf("auto REJECT group = %d, want 1", got)
	}
	// Explicit polA keeps its two decisions.
	picked := buildReport(t, jr, Config{MinLabeled: 1, PolicyHash: polA})
	if picked.PolicyHash != polA || picked.LabeledCount != 2 || picked.OtherPolicyDecisions != 1 {
		t.Fatalf("picked policy = %q labeled %d other %d, want polA/2/1",
			picked.PolicyHash, picked.LabeledCount, picked.OtherPolicyDecisions)
	}
	// A policy no verdict used is not an error; it yields no comparison.
	unused := buildReport(t, jr, Config{MinLabeled: 1, PolicyHash: polC})
	if unused.PolicyHash != polC || unused.LabeledCount != 0 || unused.OtherPolicyDecisions != 3 {
		t.Fatalf("unused policy = %q labeled %d other %d, want polC/0/3",
			unused.PolicyHash, unused.LabeledCount, unused.OtherPolicyDecisions)
	}
	if unused.Sufficient || unused.Comparisons != nil {
		t.Fatalf("unused policy sufficient=%v comparisons=%v, want false/nil",
			unused.Sufficient, unused.Comparisons != nil)
	}
}

// TestBackfillExcludedByDefault counts and excludes backfilled outcomes.
func TestBackfillExcludedByDefault(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	jr := &fakeJournal{}
	appendSpec(t, jr, decSpec{id: "b1", sig: "b1", model: "m", cost: "0.01", net: "0.10",
		verdict: domain.VerdictAllow, policy: "p1", entry: now})
	appendSpec(t, jr, decSpec{id: "b2", sig: "b2", model: "m", cost: "0.01", net: "0.20",
		verdict: domain.VerdictAllow, policy: "p1", entry: now, backfill: true})
	def := buildReport(t, jr, Config{MinLabeled: 1})
	if def.LabeledCount != 1 || def.BackfillLabeled != 1 || def.BackfillExcluded != 1 {
		t.Fatalf("default labeled=%d backfill=%d excluded=%d, want 1/1/1",
			def.LabeledCount, def.BackfillLabeled, def.BackfillExcluded)
	}
	if got := def.Comparisons.AllowReturn.String(); got != "0.10" {
		t.Fatalf("default AllowReturn = %q, want 0.10", got)
	}
	inc := buildReport(t, jr, Config{MinLabeled: 1, IncludeBackfill: true})
	if inc.LabeledCount != 2 || inc.BackfillLabeled != 1 || inc.BackfillExcluded != 0 {
		t.Fatalf("include labeled=%d backfill=%d excluded=%d, want 2/1/0",
			inc.LabeledCount, inc.BackfillLabeled, inc.BackfillExcluded)
	}
	if got := inc.Comparisons.AllowReturn.String(); got != "0.30" {
		t.Fatalf("include AllowReturn = %q, want 0.30", got)
	}
}

// TestOldJournalWithoutNewFields keeps a pre-M1-part-2 journal readable.
func TestOldJournalWithoutNewFields(t *testing.T) {
	jr := &fakeJournal{}
	appendEv(t, jr, domain.Event{Kind: domain.EventGateRequest, DecisionID: "o1",
		Payload: gateRequestPayload(t, "o1")})
	appendEv(t, jr, domain.Event{Kind: domain.EventDecisionResponse, DecisionID: "o1",
		Payload: responsePayload(t, "m", "0.01", 100, fp(0.8))})
	appendEv(t, jr, domain.Event{Kind: domain.EventGateVerdict, DecisionID: "o1",
		Payload: verdictPolicyPayload(t, domain.VerdictAllow, "")})
	appendEv(t, jr, domain.Event{Kind: domain.EventOutcomeLabeled, DecisionID: "o1",
		Payload: outcomeOptsPayload(t, "o1", "o1", "0.05", decSpec{})})
	appendEv(t, jr, domain.Event{Kind: domain.EventGateRequest, DecisionID: "o2",
		Payload: gateRequestPayload(t, "o2")})
	appendEv(t, jr, domain.Event{Kind: domain.EventDecisionResponse, DecisionID: "o2",
		Payload: responsePayload(t, "m", "0.01", 200, fp(0.2))})
	appendEv(t, jr, domain.Event{Kind: domain.EventGateVerdict, DecisionID: "o2",
		Payload: verdictPolicyPayload(t, domain.VerdictReject, "")})
	appendEv(t, jr, domain.Event{Kind: domain.EventOutcomeLabeled, DecisionID: "o2",
		Payload: outcomeOptsPayload(t, "o2", "o2", "-0.02", decSpec{})})
	rep := buildReport(t, jr, Config{MinLabeled: 1})
	if rep.PolicyHash != "" || rep.LabeledCount != 2 || !rep.Sufficient {
		t.Fatalf("old journal policy=%q labeled=%d sufficient=%v, want \"\"/2/true",
			rep.PolicyHash, rep.LabeledCount, rep.Sufficient)
	}
	if rep.Comparisons == nil || rep.Comparisons.Groups["all"].Count != 2 {
		t.Fatalf("old journal comparison = %+v, want 2 outcomes", rep.Comparisons)
	}
	if rep.Assumptions == nil || rep.Assumptions.HorizonBars != 0 ||
		rep.Assumptions.FeeBpsRoundTrip != 0 || rep.Assumptions.PriceSource != "" {
		t.Fatalf("old journal assumptions = %+v, want zero-valued", rep.Assumptions)
	}
}

// TestArenaReport aggregates arena.decision events per model.
func TestArenaReport(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	jr := &fakeJournal{}
	for _, s := range []decSpec{
		{id: "d1", sig: "d1", model: "primary", cost: "0.001", net: "0.05", p: fp(0.7), verdict: domain.VerdictAllow, policy: "p1", entry: now},
		{id: "d2", sig: "d2", model: "primary", cost: "0.001", net: "-0.02", p: fp(0.3), verdict: domain.VerdictReject, policy: "p1", entry: now},
	} {
		appendSpec(t, jr, s)
	}
	mkArena := func(id, model string, v domain.Verdict, p float64, cost string) {
		appendEv(t, jr, domain.Event{
			Kind:       domain.EventArenaDecision,
			DecisionID: id,
			Payload: mustPayload(t, arenaPayload{
				DecisionID: id,
				Model:      model,
				Response: domain.DecisionResponse{
					Answers: map[string]domain.Answer{
						riskQuestion: {
							Type:          domain.QuestionChoice,
							Choice:        "pass",
							Probabilities: map[string]float64{"pass": p, "block": 1 - p},
						},
					},
					Model: model,
					Usage: domain.Usage{CostUSD: domain.MustParseDecimal(cost), LatencyMS: 10},
				},
				Verdict: domain.GateVerdict{Verdict: v, Code: "arena", Reasons: []string{}, Checks: []domain.Check{}},
			}),
		})
	}
	mkArena("d1", "m1", domain.VerdictAllow, 0.6, "0.01")
	mkArena("d2", "m1", domain.VerdictAllow, 0.9, "0.02")
	mkArena("d1", "m2", domain.VerdictReject, 0.2, "0.03")
	mkArena("d2", "m2", domain.VerdictReject, 0.1, "0.04")
	rep := buildReport(t, jr, Config{MinLabeled: 1})
	a := rep.Arena
	if a == nil {
		t.Fatalf("Arena is nil, want populated")
	}
	if a.Decisions != 2 {
		t.Fatalf("arena decisions = %d, want 2 unique decisionIds", a.Decisions)
	}
	almostEqual(t, "agreement", a.AgreementShare, 0.5)
	if len(a.Models) != 2 {
		t.Fatalf("arena models = %d, want 2", len(a.Models))
	}
	m1, m2 := a.Models[0], a.Models[1]
	if m1.Model != "m1" || m1.Verdicts != 2 {
		t.Fatalf("m1 = %+v, want 2 verdicts", m1)
	}
	decimalIs(t, "m1 avg allow", m1.AvgAllowReturn, "0.015000000000")
	almostEqual(t, "m1 auc", m1.AUC, 0)
	decimalIs(t, "m1 cost", m1.CostUSD, "0.03")
	if m2.Model != "m2" || m2.Verdicts != 2 {
		t.Fatalf("m2 = %+v, want 2 verdicts", m2)
	}
	decimalIs(t, "m2 avg allow", m2.AvgAllowReturn, "0")
	almostEqual(t, "m2 auc", m2.AUC, 1)
	decimalIs(t, "m2 cost", m2.CostUSD, "0.07")
}
