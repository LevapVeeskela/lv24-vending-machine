package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
)

func TestInitQuickProfile(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	// A real token in the environment must never reach the printed output or
	// the config file: init prints placeholders only.
	secret := "super-secret-token-value"
	env.Getenv = func(name string) string {
		switch name {
		case localAPITokenEnv, config.SecretEnvName(config.Default().Providers.Decision.SecretRef):
			return secret
		default:
			return ""
		}
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	dataDir := filepath.Join(dir, "data")

	if code := runArgs(env, "init", "--quick", "--config", cfgPath, "--data-dir", dataDir); code != exitOK {
		t.Fatalf("init --quick = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"profile: " + config.QuickProfileName,
		"export VENDING_SECRET_CONN_LOCAL_APITOKEN='<token>'",
		"export VENDING_SECRET_CONN_DREX_APIKEY='<token>'",
		`$env:VENDING_SECRET_CONN_LOCAL_APITOKEN = "<token>"`,
		`$env:VENDING_SECRET_CONN_DREX_APIKEY = "<token>"`,
		"vend config validate --config " + cfgPath,
		"vend doctor --strict --config " + cfgPath + " --data-dir " + dataDir,
		"vend serve --config " + cfgPath + " --data-dir " + dataDir,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("init output leaks the secret value: stdout=%q stderr=%q", got, stderr.String())
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config.Validate() = %v", err)
	}
	tr := cfg.Signals.Trend
	if !tr.Enabled {
		t.Fatal("signals.trend.enabled = false, want true")
	}
	if len(tr.Symbols) != config.QuickMinSymbols {
		t.Fatalf("signals.trend.symbols = %v, want %d symbols", tr.Symbols, config.QuickMinSymbols)
	}
	if tr.Timeframe != config.QuickTimeframe {
		t.Fatalf("signals.trend.timeframe = %q, want %q", tr.Timeframe, config.QuickTimeframe)
	}
	def := config.Default()
	if tr.Fast != def.Signals.Trend.Fast || tr.Slow != def.Signals.Trend.Slow {
		t.Fatalf("fast/slow = %d/%d, want defaults %d/%d",
			tr.Fast, tr.Slow, def.Signals.Trend.Fast, def.Signals.Trend.Slow)
	}
	if len(tr.Sides) != 2 || tr.Sides[0] != "buy" || tr.Sides[1] != "sell" {
		t.Fatalf("signals.trend.sides = %v, want [buy sell]", tr.Sides)
	}
	if cfg.Outcome.LabelIntervalSec != 300 {
		t.Fatalf("outcome.labelIntervalSec = %d, want 300", cfg.Outcome.LabelIntervalSec)
	}
	if cfg.Mode != def.Mode {
		t.Fatalf("mode = %q, want default %q", cfg.Mode, def.Mode)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "<token>") {
		t.Fatalf("config file contains a secret value: %s", raw)
	}
}

func TestInitQuickJSON(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	dataDir := filepath.Join(dir, "data")

	if code := runArgs(env, "init", "--quick", "--json",
		"--config", cfgPath, "--data-dir", dataDir); code != exitOK {
		t.Fatalf("init --quick --json = %d, stderr=%s", code, stderr.String())
	}
	var res struct {
		Profile   string   `json:"profile"`
		NextSteps []string `json:"nextSteps"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode init JSON %q: %v", stdout.String(), err)
	}
	if res.Profile != config.QuickProfileName {
		t.Fatalf("profile = %q, want %q", res.Profile, config.QuickProfileName)
	}
	if len(res.NextSteps) != 3 {
		t.Fatalf("nextSteps = %v, want three commands", res.NextSteps)
	}
	for _, want := range []string{"config validate", "doctor --strict", "serve"} {
		found := false
		for _, step := range res.NextSteps {
			if strings.Contains(step, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("nextSteps %v misses %q", res.NextSteps, want)
		}
	}
	if strings.Contains(stdout.String(), "<token>") ||
		strings.Contains(stdout.String(), "VENDING_SECRET_") {
		t.Fatalf("init JSON carries secret material: %s", stdout.String())
	}
}

func TestInitQuickForce(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	dataDir := filepath.Join(dir, "data")
	first := []string{"init", "--quick", "--config", cfgPath, "--data-dir", dataDir}
	if code := runArgs(env, first...); code != exitOK {
		t.Fatalf("first init --quick = %d, stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, first...); code != exitConflict {
		t.Fatalf("second init --quick = %d, want %d, stderr=%s", code, exitConflict, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "init", "--quick", "--force",
		"--config", cfgPath, "--data-dir", dataDir); code != exitOK {
		t.Fatalf("init --quick --force = %d, stdout=%s stderr=%s",
			code, stdout.String(), stderr.String())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config.Validate() = %v", err)
	}
}

// TestInitDefaultKeepsTrendOff pins the Phase A default: only --quick turns
// the trend source on, so an existing workflow is unchanged.
func TestInitDefaultKeepsTrendOff(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	dataDir := filepath.Join(dir, "data")
	if code := runArgs(env, "init", "--config", cfgPath, "--data-dir", dataDir); code != exitOK {
		t.Fatalf("init = %d, stderr=%s", code, stderr.String())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Signals.Trend.Enabled || len(cfg.Signals.Trend.Symbols) != 0 {
		t.Fatalf("default init wrote a trend profile: %+v", cfg.Signals.Trend)
	}
	if strings.Contains(stdout.String(), "profile:") {
		t.Fatalf("default init printed a quick profile: %q", stdout.String())
	}
}
