package trend

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

var (
	barBase = time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	testNow = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
)

// fakeProvider serves canned candles keyed by requested symbol.
type fakeProvider struct {
	bySymbol map[string][]domain.Candle
	err      error
	calls    []domain.CandleRequest
}

func (f *fakeProvider) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "fake-marketdata", Kind: ports.KindMarketData}
}

func (f *fakeProvider) Candles(_ context.Context, req domain.CandleRequest) ([]domain.Candle, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	out := make([]domain.Candle, len(f.bySymbol[req.Symbol]))
	copy(out, f.bySymbol[req.Symbol])
	return out, nil
}

// makeBars builds hourly candles whose close prices are closes. The last
// bar closes at barBase+len(closes) hours, always before testNow.
func makeBars(symbol, interval string, closes ...string) []domain.Candle {
	bars := make([]domain.Candle, len(closes))
	for i, c := range closes {
		bars[i] = domain.Candle{
			Symbol:    symbol,
			Interval:  interval,
			OpenTime:  barBase.Add(time.Duration(i) * time.Hour),
			CloseTime: barBase.Add(time.Duration(i+1) * time.Hour),
			Open:      domain.MustParseDecimal(c),
			High:      domain.MustParseDecimal(c),
			Low:       domain.MustParseDecimal(c),
			Close:     domain.MustParseDecimal(c),
			Volume:    domain.MustParseDecimal("1"),
		}
	}
	return bars
}

func testDeps() ports.Deps {
	return ports.Deps{Clock: func() time.Time { return testNow }}
}

func newGenerator(t *testing.T, cfg Config, provider *fakeProvider) *Generator {
	t.Helper()
	g, err := New(cfg, provider, testDeps())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func signalID(symbol, timeframe, side string, closeTime time.Time) string {
	return "trend:" + symbol + ":" + timeframe + ":" +
		strconv.FormatInt(closeTime.Unix(), 10) + ":" + side
}

func TestScanBuyCross(t *testing.T) {
	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{
		"BTCUSDT": makeBars("BTCUSDT", "1h", "10", "9", "8", "7", "20"),
	}}
	cfg := Config{Symbols: []string{"btc/usdt"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}}
	g := newGenerator(t, cfg, provider)

	got, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("signals = %d, want 1: %+v", len(got), got)
	}
	req := got[0]
	lastClose := barBase.Add(5 * time.Hour)
	if req.SignalID != signalID("BTCUSDT", "1h", "buy", lastClose) {
		t.Fatalf("signal id = %q", req.SignalID)
	}
	if req.Instrument.Venue != "binance" || req.Instrument.Symbol != "BTCUSDT" {
		t.Fatalf("instrument = %+v", req.Instrument)
	}
	if req.Side != domain.SideBuy || req.IntentKind != domain.IntentEntry {
		t.Fatalf("side/intent = %q/%q", req.Side, req.IntentKind)
	}
	if req.Timeframe != "1h" || req.Source != "trend" {
		t.Fatalf("timeframe/source = %q/%q", req.Timeframe, req.Source)
	}
	if !req.TS.Equal(lastClose) {
		t.Fatalf("ts = %v, want %v", req.TS, lastClose)
	}
	if req.Price.String() != "20" {
		t.Fatalf("price = %s, want 20", req.Price)
	}
	approx(t, "state ema_fast", req.State["ema_fast"], 15.827160493827162)
	approx(t, "state ema_slow", req.State["ema_slow"], 13.9375)
	approx(t, "state close", req.State["close"], 20)
	approx(t, "state fast", req.State["fast"], 2)
	approx(t, "state slow", req.State["slow"], 3)

	if len(provider.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(provider.calls))
	}
	if provider.calls[0] != (domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1h", Limit: 6}) {
		t.Fatalf("candle request = %+v", provider.calls[0])
	}
}

func TestScanSellCross(t *testing.T) {
	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{
		"ETHUSDT": makeBars("ETHUSDT", "1h", "10", "11", "12", "13", "1"),
	}}
	cfg := Config{Symbols: []string{"ETHUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy", "sell"}}
	got, err := newGenerator(t, cfg, provider).Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("signals = %d, want 1: %+v", len(got), got)
	}
	req := got[0]
	if req.Side != domain.SideSell {
		t.Fatalf("side = %q, want sell", req.Side)
	}
	if req.SignalID != signalID("ETHUSDT", "1h", "sell", barBase.Add(5*time.Hour)) {
		t.Fatalf("signal id = %q", req.SignalID)
	}
	if req.Price.String() != "1" {
		t.Fatalf("price = %s, want 1", req.Price)
	}
	approx(t, "state ema_fast", req.State["ema_fast"], 4.839506172839506)
	approx(t, "state ema_slow", req.State["ema_slow"], 6.5625)
}

