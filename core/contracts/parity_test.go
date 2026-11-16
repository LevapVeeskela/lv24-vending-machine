package contracts

// Domain↔contracts parity: domain is the runtime model, contracts/* is the
// wire boundary. Marshal a domain value and decode it into the matching
// generated contracts type (and back for requests). Any future field drift
// that breaks wire compatibility fails here.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

var parityTS = time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC)

const (
	parityPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"
	parityHash     = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	parityPolicy   = "b9ae3472b8ff26cc2889f5db91eb555ed09a935a112f16984ea1014a69e59b4f"
	parityULID     = "01J9ZQ8W3E12AB3456789CDEFG"
)

func fptr(f float64) *float64 { return &f }

func mustWire(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return b
}

func TestParityDecisionRequest(t *testing.T) {
	d := domain.DecisionRequest{
		Model: "dre-x-1",
		State: map[string]any{"rsi14": 62.5},
		Questions: []domain.Question{
			{
				ID:   "direction",
				Type: domain.QuestionChoice,
				Options: []domain.Option{
					{Key: "long", Description: "Open or hold a long position"},
					{Key: "flat", Description: "Stay out of the market"},
				},
			},
			{ID: "volatility", Type: domain.QuestionNoul},
		},
	}
	var c DecisionRequest
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.DecisionRequest not wire-compatible: %v", err)
	}
	if c.Model != d.Model || len(c.Questions) != 2 || c.Questions[0].ID != "direction" {
		t.Fatalf("round-trip mismatch: %+v", c)
	}

	var back domain.DecisionRequest
	if err := json.Unmarshal(mustWire(t, c), &back); err != nil {
		t.Fatalf("contracts.DecisionRequest not domain-compatible: %v", err)
	}
	if back.Model != d.Model || len(back.Questions) != len(d.Questions) {
		t.Fatalf("reverse mismatch: %+v", back)
	}
}

func TestParityDecisionResponse(t *testing.T) {
	d := domain.DecisionResponse{
		Answers: map[string]domain.Answer{
			"direction": {
				Type:          domain.QuestionChoice,
				Choice:        "long",
				Probabilities: map[string]float64{"long": 0.6, "flat": 0.3, "short": 0.1},
				Confidence:    fptr(0.4),
			},
		},
		Model:          "dre-x-1",
		RequestedModel: "dre-x-1",
		Usage: domain.Usage{
			InputTokens:     120,
			OutputTokens:    40,
			CostUSD:         domain.MustParseDecimal("0.0000072"),
			LatencyMS:       12,
			VendorLatencyMS: 9,
		},
		RequestID: "req-001",
	}
	var c DecisionResponse
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.DecisionResponse not wire-compatible: %v", err)
	}
	if c.Model != "dre-x-1" || c.Usage.InputTokens != 120 {
		t.Fatalf("round-trip mismatch: %+v", c)
	}
	if c.Usage.LatencyMs == nil || *c.Usage.LatencyMs != 12 {
		t.Fatalf("latencyMs lost on the wire: %+v", c.Usage)
	}
	if c.Usage.VendorLatencyMs == nil || *c.Usage.VendorLatencyMs != 9 {
		t.Fatalf("vendorLatencyMs lost on the wire: %+v", c.Usage)
	}
}

func TestParityGateRequest(t *testing.T) {
	d := domain.GateRequest{
		SignalID:   "sig-001",
		Instrument: domain.Instrument{Venue: "binance", Symbol: "BTCUSDT"},
		Side:       domain.SideBuy,
		IntentKind: domain.IntentEntry,
		Timeframe:  "1h",
		TS:         parityTS,
		Price:      domain.MustParseDecimal("97500.50"),
		Source:     "strategy:trend-v1",
		State:      map[string]float64{"rsi14": 62.5},
	}
	var c GateRequest
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.GateRequest not wire-compatible: %v", err)
	}
	if string(c.Side) != "buy" || string(c.Price) != "97500.50" {
		t.Fatalf("round-trip mismatch: %+v", c)
	}

	var back domain.GateRequest
	if err := json.Unmarshal(mustWire(t, c), &back); err != nil {
		t.Fatalf("contracts.GateRequest not domain-compatible: %v", err)
	}
	if back.SignalID != d.SignalID || back.Instrument.Symbol != "BTCUSDT" {
		t.Fatalf("reverse mismatch: %+v", back)
	}
}

