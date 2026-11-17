package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/outcome"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/store/sqlite"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// Journal composition of the benchmark fixture: every signal has a request, a
// decision response and a verdict; 11,000 of 13,000 also have a labeled
// outcome. 3*13,000 + 11,000 = 50,000 events.
const (
	benchSignalCount  = 13000
	benchLabeledCount = 11000
	benchEventCount   = 3*benchSignalCount + benchLabeledCount
	benchPolicy       = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// benchBase is the first signal time. Labeled signals are spread over 13000
// ten-minute slots (about 90 days); the last 2000 unlabeled signals are recent
// enough that their labeling window is still open, so one cycle skips them
// without a candle call.
var benchBase = time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

// benchNow is the clock of the measured cycle: just after the last signal, so
// the labeled windows are closed and the unlabeled ones are still open.
func benchNow() time.Time {
	last := benchBase.Add(time.Duration(benchSignalCount-1) * 10 * time.Minute)
	return last.Add(30 * time.Second)
}

// benchSignalTime returns the timestamp of signal i. Labeled signals occupy
// one ten-minute slot each; the unlabeled tail is compressed into the last
// minute so its forward window is still open at the benchmark clock.
func benchSignalTime(i int) time.Time {
	if i < benchLabeledCount {
		return benchBase.Add(time.Duration(i) * 10 * time.Minute)
	}
	last := benchBase.Add(time.Duration(benchSignalCount-1) * 10 * time.Minute)
	return last.Add(-time.Duration(benchSignalCount-i) * 30 * time.Millisecond)
}

// benchDecisionResponse is a valid-looking decision response for one signal.
func benchDecisionResponse() domain.DecisionResponse {
	return domain.DecisionResponse{
		Answers: map[string]domain.Answer{
			"risk_check": {
				Type:          domain.QuestionChoice,
				Choice:        "pass",
				Probabilities: map[string]float64{"pass": 0.9, "block": 0.1},
			},
		},
		Model: "bench-model",
		Usage: domain.Usage{
			InputTokens:  100,
			OutputTokens: 20,
			CostUSD:      domain.MustParseDecimal("0.000006"),
			LatencyMS:    5,
		},
	}
}

// benchOutcome builds one full labeled outcome for signal i.
func benchOutcome(req domain.GateRequest, decisionID string, ts time.Time) domain.Outcome {
	entry := domain.MustParseDecimal("100000")
	exit := domain.MustParseDecimal("100010")
	gross := domain.MustParseDecimal("0.0001")
	return domain.Outcome{
		SignalID:    req.SignalID,
		DecisionID:  decisionID,
		Instrument:  req.Instrument,
		Side:        req.Side,
		Timeframe:   req.Timeframe,
		EntryTS:     ts.Add(time.Minute),
		EntryPrice:  entry,
		ExitTS:      ts.Add(2 * time.Minute),
		ExitPrice:   exit,
		HorizonBars: 1,
		FeeBps:      0,
		GrossReturn: gross,
		NetReturn:   gross,
		LabeledAt:   ts.Add(2 * time.Minute),
		PriceSource: "bench",
	}
}

// seedBenchJournal writes the 50,000-event fixture through the real sqlite
// journal, so the hash chain and the append cost stay representative.
func seedBenchJournal(b *testing.B, ctx context.Context, jr ports.Journal) {
	b.Helper()
	appendEvent := func(kind, decisionID string, ts time.Time, payload any) {
		raw, err := json.Marshal(payload)
		if err != nil {
			b.Fatalf("encode %s: %v", kind, err)
		}
		if _, err := jr.Append(ctx, domain.Event{
			Kind:          kind,
			TS:            ts,
			SchemaVersion: domain.EventSchemaVersion,
			Payload:       raw,
			CodeVersion:   "bench",
			DecisionID:    decisionID,
			PolicyHash:    benchPolicy,
		}); err != nil {
			b.Fatalf("append %s: %v", kind, err)
		}
	}
	resp := benchDecisionResponse()
	for i := 0; i < benchSignalCount; i++ {
		ts := benchSignalTime(i)
		decisionID := fmt.Sprintf("bench-dec-%05d", i)
		req := domain.GateRequest{
			SignalID:   fmt.Sprintf("bench-%05d", i),
			Instrument: domain.Instrument{Venue: "binance", Symbol: "BTC/USDT"},
			Side:       domain.SideBuy,
			IntentKind: domain.IntentEntry,
			Timeframe:  "1m",
			TS:         ts,
			Price:      domain.MustParseDecimal("100000"),
			Source:     "bench",
			State:      map[string]float64{"rsi": 55.5},
		}
		verdict := domain.GateVerdict{
			Verdict:    domain.VerdictAllow,
			Code:       "allow",
			Reasons:    []string{"all gate checks passed"},
			DecisionID: decisionID,
			Model:      "bench-model",
			PolicyHash: benchPolicy,
			DecidedAt:  ts.Add(time.Second),
		}
		appendEvent(domain.EventGateRequest, decisionID, ts, req)
		appendEvent(domain.EventDecisionResponse, decisionID, ts, resp)
		appendEvent(domain.EventGateVerdict, decisionID, ts.Add(time.Second), verdict)
		if i < benchLabeledCount {
			appendEvent(domain.EventOutcomeLabeled, decisionID, ts.Add(2*time.Minute), benchOutcome(req, decisionID, ts))
		}
	}
}

// benchCandleSource serves deterministic closed 1m candles for the labeler.
// The fixture's unlabeled signals are recent, so one cycle must not call it.
type benchCandleSource struct {
	now   time.Time
	calls int
}

// Candles returns the requested number of closed bars after req.From.
func (s *benchCandleSource) Candles(_ context.Context, req domain.CandleRequest) ([]domain.Candle, error) {
	s.calls++
	step := time.Minute
	first := req.From.UTC().Truncate(step)
	if first.Before(req.From.UTC()) {
		first = first.Add(step)
	}
	out := make([]domain.Candle, 0, req.Limit)
	for k := 0; k < req.Limit; k++ {
		open := first.Add(time.Duration(k) * step)
		if !open.Add(step).Before(s.now) {
			break
		}
		out = append(out, domain.Candle{
			Symbol:    req.Symbol,
			Interval:  req.Interval,
			OpenTime:  open,
			CloseTime: open.Add(step),
			Open:      domain.MustParseDecimal("100000"),
			High:      domain.MustParseDecimal("100010"),
			Low:       domain.MustParseDecimal("99990"),
			Close:     domain.MustParseDecimal("100005"),
			Volume:    domain.MustParseDecimal("10"),
		})
	}
	return out, nil
}

// benchNotifier accepts every notification and counts it.
type benchNotifier struct {
	sends int
}

// Send records one delivery.
func (n *benchNotifier) Send(context.Context, ports.Notification) error {
	n.sends++
	return nil
}

// benchReporter returns a fixed report for the weekly summary.
type benchReporter struct {
	calls int
}

// Build records one report build.
func (r *benchReporter) Build(context.Context) (domain.Report, error) {
	r.calls++
	return domain.Report{
		LabeledCount:  benchLabeledCount,
		VerdictCounts: map[string]int{string(domain.VerdictAllow): benchSignalCount},
	}, nil
}

// BenchmarkRunnerCycle50kEvents measures one full shadow.Runner cycle over a
// 50,000-event journal: outcome labeling (no candle calls in this fixture),
// notification scheduling, and the status snapshot. The journal is seeded
// through the real sqlite store before the timer starts, so setup is excluded.
//
// Run it with a single iteration to measure one cycle:
//
//	go -C core test ./internal/app/shadow -run '^$' \
//	  -bench BenchmarkRunnerCycle50kEvents -benchtime=1x
func BenchmarkRunnerCycle50kEvents(b *testing.B) {
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		dir := b.TempDir()
		jr, err := sqlite.Open(ctx, filepath.Join(dir, "bench.db"))
		if err != nil {
			b.Fatalf("open journal: %v", err)
		}
		seedStart := time.Now()
		seedBenchJournal(b, ctx, jr)
		b.Logf("seeded %d events in %s", benchEventCount, time.Since(seedStart).Round(time.Millisecond))

		now := benchNow()
		clock := func() time.Time { return now }
		deps := ports.Deps{
			Clock: clock,
			NewID: func() string { return "bench-decision" },
		}
		candles := &benchCandleSource{now: now}
		labeler, err := outcome.New(jr, candles, deps, outcome.Config{
			HorizonBars: 1,
			FeeBps:      0,
			CodeVersion: "bench",
			PriceSource: "bench",
		})
		if err != nil {
			b.Fatalf("build labeler: %v", err)
		}
		notifier := &benchNotifier{}
		reporter := &benchReporter{}
		runner, err := New(jr, labeler, nil, nil, reporter, notifier, deps, Config{
			LabelInterval: 5 * time.Minute,
			StatusPath:    filepath.Join(dir, "shadow-status.json"),
			CodeVersion:   "bench",
		})
		if err != nil {
			b.Fatalf("build runner: %v", err)
		}

		b.StartTimer()
		if err := runner.Step(ctx); err != nil {
			b.Fatalf("step: %v", err)
		}
		b.StopTimer()

		st := runner.Status()
		if st.Labeled != benchLabeledCount {
			b.Fatalf("labeled = %d, want %d", st.Labeled, benchLabeledCount)
		}
		if st.LastCycleError != "" {
			b.Fatalf("cycle error: %s", st.LastCycleError)
		}
		if candles.calls != 0 {
			b.Fatalf("candle calls = %d, want 0 while the unlabeled windows are open", candles.calls)
		}
		if reporter.calls != 1 || notifier.sends != 1 {
			b.Fatalf("weekly notification = reporter %d notifier %d, want 1/1", reporter.calls, notifier.sends)
		}
		if err := jr.Close(); err != nil {
			b.Fatalf("close journal: %v", err)
		}
	}
}
