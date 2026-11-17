package trend

import (
	"context"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

func mustSides(t *testing.T, raw ...string) map[domain.Side]bool {
	t.Helper()
	sides, err := Sides(raw)
	if err != nil {
		t.Fatalf("Sides(%v): %v", raw, err)
	}
	return sides
}

func TestCrossingsKnownCount(t *testing.T) {
	bars := makeBars("BTCUSDT", "1h", "10", "10", "20", "20", "5", "5", "30")
	got, err := Crossings(bars, 1, 2, mustSides(t, "buy", "sell"))
	if err != nil {
		t.Fatalf("Crossings: %v", err)
	}
	want := []struct {
		index int
		side  domain.Side
	}{
		{2, domain.SideBuy},
		{4, domain.SideSell},
		{6, domain.SideBuy},
	}
	if len(got) != len(want) {
		t.Fatalf("crossings = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Index != w.index || got[i].Side != w.side {
			t.Fatalf("crossing %d = %+v, want index %d side %s", i, got[i], w.index, w.side)
		}
	}
	if got[0].EMAFast != 20 || got[0].Close != 20 {
		t.Fatalf("crossing values = %+v", got[0])
	}

	got, err = Crossings(bars, 1, 2, mustSides(t, "buy"))
	if err != nil {
		t.Fatalf("Crossings buy only: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("buy crossings = %d, want 2: %+v", len(got), got)
	}
}

func TestCrossingsInsufficientBars(t *testing.T) {
	// slow+2 is the minimum: a slow EMA needs a previous bar and a bar to
	// cross on.
	bars := makeBars("BTCUSDT", "1h", "10", "10", "20")
	got, err := Crossings(bars, 1, 2, mustSides(t, "buy", "sell"))
	if err != nil {
		t.Fatalf("Crossings: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("crossings = %+v, want none below slow+2 bars", got)
	}
}

// estimateCloses builds 32 hourly closes: flat 10 followed by a jump to 20 at
// index 20. The only crossing lands inside the one-day estimate window.
func estimateCloses() []float64 {
	closes := make([]float64, 32)
	for i := range closes {
		closes[i] = 10
	}
	for i := 20; i < len(closes); i++ {
		closes[i] = 20
	}
	return closes
}

// TestEstimateMatchesSequentialScan feeds the same bars to Scan one at a time
// and requires the estimate to count exactly the signals the scan emitted.
func TestEstimateMatchesSequentialScan(t *testing.T) {
	bars := makeSeries("BTCUSDT", "1h", estimateCloses())

	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{"BTCUSDT": bars}}
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 1, Slow: 2, Sides: []string{"buy", "sell"}}
	g := newGenerator(t, cfg, provider)

	estimates, err := g.Estimate(context.Background(), 1)
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if len(estimates) != 1 || estimates[0].Err != nil {
		t.Fatalf("estimates = %+v", estimates)
	}
	// One day of hourly bars plus the slow*4 warm-up reserve and the slack,
	// anchored at the start of the warm-up history so a paginating source
	// serves the whole window.
	if len(provider.calls) != 1 || provider.calls[0].Limit != 34 {
		t.Fatalf("estimate candle request = %+v, want limit 34", provider.calls)
	}
	if want := testNow.Add(-32 * time.Hour); !provider.calls[0].From.Equal(want) {
		t.Fatalf("estimate candle request from = %s, want %s", provider.calls[0].From, want)
	}

	var scanned int
	for n := 1; n <= len(bars); n++ {
		provider.bySymbol["BTCUSDT"] = bars[:n]
		got, err := g.Scan(context.Background())
		if err != nil {
			t.Fatalf("scan prefix %d: %v", n, err)
		}
		scanned += len(got)
	}
	if estimates[0].Signals != scanned {
		t.Fatalf("estimate = %d, sequential scan = %d", estimates[0].Signals, scanned)
	}
	if scanned != 1 {
		t.Fatalf("sequential scan = %d, want the single crossing", scanned)
	}
}

// TestEstimateExcludesWarmupWindow keeps the only crossings in the warm-up
// history: they warm the EMAs up but are not counted in the window.
func TestEstimateExcludesWarmupWindow(t *testing.T) {
	closes := make([]float64, 32)
	for i := range closes {
		closes[i] = 5
	}
	copy(closes[:5], []float64{10, 10, 20, 20, 5})
	bars := makeSeries("BTCUSDT", "1h", closes)
	all, err := Crossings(bars, 1, 2, mustSides(t, "buy", "sell"))
	if err != nil {
		t.Fatalf("Crossings: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("crossings in the full series = %d, want 2", len(all))
	}

	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{"BTCUSDT": bars}}
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 1, Slow: 2, Sides: []string{"buy", "sell"}}
	got, err := newGenerator(t, cfg, provider).Estimate(context.Background(), 1)
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if got[0].Signals != 0 {
		t.Fatalf("window signals = %d, want 0 (crossings are warm-up only)", got[0].Signals)
	}
}

func TestEstimateProviderError(t *testing.T) {
	provider := &fakeProvider{
		bySymbol: map[string][]domain.Candle{
			"ETHUSDT": makeSeries("ETHUSDT", "1h", longSeries(10, 10, 20, 20, 5)),
		},
		errBySymbol: map[string]error{
			"BTCUSDT": domain.NewError(domain.CodeUnavailable, "venue down"),
		},
	}
	cfg := Config{Symbols: []string{"BTCUSDT", "ETHUSDT"}, Timeframe: "1h", Fast: 1, Slow: 2, Sides: []string{"buy", "sell"}}
	got, err := newGenerator(t, cfg, provider).Estimate(context.Background(), 1)
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("estimates = %+v, want two rows", got)
	}
	if got[0].Symbol != "BTCUSDT" || got[0].Err == nil || domain.CodeOf(got[0].Err) != domain.CodeUnavailable {
		t.Fatalf("first estimate = %+v, want the provider error", got[0])
	}
	if got[1].Symbol != "ETHUSDT" || got[1].Err != nil || got[1].Signals != 2 {
		t.Fatalf("second estimate = %+v, want two signals", got[1])
	}
}

// longSeries pads tail with flat closes so the series covers a one-day window
// plus the warm-up history of the test generators.
func longSeries(tail ...float64) []float64 {
	const total = 40
	closes := make([]float64, 0, total)
	for len(closes) < total-len(tail) {
		closes = append(closes, tail[0])
	}
	return append(closes, tail...)
}

// TestEstimateShortHistory proves a symbol whose candles start inside the
// window is reported, not counted: dividing its crossings by the full number
// of days would understate the frequency.
func TestEstimateShortHistory(t *testing.T) {
	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{
		"BTCUSDT": makeSeries("BTCUSDT", "1h", []float64{10, 10, 20, 20, 5}),
	}}
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 1, Slow: 2, Sides: []string{"buy", "sell"}}
	got, err := newGenerator(t, cfg, provider).Estimate(context.Background(), 1)
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if len(got) != 1 || got[0].Err == nil || domain.CodeOf(got[0].Err) != domain.CodeInvalidRequest {
		t.Fatalf("estimate = %+v, want an invalid_request error for the short history", got)
	}
	if got[0].Signals != 0 {
		t.Fatalf("signals = %d, want 0 for an uncovered window", got[0].Signals)
	}
}

func TestEstimateInvalidDays(t *testing.T) {
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 1, Slow: 2, Sides: []string{"buy"}}
	g := newGenerator(t, cfg, &fakeProvider{})
	if _, err := g.Estimate(context.Background(), 0); err == nil {
		t.Fatal("Estimate(0): want error, got nil")
	}
}