func TestParityGateVerdict(t *testing.T) {
	d := domain.GateVerdict{
		Verdict:    domain.VerdictAllow,
		Code:       "allow",
		Reasons:    []string{"all gate checks passed"},
		Checks:     []domain.Check{{Name: "risk_check", Passed: true, Detail: "within daily loss limit"}},
		DecisionID: parityULID,
		Model:      "dre-x-1",
		PolicyHash: parityPolicy,
		DecidedAt:  parityTS,
	}
	var c GateVerdict
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.GateVerdict not wire-compatible: %v", err)
	}
	if string(c.Verdict) != "ALLOW" || c.PolicyHash != parityPolicy {
		t.Fatalf("round-trip mismatch: %+v", c)
	}
}

func TestParityEvent(t *testing.T) {
	d := domain.Event{
		Seq:           0,
		EventID:       parityULID,
		Kind:          "run.started",
		TS:            parityTS,
		SchemaVersion: 1,
		Payload:       json.RawMessage(`{"mode":"shadow"}`),
		CodeVersion:   "v0.0.0-m0",
		PrevHash:      parityPrevHash,
		Hash:          parityHash,
		RunID:         "run-001",
		PolicyHash:    parityPolicy,
	}
	var c Event
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.Event not wire-compatible: %v", err)
	}
	if c.Seq != 0 || c.Kind != "run.started" || c.SchemaVersion != 1 {
		t.Fatalf("round-trip mismatch: %+v", c)
	}
}

func TestParityOutcome(t *testing.T) {
	d := domain.Outcome{
		SignalID:     "sig-001",
		DecisionID:   parityULID,
		Instrument:   domain.Instrument{Venue: "binance", Symbol: "BTCUSDT"},
		Side:         domain.SideBuy,
		Timeframe:    "1h",
		EntryTS:      parityTS,
		EntryPrice:   domain.MustParseDecimal("97500.50"),
		ExitTS:       parityTS.Add(6 * time.Hour),
		ExitPrice:    domain.MustParseDecimal("98200.75"),
		HorizonBars:  6,
		FeeBps:       5,
		GrossReturn:  domain.MustParseDecimal("0.007184"),
		NetReturn:    domain.MustParseDecimal("0.006684"),
		MFE:          domain.MustParseDecimal("0.009100"),
		MAE:          domain.MustParseDecimal("-0.002300"),
		CandlesHash:  parityHash,
		LabeledAt:    parityTS.Add(6*time.Hour + 5*time.Minute),
		ReceivedAt:   parityTS,
		Backfill:     true,
		PriceSource:  "marketdata-binance",
		EntryLagMS:   120000,
		EntryShifted: true,
	}
	var c Outcome
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.Outcome not wire-compatible: %v", err)
	}
	if c.SignalID != "sig-001" || string(c.Side) != "buy" {
		t.Fatalf("round-trip mismatch: %+v", c)
	}
	if string(c.EntryPrice) != "97500.50" || string(c.NetReturn) != "0.006684" {
		t.Fatalf("money lost on the wire: %+v", c)
	}
	if string(c.Mae) != "-0.002300" || c.CandlesHash != parityHash {
		t.Fatalf("tail fields lost on the wire: %+v", c)
	}
	if c.DecisionID == nil || *c.DecisionID != parityULID {
		t.Fatalf("decisionId lost on the wire: %+v", c)
	}
	if c.ReceivedAt == nil || !c.ReceivedAt.Equal(parityTS) {
		t.Fatalf("receivedAt lost on the wire: %+v", c)
	}
	if c.Backfill == nil || !*c.Backfill {
		t.Fatalf("backfill lost on the wire: %+v", c)
	}
	if c.PriceSource == nil || *c.PriceSource != "marketdata-binance" {
		t.Fatalf("priceSource lost on the wire: %+v", c)
	}
	if c.EntryLagMs == nil || *c.EntryLagMs != 120000 {
		t.Fatalf("entryLagMs lost on the wire: %+v", c)
	}
	if c.EntryShifted == nil || !*c.EntryShifted {
		t.Fatalf("entryShifted lost on the wire: %+v", c)
	}

	var back domain.Outcome
	if err := json.Unmarshal(mustWire(t, c), &back); err != nil {
		t.Fatalf("contracts.Outcome not domain-compatible: %v", err)
	}
	if back.SignalID != d.SignalID || back.Side != d.Side || back.HorizonBars != 6 || back.FeeBps != 5 {
		t.Fatalf("reverse mismatch: %+v", back)
	}
	if back.EntryPrice.String() != "97500.50" || back.NetReturn.String() != "0.006684" {
		t.Fatalf("reverse money mismatch: %+v", back)
	}
	if !back.ReceivedAt.Equal(parityTS) || !back.Backfill || back.PriceSource != "marketdata-binance" {
		t.Fatalf("reverse wire fields mismatch: %+v", back)
	}
	if back.EntryLagMS != 120000 || !back.EntryShifted {
		t.Fatalf("reverse entry lag mismatch: %+v", back)
	}
}

