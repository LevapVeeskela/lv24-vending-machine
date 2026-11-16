package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/outcome"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/store/sqlite"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// Seven-day shadow soak parameters: 2000 entry signals, one 5m cycle each,
// 2016 cycles == 7 days.
const (
	sevenDaySignals  = 2000
	sevenDaySteps    = 2016
	sevenDayInterval = 5 * time.Minute
)

// sevenDayBase is the signal grid origin: 2026-01-01T00:00:00Z.
var sevenDayBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fixtureCandles serves exactly one synthetic 5m bar per request, opening at
// the requested (5m-rounded) time and closing one interval later.
type fixtureCandles struct {
	calls int
}

func (f *fixtureCandles) Candles(_ context.Context, req domain.CandleRequest) ([]domain.Candle, error) {
	f.calls++
	open := req.From.UTC().Truncate(sevenDayInterval)
	return []domain.Candle{{
		Symbol:    req.Symbol,
		Interval:  req.Interval,
		OpenTime:  open,
		CloseTime: open.Add(sevenDayInterval),
		Open:      domain.MustParseDecimal("100"),
		High:      domain.MustParseDecimal("101"),
		Low:       domain.MustParseDecimal("99"),
		Close:     domain.MustParseDecimal("100.5"),
		Volume:    domain.MustParseDecimal("1"),
	}}, nil
}

// seedSevenDayRequests appends 2000 gate.request events on a 5m grid.
func seedSevenDayRequests(t *testing.T, ctx context.Context, jr *sqlite.Store) {
	t.Helper()
	for i := 0; i < sevenDaySignals; i++ {
		ts := sevenDayBase.Add(time.Duration(i) * sevenDayInterval)
		req := domain.GateRequest{
			SignalID:   fmt.Sprintf("sig-%d", i),
			Instrument: domain.Instrument{Venue: "test", Symbol: "BTCUSDT"},
			Side:       domain.SideBuy,
			IntentKind: domain.IntentEntry,
			Timeframe:  "5m",
			TS:         ts,
			Price:      domain.MustParseDecimal("100"),
			Source:     "test",
			State:      map[string]float64{"x": 1},
		}
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal gate request %d: %v", i, err)
		}
		if _, err := jr.Append(ctx, domain.Event{
			Kind:          domain.EventGateRequest,
			TS:            ts,
			SchemaVersion: domain.EventSchemaVersion,
			Payload:       raw,
			CodeVersion:   "test",
			DecisionID:    fmt.Sprintf("dec-%d", i),
		}); err != nil {
			t.Fatalf("append gate request %d: %v", i, err)
		}
	}
}

// labeledSignalIDs walks the journal and returns the signalId of every
// outcome.labeled event.
func labeledSignalIDs(t *testing.T, ctx context.Context, jr *sqlite.Store) []string {
	t.Helper()
	var ids []string
	if err := jr.Walk(ctx, 0, domain.EventOutcomeLabeled, func(ev domain.Event) error {
		var v struct {
			SignalID string `json:"signalId"`
		}
		if err := json.Unmarshal(ev.Payload, &v); err != nil {
			t.Errorf("decode outcome.labeled seq %d: %v", ev.Seq, err)
			return nil
		}
		ids = append(ids, v.SignalID)
		return nil
	}); err != nil {
		t.Fatalf("walk outcome.labeled: %v", err)
	}
	return ids
}

func TestShadowSevenDays(t *testing.T) {
	ctx := context.Background()

	jr, err := sqlite.Open(ctx,
		filepath.Join(t.TempDir(), "journal.sqlite"),
		sqlite.WithClock(func() time.Time { return sevenDayBase }),
	)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(func() {
		if err := jr.Close(); err != nil {
			t.Errorf("close journal: %v", err)
		}
	})

	seedSevenDayRequests(t, ctx, jr)

	// One advancing fake clock drives both the labeler and the runner.
	now := sevenDayBase.Add(10 * time.Minute)
	deps := ports.Deps{Clock: func() time.Time { return now }}

	src := &fixtureCandles{}
	labeler, err := outcome.New(jr, src, deps, outcome.Config{
		HorizonBars: 1,
		FeeBps:      0,
		PriceSource: "fixture",
		CodeVersion: "test",
	})
	if err != nil {
		t.Fatalf("new labeler: %v", err)
	}

	notifier := &fakeNotifier{}
	reporter := &fakeReporter{}

	runner, err := New(jr, labeler, nil, nil, reporter, notifier, deps, Config{CodeVersion: "test"})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}

	sizeStart := runner.debugSize()
	goroutinesStart := runtime.NumGoroutine()

	start := time.Now()
	for i := 0; i < sevenDaySteps; i++ {
		if err := runner.Step(ctx); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		now = now.Add(sevenDayInterval)
	}
	elapsed := time.Since(start)

	ids := labeledSignalIDs(t, ctx, jr)
	if len(ids) != sevenDaySignals {
		t.Fatalf("outcome.labeled = %d, want %d", len(ids), sevenDaySignals)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate outcome for signal %q", id)
		}
		seen[id] = true
	}
	for i := 0; i < sevenDaySignals; i++ {
		if !seen[fmt.Sprintf("sig-%d", i)] {
			t.Fatalf("signal sig-%d never labeled", i)
		}
	}

	// One further cycle must not append anything.
	before := len(ids)
	if err := runner.Step(ctx); err != nil {
		t.Fatalf("extra step: %v", err)
	}
	if got := len(labeledSignalIDs(t, ctx, jr)); got != before {
		t.Fatalf("outcome.labeled after idle step = %d, want %d", got, before)
	}

	res, err := jr.Verify(ctx)
	if err != nil {
		t.Fatalf("verify journal: %v", err)
	}
	if !res.OK {
		t.Fatalf("verify journal: ok=false brokenSeq=%d reason=%q", res.BrokenSeq, res.Reason)
	}

	if sizeEnd := runner.debugSize(); sizeEnd != sizeStart {
		t.Fatalf("debugSize start=%d end=%d, want bounded", sizeStart, sizeEnd)
	}

	// Wait for background goroutines to settle before comparing counts.
	deadline := time.Now().Add(10 * time.Second)
	for runtime.NumGoroutine() > goroutinesStart && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got != goroutinesStart {
		t.Fatalf("goroutines before=%d after=%d, want no leak", goroutinesStart, got)
	}

	t.Logf("7d shadow soak: %d steps, %d signals, %d candles calls, %d journal events, %s",
		sevenDaySteps, sevenDaySignals, src.calls, res.Count, elapsed.Round(time.Millisecond))
}
