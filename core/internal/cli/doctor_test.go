package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
		Answers: map[string]domain.Answer{
			"data_quality": {
				Type: domain.QuestionChoice, Choice: "ok",
				Probabilities: map[string]float64{"ok": 0.95, "insufficient": 0.05},
			},
			"signal_alignment": {
				Type: domain.QuestionChoice, Choice: "aligned",
				Probabilities: map[string]float64{"aligned": 0.8, "neutral": 0.15, "conflict": 0.05},
			},
			"market_regime": {
				Type: domain.QuestionChoice, Choice: "favorable",
				Probabilities: map[string]float64{"favorable": 0.7, "neutral": 0.2, "adverse": 0.1},
			},
			"risk_check": {
				Type: domain.QuestionChoice, Choice: "pass",
				Probabilities: map[string]float64{"pass": 0.9, "block": 0.1},
			},
		},
		Model: "drex-max-v1.1",
		Usage: domain.Usage{CostUSD: domain.MustParseDecimal("0.0012")},
		Raw:   raw,
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
		"decision: ok verdict=ALLOW code=allow cost=0.0012 USD", "save_raw: ok", "candles: ok", "telegram: ok",
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

// TestDoctorLiveInvalidOutputFails: a provider answer that the gate classifies
// as invalid output is a FAIL probe, not a successful live call.
func TestDoctorLiveInvalidOutputFails(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	dec := &fakeDecision{resp: domain.DecisionResponse{
		Answers: map[string]domain.Answer{},
		Usage:   domain.Usage{CostUSD: domain.MustParseDecimal("0.0012")},
	}}
	env.NewRegistry = liveRegistry(t, dec, okCandles(), nil)
	args := []string{"doctor", "--live", "--config", cfgPath, "--data-dir", dataDir}
	if code := runArgs(env, args...); code != 1 {
		t.Fatalf("doctor --live invalid output = %d, want 1, stdout=%s stderr=%s",
			code, stdout.String(), stderr.String())
	}
	want := "decision: FAIL verdict=ABSTAIN code=invalid_output cost=0.0012 USD"
	if got := stdout.String(); !strings.Contains(got, want) {
		t.Fatalf("stdout misses %q: %q", want, got)
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
		"decision: auth (check the provider secret and quota)",
		"save_raw: unavailable",
		"candles: timeout (check the network/provider and retry)",
		"telegram: unavailable (check the provider URL and network, then retry)",
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

func TestDoctorUnknownDecisionProvider(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	env.NewRegistry = liveRegistry(t, okDecision(), okCandles(), nil)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Providers.Decision.ID = "decision-unknown"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	// The provider id is registry knowledge: config validate still accepts it.
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config.Validate() = %v, want nil", err)
	}

	if code := runArgs(env, "doctor", "--config", cfgPath, "--data-dir", dataDir); code != exitGeneral {
		t.Fatalf("doctor unknown provider = %d, want %d, stdout=%s stderr=%s",
			code, exitGeneral, stdout.String(), stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		`unknown decision provider "decision-unknown"`,
		"registered: decision-drex",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout misses %q: %q", want, got)
		}
	}
}

func TestDoctorMissingLocalAPIToken(t *testing.T) {
	env, stdout, _, cfgPath, dataDir := initTestEnv(t)
	env.Getenv = func(string) string { return "" }
	env.NewRegistry = liveRegistry(t, okDecision(), okCandles(), nil)

	if code := runArgs(env, "doctor", "--config", cfgPath, "--data-dir", dataDir); code != exitGeneral {
		t.Fatalf("doctor without local API token = %d, want %d, stdout=%s",
			code, exitGeneral, stdout.String())
	}
	got := stdout.String()
	if !strings.Contains(got, localAPITokenEnv) {
		t.Fatalf("stdout misses %s: %q", localAPITokenEnv, got)
	}
	if !strings.Contains(got, "not set") {
		t.Fatalf("stdout misses the not-set wording: %q", got)
	}
}

// TestDoctorLiveMissingTokenStillRunsProbes checks that the token failure does
// not stop the opt-in probes: the remaining checks still run and report their
// own status.
func TestDoctorLiveMissingTokenStillRunsProbes(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	env.Getenv = func(string) string { return "" }
	dec, cd := okDecision(), okCandles()
	env.NewRegistry = liveRegistry(t, dec, cd, nil)

	if code := runArgs(env, "doctor", "--live", "--config", cfgPath, "--data-dir", dataDir); code != exitGeneral {
		t.Fatalf("doctor --live without token = %d, want %d, stdout=%s stderr=%s",
			code, exitGeneral, stdout.String(), stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, localAPITokenEnv) {
		t.Fatalf("stdout misses %s: %q", localAPITokenEnv, got)
	}
	for _, want := range []string{"decision: ok", "candles: ok"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout misses %q: %q", want, got)
		}
	}
	if dec.calls != 1 || cd.calls != 1 {
		t.Fatalf("probes = decision %d, candles %d, want 1 each", dec.calls, cd.calls)
	}
}

func TestDefaultDecisionProviderRegistered(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	env.NewRegistry = liveRegistry(t, okDecision(), okCandles(), nil)
	if code := runArgs(env, "doctor", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("doctor = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "ok providers:") {
		t.Fatalf("stdout misses the providers check: %q", stdout.String())
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

// strictSetup writes the quick profile and returns an env with both required
// secrets set plus a registry that passes the provider check.
func strictSetup(t *testing.T) (Env, *bytes.Buffer, *bytes.Buffer, string, string) {
	t.Helper()
	env, stdout, stderr := testEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	dataDir := filepath.Join(dir, "data")
	env.Getenv = func(name string) string {
		switch name {
		case localAPITokenEnv, config.SecretEnvName(config.Default().Providers.Decision.SecretRef):
			return "dummy-token-value"
		default:
			return ""
		}
	}
	env.NewRegistry = liveRegistry(t, okDecision(), okCandles(), nil)
	if code := runArgs(env, "init", "--quick", "--config", cfgPath, "--data-dir", dataDir); code != exitOK {
		t.Fatalf("init --quick = %d, stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	return env, stdout, stderr, cfgPath, dataDir
}

func mutateConfig(t *testing.T, cfgPath string, mut func(*config.Config)) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	mut(&cfg)
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func TestDoctorStrictReady(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := strictSetup(t)

	if code := runArgs(env, "doctor", "--strict", "--config", cfgPath, "--data-dir", dataDir); code != exitOK {
		t.Fatalf("doctor --strict = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"ok trend: signals.trend.enabled=true",
		"ok symbols: signals.trend.symbols has 10 symbols",
		"ok timeframe: signals.trend.timeframe=15m",
		"ok label_interval: outcome.labelIntervalSec=300s",
		"ok decision_secret: VENDING_SECRET_CONN_DREX_APIKEY is set",
		"profile ready; volume unverified",
		"next: vend signals estimate --days 7",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "G1 ready") {
		t.Fatalf("doctor --strict promised G1: %q", got)
	}
}

func TestDoctorStrictJSON(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := strictSetup(t)

	if code := runArgs(env, "doctor", "--strict", "--json",
		"--config", cfgPath, "--data-dir", dataDir); code != exitOK {
		t.Fatalf("doctor --strict --json = %d, stderr=%s", code, stderr.String())
	}
	var res struct {
		OK             bool     `json:"ok"`
		Strict         bool     `json:"strict"`
		Profile        string   `json:"profile"`
		VolumeVerified bool     `json:"volumeVerified"`
		NextSteps      []string `json:"nextSteps"`
		Checks         []struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode doctor JSON %q: %v", stdout.String(), err)
	}
	if !res.OK || !res.Strict {
		t.Fatalf("doctor JSON ok/strict = %v/%v: %+v", res.OK, res.Strict, res.Checks)
	}
	if res.Profile != config.QuickProfileName {
		t.Fatalf("profile = %q, want %q", res.Profile, config.QuickProfileName)
	}
	if res.VolumeVerified {
		t.Fatal("volumeVerified = true; offline doctor cannot verify volume")
	}
	if len(res.NextSteps) != 1 || res.NextSteps[0] != "vend signals estimate --days 7" {
		t.Fatalf("nextSteps = %v, want [vend signals estimate --days 7]", res.NextSteps)
	}
	found := false
	for _, c := range res.Checks {
		if c.Name == "decision_secret" && c.OK {
			found = true
		}
	}
	if !found {
		t.Fatalf("checks miss an ok decision_secret: %+v", res.Checks)
	}
}

func TestDoctorStrictFailures(t *testing.T) {
	cases := []struct {
		name   string
		setenv func(*Env)
		mut    func(*config.Config)
		want   string
	}{
		{
			name:   "missing local api token",
			setenv: func(e *Env) { e.Getenv = func(string) string { return "" } },
			want:   localAPITokenEnv,
		},
		{
			name: "missing decision secret",
			setenv: func(e *Env) {
				e.Getenv = func(name string) string {
					if name == localAPITokenEnv {
						return "dummy-token-value"
					}
					return ""
				}
			},
			want: "VENDING_SECRET_CONN_DREX_APIKEY",
		},
		{
			name: "five symbols",
			mut: func(c *config.Config) {
				c.Signals.Trend.Symbols = c.Signals.Trend.Symbols[:5]
			},
			want: "signals.trend.symbols",
		},
		{
			name: "trend disabled",
			mut:  func(c *config.Config) { c.Signals.Trend.Enabled = false },
			want: "signals.trend.enabled",
		},
		{
			name: "short timeframe",
			mut:  func(c *config.Config) { c.Signals.Trend.Timeframe = "1m" },
			want: "signals.trend.timeframe",
		},
		{
			name: "label interval too long",
			mut:  func(c *config.Config) { c.Outcome.LabelIntervalSec = 600 },
			want: "outcome.labelIntervalSec",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stdout, _, cfgPath, dataDir := strictSetup(t)
			if tc.setenv != nil {
				tc.setenv(&env)
			}
			if tc.mut != nil {
				mutateConfig(t, cfgPath, tc.mut)
			}
			if code := runArgs(env, "doctor", "--strict", "--config", cfgPath, "--data-dir", dataDir); code == exitOK {
				t.Fatalf("doctor --strict = 0, want a failure; stdout=%s", stdout.String())
			}
			if got := stdout.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("stdout misses %q:\n%s", tc.want, got)
			}
			if strings.Contains(stdout.String(), "profile ready") {
				t.Fatalf("failed doctor printed the ready line: %q", stdout.String())
			}
		})
	}
}