func TestParityReport(t *testing.T) {
	d := domain.Report{
		GeneratedAt:          parityTS,
		LabeledCount:         42,
		AwaitingLabeling:     4,
		NoOutcomeByDeadline:  2,
		SourceRejected:       1,
		Sufficient:           true,
		VerdictCounts:        map[string]int{"ALLOW": 25, "REJECT": 15, "ABSTAIN": 2},
		DecisionCostUSD:      domain.MustParseDecimal("0.0003024"),
		LatencyP50MS:         11,
		LatencyP95MS:         38,
		PolicyHash:           parityPolicy,
		OtherPolicyDecisions: 3,
		OtherLabelParams:     2,
		BackfillLabeled:      5,
		BackfillExcluded:     2,
		Assumptions: &domain.ReportAssumptions{
			HorizonBars:      12,
			FeeBpsRoundTrip:  20,
			PriceSource:      "marketdata-binance",
			EqualSize:        true,
			NoSlippage:       true,
			NoFunding:        true,
			EntryLagMedian:   0.02,
			EntryLagP95:      0.25,
			EntryLagMedianMS: 72000,
			EntryLagP95MS:    900000,
			EntryShifted:     1,
		},
		Arena: &domain.ArenaReport{
			Decisions:      10,
			AgreementShare: 0.8,
			Models: []domain.ArenaModelStats{
				{
					Model:          "dre-x-1",
					Verdicts:       10,
					AvgAllowReturn: domain.MustParseDecimal("0.001800"),
					AUC:            0.62,
					CostUSD:        domain.MustParseDecimal("0.0001512"),
				},
			},
		},
		Comparisons: &domain.ReportComparisons{
			AllEntriesReturn:  domain.MustParseDecimal("0.012400"),
			AllowReturn:       domain.MustParseDecimal("0.018200"),
			RejectedAvgReturn: domain.MustParseDecimal("-0.004100"),
			Brier:             0.21,
			ECE:               0.06,
			BaseRate:          0.55,
			ConstantBrier:     0.25,
			BrierSkillScore:   0.16,
			AUC:               0.62,
			CalibrationCount:  42,
			Groups: map[string]domain.GroupStats{
				"allow": {
					Count:        25,
					SumNetReturn: domain.MustParseDecimal("0.455000"),
					AvgNetReturn: domain.MustParseDecimal("0.018200"),
				},
			},
			Difference: &domain.DifferenceStats{
				Mean:          domain.MustParseDecimal("0.022300"),
				Low:           domain.MustParseDecimal("0.004100"),
				High:          domain.MustParseDecimal("0.040500"),
				ContainsZero:  false,
				BootstrapReps: 1000,
				Days:          7,
			},
			Reliability: []domain.ReliabilityBucket{
				{LowerBound: 0, Count: 10, MeanPredicted: 0.12, ObservedRate: 0.1},
				{LowerBound: 0.5, Count: 20, MeanPredicted: 0.62, ObservedRate: 0.6},
			},
			ByModel: []domain.ModelBreakdown{
				{
					Model:        "dre-x-1",
					Count:        42,
					Brier:        0.21,
					ECE:          0.06,
					NetReturnSum: domain.MustParseDecimal("0.280800"),
					CostUSD:      domain.MustParseDecimal("0.0003024"),
					AvgNetReturn: domain.MustParseDecimal("0.006686"),
				},
			},
		},
	}
	var c Report
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.Report not wire-compatible: %v", err)
	}
	if c.LabeledCount != 42 || !c.Sufficient || c.VerdictCounts["ALLOW"] != 25 {
		t.Fatalf("round-trip mismatch: %+v", c)
	}
	if string(c.DecisionCostUSD) != "0.0003024" {
		t.Fatalf("money lost on the wire: %+v", c)
	}
	if c.Comparisons == nil {
		t.Fatalf("comparisons lost on the wire: %+v", c)
	}
	if len(c.Comparisons.Reliability) != 2 || c.Comparisons.Reliability[1].MeanPredicted != 0.62 {
		t.Fatalf("reliability lost on the wire: %+v", c.Comparisons)
	}
	if len(c.Comparisons.ByModel) != 1 || c.Comparisons.ByModel[0].Model != "dre-x-1" {
		t.Fatalf("byModel lost on the wire: %+v", c.Comparisons)
	}
	if string(c.Comparisons.ByModel[0].NetReturnSum) != "0.280800" {
		t.Fatalf("byModel money lost on the wire: %+v", c.Comparisons.ByModel[0])
	}
	if c.Comparisons.ByModel[0].AvgNetReturn == nil || string(*c.Comparisons.ByModel[0].AvgNetReturn) != "0.006686" {
		t.Fatalf("byModel avgNetReturn lost on the wire: %+v", c.Comparisons.ByModel[0])
	}
	if c.Comparisons.BaseRate == nil || *c.Comparisons.BaseRate != 0.55 {
		t.Fatalf("baseRate lost on the wire: %+v", c.Comparisons)
	}
	if c.Comparisons.ConstantBrier == nil || *c.Comparisons.ConstantBrier != 0.25 {
		t.Fatalf("constantBrier lost on the wire: %+v", c.Comparisons)
	}
	if c.Comparisons.BrierSkillScore == nil || *c.Comparisons.BrierSkillScore != 0.16 {
		t.Fatalf("brierSkillScore lost on the wire: %+v", c.Comparisons)
	}
	if c.Comparisons.Auc == nil || *c.Comparisons.Auc != 0.62 {
		t.Fatalf("auc lost on the wire: %+v", c.Comparisons)
	}
	if c.Comparisons.CalibrationCount == nil || *c.Comparisons.CalibrationCount != 42 {
		t.Fatalf("calibrationCount lost on the wire: %+v", c.Comparisons)
	}
	if len(c.Comparisons.Groups) != 1 || c.Comparisons.Groups["allow"].Count != 25 {
		t.Fatalf("groups lost on the wire: %+v", c.Comparisons.Groups)
	}
	if string(c.Comparisons.Groups["allow"].SumNetReturn) != "0.455000" {
		t.Fatalf("groups money lost on the wire: %+v", c.Comparisons.Groups)
	}
	if c.Comparisons.Difference == nil || string(c.Comparisons.Difference.Mean) != "0.022300" {
		t.Fatalf("difference lost on the wire: %+v", c.Comparisons.Difference)
	}
	if c.Comparisons.Difference.ContainsZero || c.Comparisons.Difference.BootstrapReps != 1000 || c.Comparisons.Difference.Days != 7 {
		t.Fatalf("difference tail lost on the wire: %+v", c.Comparisons.Difference)
	}
	if c.PolicyHash == nil || *c.PolicyHash != parityPolicy {
		t.Fatalf("policyHash lost on the wire: %+v", c)
	}
	if c.OtherPolicyDecisions == nil || *c.OtherPolicyDecisions != 3 {
		t.Fatalf("otherPolicyDecisions lost on the wire: %+v", c)
	}
	if c.BackfillLabeled == nil || *c.BackfillLabeled != 5 {
		t.Fatalf("backfillLabeled lost on the wire: %+v", c)
	}
	if c.BackfillExcluded == nil || *c.BackfillExcluded != 2 {
		t.Fatalf("backfillExcluded lost on the wire: %+v", c)
	}
	if c.OtherLabelParams == nil || *c.OtherLabelParams != 2 {
		t.Fatalf("otherLabelParams lost on the wire: %+v", c)
	}
	if c.AwaitingLabeling == nil || *c.AwaitingLabeling != 4 {
		t.Fatalf("awaitingLabeling lost on the wire: %+v", c)
	}
	if c.NoOutcomeByDeadline == nil || *c.NoOutcomeByDeadline != 2 {
		t.Fatalf("noOutcomeByDeadline lost on the wire: %+v", c)
	}
	if c.SourceRejected == nil || *c.SourceRejected != 1 {
		t.Fatalf("sourceRejected lost on the wire: %+v", c)
	}
	if c.Assumptions == nil || c.Assumptions.HorizonBars != 12 || c.Assumptions.FeeBpsRoundTrip != 20 {
		t.Fatalf("assumptions lost on the wire: %+v", c.Assumptions)
	}
	if c.Assumptions.EntryLagMedian == nil || *c.Assumptions.EntryLagMedian != 0.02 {
		t.Fatalf("entryLagMedian lost on the wire: %+v", c.Assumptions)
	}
	if c.Assumptions.EntryLagP95 == nil || *c.Assumptions.EntryLagP95 != 0.25 {
		t.Fatalf("entryLagP95 lost on the wire: %+v", c.Assumptions)
	}
	if c.Assumptions.EntryShifted == nil || *c.Assumptions.EntryShifted != 1 {
		t.Fatalf("entryShifted lost on the wire: %+v", c.Assumptions)
	}
	if c.Assumptions.EntryLagMedianMs == nil || *c.Assumptions.EntryLagMedianMs != 72000 {
		t.Fatalf("entryLagMedianMs lost on the wire: %+v", c.Assumptions)
	}
	if c.Assumptions.EntryLagP95Ms == nil || *c.Assumptions.EntryLagP95Ms != 900000 {
		t.Fatalf("entryLagP95Ms lost on the wire: %+v", c.Assumptions)
	}
	if c.Assumptions.PriceSource != "marketdata-binance" || !c.Assumptions.EqualSize || !c.Assumptions.NoSlippage || !c.Assumptions.NoFunding {
		t.Fatalf("assumptions tail lost on the wire: %+v", c.Assumptions)
	}
	if c.Arena == nil || c.Arena.Decisions != 10 || c.Arena.AgreementShare != 0.8 {
		t.Fatalf("arena lost on the wire: %+v", c.Arena)
	}
	if len(c.Arena.Models) != 1 || c.Arena.Models[0].Model != "dre-x-1" || c.Arena.Models[0].Verdicts != 10 {
		t.Fatalf("arena models lost on the wire: %+v", c.Arena)
	}
	if string(c.Arena.Models[0].AvgAllowReturn) != "0.001800" {
		t.Fatalf("arena money lost on the wire: %+v", c.Arena.Models[0])
	}

	var back domain.Report
	if err := json.Unmarshal(mustWire(t, c), &back); err != nil {
		t.Fatalf("contracts.Report not domain-compatible: %v", err)
	}
	if back.LabeledCount != d.LabeledCount || back.LatencyP50MS != 11 || back.LatencyP95MS != 38 {
		t.Fatalf("reverse mismatch: %+v", back)
	}
	if back.Comparisons == nil || back.Comparisons.Brier != 0.21 || len(back.Comparisons.ByModel) != 1 {
		t.Fatalf("reverse comparisons mismatch: %+v", back.Comparisons)
	}
	if back.PolicyHash != parityPolicy || back.OtherPolicyDecisions != 3 {
		t.Fatalf("reverse policy mismatch: %+v", back)
	}
	if back.BackfillLabeled != 5 || back.BackfillExcluded != 2 {
		t.Fatalf("reverse backfill mismatch: %+v", back)
	}
	if back.AwaitingLabeling != 4 || back.NoOutcomeByDeadline != 2 || back.SourceRejected != 1 {
		t.Fatalf("reverse labeling progress mismatch: %+v", back)
	}
	if back.Assumptions == nil || back.Assumptions.EntryLagMedianMS != 72000 || back.Assumptions.EntryLagP95MS != 900000 {
		t.Fatalf("reverse entry lag ms mismatch: %+v", back.Assumptions)
	}
	if back.OtherLabelParams != 2 {
		t.Fatalf("reverse otherLabelParams mismatch: %+v", back)
	}
	if back.Assumptions == nil || *back.Assumptions != *d.Assumptions {
		t.Fatalf("reverse assumptions mismatch: %+v", back.Assumptions)
	}
	if back.Arena == nil || back.Arena.Decisions != 10 || back.Arena.AgreementShare != 0.8 || len(back.Arena.Models) != 1 {
		t.Fatalf("reverse arena mismatch: %+v", back.Arena)
	}
	gotArena := back.Arena.Models[0]
	wantArena := d.Arena.Models[0]
	if gotArena.Model != wantArena.Model || gotArena.Verdicts != wantArena.Verdicts ||
		gotArena.AvgAllowReturn.String() != wantArena.AvgAllowReturn.String() ||
		gotArena.AUC != wantArena.AUC || gotArena.CostUSD.String() != wantArena.CostUSD.String() {
		t.Fatalf("reverse arena model mismatch: %+v", gotArena)
	}
	gotGrp := back.Comparisons.Groups["allow"]
	wantGrp := d.Comparisons.Groups["allow"]
	if gotGrp.Count != wantGrp.Count ||
		gotGrp.SumNetReturn.String() != wantGrp.SumNetReturn.String() ||
		gotGrp.AvgNetReturn.String() != wantGrp.AvgNetReturn.String() {
		t.Fatalf("reverse groups mismatch: %+v", gotGrp)
	}
	gotDiff, wantDiff := back.Comparisons.Difference, d.Comparisons.Difference
	if gotDiff == nil ||
		gotDiff.Mean.String() != wantDiff.Mean.String() ||
		gotDiff.Low.String() != wantDiff.Low.String() ||
		gotDiff.High.String() != wantDiff.High.String() ||
		gotDiff.ContainsZero != wantDiff.ContainsZero ||
		gotDiff.BootstrapReps != wantDiff.BootstrapReps ||
		gotDiff.Days != wantDiff.Days {
		t.Fatalf("reverse difference mismatch: %+v", gotDiff)
	}
	if back.Comparisons.BaseRate != 0.55 || back.Comparisons.ConstantBrier != 0.25 ||
		back.Comparisons.BrierSkillScore != 0.16 || back.Comparisons.AUC != 0.62 ||
		back.Comparisons.CalibrationCount != 42 {
		t.Fatalf("reverse calibration mismatch: %+v", back.Comparisons)
	}
	if back.Comparisons.ByModel[0].AvgNetReturn.String() != "0.006686" {
		t.Fatalf("reverse byModel avg mismatch: %+v", back.Comparisons.ByModel[0])
	}
}

