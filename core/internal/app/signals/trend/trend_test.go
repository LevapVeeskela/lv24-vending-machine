package trend

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
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
	bySymbol    map[string][]domain.Candle
	err         error
	errBySymbol map[string]error
	calls       []domain.CandleRequest
}

func (f *fakeProvider) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "fake-marketdata", Kind: ports.KindMarketData}
}

func (f *fakeProvider) Candles(_ context.Context, req domain.CandleRequest) ([]domain.Candle, error) {
	f.calls = append(f.calls, req)
	if err := f.errBySymbol[req.Symbol]; err != nil {
		return nil, err
	}
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

// makeSeries builds hourly candles from float closes whose last bar closes
// one hour before testNow, so every bar is closed at the scan time.
func makeSeries(symbol, interval string, closes []float64) []domain.Candle {
	open := testNow.Add(-time.Duration(len(closes)+1) * time.Hour)
	bars := make([]domain.Candle, len(closes))
	for i, c := range closes {
		s := strconv.FormatFloat(c, 'f', -1, 64)
		bars[i] = domain.Candle{
			Symbol:    symbol,
			Interval:  interval,
			OpenTime:  open.Add(time.Duration(i) * time.Hour),
			CloseTime: open.Add(time.Duration(i+1) * time.Hour),
			Open:      domain.MustParseDecimal(s),
			High:      domain.MustParseDecimal(s),
			Low:       domain.MustParseDecimal(s),
			Close:     domain.MustParseDecimal(s),
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

func signalIDs(reqs []domain.GateRequest) []string {
	out := make([]string, len(reqs))
	for i, req := range reqs {
		out[i] = req.SignalID
	}
	sort.Strings(out)
	return out
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
	// Limit is max(4*Slow, Slow+2) = max(12, 5) = 12.
	if provider.calls[0] != (domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1h", Limit: 12}) {
		t.Fatalf("candle request = %+v", provider.calls[0])
	}
}

func TestScanRequestedLimitTracksSlow(t *testing.T) {
	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{
		"BTCUSDT": makeBars("BTCUSDT", "1h", "10", "9", "8", "7", "20"),
	}}
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 20, Sides: []string{"buy"}}
	if _, err := newGenerator(t, cfg, provider).Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(provider.calls) != 1 || provider.calls[0].Limit != 80 {
		t.Fatalf("candle request = %+v, want limit 80", provider.calls)
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
	if !got[0].TS.Equal(barBase.Add(5 * time.Hour)) {
		t.Fatalf("ts = %v, want the last closed bar", got[0].TS)
	}
}

func TestScanRepeatNoDuplicate(t *testing.T) {
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
	if len(first) != 2 || first[0].Instrument.Symbol != "BTCUSDT" || first[1].Instrument.Symbol != "ETHUSDT" {
		t.Fatalf("signals = %+v, want BTCUSDT then ETHUSDT", first)
	}
	second, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second scan signals = %+v, want none", second)
	}
}

func TestScanMultipleNewBars(t *testing.T) {
	// fast=2, slow=3. Crosses land on bars 6 (sell), 7 (buy) and 8 (sell).
	all := makeBars("BTCUSDT", "1h", "10", "10", "10", "10", "100", "100", "1", "100", "1")
	provider := &fakeProvider{bySymbol: map[string][]domain.Candle{"BTCUSDT": all[:6]}}
	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy", "sell"}}
	g := newGenerator(t, cfg, provider)

	// First scan: bar 4 crosses but it is history, so only the last closed
	// bar (5) is evaluated and it does not cross.
	first, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(first) != 0 {
		t.Fatalf("first scan signals = %+v, want none", first)
	}

	// Three new closed bars, each with a cross.
	provider.bySymbol["BTCUSDT"] = all
	second, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	want := []domain.GateRequest{
		{SignalID: signalID("BTCUSDT", "1h", "sell", barBase.Add(7*time.Hour))},
		{SignalID: signalID("BTCUSDT", "1h", "buy", barBase.Add(8*time.Hour))},
		{SignalID: signalID("BTCUSDT", "1h", "sell", barBase.Add(9*time.Hour))},
	}
	if len(second) != len(want) {
		t.Fatalf("second scan signals = %+v, want %d", second, len(want))
	}
	for i, req := range second {
		if req.SignalID != want[i].SignalID {
			t.Fatalf("signal %d = %q, want %q", i, req.SignalID, want[i].SignalID)
		}
		if !req.TS.Equal(barBase.Add(time.Duration(i+7) * time.Hour)) {
			t.Fatalf("signal %d ts = %v", i, req.TS)
		}
	}
	if second[0].Price.String() != "1" || second[1].Price.String() != "100" || second[2].Price.String() != "1" {
		t.Fatalf("prices = %s/%s/%s", second[0].Price, second[1].Price, second[2].Price)
	}

	// A third scan without new bars emits nothing.
	third, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("third scan: %v", err)
	}
	if len(third) != 0 {
		t.Fatalf("third scan signals = %+v, want none", third)
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
	g := newGenerator(t, cfg, provider)
	got, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("signals = %+v, want none", got)
	}
	// An insufficient history leaves no baseline behind: a later scan with
	// enough bars still behaves like a first scan.
	provider.bySymbol["BTCUSDT"] = makeBars("BTCUSDT", "1h", "10", "9", "8", "7", "20")
	got, err = g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || got[0].Side != domain.SideBuy {
		t.Fatalf("signals = %+v, want one buy", got)
	}
}

func TestScanOneBadSymbol(t *testing.T) {
	var logs bytes.Buffer
	deps := testDeps()
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	provider := &fakeProvider{
		bySymbol: map[string][]domain.Candle{
			"ETHUSDT": makeBars("ETHUSDT", "1h", "10", "11", "12", "13", "1"),
		},
		errBySymbol: map[string]error{"BTCUSDT": errors.New("venue unavailable")},
	}
	cfg := Config{Symbols: []string{"BTCUSDT", "ETHUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy", "sell"}}
	g, err := New(cfg, provider, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: want nil error, got %v", err)
	}
	if len(got) != 1 || got[0].Instrument.Symbol != "ETHUSDT" || got[0].Side != domain.SideSell {
		t.Fatalf("signals = %+v, want one ETHUSDT sell", got)
	}
	if !strings.Contains(logs.String(), "BTCUSDT") {
		t.Fatalf("log = %q, want the failing symbol", logs.String())
	}
}

func TestScanAllSymbolsFail(t *testing.T) {
	var logs bytes.Buffer
	deps := testDeps()
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	provider := &fakeProvider{err: errors.New("boom")}
	cfg := Config{Symbols: []string{"BTCUSDT", "ETHUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}}
	g, err := New(cfg, provider, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := g.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: want nil error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("signals = %+v, want none", got)
	}
	if !strings.Contains(logs.String(), "BTCUSDT") || !strings.Contains(logs.String(), "ETHUSDT") {
		t.Fatalf("log = %q, want both failing symbols", logs.String())
	}
}

func TestScanVenue(t *testing.T) {
	bars := makeBars("BTCUSDT", "1h", "10", "9", "8", "7", "20")
	base := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}}

	defaulted := base
	got, err := newGenerator(t, defaulted, &fakeProvider{bySymbol: map[string][]domain.Candle{"BTCUSDT": bars}}).Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || got[0].Instrument.Venue != "binance" {
		t.Fatalf("signals = %+v, want venue binance", got)
	}

	custom := base
	custom.Venue = "bybit"
	got, err = newGenerator(t, custom, &fakeProvider{bySymbol: map[string][]domain.Candle{"BTCUSDT": bars}}).Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || got[0].Instrument.Venue != "bybit" {
		t.Fatalf("signals = %+v, want venue bybit", got)
	}
}

// TestScanWindowStability slides the start of a 500-bar window and requires
// the same signal set from a fresh generator, whose first scan evaluates only
// the last closed bar.
func TestScanWindowStability(t *testing.T) {
	closes := make([]float64, 500)
	for i := 0; i < 460; i++ {
		closes[i] = 100 + 20*math.Sin(float64(i)/9)
	}
	// A steady ramp leaves the fast EMA above the slow EMA; the final drop
	// then produces exactly one sell cross on the last closed bar.
	for i := 460; i < 499; i++ {
		closes[i] = 100 + float64(i-460)*0.5
	}
	closes[499] = 50

	cfg := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 5, Slow: 12, Sides: []string{"buy", "sell"}}
	var base []string
	for offset := 0; offset <= 10; offset++ {
		provider := &fakeProvider{bySymbol: map[string][]domain.Candle{
			"BTCUSDT": makeSeries("BTCUSDT", "1h", closes[offset:]),
		}}
		got, err := newGenerator(t, cfg, provider).Scan(context.Background())
		if err != nil {
			t.Fatalf("offset %d: Scan: %v", offset, err)
		}
		ids := signalIDs(got)
		if offset == 0 {
			base = ids
			if len(base) != 1 {
				t.Fatalf("base signals = %v, want one sell cross", base)
			}
			continue
		}
		if !reflect.DeepEqual(ids, base) {
			t.Fatalf("offset %d signals = %v, want %v", offset, ids, base)
		}
	}
}

