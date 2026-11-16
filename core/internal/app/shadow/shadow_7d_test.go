package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/gateflow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/outcome"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/store/sqlite"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// TestShadowSevenDays drives the real shadow Runner through a full simulated
// week (2016 five-minute cycles) against a real SQLite journal, the real
// outcome.Labeler and the real gateflow.Flow, with generated candles and a
// scripted decision provider. No network and no secrets are involved.
const (
	sevenDaySteps    = 2016
	sevenDaySignals  = 2000
	sevenDayInterval = 5 * time.Minute
)

var sevenDayBase = time.Date(2026, time.November, 16, 6, 0, 0, 0, time.UTC)

// sevenDayDec converts an integer count into a positive Decimal.
func sevenDayDec(v int64) domain.Decimal {
	return domain.MustParseDecimal(strconv.FormatInt(v, 10))
}

// sevenDayBar returns deterministic positive OHLC values for the 5-minute bar
// opening at open. open always lies on the 5m grid: the fake clock advances
// from sevenDayBase by exactly one interval per step.
func sevenDayBar(open time.Time) (o, h, l, c int64) {
	n := open.Unix() / int64(sevenDayInterval/time.Second)
	o = 60000 + (n % 500)
	c = o + 10 + (n % 21)
	if c > o {
		h = c + 5
		l = o - 5
	} else {
		h = o + 5
		l = c - 5
	}
	return o, h, l, c
}

// sevenDayCandle builds one generated 5m BTCUSDT candle for open.
func sevenDayCandle(open time.Time) domain.Candle {
	o, h, l, c := sevenDayBar(open)
	n := open.Unix() / int64(sevenDayInterval/time.Second)
	return domain.Candle{
		Symbol:    "BTCUSDT",
		Interval:  "5m",
		OpenTime:  open,
		CloseTime: open.Add(sevenDayInterval),
		Open:      sevenDayDec(o),
		High:      sevenDayDec(h),
		Low:       sevenDayDec(l),
		Close:     sevenDayDec(c),
		Volume:    sevenDayDec(1000 + (n % 1000)),
	}
}

// sevenDayCandleSource serves generated 5m bars, returning only bars that
// already closed before the fake now.
type sevenDayCandleSource struct {
	now func() time.Time
}

func (s *sevenDayCandleSource) Candles(_ context.Context, req domain.CandleRequest) ([]domain.Candle, error) {
	now := s.now().UTC()
	from := req.From.UTC().Truncate(sevenDayInterval)
	var out []domain.Candle
	for k := 0; k < req.Limit; k++ {
		open := from.Add(time.Duration(k) * sevenDayInterval)
		if !open.Add(sevenDayInterval).Before(now) {
			break
		}
		out = append(out, sevenDayCandle(open))
	}
	return out, nil
}

// sevenDaySignalSource emits exactly one entry signal per Scan for the first
// sevenDaySignals calls, then none.
type sevenDaySignalSource struct {
	now func() time.Time
	i   int
}

func (s *sevenDaySignalSource) Scan(_ context.Context) ([]domain.GateRequest, error) {
	if s.i >= sevenDaySignals {
		return nil, nil
	}
	i := s.i
	s.i++
	ts := s.now().Add(-sevenDayInterval)
	_, _, _, c := sevenDayBar(ts)
	return []domain.GateRequest{{
		SignalID:   fmt.Sprintf("sig-%d", i),
		Instrument: domain.Instrument{Venue: "binance", Symbol: "BTCUSDT"},
		Side:       domain.SideBuy,
		IntentKind: domain.IntentEntry,
		Timeframe:  "5m",
		TS:         ts,
		Price:      sevenDayDec(c),
		Source:     "fixture",
		State:      map[string]float64{"rsi": 50},
	}}, nil
}

// sevenDayPtr returns a pointer to v for optional Answer fields.
func sevenDayPtr(v float64) *float64 { return &v }

// sevenDayDecision answers every request with a gate-v1 ALLOW.
type sevenDayDecision struct{}

func (sevenDayDecision) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "fixture-decision", Name: "fixture", Kind: ports.KindDecision, Version: "v0"}
}

func (sevenDayDecision) Decide(_ context.Context, _ domain.DecisionRequest) (domain.DecisionResponse, error) {
	return domain.DecisionResponse{
		Answers: map[string]domain.Answer{
			"data_quality":     {Type: domain.QuestionChoice, Choice: "ok", Probabilities: map[string]float64{"ok": 0.9, "insufficient": 0.1}, Confidence: sevenDayPtr(0.9)},
			"signal_alignment": {Type: domain.QuestionChoice, Choice: "aligned", Probabilities: map[string]float64{"aligned": 0.8, "neutral": 0.15, "conflict": 0.05}, Confidence: sevenDayPtr(0.8)},
			"market_regime":    {Type: domain.QuestionChoice, Choice: "favorable", Probabilities: map[string]float64{"favorable": 0.7, "neutral": 0.25, "adverse": 0.05}, Confidence: sevenDayPtr(0.7)},
			"risk_check":       {Type: domain.QuestionChoice, Choice: "pass", Probabilities: map[string]float64{"pass": 0.9, "block": 0.1}, Confidence: sevenDayPtr(0.9)},
		},
		Model: "fixture",
		Usage: domain.Usage{InputTokens: 1, OutputTokens: 1, CostUSD: domain.MustParseDecimal("0")},
	}, nil
}