func TestParityModelChanged(t *testing.T) {
	d := domain.ModelChanged{
		RequestedModel: "dre-x-1",
		PreviousModel:  "dre-x-1",
		ActualModel:    "dre-x-2",
		DetectedAt:     parityTS,
	}
	var c DecisionModelChanged
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.ModelChanged not wire-compatible: %v", err)
	}
	if c.RequestedModel != "dre-x-1" || c.ActualModel != "dre-x-2" {
		t.Fatalf("round-trip mismatch: %+v", c)
	}

	var back domain.ModelChanged
	if err := json.Unmarshal(mustWire(t, c), &back); err != nil {
		t.Fatalf("contracts.DecisionModelChanged not domain-compatible: %v", err)
	}
	if back != d {
		t.Fatalf("reverse mismatch: %+v", back)
	}
}

func TestParityArenaDecision(t *testing.T) {
	choice := "long"
	resp := DecisionResponse{
		Answers: DecisionResponseAnswers{
			"direction": Answer{Type: AnswerTypeChoice, Choice: &choice},
		},
		Model: "dre-x-1",
	}
	v := GateVerdict{
		Verdict:    GateVerdictVerdictALLOW,
		Code:       "allow",
		Reasons:    []string{"all gate checks passed"},
		Checks:     []Check{{Name: "risk_check", Passed: true}},
		PolicyHash: parityPolicy,
		DecidedAt:  parityTS,
	}
	c := ArenaDecision{
		DecisionID: parityULID,
		Model:      "dre-x-1",
		Response:   resp,
		Verdict:    v,
	}
	wire := mustWire(t, c)
	var back ArenaDecision
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatalf("ArenaDecision not wire-compatible: %v", err)
	}
	if back.DecisionID != parityULID || back.Model != "dre-x-1" {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
	if string(back.Verdict.Verdict) != "ALLOW" || back.Verdict.PolicyHash != parityPolicy {
		t.Fatalf("verdict lost on the wire: %+v", back.Verdict)
	}
	if back.Response.Model != "dre-x-1" || back.Response.Answers["direction"].Choice == nil || *back.Response.Answers["direction"].Choice != "long" {
		t.Fatalf("response lost on the wire: %+v", back.Response)
	}
}

