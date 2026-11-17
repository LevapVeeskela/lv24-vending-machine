package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
)

func appendReportEvent(t *testing.T, env Env, dataDir string, ev domain.Event) {
	t.Helper()
	jr, closer, err := env.OpenJournal(config.DBPath(dataDir))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer func() { _ = closer.Close() }()
	if _, err := jr.Append(context.Background(), ev); err != nil {
		t.Fatalf("append %s: %v", ev.Kind, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// seedSmallReport writes 2 labeled decisions (< 100) into the journal.
func seedSmallReport(t *testing.T, env Env, dataDir string) {
	t.Helper()
	now := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	mkReq := func(sig string) domain.Event {
		return domain.Event{
			Kind:       domain.EventGateRequest,
			DecisionID: "dec-" + sig,
			Payload: mustJSON(t, domain.GateRequest{
				SignalID:   sig,
				Instrument: domain.Instrument{Venue: "test-venue", Symbol: "BTC/USDT"},
				Side:       domain.SideBuy,
				IntentKind: domain.IntentEntry,
				Timeframe:  "1h",
				TS:         now,
				Price:      domain.MustParseDecimal("100"),
				Source:     "test",
			}),
		}
	}
	mkResp := func(sig, model, cost string, latency int64, pPass float64) domain.Event {
		return domain.Event{
			Kind:       domain.EventDecisionResponse,
			DecisionID: "dec-" + sig,
			Payload: mustJSON(t, domain.DecisionResponse{
				Answers: map[string]domain.Answer{
					"risk_check": {
						Type:          domain.QuestionChoice,
						Choice:        "pass",
						Probabilities: map[string]float64{"pass": pPass, "block": 1 - pPass},
					},
				},
				Model: model,
				Usage: domain.Usage{
					CostUSD:   domain.MustParseDecimal(cost),
					LatencyMS: latency,
				},
			}),
		}
	}
	mkVerdict := func(sig string, v domain.Verdict) domain.Event {
		return domain.Event{
			Kind:       domain.EventGateVerdict,
			DecisionID: "dec-" + sig,
			Payload: mustJSON(t, domain.GateVerdict{
				Verdict:    v,
				Code:       "test",
				Reasons:    []string{"test"},
				Checks:     []domain.Check{},
				PolicyHash: "test-policy",
			}),
		}
	}
	mkOutcome := func(sig, net string) domain.Event {
		return domain.Event{
			Kind:       domain.EventOutcomeLabeled,
			DecisionID: "dec-" + sig,
			Payload: mustJSON(t, domain.Outcome{
				SignalID:    sig,
				DecisionID:  "dec-" + sig,
				Instrument:  domain.Instrument{Venue: "test-venue", Symbol: "BTC/USDT"},
				Side:        domain.SideBuy,
				Timeframe:   "1h",
				EntryPrice:  domain.MustParseDecimal("100"),
				ExitPrice:   domain.MustParseDecimal("101"),
				NetReturn:   domain.MustParseDecimal(net),
				GrossReturn: domain.MustParseDecimal(net),
				LabeledAt:   now,
			}),
		}
	}
	mkUnlabelable := func(sig, reason string) domain.Event {
		return domain.Event{
			Kind:       domain.EventOutcomeUnlabelable,
			DecisionID: "dec-" + sig,
			Payload: mustJSON(t, domain.OutcomeUnlabelable{
				SignalID:   sig,
				DecisionID: "dec-" + sig,
				Reason:     reason,
			}),
		}
	}
	for _, ev := range []domain.Event{
		mkReq("a"), mkResp("a", "alpha", "0.010", 100, 0.75),
		mkVerdict("a", domain.VerdictAllow), mkOutcome("a", "0.05"),
		mkReq("b"), mkResp("b", "alpha", "0.020", 200, 0.5),
		mkVerdict("b", domain.VerdictReject), mkOutcome("b", "-0.01"),
		mkReq("c"), mkUnlabelable("c", "source_rejected"),
	} {
		appendReportEvent(t, env, dataDir, ev)
	}
}

func TestGateReportInsufficient(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	seedSmallReport(t, env, dataDir)

	if code := runArgs(env, "gate", "report",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report = %d, stderr=%s", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"verdicts:", "labeled: 2", "awaiting labeling: 0",
		"no outcome by deadline: 0", "source rejected: 1", "decision cost:", "latency", "insufficient data"} {
		if !strings.Contains(got, want) {
			t.Fatalf("human report misses %q: %q", want, got)
		}
	}
	if strings.Contains(got, "brier") {
		t.Fatalf("human report must not contain comparisons: %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "gate", "report", "--json",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report --json = %d, stderr=%s", code, stderr.String())
	}
	var rep domain.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("decode report JSON: %v", err)
	}
	if rep.Sufficient {
		t.Fatalf("sufficient = true, want false (2 < 100)")
	}
	if rep.Comparisons != nil {
		t.Fatalf("comparisons present, want absent")
	}
	if rep.LabeledCount != 2 {
		t.Fatalf("labeled = %d, want 2", rep.LabeledCount)
	}
	if rep.AwaitingLabeling != 0 || rep.NoOutcomeByDeadline != 0 || rep.SourceRejected != 1 {
		t.Fatalf("progress = awaiting %d, missed %d, rejected %d, want 0/0/1",
			rep.AwaitingLabeling, rep.NoOutcomeByDeadline, rep.SourceRejected)
	}
}

func TestGateReportMissingDB(t *testing.T) {
	env, _, stderr := testEnv(t)
	dir := t.TempDir()
	if code := runArgs(env, "gate", "report",
		"--data-dir", dir); code != 3 {
		t.Fatalf("gate report missing db = %d, want 3, stderr=%s", code, stderr.String())
	}
}

func TestPrintReportHumanModelThreshold(t *testing.T) {
	rep := domain.Report{
		LabeledCount:  100,
		Sufficient:    true,
		VerdictCounts: map[string]int{"ALLOW": 60, "REJECT": 40},
		PolicyHash:    strings.Repeat("a", 64),
		Comparisons: &domain.ReportComparisons{
			Groups: map[string]domain.GroupStats{
				"all": {Count: 100, SumNetReturn: domain.MustParseDecimal("1.00"),
					AvgNetReturn: domain.MustParseDecimal("0.01")},
			},
			ByModel: []domain.ModelBreakdown{
				{Model: "big", Count: 30, CalibrationCount: 30},
				{Model: "small", Count: 29},
				{Model: "nopairs", Count: 40, CalibrationCount: 0},
			},
		},
	}
	var buf strings.Builder
	printReportHuman(&buf, rep, 0, reportWindow{})
	got := buf.String()
	if !strings.Contains(got, "big: n=30") {
		t.Fatalf("report misses the >=30 model: %q", got)
	}
	if !strings.Contains(got, "model small: insufficient data (29 labeled)") {
		t.Fatalf("report misses the model threshold: %q", got)
	}
	if !strings.Contains(got, "model nopairs: n=40 insufficient calibration (0 pairs, need 30)") {
		t.Fatalf("report prints metrics for a model without calibration pairs: %q", got)
	}
	if strings.Contains(got, "nopairs: n=40 brier=") {
		t.Fatalf("report prints zero metrics for a model without pairs: %q", got)
	}
	if !strings.Contains(got, "policy: aaaaaaaaaaaa") {
		t.Fatalf("report misses the 12-char policy prefix: %q", got)
	}
	for _, want := range []string{"other policies: 0", "backfill: labeled=0 excluded=0",
		"difference ALLOW-REJECT: insufficient data"} {
		if !strings.Contains(got, want) {
			t.Fatalf("report misses %q: %q", want, got)
		}
	}
}

func TestGateReportInvalidPolicy(t *testing.T) {
	env, _, stderr := testEnv(t)
	if code := runArgs(env, "gate", "report", "--policy", "not-a-hash"); code != 2 {
		t.Fatalf("gate report invalid policy = %d, want 2, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "invalid --policy") {
		t.Fatalf("stderr misses policy error: %q", stderr.String())
	}
}

func TestPrintReportHumanInsufficientDays(t *testing.T) {
	rep := domain.Report{
		LabeledCount:  60,
		Sufficient:    true,
		VerdictCounts: map[string]int{"ALLOW": 30, "REJECT": 30},
		Comparisons: &domain.ReportComparisons{
			Groups: map[string]domain.GroupStats{
				"ALLOW":  {Count: 30, AvgNetReturn: domain.MustParseDecimal("0.04")},
				"REJECT": {Count: 30, AvgNetReturn: domain.MustParseDecimal("-0.005")},
			},
		},
	}
	var buf strings.Builder
	printReportHuman(&buf, rep, 2, reportWindow{})
	got := buf.String()
	if !strings.Contains(got, "difference ALLOW-REJECT: mean=0.045") {
		t.Fatalf("report misses the point mean: %q", got)
	}
	if !strings.Contains(got, "insufficient days for a 95% interval (2 days, need 5)") {
		t.Fatalf("report misses the days warning: %q", got)
	}
	if strings.Contains(got, "ci95=") {
		t.Fatalf("report must not print a CI for 2 days: %q", got)
	}
}

// TestPrintReportHumanGroupTooSmall: five observations per group must not
// produce a 95% interval, even over enough days.
func TestPrintReportHumanGroupTooSmall(t *testing.T) {
	rep := domain.Report{
		LabeledCount:  10,
		Sufficient:    true,
		VerdictCounts: map[string]int{"ALLOW": 5, "REJECT": 5},
		Comparisons: &domain.ReportComparisons{
			Groups: map[string]domain.GroupStats{
				"ALLOW":  {Count: 5, AvgNetReturn: domain.MustParseDecimal("0.04")},
				"REJECT": {Count: 5, AvgNetReturn: domain.MustParseDecimal("-0.005")},
			},
		},
	}
	var buf strings.Builder
	printReportHuman(&buf, rep, 7, reportWindow{})
	got := buf.String()
	if !strings.Contains(got, "difference ALLOW-REJECT: mean=0.045") {
		t.Fatalf("report misses the point mean: %q", got)
	}
	for _, want := range []string{"insufficient group size", "ALLOW=5", "REJECT=5", "need 30"} {
		if !strings.Contains(got, want) {
			t.Fatalf("report misses %q: %q", want, got)
		}
	}
	if strings.Contains(got, "ci95=") {
		t.Fatalf("report must not print a CI for 5/5: %q", got)
	}
	if strings.Contains(got, "insufficient days") {
		t.Fatalf("group size, not days, is the limiting factor: %q", got)
	}
}

// TestPrintReportHumanBSSUndefined prints n/a for a degenerate base rate
// instead of a misleading zero skill score.
func TestPrintReportHumanBSSUndefined(t *testing.T) {
	newRep := func(baseRate, bss float64) domain.Report {
		return domain.Report{
			LabeledCount:  10,
			Sufficient:    true,
			VerdictCounts: map[string]int{"ALLOW": 5, "REJECT": 5},
			Comparisons: &domain.ReportComparisons{
				Groups: map[string]domain.GroupStats{
					"ALLOW":  {Count: 5, AvgNetReturn: domain.MustParseDecimal("0.04")},
					"REJECT": {Count: 5, AvgNetReturn: domain.MustParseDecimal("-0.005")},
				},
				CalibrationCount:      30,
				CalibrationSufficient: true,
				BaseRate:              baseRate,
				BrierSkillScore:       bss,
			},
		}
	}
	for _, baseRate := range []float64{0, 1} {
		var buf strings.Builder
		printReportHuman(&buf, newRep(baseRate, 0), 0, reportWindow{})
		got := buf.String()
		if !strings.Contains(got, "bss: n/a") {
			t.Fatalf("base rate %v misses bss n/a: %q", baseRate, got)
		}
		if strings.Contains(got, "bss: 0.000000") {
			t.Fatalf("base rate %v printed a zero skill score: %q", baseRate, got)
		}
	}
	var buf strings.Builder
	printReportHuman(&buf, newRep(0.5, 0.25), 0, reportWindow{})
	if !strings.Contains(buf.String(), "bss: 0.250000") {
		t.Fatalf("defined base rate misses the skill score: %q", buf.String())
	}
}

func emptyBuckets() []domain.ReliabilityBucket {
	buckets := make([]domain.ReliabilityBucket, 10)
	for i := range buckets {
		buckets[i].LowerBound = float64(i) / 10
	}
	return buckets
}

// TestPrintReportHumanCalibrationInsufficient: 100 labeled outcomes with no
// calibration pairs must print n/a instead of zero metrics and empty buckets.
func TestPrintReportHumanCalibrationInsufficient(t *testing.T) {
	rep := domain.Report{
		LabeledCount:  100,
		Sufficient:    true,
		VerdictCounts: map[string]int{"ALLOW": 50, "REJECT": 50},
		Comparisons: &domain.ReportComparisons{
			Groups: map[string]domain.GroupStats{
				"ALLOW":  {Count: 50, AvgNetReturn: domain.MustParseDecimal("0.01")},
				"REJECT": {Count: 50, AvgNetReturn: domain.MustParseDecimal("-0.01")},
			},
			CalibrationCount:      0,
			CalibrationSufficient: false,
			Brier:                 0,
			ECE:                   0,
			AUC:                   0.5,
			BaseRate:              0,
			ConstantBrier:         0,
			BrierSkillScore:       0,
			Reliability:           emptyBuckets(),
			ByModel: []domain.ModelBreakdown{
				{Model: "m", Count: 100, Brier: 0, ECE: 0, CalibrationCount: 0},
			},
		},
	}
	var buf strings.Builder
	printReportHuman(&buf, rep, 7, reportWindow{})
	got := buf.String()
	for _, want := range []string{
		"calibration pairs: 0 (need 30)",
		"brier: n/a",
		"ece: n/a",
		"auc: n/a",
		"bss: n/a",
		"base rate: n/a",
		"constant brier: n/a",
		"reliability: n/a",
		"model m: n=100 insufficient calibration (0 pairs, need 30)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("report misses %q: %q", want, got)
		}
	}
	for _, zero := range []string{"brier: 0.000000", "ece: 0.000000", "auc: 0.500000", "[0.0-0.1)"} {
		if strings.Contains(got, zero) {
			t.Fatalf("report prints a meaningless %q: %q", zero, got)
		}
	}
}

// TestPrintReportHumanCalibrationSufficient keeps the printed numbers of a
// valid report unchanged.
func TestPrintReportHumanCalibrationSufficient(t *testing.T) {
	rep := domain.Report{
		LabeledCount:  100,
		Sufficient:    true,
		VerdictCounts: map[string]int{"ALLOW": 60, "REJECT": 40},
		Comparisons: &domain.ReportComparisons{
			Groups: map[string]domain.GroupStats{
				"ALLOW":  {Count: 60, AvgNetReturn: domain.MustParseDecimal("0.01")},
				"REJECT": {Count: 40, AvgNetReturn: domain.MustParseDecimal("-0.01")},
			},
			CalibrationCount:      30,
			CalibrationSufficient: true,
			Brier:                 0.21,
			ECE:                   0.06,
			AUC:                   0.62,
			BaseRate:              0.55,
			ConstantBrier:         0.2475,
			BrierSkillScore:       0.1515151515,
			Reliability:           emptyBuckets(),
		},
	}
	var buf strings.Builder
	printReportHuman(&buf, rep, 7, reportWindow{})
	got := buf.String()
	for _, want := range []string{
		"calibration pairs: 30",
		"brier: 0.210000",
		"ece: 0.060000",
		"auc: 0.620000",
		"bss: 0.151515",
		"reliability:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("report misses %q: %q", want, got)
		}
	}
}

// TestPrintReportHumanAUCUndefined: a single observed class leaves the AUC
// undefined even when the calibration sample is large enough.
func TestPrintReportHumanAUCUndefined(t *testing.T) {
	rep := domain.Report{
		LabeledCount:  100,
		Sufficient:    true,
		VerdictCounts: map[string]int{"ALLOW": 100},
		Comparisons: &domain.ReportComparisons{
			CalibrationCount:      30,
			CalibrationSufficient: true,
			Brier:                 0.04,
			ECE:                   0.04,
			AUC:                   0.5,
			BaseRate:              1,
			ConstantBrier:         0,
			BrierSkillScore:       0,
			Reliability:           emptyBuckets(),
		},
	}
	var buf strings.Builder
	printReportHuman(&buf, rep, 7, reportWindow{})
	got := buf.String()
	if !strings.Contains(got, "auc: n/a") {
		t.Fatalf("empty class misses auc n/a: %q", got)
	}
	if strings.Contains(got, "auc: 0.500000") {
		t.Fatalf("empty class printed a 0.5 AUC: %q", got)
	}
	if !strings.Contains(got, "brier: 0.040000") {
		t.Fatalf("valid brier was hidden: %q", got)
	}
}

// tamperJournal appends a forged row directly to the database without
// sealing it, so Verify must report a broken hash chain.
func tamperJournal(t *testing.T, path string) {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(10000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open journal for tampering: %v", err)
	}
	defer func() { _ = db.Close() }()
	zero := strings.Repeat("0", 64)
	if _, err := db.Exec(
		`INSERT INTO events (seq, event_id, kind, ts, schema_version, payload,
			code_version, prev_hash, hash)
		 SELECT COALESCE(MAX(seq),0)+1, 'tampered-event', 'gate.request',
			'2026-11-16T00:00:00Z', 1, '{}', 'tampered', ?, ? FROM events`,
		zero, zero); err != nil {
		t.Fatalf("tamper journal: %v", err)
	}
}

// TestGateReportBrokenChain: a broken hash chain blocks the report unless the
// operator explicitly opts in.
func TestGateReportBrokenChain(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	seedSmallReport(t, env, dataDir)
	tamperJournal(t, config.DBPath(dataDir))

	if code := runArgs(env, "gate", "report",
		"--config", cfgPath, "--data-dir", dataDir); code == 0 {
		t.Fatalf("gate report on a broken chain = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "chain broken") {
		t.Fatalf("stderr misses the chain error: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("broken chain still produced a report: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "gate", "report", "--json",
		"--config", cfgPath, "--data-dir", dataDir); code == 0 {
		t.Fatalf("gate report --json on a broken chain = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), `"code":"general"`) {
		t.Fatalf("json error misses the envelope: %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "gate", "report", "--allow-broken-chain",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report --allow-broken-chain = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "labeled: 2") {
		t.Fatalf("allowed broken chain misses the report: %q", stdout.String())
	}
}
