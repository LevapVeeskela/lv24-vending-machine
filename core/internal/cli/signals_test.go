package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/registry"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// candlesDouble serves canned candles to the CLI tests without any network.
type candlesDouble struct {
	bySymbol    map[string][]domain.Candle
	errBySymbol map[string]error
	calls       []domain.CandleRequest
}

func (p *candlesDouble) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "marketdata-binance", Kind: ports.KindMarketData}
}

func (p *candlesDouble) Candles(_ context.Context, req domain.CandleRequest) ([]domain.Candle, error) {
	p.calls = append(p.calls, req)
	if err := p.errBySymbol[req.Symbol]; err != nil {
		return nil, err
	}
	return append([]domain.Candle(nil), p.bySymbol[req.Symbol]...), nil
}

// cliBars builds hourly bars from closes; the last bar closes one hour before
// now, so every bar is closed and recent enough for a one-day window.
func cliBars(symbol string, closes []string) []domain.Candle {
	now := time.Now().UTC()
	bars := make([]domain.Candle, len(closes))
	for i, c := range closes {
		open := now.Add(-time.Duration(len(closes)-i+1) * time.Hour)
		price := domain.MustParseDecimal(c)
		bars[i] = domain.Candle{
			Symbol:    symbol,
			Interval:  "1h",
			OpenTime:  open,
			CloseTime: open.Add(time.Hour),
			Open:      price,
			High:      price,
			Low:       price,
			Close:     price,
			Volume:    domain.MustParseDecimal("1"),
		}
	}
	return bars
}

// alternatingCloses alternates two prices; with fast=1 and slow=2 every bar
// after the first is a crossing.
func alternatingCloses(n int) []string {
	out := make([]string, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = "10"
		} else {
			out[i] = "20"
		}
	}
	return out
}

// signalsEnv builds a CLI environment whose registry serves only provider.
func signalsEnv(t *testing.T, provider ports.CandleProvider) (Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	env, stdout, stderr := testEnv(t)
	env.NewRegistry = func(config.Config, *slog.Logger) (*registry.Registry, error) {
		reg := registry.New()
		if err := reg.RegisterCandle(provider); err != nil {
			return nil, err
		}
		return reg, nil
	}
	return env, stdout, stderr
}

// initSignalsEnv creates a config and data directory and enables the trend
// source with the given overrides.
func initSignalsEnv(t *testing.T, env Env, tr config.TrendConfig) (cfgPath, dataDir string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.json")
	dataDir = filepath.Join(dir, "data")
	if code := runArgs(env, "init", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("init = %d", code)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if tr.Venue == "" {
		tr.Venue = "binance"
	}
	cfg.Signals.Trend = tr
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return cfgPath, dataDir
}

func trendConfig(symbols ...string) config.TrendConfig {
	return config.TrendConfig{
		Enabled:   true,
		Venue:     "binance",
		Symbols:   symbols,
		Timeframe: "1h",
		Fast:      1,
		Slow:      2,
		Sides:     []string{"buy", "sell"},
	}
}

func TestSignalsEstimateHuman(t *testing.T) {
	provider := &candlesDouble{bySymbol: map[string][]domain.Candle{
		"BTCUSDT": cliBars("BTCUSDT", []string{"10", "10", "20", "20", "5", "5", "30"}),
	}}
	env, stdout, stderr := signalsEnv(t, provider)
	cfgPath, dataDir := initSignalsEnv(t, env, trendConfig("BTCUSDT"))
	stdout.Reset()
	stderr.Reset()

	if code := runArgs(env, "signals", "estimate", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("signals estimate = %d, stderr=%s", code, stderr.String())
	}
	want := strings.Join([]string{
		"BTCUSDT signals=3 per_day=0.4",
		"total: signals=3 per_day=0.4",
		"target: 15 per day (100 labeled in 7 days)",
		"verdict: low",
		"past frequency does not predict future frequency",
		"",
	}, "\n")
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if len(provider.calls) != 1 || provider.calls[0].Interval != "1h" {
		t.Fatalf("candle calls = %+v", provider.calls)
	}
}

func TestSignalsEstimateEnough(t *testing.T) {
	provider := &candlesDouble{bySymbol: map[string][]domain.Candle{
		"BTCUSDT": cliBars("BTCUSDT", alternatingCloses(20)),
	}}
	env, stdout, stderr := signalsEnv(t, provider)
	cfgPath, dataDir := initSignalsEnv(t, env, trendConfig("BTCUSDT"))
	stdout.Reset()
	stderr.Reset()

	if code := runArgs(env, "signals", "estimate", "--days", "1", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("signals estimate = %d, stderr=%s", code, stderr.String())
	}
	for _, want := range []string{
		"BTCUSDT signals=19 per_day=19.0",
		"total: signals=19 per_day=19.0",
		"verdict: enough",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout misses %q: %q", want, stdout.String())
		}
	}
}

