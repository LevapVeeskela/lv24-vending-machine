package cli

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/shadow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

func serveTestAddr() string {
	return "127.0.0.1:18421"
}

func TestServeMissingToken(t *testing.T) {
	env, _, stderr := testEnv(t)
	env.Getenv = func(string) string { return "" }
	addr := serveTestAddr()
	code := runArgs(env, "serve", "--addr", addr)
	if code != exitForbidden {
		t.Fatalf("serve without token = %d, want %d, stderr=%s", code, exitForbidden, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "local API token is not set") {
		t.Fatalf("stderr misses message: %q", got)
	}
	if got := stderr.String(); !strings.Contains(got, "VENDING_SECRET_CONN_LOCAL_APITOKEN") {
		t.Fatalf("stderr misses env name: %q", got)
	}
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("serve started a listener despite the missing token")
	}
}

func TestServeInvalidAddr(t *testing.T) {
	env, _, stderr := testEnv(t)
	for _, addr := range []string{"localhost:8420", "0.0.0.0:8420", "[::1]:8420", "127.0.0.1:0"} {
		stderr.Reset()
		code := runArgs(env, "serve", "--addr", addr)
		if code != exitUsage {
			t.Fatalf("serve --addr %q = %d, want %d, stderr=%s", addr, code, exitUsage, stderr.String())
		}
	}
}

func TestServeBusyPortMessage(t *testing.T) {
	env, _, stderr, cfgPath, dataDir := initTestEnv(t)
	env.Getenv = func(string) string { return "test-token" }
	env.NewRegistry = liveRegistry(t, okDecision(), okCandles(), nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	if code := runArgs(env, "serve", "--addr", addr, "--config", cfgPath, "--data-dir", dataDir); code != exitGeneral {
		t.Fatalf("serve busy port = %d, want %d, stderr=%s", code, exitGeneral, stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "starting on http://"+addr) {
		t.Fatalf("stderr misses the startup line: %q", got)
	}
	if !strings.Contains(got, "check the address is free and retry") {
		t.Fatalf("stderr misses the busy-port hint: %q", got)
	}
}

// staticCandles is a candle provider with no data; Scan returns no signals.
type staticCandles struct{}

func (staticCandles) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "marketdata-static", Kind: ports.KindMarketData}
}

func (staticCandles) Candles(context.Context, domain.CandleRequest) ([]domain.Candle, error) {
	return nil, nil
}

func shadowTestDeps() ports.Deps {
	return ports.Deps{
		Clock: func() time.Time {
			return time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
		},
		NewID: func() string { return "id-shadow-test" },
	}
}

// TestShadowSignalsTrendDisabled is the regression test for the typed-nil
// SignalSource: with signals.trend.enabled=false the runner must receive a
// nil interface and its first Step must not panic.
func TestShadowSignalsTrendDisabled(t *testing.T) {
	env, _, _ := testEnv(t)
	dataDir := t.TempDir()
	jr, closer, err := env.OpenJournal(filepath.Join(dataDir, "journal.db"))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer func() { _ = closer.Close() }()
	deps := shadowTestDeps()

	signals, err := shadowSignals(config.TrendConfig{Enabled: false}, 0, nil, deps)
	if err != nil {
		t.Fatalf("shadowSignals(disabled) = %v", err)
	}
	if signals != nil {
		t.Fatalf("trend disabled returned a non-nil SignalSource %T", signals)
	}
	r, err := shadow.New(jr, nil, signals, nil, nil, nil, deps, shadow.Config{
		StatusPath: filepath.Join(dataDir, "shadow-status.json"),
	})
	if err != nil {
		t.Fatalf("shadow.New() = %v", err)
	}
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
}

func TestShadowSignalsTrendEnabled(t *testing.T) {
	signals, err := shadowSignals(config.TrendConfig{
		Enabled:   true,
		Symbols:   []string{"BTCUSDT"},
		Timeframe: "1h",
		Fast:      2,
		Slow:      3,
		Sides:     []string{"buy"},
	}, 0, staticCandles{}, shadowTestDeps())
	if err != nil {
		t.Fatalf("shadowSignals(enabled) = %v", err)
	}
	if signals == nil {
		t.Fatal("trend enabled returned a nil SignalSource")
	}
	if _, err := signals.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() = %v", err)
	}
}

// crossCandles serves six hourly bars; the last one is still forming at the
// fixed test clock, so the first five are closed and the last closed bar
// produces exactly one buy cross (the closes mirror the trend package tests).
type crossCandles struct{}

func (crossCandles) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "marketdata-cross", Kind: ports.KindMarketData}
}

func (crossCandles) Candles(context.Context, domain.CandleRequest) ([]domain.Candle, error) {
	closes := []string{"10", "9", "8", "7", "20", "30"}
	base := time.Date(2026, time.November, 10, 6, 0, 0, 0, time.UTC)
	bars := make([]domain.Candle, len(closes))
	for i, c := range closes {
		bars[i] = domain.Candle{
			Symbol:    "BTCUSDT",
			Interval:  "1h",
			OpenTime:  base.Add(time.Duration(i) * time.Hour),
			CloseTime: base.Add(time.Duration(i+1) * time.Hour),
			Open:      domain.MustParseDecimal(c),
			High:      domain.MustParseDecimal(c),
			Low:       domain.MustParseDecimal(c),
			Close:     domain.MustParseDecimal(c),
			Volume:    domain.MustParseDecimal("1"),
		}
	}
	return bars, nil
}

// TestShadowSignalsForwardsVenueAndLabel proves the generator receives both
// values: the venue is visible on the emitted request, and the label interval
// is observable at the guard boundary (1h holds exactly two 30m windows).
func TestShadowSignalsForwardsVenueAndLabel(t *testing.T) {
	signals, err := shadowSignals(config.TrendConfig{
		Enabled:   true,
		Venue:     "bybit",
		Symbols:   []string{"BTCUSDT"},
		Timeframe: "1h",
		Fast:      2,
		Slow:      3,
		Sides:     []string{"buy"},
	}, 1800, crossCandles{}, shadowTestDeps())
	if err != nil {
		t.Fatalf("shadowSignals() = %v", err)
	}
	reqs, err := signals.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() = %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("signals = %d, want 1", len(reqs))
	}
	if got := reqs[0].Instrument.Venue; got != "bybit" {
		t.Fatalf("venue = %q, want bybit (signals.trend.venue not forwarded)", got)
	}
}

// TestShadowSignalsLabelWindowGuard is the regression for the dead guard: the
// label granularity was not forwarded before, so a 1h candle was accepted
// with a 1h label interval.
func TestShadowSignalsLabelWindowGuard(t *testing.T) {
	_, err := shadowSignals(config.TrendConfig{
		Enabled:   true,
		Symbols:   []string{"BTCUSDT"},
		Timeframe: "1h",
		Fast:      2,
		Slow:      3,
		Sides:     []string{"buy"},
	}, 3600, crossCandles{}, shadowTestDeps())
	if err == nil {
		t.Fatal("shadowSignals() accepted timeframe shorter than twice the label interval")
	}
	if !strings.Contains(err.Error(), "twice the label interval") {
		t.Fatalf("error %q misses the label-window guard", err)
	}
}