func TestShadowSevenDays(t *testing.T) {
	if testing.Short() {
		t.Skip("seven-day shadow simulation")
	}
	ctx := context.Background()
	now := sevenDayBase
	clock := func() time.Time { return now }

	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), sqlite.WithClock(clock))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	var idSeq atomic.Int64
	deps := ports.Deps{
		Clock: clock,
		NewID: func() string { return fmt.Sprintf("dec-%d", idSeq.Add(1)) },
	}

	labeler, err := outcome.New(store, &sevenDayCandleSource{now: clock}, deps, outcome.Config{
		HorizonBars: 1, FeeBps: 0, CodeVersion: "test", PriceSource: "fixture",
	})
	if err != nil {
		t.Fatalf("outcome.New: %v", err)
	}
	flow, err := gateflow.New(store, sevenDayDecision{}, deps, gateflow.Config{
		Model: "fixture", Policy: domain.DefaultGatePolicy(), CodeVersion: "test",
	})
	if err != nil {
		t.Fatalf("gateflow.New: %v", err)
	}

	reporter := &fakeReporter{rep: domain.Report{
		LabeledCount:  sevenDaySignals,
		VerdictCounts: map[string]int{string(domain.VerdictAllow): sevenDaySignals},
	}}
	notifier := &fakeNotifier{}

	runner, err := New(store, labeler, &sevenDaySignalSource{now: clock}, flow, reporter, notifier, deps, Config{
		LabelInterval: sevenDayInterval,
		StatusPath:    filepath.Join(t.TempDir(), "status.json"),
		CodeVersion:   "test",
	})
	if err != nil {
		t.Fatalf("shadow.New: %v", err)
	}

	startDebug := runner.debugSize()
	beforeG := runtime.NumGoroutine()

	start := time.Now()
	for i := 0; i < sevenDaySteps; i++ {
		if err := runner.Step(ctx); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		now = now.Add(sevenDayInterval)
	}
	loopDur := time.Since(start)
	t.Logf("7-day loop: %d steps duration=%v", sevenDaySteps, loopDur)

	reqs, err := store.Query(ctx, 0, domain.EventGateRequest)
	if err != nil {
		t.Fatalf("query gate.request: %v", err)
	}
	if len(reqs) != sevenDaySignals {
		t.Fatalf("gate.request = %d, want %d", len(reqs), sevenDaySignals)
	}

	labeled, err := store.Query(ctx, 0, domain.EventOutcomeLabeled)
	if err != nil {
		t.Fatalf("query outcome.labeled: %v", err)
	}
	if len(labeled) != sevenDaySignals {
		t.Fatalf("outcome.labeled = %d, want %d", len(labeled), sevenDaySignals)
	}
	seen := make(map[string]bool, len(labeled))
	for _, ev := range labeled {
		var oc domain.Outcome
		if err := json.Unmarshal(ev.Payload, &oc); err != nil {
			t.Fatalf("decode outcome seq %d: %v", ev.Seq, err)
		}
		if oc.SignalID == "" {
			t.Fatalf("outcome seq %d: empty signalId", ev.Seq)
		}
		if seen[oc.SignalID] {
			t.Fatalf("duplicate outcome signalId %q", oc.SignalID)
		}
		seen[oc.SignalID] = true
	}

	vr, err := store.Verify(ctx)
	if err != nil {
		t.Fatalf("journal verify: %v", err)
	}
	if !vr.OK {
		t.Fatalf("journal verify not ok: %+v", vr)
	}

	// One extra Step must add neither outcomes nor new signals.
	reqCount, labeledCount := len(reqs), len(labeled)
	if err := runner.Step(ctx); err != nil {
		t.Fatalf("extra step: %v", err)
	}
	reqsAfter, err := store.Query(ctx, 0, domain.EventGateRequest)
	if err != nil {
		t.Fatalf("query gate.request after: %v", err)
	}
	labeledAfter, err := store.Query(ctx, 0, domain.EventOutcomeLabeled)
	if err != nil {
		t.Fatalf("query outcome.labeled after: %v", err)
	}
	if len(reqsAfter) != reqCount {
		t.Fatalf("extra step added signals: before=%d after=%d", reqCount, len(reqsAfter))
	}
	if len(labeledAfter) != labeledCount {
		t.Fatalf("extra step added outcomes: before=%d after=%d", labeledCount, len(labeledAfter))
	}

	var afterG int
	settled := false
	for attempt := 1; attempt <= 3; attempt++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
		afterG = runtime.NumGoroutine()
		if afterG == beforeG {
			settled = true
			break
		}
	}
	if !settled {
		t.Fatalf("goroutines did not settle: before=%d after=%d", beforeG, afterG)
	}

	endDebug := runner.debugSize()
	t.Logf("debugSize start=%d end=%d", startDebug, endDebug)
	// debugSize must stay bounded across the week: it counts only the retry
	// state of undelivered notifications, which is bounded by the fixed set of
	// alert series.
	if endDebug > startDebug+1 {
		t.Fatalf("debugSize grew unbounded: start=%d end=%d", startDebug, endDebug)
	}

	if sends := notifier.sent(); len(sends) > 0 {
		kinds := make([]string, len(sends))
		for i, n := range sends {
			kinds[i] = n.Kind
		}
		t.Logf("notifier sends=%d kinds=%v", len(sends), kinds)
	} else {
		t.Fatalf("notifier recorded no sends; weekly summary should have fired")
	}

	t.Logf("total duration=%v", time.Since(start))
}