// TestParseTimeframe pins the shared domain parser through the trend facade:
// units are lowercase, the amount is a positive integer.
func TestParseTimeframe(t *testing.T) {
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{"15m", 15 * time.Minute},
		{"1h", time.Hour},
		{"1d", 24 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
	}
	for _, tt := range tests {
		got, err := domain.ParseTimeframe(tt.raw)
		if err != nil {
			t.Fatalf("ParseTimeframe(%q): %v", tt.raw, err)
		}
		if got != tt.want {
			t.Fatalf("ParseTimeframe(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
	for _, raw := range []string{"", "h", "0m", "-1h", "1x", "1", "1.5h", "m15", "4H"} {
		if _, err := domain.ParseTimeframe(raw); err == nil {
			t.Fatalf("ParseTimeframe(%q): want error, got nil", raw)
		}
	}
}

func TestNewLabelInterval(t *testing.T) {
	valid := Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}}
	for _, cfg := range []Config{
		{Timeframe: "1h", LabelIntervalSec: 1800},
		{Timeframe: "1d", LabelIntervalSec: 43200},
		{Timeframe: "1w", LabelIntervalSec: 86400},
	} {
		cfg.Symbols = valid.Symbols
		cfg.Fast = valid.Fast
		cfg.Slow = valid.Slow
		cfg.Sides = valid.Sides
		if _, err := New(cfg, &fakeProvider{}, testDeps()); err != nil {
			t.Fatalf("New(%+v): %v", cfg, err)
		}
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
		{"negative label interval", Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}, LabelIntervalSec: -1}, &fakeProvider{}, domain.CodeInvalidRequest},
		{"label interval too long", Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}, LabelIntervalSec: 3600}, &fakeProvider{}, domain.CodeInvalidRequest},
		{"label interval equal to half", Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1h", Fast: 2, Slow: 3, Sides: []string{"buy"}, LabelIntervalSec: 1801}, &fakeProvider{}, domain.CodeInvalidRequest},
		{"unparseable timeframe with label", Config{Symbols: []string{"BTCUSDT"}, Timeframe: "1x", Fast: 2, Slow: 3, Sides: []string{"buy"}, LabelIntervalSec: 60}, &fakeProvider{}, domain.CodeInvalidRequest},
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