func TestParityNotifySent(t *testing.T) {
	c := NotifySent{Kind: "verdict", Channel: "telegram", Ok: true}
	var back NotifySent
	if err := json.Unmarshal(mustWire(t, c), &back); err != nil {
		t.Fatalf("NotifySent not wire-compatible: %v", err)
	}
	if back != c {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
}

func TestParityOutcomeUnlabelable(t *testing.T) {
	d := domain.OutcomeUnlabelable{
		SignalID:   "sig-002",
		DecisionID: parityULID,
		Reason:     "entry bar has no trades for the signal symbol",
	}
	var c OutcomeUnlabelable
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.OutcomeUnlabelable not wire-compatible: %v", err)
	}
	if c.SignalID != d.SignalID || c.DecisionID != parityULID || c.Reason != d.Reason {
		t.Fatalf("round-trip mismatch: %+v", c)
	}

	var back domain.OutcomeUnlabelable
	if err := json.Unmarshal(mustWire(t, c), &back); err != nil {
		t.Fatalf("contracts.OutcomeUnlabelable not domain-compatible: %v", err)
	}
	if back != d {
		t.Fatalf("reverse mismatch: %+v", back)
	}

	// decisionId is required on the wire but may be empty.
	empty := domain.OutcomeUnlabelable{SignalID: "sig-003", Reason: "no decision recorded"}
	var cEmpty OutcomeUnlabelable
	if err := json.Unmarshal(mustWire(t, empty), &cEmpty); err != nil {
		t.Fatalf("empty decisionId must be accepted: %v", err)
	}
	if cEmpty.DecisionID != "" || cEmpty.SignalID != "sig-003" {
		t.Fatalf("empty decisionId mismatch: %+v", cEmpty)
	}

	if domain.EventOutcomeUnlabelable != "outcome.unlabelable" {
		t.Fatalf("event kind = %q", domain.EventOutcomeUnlabelable)
	}
}
