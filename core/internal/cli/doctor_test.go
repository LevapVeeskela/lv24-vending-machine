package cli

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/registry"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// fakeDecision answers the live decision probe.
type fakeDecision struct {
	resp  domain.DecisionResponse
	err   error
	calls int
}

func (p *fakeDecision) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "decision-drex", Name: "fake", Kind: ports.KindDecision, Version: "v0"}
}

func (p *fakeDecision) Decide(_ context.Context, _ domain.DecisionRequest) (domain.DecisionResponse, error) {
	p.calls++
	return p.resp, p.err
}

// fakeCandle answers the live candles probe.
type fakeCandle struct {
	candles []domain.Candle
	err     error
	calls   int
}

func (p *fakeCandle) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "marketdata-binance", Name: "fake", Kind: ports.KindMarketData, Version: "v0"}
}

func (p *fakeCandle) Candles(_ context.Context, _ domain.CandleRequest) ([]domain.Candle, error) {
	p.calls++
	return p.candles, p.err
}

// fakeNotifier records the live Telegram probe.
type fakeNotifier struct {
	err   error
	calls int
	texts []string
}

func (p *fakeNotifier) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "notify-telegram", Name: "fake", Kind: "notifier", Version: "v0"}
}

func (p *fakeNotifier) Send(_ context.Context, n ports.Notification) error {
	p.calls++
	p.texts = append(p.texts, n.Text)
	return p.err
}

// liveRegistry wires the fakes into the composition-root factory.
func liveRegistry(t *testing.T, dec *fakeDecision, cd *fakeCandle, nt *fakeNotifier) func(config.Config, *slog.Logger) (*registry.Registry, error) {
	t.Helper()
	return func(config.Config, *slog.Logger) (*registry.Registry, error) {
		reg := registry.New()
		if err := reg.RegisterDecision(dec); err != nil {
			return nil, err
		}
		if err := reg.RegisterCandle(cd); err != nil {
			return nil, err
		}
		if nt != nil {
			if err := reg.RegisterNotifier(nt); err != nil {
				return nil, err
			}
		}
		return reg, nil
	}
}

// enableTelegram turns the Telegram channel on in the config file.
func enableTelegram(t *testing.T, cfgPath string) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Notify.Telegram.Enabled = true
	cfg.Notify.Telegram.ChatID = "123456"
	cfg.Notify.Telegram.SecretRef = "conn/tg/token"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func okDecision() *fakeDecision {
	raw := []byte(`{"answers":{}}`)
	return &fakeDecision{resp: domain.DecisionResponse{
		Answers: map[string]domain.Answer{},
		Usage:   domain.Usage{CostUSD: domain.MustParseDecimal("0.0012")},
		Raw:     raw,
	}}
}

func okCandles() *fakeCandle {
	return &fakeCandle{candles: []domain.Candle{
		{Symbol: "BTCUSDT", Interval: "1h"},
		{Symbol: "BTCUSDT", Interval: "1h"},
	}}
}

func TestDoctorLiveOK(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	enableTelegram(t, cfgPath)
	dec, cd, nt := okDecision(), okCandles(), &fakeNotifier{}
	env.NewRegistry = liveRegistry(t, dec, cd, nt)

	rawPath := filepath.Join(t.TempDir(), "raw.json")
	args := []string{"doctor", "--live", "--config", cfgPath, "--data-dir", dataDir,
		"--save-raw", rawPath}
	if code := runArgs(env, args...); code != 0 {
		t.Fatalf("doctor --live = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"decision: ok cost=0.0012 USD", "save_raw: ok", "candles: ok", "telegram: ok",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout misses %q: %q", want, got)
		}
	}
	if dec.calls != 1 || cd.calls != 1 || nt.calls != 1 {
		t.Fatalf("calls = decision %d, candles %d, telegram %d, want 1 each",
			dec.calls, cd.calls, nt.calls)
	}

	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read saved raw: %v", err)
	}
	if string(raw) != string(dec.resp.Raw) {
		t.Fatalf("saved raw = %q, want %q", raw, dec.resp.Raw)
	}
	entries, err := os.ReadDir(filepath.Dir(rawPath))
	if err != nil {
		t.Fatalf("read raw dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "raw.json" {
		t.Fatalf("raw dir entries = %v, want only raw.json (no temp leftovers)", entries)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(rawPath)
		if err != nil {
			t.Fatalf("stat saved raw: %v", err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Fatalf("saved raw perm = %o, want 600", perm)
		}
	}
}

func TestDoctorLiveFailures(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	enableTelegram(t, cfgPath)
	dec := &fakeDecision{err: domain.NewError(domain.CodeAuth, "token secret-123 rejected")}
	cd := &fakeCandle{err: domain.NewError(domain.CodeTimeout, "candles timed out")}
	nt := &fakeNotifier{err: errors.New("transport is down")}
	env.NewRegistry = liveRegistry(t, dec, cd, nt)

	rawPath := filepath.Join(t.TempDir(), "raw.json")
	args := []string{"doctor", "--live", "--config", cfgPath, "--data-dir", dataDir,
		"--save-raw", rawPath}
	if code := runArgs(env, args...); code != 1 {
		t.Fatalf("doctor --live failures = %d, want 1, stdout=%s", code, stdout.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"decision: auth", "save_raw: unavailable", "candles: timeout", "telegram: unavailable",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout misses %q: %q", want, got)
		}
	}
	for _, leak := range []string{"secret-123", "timed out", "transport is down"} {
		if strings.Contains(got, leak) || strings.Contains(stderr.String(), leak) {
			t.Fatalf("output leaks provider message %q: stdout=%q stderr=%q",
				leak, got, stderr.String())
		}
	}
	if _, err := os.Stat(rawPath); !os.IsNotExist(err) {
		t.Fatalf("raw file exists after a failed decision: %v", err)
	}
}

func TestDoctorWithoutLiveMakesNoProviderCall(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	enableTelegram(t, cfgPath)
	dec, cd, nt := okDecision(), okCandles(), &fakeNotifier{}
	env.NewRegistry = liveRegistry(t, dec, cd, nt)

	if code := runArgs(env, "doctor", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("doctor = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if dec.calls != 0 || cd.calls != 0 || nt.calls != 0 {
		t.Fatalf("offline doctor called providers: decision %d, candles %d, telegram %d",
			dec.calls, cd.calls, nt.calls)
	}
	if strings.Contains(stdout.String(), "decision:") {
		t.Fatalf("offline doctor printed live checks: %q", stdout.String())
	}
}

func TestDoctorLiveRequiresLiveFlag(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)

	for _, args := range [][]string{
		{"doctor", "--save-raw", filepath.Join(t.TempDir(), "raw.json"),
			"--config", cfgPath, "--data-dir", dataDir},
		{"doctor", "--symbol", "ETHUSDT", "--config", cfgPath, "--data-dir", dataDir},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := runArgs(env, args...); code != 2 {
			t.Fatalf("%v = %d, want 2, stderr=%s", args, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "require --live") {
			t.Fatalf("%v stderr misses hint: %q", args, stderr.String())
		}
	}
}