func TestSignalsEstimateBuyOnly(t *testing.T) {
	provider := &candlesDouble{bySymbol: map[string][]domain.Candle{
		"BTCUSDT": cliBars("BTCUSDT", alternatingCloses(20)),
	}}
	env, stdout, stderr := signalsEnv(t, provider)
	tr := trendConfig("BTCUSDT")
	tr.Sides = []string{"buy"}
	cfgPath, dataDir := initSignalsEnv(t, env, tr)
	stdout.Reset()
	stderr.Reset()

	if code := runArgs(env, "signals", "estimate", "--days", "1", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("signals estimate = %d, stderr=%s", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "BTCUSDT signals=10 per_day=10.0") {
		t.Fatalf("stdout = %q, want only the ten buy crossings", got)
	}
}

func TestSignalsEstimateOneBadSymbol(t *testing.T) {
	provider := &candlesDouble{
		bySymbol: map[string][]domain.Candle{
			"ETHUSDT": cliBars("ETHUSDT", []string{"10", "10", "20", "20", "5", "5", "30"}),
		},
		errBySymbol: map[string]error{
			"BTCUSDT": domain.NewError(domain.CodeUnavailable, "venue down"),
		},
	}
	env, stdout, stderr := signalsEnv(t, provider)
	cfgPath, dataDir := initSignalsEnv(t, env, trendConfig("BTCUSDT", "ETHUSDT"))
	stdout.Reset()
	stderr.Reset()

	if code := runArgs(env, "signals", "estimate", "--config", cfgPath, "--data-dir", dataDir); code != exitProvider {
		t.Fatalf("signals estimate = %d, want %d, stderr=%s", code, exitProvider, stderr.String())
	}
	want := strings.Join([]string{
		"BTCUSDT error=unavailable",
		"ETHUSDT signals=3 per_day=0.4",
		"total: signals=3 per_day=0.4",
		"target: 15 per day (100 labeled in 7 days)",
		"verdict: low",
		"past frequency does not predict future frequency",
		"",
	}, "\n")
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if !strings.Contains(stderr.String(), "signals estimate failed for 1 of 2 symbols") {
		t.Fatalf("stderr = %q, want the per-symbol failure summary", stderr.String())
	}

	// The command must not touch the journal.
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "verify", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal verify = %d, stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "journal ok: 0 events" {
		t.Fatalf("journal stdout = %q, want 0 events", got)
	}
}

func TestSignalsEstimateJSON(t *testing.T) {
	provider := &candlesDouble{bySymbol: map[string][]domain.Candle{
		"BTCUSDT": cliBars("BTCUSDT", []string{"10", "10", "20", "20", "5", "5", "30"}),
	}}
	env, stdout, stderr := signalsEnv(t, provider)
	cfgPath, dataDir := initSignalsEnv(t, env, trendConfig("BTCUSDT"))
	stdout.Reset()
	stderr.Reset()

	if code := runArgs(env, "signals", "estimate", "--json", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("signals estimate --json = %d, stderr=%s", code, stderr.String())
	}
	var res struct {
		Days         int     `json:"days"`
		TargetPerDay float64 `json:"targetPerDay"`
		Symbols      []struct {
			Symbol  string   `json:"symbol"`
			Signals *int     `json:"signals"`
			PerDay  *float64 `json:"perDay"`
			Error   string   `json:"error"`
		} `json:"symbols"`
		Total struct {
			Signals int     `json:"signals"`
			PerDay  float64 `json:"perDay"`
		} `json:"total"`
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, stdout.String())
	}
	if res.Days != 7 || res.TargetPerDay != 15 {
		t.Fatalf("json header = %+v", res)
	}
	if len(res.Symbols) != 1 || res.Symbols[0].Symbol != "BTCUSDT" ||
		res.Symbols[0].Signals == nil || *res.Symbols[0].Signals != 3 ||
		res.Symbols[0].PerDay == nil || *res.Symbols[0].PerDay != 0.4 {
		t.Fatalf("json symbols = %+v", res.Symbols)
	}
	if res.Total.Signals != 3 || res.Total.PerDay != 0.4 || res.Verdict != "low" {
		t.Fatalf("json total = %+v verdict=%q", res.Total, res.Verdict)
	}
}

func TestSignalsEstimateUsageErrors(t *testing.T) {
	env, _, stderr := signalsEnv(t, &candlesDouble{})
	cfgPath, dataDir := initSignalsEnv(t, env, config.TrendConfig{})

	for _, days := range []string{"0", "31"} {
		stderr.Reset()
		if code := runArgs(env, "signals", "estimate", "--days", days,
			"--config", cfgPath, "--data-dir", dataDir); code != exitUsage {
			t.Fatalf("--days %s = %d, want %d", days, code, exitUsage)
		}
		if !strings.Contains(stderr.String(), "invalid --days") ||
			!strings.Contains(stderr.String(), "between 1 and 30") {
			t.Fatalf("--days %s stderr = %q", days, stderr.String())
		}
	}

	stderr.Reset()
	if code := runArgs(env, "signals", "estimate", "--config", cfgPath, "--data-dir", dataDir); code != exitUsage {
		t.Fatalf("disabled trend = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "signals.trend is disabled") ||
		!strings.Contains(stderr.String(), "hint:") {
		t.Fatalf("disabled stderr = %q", stderr.String())
	}
}

func TestSignalsEstimateEmptySymbols(t *testing.T) {
	env, _, stderr := signalsEnv(t, &candlesDouble{})
	cfgPath, dataDir := initSignalsEnv(t, env, trendConfig())
	stderr.Reset()

	if code := runArgs(env, "signals", "estimate", "--config", cfgPath, "--data-dir", dataDir); code != exitUsage {
		t.Fatalf("empty symbols = %d, want %d, stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "signals.trend.symbols must not be empty") {
		t.Fatalf("stderr = %q, want the config validation error", stderr.String())
	}
}

func TestSignalsUnknownSubcommand(t *testing.T) {
	env, _, stderr := signalsEnv(t, &candlesDouble{})
	if code := runArgs(env, "signals", "guess"); code != exitUsage {
		t.Fatalf("unknown signals subcommand = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), `unknown signals subcommand "guess"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
