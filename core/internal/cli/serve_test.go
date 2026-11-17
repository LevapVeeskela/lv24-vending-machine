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

	signals, err := shadowSignals(config.TrendConfig{Enabled: false}, nil, deps)
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
	}, staticCandles{}, shadowTestDeps())
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