func TestScanIgnoresUnclosedBar(t *testing.T) {
	bars := makeBars("BTCUSDT", "1h", "10", "9", "8", "7", "20")
	// An unclosed bar at now that would cross back down.
	unclosed := makeBars("BTCUSDT", "1h", "1")[0]
	unclosed.OpenTime = testNow
	unclosed.CloseTime = testNow
	bars = append(bars, unclosed)

	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{"BTCUSDT": bars}}
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy", "sell"}}
	got, err := newGenerator(t, cfg, provider).Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || got[0].Side != domain.SideBuy {
		t.Fatalf("signals = %+v, want one buy", got)
	}
}

func TestScanRepeatIsDeterministic(t *testing.T) {
	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{
		"BTCUSDT": makeBars("BTCUSDT", "1h", "10", "9", "8", "7", "20"),
		"ETHUSDT": makeBars("ETHUSDT", "1h", "10", "11", "12", "13", "1"),
	}}
	cfg := Config{Symbols: []string{"BTCUSDT", "ETHUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy", "sell"}}
	g := newGenerator(t, cfg, provider)

	first, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	second, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("scans differ:\nfirst  = %+v\nsecond = %+v", first, second)
	}
	if len(first) != 2 || first[0].Instrument.Symbol != "BTCUSDT" || first[1].Instrument.Symbol != "ETHUSDT" {
		t.Fatalf("signals = %+v, want BTCUSDT then ETHUSDT", first)
	}
}

func TestScanNoCross(t *testing.T) {
	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{
		"BTCUSDT": makeBars("BTCUSDT", "1h", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10"),
	}}
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy", "sell"}}
	got, err := newGenerator(t, cfg, provider).Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("signals = %+v, want none", got)
	}
}

func TestScanSuppressesDisallowedSide(t *testing.T) {
	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{
		"BTCUSDT": makeBars("BTCUSDT", "1h", "10", "11", "12", "13", "1"),
	}}
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}}
	got, err := newGenerator(t, cfg, provider).Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("signals = %+v, want none for a suppressed sell", got)
	}
}

func TestScanFiltersSortsAndDedups(t *testing.T) {
	bars := makeBars("BTCUSDT", "1h", "10", "9", "8", "7", "20")
	duplicate := bars[len(bars)-1]
	duplicate.Close = domain.MustParseDecimal("999")

	raw := []domain.Candle{
		makeBars("ETHUSDT", "1h", "10", "9", "8", "7", "20")[0],
		makeBars("BTCUSDT", "4h", "10", "9", "8", "7", "20")[0],
		bars[4], bars[0], bars[3], duplicate, bars[1], bars[2],
	}
	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{"BTCUSDT": raw}}
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}}
	got, err := newGenerator(t, cfg, provider).Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("signals = %d, want 1: %+v", len(got), got)
	}
	if got[0].Price.String() != "20" {
		t.Fatalf("price = %s, want the first bar of the duplicated close time (20)", got[0].Price)
	}
	if !got[0].TS.Equal(barBase.Add(5 * time.Hour)) {
		t.Fatalf("ts = %v", got[0].TS)
	}
}

func TestScanInsufficientBars(t *testing.T) {
	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{
		"BTCUSDT": makeBars("BTCUSDT", "1h", "10", "9", "8", "7"),
	}}
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy", "sell"}}
	got, err := newGenerator(t, cfg, provider).Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("signals = %+v, want none", got)
	}
}

func TestScanProviderError(t *testing.T) {
	provider := &fakeProvider{err: errors.New("boom")}
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}}
	if _, err := newGenerator(t, cfg, provider).Scan(context.Background()); err == nil {
		t.Fatal("Scan: want provider error, got nil")
	}
}

func TestNewValidation(t *testing.T) {
	valid := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}}
	tests := []struct {
		name     string
		cfg      Config
		provider ports.CandleProvider
		wantCode domain.ErrorCode
	}{
		{"nil provider", valid, nil, domain.CodeInvalidRequest},
		{"no symbols", Config{Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}}, &fakeProvider{}, domain.CodeInvalidRequest},
		{"blank symbol", Config{Symbols: []string{" /- "}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}}, &fakeProvider{}, domain.CodeInvalidRequest},
		{"blank timeframe", Config{Symbols: []string{"BTCUSDT"}, Timeframe: "  ", Fast: 2, Slow: 3, Sides: []string{"buy"}}, &fakeProvider{}, domain.CodeInvalidRequest},
		{"zero fast", Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 0, Slow: 3, Sides: []string{"buy"}}, &fakeProvider{}, domain.CodeInvalidRequest},
		{"fast equals slow", Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 3, Slow: 3, Sides: []string{"buy"}}, &fakeProvider{}, domain.CodeInvalidRequest},
		{"no sides", Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3}, &fakeProvider{}, domain.CodeInvalidRequest},
		{"bad side", Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"hold"}}, &fakeProvider{}, domain.CodeInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg, tt.provider, testDeps())
			if err == nil {
				t.Fatal("New: want error, got nil")
			}
			if code := domain.CodeOf(err); code != tt.wantCode {
				t.Fatalf("code = %q, want %q", code, tt.wantCode)
			}
		})
	}
}
