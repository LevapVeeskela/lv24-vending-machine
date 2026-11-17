package report

import (
	"context"
	"encoding/json"
	"fmt"
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
	entryLagMS                int64
	entryShifted              bool
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
		SignalID:     sig,
		DecisionID:   did,
		Instrument:   domain.Instrument{Venue: "test-venue", Symbol: "BTC/USDT"},
		Side:         domain.SideBuy,
		Timeframe:    "1h",
		EntryTS:      d.entry,
		EntryPrice:   domain.MustParseDecimal("100"),
		ExitPrice:    domain.MustParseDecimal("101"),
		HorizonBars:  d.horizon,
		FeeBps:       d.feeBps,
		NetReturn:    domain.MustParseDecimal(net),
		GrossReturn:  domain.MustParseDecimal(net),
		LabeledAt:    testClock,
		Backfill:     d.backfill,
		PriceSource:  d.priceSource,
		EntryLagMS:   d.entryLagMS,
		EntryShifted: d.entryShifted,
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
	day4 := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	day5 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	specs := []decSpec{
		{id: "d1", sig: "s1", model: "alpha", cost: "0.010", net: "0.10", latency: 100, p: fp(0.9), verdict: domain.VerdictAllow, policy: "p1", entry: day1, feeBps: 10, horizon: 12, priceSource: "md"},
		{id: "d2", sig: "s2", model: "alpha", cost: "0.020", net: "-0.02", latency: 200, p: fp(0.6), verdict: domain.VerdictAllow, policy: "p1", entry: day2, feeBps: 10, horizon: 12, priceSource: "md"},
		{id: "d3", sig: "s3", model: "beta", cost: "0.030", net: "0.04", latency: 300, p: fp(0.5), verdict: domain.VerdictAllow, policy: "p1", entry: day3, feeBps: 10, horizon: 12, priceSource: "md"},
		{id: "d4", sig: "s4", model: "beta", cost: "0.040", net: "0.02", latency: 400, p: fp(0.2), verdict: domain.VerdictReject, policy: "p1", entry: day4, feeBps: 10, horizon: 12, priceSource: "md"},
		{id: "d5", sig: "s5", model: "gamma", cost: "0.050", net: "-0.03", latency: 500, p: fp(0.1), verdict: domain.VerdictReject, policy: "p1", entry: day5, feeBps: 10, horizon: 12, priceSource: "md"},
		{id: "d6", sig: "s6", model: "gamma", cost: "0.060", net: "-0.01", latency: 600, p: fp(0.4), verdict: domain.VerdictAbstain, policy: "p1", entry: day5, feeBps: 10, horizon: 12, priceSource: "md"},
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
	// Six pairs are below the 30-pair threshold, so the metrics are computed
	// but not declared valid.
	if c.CalibrationSufficient {
		t.Fatalf("CalibrationSufficient = true with 6 pairs, want false")
	}
	// The interval needs at least 30 observations per group; three ALLOW and
	// two REJECT keep only the point mean.
	if c.Difference != nil {
		t.Fatalf("Difference = %+v, want nil for 3 ALLOW / 2 REJECT", c.Difference)
	}
	decimalIs(t, "difference point mean",
		c.Groups["ALLOW"].AvgNetReturn.Sub(c.Groups["REJECT"].AvgNetReturn), "0.045000000000")
}

// TestBootstrapDeterministicAndNoise contains a reproducible CI and one
// that spans zero for symmetric noise across six UTC days.
func TestBootstrapDeterministicAndNoise(t *testing.T) {
	jr := &fakeJournal{}
	for i := 1; i <= 6; i++ {
		day := time.Date(2026, 11, i, 0, 0, 0, 0, time.UTC)
		allow, reject := "0.05", "-0.05"
		if i%2 == 0 {
			allow, reject = "-0.05", "0.05"
		}
		// Thirty outcomes per group per day clear the group-size floor while
		// keeping the day-level means (and therefore the interval) the same.
		for j := 0; j < 30; j++ {
			aid := fmt.Sprintf("na%d-%02d", i, j)
			rid := fmt.Sprintf("nr%d-%02d", i, j)
			appendSpec(t, jr, decSpec{id: aid, sig: aid,
				model: "m", cost: "0.01", net: allow, p: fp(0.6), verdict: domain.VerdictAllow, policy: "p1", entry: day})
			appendSpec(t, jr, decSpec{id: rid, sig: rid,
				model: "m", cost: "0.01", net: reject, p: fp(0.4), verdict: domain.VerdictReject, policy: "p1", entry: day})
		}
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
	if d1.Days != 6 {
		t.Fatalf("difference days = %d, want 6", d1.Days)
	}
	if !d1.ContainsZero {
		t.Fatalf("noise CI = [%s,%s], want it to contain zero",
			d1.Low.String(), d1.High.String())
	}
}

// TestTwoDaysNoDifference drops the CI when fewer than five UTC days are
// available while keeping the point mean.
func TestTwoDaysNoDifference(t *testing.T) {
	jr := &fakeJournal{}
	day1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		aid := fmt.Sprintf("t-a%02d", i)
		rid := fmt.Sprintf("t-r%02d", i)
		day := day1
		if i%2 == 1 {
			day = day2
		}
		appendSpec(t, jr, decSpec{id: aid, sig: aid, model: "m", cost: "0.01", net: "0.10",
			p: fp(0.7), verdict: domain.VerdictAllow, policy: "p1", entry: day})
		appendSpec(t, jr, decSpec{id: rid, sig: rid, model: "m", cost: "0.01", net: "-0.04",
			p: fp(0.3), verdict: domain.VerdictReject, policy: "p1", entry: day})
	}
	b, err := New(jr, testDeps(), Config{MinLabeled: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rep.Comparisons == nil {
		t.Fatalf("Comparisons is nil")
	}
	if rep.Comparisons.Groups["ALLOW"].Count != 30 || rep.Comparisons.Groups["REJECT"].Count != 30 {
		t.Fatalf("groups = %+v, want 30/30", rep.Comparisons.Groups)
	}
	if rep.Comparisons.Difference != nil {
		t.Fatalf("Difference = %+v, want nil for 2 days", rep.Comparisons.Difference)
	}
	if b.Days() != 2 {
		t.Fatalf("Days() = %d, want 2", b.Days())
	}
}

// TestCalibrationNoPairsWithFullLabeling: 100 labeled outcomes without a
// risk_check probability are "sufficient" for comparisons but have zero
// calibration pairs, so the calibration metrics must not be declared valid.
func TestCalibrationNoPairsWithFullLabeling(t *testing.T) {
	jr := &fakeJournal{}
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		verdict := domain.VerdictAllow
		net := "0.01"
		if i%2 == 1 {
			verdict = domain.VerdictReject
			net = "-0.01"
		}
		id := fmt.Sprintf("n%03d", i)
		appendSpec(t, jr, decSpec{id: id, sig: id, model: "m", cost: "0.01", net: net,
			verdict: verdict, policy: "p1", entry: base.AddDate(0, 0, i%7)})
	}
	rep := buildReport(t, jr, Config{})
	if rep.LabeledCount != 100 || !rep.Sufficient {
		t.Fatalf("labeled/sufficient = %d/%v, want 100/true", rep.LabeledCount, rep.Sufficient)
	}
	c := rep.Comparisons
	if c == nil {
		t.Fatalf("Comparisons is nil")
	}
	if c.CalibrationCount != 0 {
		t.Fatalf("CalibrationCount = %d, want 0", c.CalibrationCount)
	}
	if c.CalibrationSufficient {
		t.Fatalf("CalibrationSufficient = true with 0 pairs, want false")
	}
}

// TestCalibrationThreshold: 29 pairs stay invalid, 30 pairs become valid.
func TestCalibrationThreshold(t *testing.T) {
	for _, tc := range []struct {
		pairs int
		want  bool
	}{{29, false}, {30, true}} {
		jr := &fakeJournal{}
		base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
		for i := 0; i < tc.pairs; i++ {
			id := fmt.Sprintf("th%02d", i)
			appendSpec(t, jr, decSpec{id: id, sig: id, model: "m", cost: "0.01", net: "0.01",
				p: fp(0.6), verdict: domain.VerdictAllow, policy: "p1", entry: base.AddDate(0, 0, i%7)})
		}
		rep := buildReport(t, jr, Config{MinLabeled: 1})
		c := rep.Comparisons
		if c == nil {
			t.Fatalf("%d pairs: Comparisons is nil", tc.pairs)
		}
		if c.CalibrationCount != tc.pairs {
			t.Fatalf("%d pairs: CalibrationCount = %d", tc.pairs, c.CalibrationCount)
		}
		if c.CalibrationSufficient != tc.want {
			t.Fatalf("%d pairs: CalibrationSufficient = %v, want %v",
				tc.pairs, c.CalibrationSufficient, tc.want)
		}
	}
}

// TestDifferenceRequiresGroupSize: one ALLOW among 99 REJECT must not produce
// a confidence interval that looks significant.
func TestDifferenceRequiresGroupSize(t *testing.T) {
	jr := &fakeJournal{}
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("g%03d", i)
		verdict := domain.VerdictReject
		net := "-0.01"
		if i == 0 {
			verdict = domain.VerdictAllow
			net = "0.50"
		}
		appendSpec(t, jr, decSpec{id: id, sig: id, model: "m", cost: "0.01", net: net,
			p: fp(0.6), verdict: verdict, policy: "p1", entry: base.AddDate(0, 0, i%7)})
	}
	rep := buildReport(t, jr, Config{})
	c := rep.Comparisons
	if c == nil {
		t.Fatalf("Comparisons is nil")
	}
	if c.Groups["ALLOW"].Count != 1 || c.Groups["REJECT"].Count != 99 {
		t.Fatalf("groups = %+v, want ALLOW=1 REJECT=99", c.Groups)
	}
	if c.Difference != nil {
		t.Fatalf("Difference = %+v, want nil for 1 ALLOW / 99 REJECT", c.Difference)
	}
}

// TestDifferenceThirtyPerGroup keeps the interval once both groups have 30
// observations over at least five days.
func TestDifferenceThirtyPerGroup(t *testing.T) {
	jr := &fakeJournal{}
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		aid := fmt.Sprintf("da%02d", i)
		rid := fmt.Sprintf("dr%02d", i)
		day := base.AddDate(0, 0, i%5)
		appendSpec(t, jr, decSpec{id: aid, sig: aid, model: "m", cost: "0.01", net: "0.10",
			p: fp(0.7), verdict: domain.VerdictAllow, policy: "p1", entry: day})
		appendSpec(t, jr, decSpec{id: rid, sig: rid, model: "m", cost: "0.01", net: "-0.04",
			p: fp(0.3), verdict: domain.VerdictReject, policy: "p1", entry: day})
	}
	rep := buildReport(t, jr, Config{MinLabeled: 60})
	c := rep.Comparisons
	if c == nil {
		t.Fatalf("Comparisons is nil")
	}
	if c.Groups["ALLOW"].Count != 30 || c.Groups["REJECT"].Count != 30 {
		t.Fatalf("groups = %+v, want 30/30", c.Groups)
	}
	d := c.Difference
	if d == nil {
		t.Fatalf("Difference is nil, want an interval for 30/30")
	}
	if d.Days != 5 {
		t.Fatalf("difference days = %d, want 5", d.Days)
	}
	if d.BootstrapReps <= 0 || d.BootstrapReps > DefaultBootstrapReps {
		t.Fatalf("bootstrap reps = %d, want (0,%d]", d.BootstrapReps, DefaultBootstrapReps)
	}
	decimalIs(t, "difference mean", d.Mean, "0.140000000000")
	if d.Low.Cmp(d.High) > 0 {
		t.Fatalf("interval low %s > high %s", d.Low.String(), d.High.String())
	}
}

