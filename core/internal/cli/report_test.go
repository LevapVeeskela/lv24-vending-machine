package cli

import (
	"context"
	"encoding/json"
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
	for _, ev := range []domain.Event{
		mkReq("a"), mkResp("a", "alpha", "0.010", 100, 0.75),
		mkVerdict("a", domain.VerdictAllow), mkOutcome("a", "0.05"),
		mkReq("b"), mkResp("b", "alpha", "0.020", 200, 0.5),
		mkVerdict("b", domain.VerdictReject), mkOutcome("b", "-0.01"),
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
	for _, want := range []string{"verdicts:", "labeled: 2", "decision cost:", "latency", "insufficient data"} {
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
}

func TestGateReportMissingDB(t *testing.T) {
	env, _, stderr := testEnv(t)
	dir := t.TempDir()
	if code := runArgs(env, "gate", "report",
		"--data-dir", dir); code != 3 {
		t.Fatalf("gate report missing db = %d, want 3, stderr=%s", code, stderr.String())
	}
}