// TestSkipCountsInAll makes the "all" group cover every included outcome.
func TestSkipCountsInAll(t *testing.T) {
	jr := &fakeJournal{}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	appendSpec(t, jr, decSpec{id: "k1", sig: "k1", model: "m", cost: "0.01", net: "0.03",
		verdict: domain.VerdictSkip, policy: "p1", entry: now})
	rep := buildReport(t, jr, Config{MinLabeled: 1})
	if rep.LabeledCount != 1 || rep.Comparisons == nil {
		t.Fatalf("labeled=%d comparisons=%v, want 1/populated", rep.LabeledCount, rep.Comparisons != nil)
	}
	c := rep.Comparisons
	if got := c.Groups["all"].Count; got != rep.LabeledCount {
		t.Fatalf("groups[all].Count = %d, want LabeledCount %d", got, rep.LabeledCount)
	}
	decimalIs(t, "groups all sum", c.Groups["all"].SumNetReturn, c.AllEntriesReturn.String())
	if c.Groups["ALLOW"].Count != 0 || c.Groups["REJECT"].Count != 0 || c.Groups["ABSTAIN"].Count != 0 {
		t.Fatalf("SKIP leaked into a verdict group: %+v", c.Groups)
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

// TestArenaPolicyFilter keeps only arena events of the selected policy.
func TestArenaPolicyFilter(t *testing.T) {
	polA := strings.Repeat("a", 64)
	polB := strings.Repeat("b", 64)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	jr := &fakeJournal{}
	appendSpec(t, jr, decSpec{id: "ad1", sig: "ad1", model: "primary", cost: "0.001",
		net: "0.05", p: fp(0.7), verdict: domain.VerdictAllow, policy: polA, entry: now})
	appendSpec(t, jr, decSpec{id: "ad2", sig: "ad2", model: "primary", cost: "0.001",
		net: "0.20", p: fp(0.8), verdict: domain.VerdictAllow, policy: polB, entry: now})
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
	mkArena("ad1", "m1", domain.VerdictAllow, 0.6, "0.01")
	mkArena("ad2", "m2", domain.VerdictAllow, 0.9, "0.02")
	picked := buildReport(t, jr, Config{MinLabeled: 1, PolicyHash: polA})
	if picked.Arena == nil || picked.Arena.Decisions != 1 {
		t.Fatalf("arena = %+v, want 1 selected-policy decision", picked.Arena)
	}
	if len(picked.Arena.Models) != 1 || picked.Arena.Models[0].Model != "m1" ||
		picked.Arena.Models[0].Verdicts != 1 {
		t.Fatalf("arena models = %+v, want only m1", picked.Arena.Models)
	}
	if picked.Arena.Models[0].CostUSD.String() != "0.01" {
		t.Fatalf("arena m1 cost = %q, want 0.01", picked.Arena.Models[0].CostUSD.String())
	}
	// Without an explicit policy the highest-seq verdict (polB) selects ad2.
	auto := buildReport(t, jr, Config{MinLabeled: 1})
	if auto.Arena == nil || auto.Arena.Decisions != 1 || len(auto.Arena.Models) != 1 ||
		auto.Arena.Models[0].Model != "m2" {
		t.Fatalf("auto arena = %+v, want only m2", auto.Arena)
	}
}

// TestArenaAgreementDenominator divides by the considered arena events of
// the selected policy, not by every recorded event.
func TestArenaAgreementDenominator(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	jr := &fakeJournal{}
	for _, s := range []decSpec{
		{id: "d1", sig: "d1", model: "primary", cost: "0.001", net: "0.05", p: fp(0.7), verdict: domain.VerdictAllow, policy: "p1", entry: now},
		{id: "d2", sig: "d2", model: "primary", cost: "0.001", net: "-0.02", p: fp(0.3), verdict: domain.VerdictReject, policy: "p1", entry: now},
	} {
		appendSpec(t, jr, s)
	}
	mkArena := func(id, model string, v domain.Verdict) {
		appendEv(t, jr, domain.Event{
			Kind:       domain.EventArenaDecision,
			DecisionID: id,
			Payload: mustPayload(t, arenaPayload{
				DecisionID: id,
				Model:      model,
				Response: domain.DecisionResponse{
					Model: model,
					Usage: domain.Usage{CostUSD: domain.MustParseDecimal("0.01"), LatencyMS: 10},
				},
				Verdict: domain.GateVerdict{Verdict: v, Code: "arena", Reasons: []string{}, Checks: []domain.Check{}},
			}),
		})
	}
	mkArena("d1", "m1", domain.VerdictAllow)    // agrees
	mkArena("d2", "m1", domain.VerdictAllow)    // disagrees
	mkArena("d1", "m2", domain.VerdictAllow)    // agrees
	mkArena("d2", "m2", domain.VerdictReject)   // agrees
	mkArena("ghost", "m1", domain.VerdictAllow) // no verdict: ignored
	rep := buildReport(t, jr, Config{MinLabeled: 1})
	a := rep.Arena
	if a == nil {
		t.Fatalf("Arena is nil, want populated")
	}
	// 3 of the 4 considered events agree; the ghost event must not be a
	// denominator term (that would give 3/5 = 0.6).
	almostEqual(t, "agreement", a.AgreementShare, 0.75)
	if a.AgreementShare < 0 || a.AgreementShare > 1 {
		t.Fatalf("agreement = %v, want within [0,1]", a.AgreementShare)
	}
	if a.Decisions != 2 {
		t.Fatalf("arena decisions = %d, want 2", a.Decisions)
	}
}

// TestLabelPairsLatestWins keeps only the latest (horizon, fee) pair in the
// comparison and counts the rest in OtherLabelParams.
func TestLabelPairsLatestWins(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	jr := &fakeJournal{}
	for _, s := range []decSpec{
		{id: "d1", sig: "d1", model: "m", cost: "0.01", net: "0.10", p: fp(0.8), verdict: domain.VerdictAllow, policy: "p1", entry: now, horizon: 12, feeBps: 10},
		{id: "d2", sig: "d2", model: "m", cost: "0.01", net: "-0.02", p: fp(0.2), verdict: domain.VerdictAllow, policy: "p1", entry: now, horizon: 12, feeBps: 10},
		{id: "d3", sig: "d3", model: "m", cost: "0.01", net: "0.04", p: fp(0.9), verdict: domain.VerdictAllow, policy: "p1", entry: now, horizon: 24, feeBps: 20},
		{id: "d4", sig: "d4", model: "m", cost: "0.01", net: "0.02", p: fp(0.6), verdict: domain.VerdictReject, policy: "p1", entry: now, horizon: 24, feeBps: 20},
	} {
		appendSpec(t, jr, s)
	}
	rep := buildReport(t, jr, Config{MinLabeled: 1})
	if rep.LabeledCount != 4 {
		t.Fatalf("LabeledCount = %d, want 4", rep.LabeledCount)
	}
	// d4 carries the last outcome.labeled event, so (24, 20) wins.
	if rep.OtherLabelParams != 2 {
		t.Fatalf("OtherLabelParams = %d, want 2", rep.OtherLabelParams)
	}
	a := rep.Assumptions
	if a == nil || a.HorizonBars != 24 || a.FeeBpsRoundTrip != 40 {
		t.Fatalf("Assumptions = %+v, want horizon 24, fees 40", a)
	}
	c := rep.Comparisons
	if c == nil {
		t.Fatalf("Comparisons is nil")
	}
	if got := c.Groups["all"].Count; got != 2 {
		t.Fatalf("groups[all].Count = %d, want 2 (only the latest pair)", got)
	}
	decimalIs(t, "groups all sum", c.Groups["all"].SumNetReturn, "0.06")
	decimalIs(t, "AllowReturn", c.AllowReturn, "0.04")
	if c.CalibrationCount != 2 {
		t.Fatalf("CalibrationCount = %d, want 2", c.CalibrationCount)
	}
	if c.Groups["REJECT"].Count != 1 || c.Groups["ALLOW"].Count != 1 {
		t.Fatalf("groups = %+v, want ALLOW=1 REJECT=1", c.Groups)
	}
}

// TestEntryLagAssumptions pins the lag fractions, median, p95 and shifted
// count of four one-hour outcomes.
func TestEntryLagAssumptions(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	jr := &fakeJournal{}
	lags := []struct {
		ms      int64
		shifted bool
	}{
		{360000, false},  // 0.10 bar
		{900000, true},   // 0.25 bar
		{1800000, false}, // 0.50 bar
		{2700000, false}, // 0.75 bar
	}
	for i, lag := range lags {
		id := "l" + string(rune('1'+i))
		appendSpec(t, jr, decSpec{
			id: id, sig: id, model: "m", cost: "0.01", net: "0.01",
			p: fp(0.6), verdict: domain.VerdictAllow, policy: "p1", entry: now,
			horizon: 12, feeBps: 10, entryLagMS: lag.ms, entryShifted: lag.shifted,
		})
	}
	rep := buildReport(t, jr, Config{MinLabeled: 1})
	a := rep.Assumptions
	if a == nil {
		t.Fatalf("Assumptions is nil")
	}
	// Median of {0.10,0.25,0.50,0.75} = 0.375; nearest-rank p95 with n=4
	// is rank ceil(3.8)=4 -> 0.75.
	almostEqual(t, "EntryLagMedian", a.EntryLagMedian, 0.375)
	almostEqual(t, "EntryLagP95", a.EntryLagP95, 0.75)
	if a.EntryShifted != 1 {
		t.Fatalf("EntryShifted = %d, want 1", a.EntryShifted)
	}
}

// TestParseTimeframe covers the shared supported bar units and rejects junk.
func TestParseTimeframe(t *testing.T) {
	for _, tc := range []struct {
		tf   string
		want time.Duration
		ok   bool
	}{
		{"5m", 5 * time.Minute, true},
		{"1h", time.Hour, true},
		{"1d", 24 * time.Hour, true},
		{"2w", 14 * 24 * time.Hour, true},
		{"", 0, false},
		{"h", 0, false},
		{"0h", 0, false},
		{"1x", 0, false},
		{"-1h", 0, false},
		{"1H", 0, false},
	} {
		got, err := domain.ParseTimeframe(tc.tf)
		if (err == nil) != tc.ok || got != tc.want {
			t.Fatalf("ParseTimeframe(%q) = %v/%v, want %v/%v", tc.tf, got, err, tc.want, tc.ok)
		}
	}
}

// TestLabelingProgressCounts pins the awaiting-labeling,
// no-outcome-by-deadline and source-rejected counters used by the report
// header. Exit requests do not need an outcome, a labeled signal leaves the
// queue and a timeout record counts as a deadline miss.
func TestLabelingProgressCounts(t *testing.T) {
	jr := &fakeJournal{}
	for _, sig := range []string{"s1", "s2", "s3", "s5"} {
		appendEv(t, jr, domain.Event{
			Kind:       domain.EventGateRequest,
			DecisionID: "d-" + sig,
			Payload:    gateRequestPayload(t, sig),
		})
	}
	exitReq := domain.GateRequest{SignalID: "s4", IntentKind: domain.IntentExit}
	appendEv(t, jr, domain.Event{
		Kind:       domain.EventGateRequest,
		DecisionID: "d-s4",
		Payload:    mustPayload(t, exitReq),
	})
	appendEv(t, jr, domain.Event{
		Kind:       domain.EventGateVerdict,
		DecisionID: "d-s1",
		Payload:    verdictPolicyPayload(t, domain.VerdictAllow, "p1"),
	})
	appendEv(t, jr, domain.Event{
		Kind:       domain.EventOutcomeLabeled,
		DecisionID: "d-s1",
		Payload: outcomeOptsPayload(t, "s1", "d-s1", "0.01",
			decSpec{entry: testClock, horizon: 1, feeBps: 0}),
	})
	appendEv(t, jr, domain.Event{
		Kind:    domain.EventOutcomeUnlabelable,
		Payload: mustPayload(t, domain.OutcomeUnlabelable{SignalID: "s2", DecisionID: "d-s2", Reason: "timeout"}),
	})
	appendEv(t, jr, domain.Event{
		Kind:    domain.EventOutcomeUnlabelable,
		Payload: mustPayload(t, domain.OutcomeUnlabelable{SignalID: "s5", DecisionID: "d-s5", Reason: "source_rejected"}),
	})

	rep := buildReport(t, jr, Config{})
	if rep.LabeledCount != 1 {
		t.Fatalf("labeled = %d, want 1", rep.LabeledCount)
	}
	if rep.AwaitingLabeling != 1 {
		t.Fatalf("awaitingLabeling = %d, want 1 (s3)", rep.AwaitingLabeling)
	}
	if rep.NoOutcomeByDeadline != 1 {
		t.Fatalf("noOutcomeByDeadline = %d, want 1 (s2)", rep.NoOutcomeByDeadline)
	}
	if rep.SourceRejected != 1 {
		t.Fatalf("sourceRejected = %d, want 1 (s5)", rep.SourceRejected)
	}
}

// TestEntryLagOnlyWhenRecorded: an outcome written before entryLagMs existed
// must not be read as a zero lag. With one recorded 0.5-bar lag and one old
// outcome, the median stays 0.5 bar (not 0.25).
func TestEntryLagOnlyWhenRecorded(t *testing.T) {
	jr := &fakeJournal{}
	for _, id := range []string{"d-new", "d-old"} {
		appendEv(t, jr, domain.Event{
			Kind:       domain.EventGateVerdict,
			DecisionID: id,
			Payload:    verdictPolicyPayload(t, domain.VerdictAllow, "p1"),
		})
	}
	appendEv(t, jr, domain.Event{
		Kind:       domain.EventOutcomeLabeled,
		DecisionID: "d-new",
		Payload: outcomeOptsPayload(t, "s-new", "d-new", "0.01", decSpec{
			entry: testClock, horizon: 1, feeBps: 0, entryLagMS: 1800000,
		}),
	})
	// Old journal shape: no entryLagMs field at all.
	old := []byte(`{"signalId":"s-old","decisionId":"d-old",` +
		`"instrument":{"venue":"test-venue","symbol":"BTC/USDT"},"side":"buy",` +
		`"timeframe":"1h","entryPrice":"100","exitPrice":"101",` +
		`"horizonBars":1,"feeBps":0,"netReturn":"0.01","grossReturn":"0.01"}`)
	appendEv(t, jr, domain.Event{
		Kind:       domain.EventOutcomeLabeled,
		DecisionID: "d-old",
		Payload:    old,
	})

	rep := buildReport(t, jr, Config{})
	if rep.LabeledCount != 2 {
		t.Fatalf("labeled = %d, want 2", rep.LabeledCount)
	}
	a := rep.Assumptions
	if a == nil {
		t.Fatalf("Assumptions is nil")
	}
	almostEqual(t, "EntryLagMedian", a.EntryLagMedian, 0.5)
	if a.EntryLagMedianMS != 1800000 {
		t.Fatalf("EntryLagMedianMS = %d, want 1800000", a.EntryLagMedianMS)
	}
}
