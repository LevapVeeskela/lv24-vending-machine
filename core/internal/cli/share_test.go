package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
)

// shareCanaries are the strings that must never leak into a --share body.
var shareCanaries = []string{
	"CANARY",
	"12345.67",
	"C:\\CANARYPATH\\candles.csv",
}

// initShareEnv initializes a data directory whose own path carries a canary,
// so a leaked path is caught by the same assertions.
func initShareEnv(t *testing.T) (Env, *bytes.Buffer, *bytes.Buffer, string, string) {
	t.Helper()
	env, stdout, stderr := testEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	dataDir := filepath.Join(dir, "CANARY_DATA_DIR", "data")
	if code := runArgs(env, "init", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("init = %d, stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	return env, stdout, stderr, cfgPath, dataDir
}

// seedShareReport writes 120 decisions over 7 days; 100 of them carry a
// labeled outcome, which unlocks the comparison metrics.
func seedShareReport(t *testing.T, env Env, dataDir string) {
	t.Helper()
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	jr, closer, err := env.OpenJournal(config.DBPath(dataDir))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer func() { _ = closer.Close() }()
	for i := 0; i < 120; i++ {
		sig := fmt.Sprintf("CANARYSIGNAL-%d", i)
		dec := "CANARYDECISION-" + sig
		ts := now.AddDate(0, 0, i%7)
		verdict := domain.VerdictAllow
		net := "0.01"
		if i%2 == 1 {
			verdict = domain.VerdictReject
			net = "-0.01"
		}
		events := []domain.Event{
			{
				Kind:       domain.EventGateRequest,
				DecisionID: dec,
				Payload: mustJSON(t, domain.GateRequest{
					SignalID:   sig,
					Instrument: domain.Instrument{Venue: "CANARYVENUE", Symbol: "CANARYSYMBOL"},
					Side:       domain.SideBuy,
					IntentKind: domain.IntentEntry,
					Timeframe:  "1h",
					TS:         ts,
					Price:      domain.MustParseDecimal("12345.67"),
					Source:     "CANARYSOURCE",
					State:      map[string]float64{"CANARYSTATE": 42},
				}),
			},
			{
				Kind:       domain.EventDecisionResponse,
				DecisionID: dec,
				Payload: mustJSON(t, domain.DecisionResponse{
					Answers: map[string]domain.Answer{
						"risk_check": {
							Type:          domain.QuestionChoice,
							Choice:        "pass",
							Probabilities: map[string]float64{"pass": 0.6, "block": 0.4},
						},
					},
					Model: "CANARYMODEL",
					Usage: domain.Usage{CostUSD: domain.MustParseDecimal("0.01"), LatencyMS: 120},
				}),
			},
			{
				Kind:       domain.EventGateVerdict,
				DecisionID: dec,
				Payload: mustJSON(t, domain.GateVerdict{
					Verdict:    verdict,
					Code:       "test",
					Reasons:    []string{"test"},
					Checks:     []domain.Check{},
					PolicyHash: strings.Repeat("ab", 32),
				}),
			},
		}
		if i < 100 {
			events = append(events, domain.Event{
				Kind:       domain.EventOutcomeLabeled,
				DecisionID: dec,
				Payload: mustJSON(t, domain.Outcome{
					SignalID:    sig,
					DecisionID:  dec,
					Instrument:  domain.Instrument{Venue: "CANARYVENUE", Symbol: "CANARYSYMBOL"},
					Side:        domain.SideBuy,
					Timeframe:   "1h",
					EntryTS:     ts,
					EntryPrice:  domain.MustParseDecimal("12345.67"),
					ExitPrice:   domain.MustParseDecimal("12346.67"),
					NetReturn:   domain.MustParseDecimal(net),
					GrossReturn: domain.MustParseDecimal(net),
					LabeledAt:   ts.Add(24 * time.Hour),
					HorizonBars: 24,
					FeeBps:      20,
					PriceSource: `C:\CANARYPATH\candles.csv`,
				}),
			})
		}
		for _, ev := range events {
			if _, err := jr.Append(context.Background(), ev); err != nil {
				t.Fatalf("append %s: %v", ev.Kind, err)
			}
		}
	}
}

// assertNoCanaries fails when any seeded secret-like string leaked.
func assertNoCanaries(t *testing.T, where, body string) {
	t.Helper()
	for _, c := range shareCanaries {
		if strings.Contains(body, c) {
			t.Fatalf("%s leaks %q: %q", where, c, body)
		}
	}
}

func TestGateReportShare(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initShareEnv(t)
	seedShareReport(t, env, dataDir)

	if code := runArgs(env, "gate", "report", "--share",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report --share = %d, stderr=%s", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"# LV24 Vending — shadow gate summary",
		"policy: abababab",
		"decisions: 120",
		"days: 7",
		"labeled outcomes: 100",
		"ALLOW=60 REJECT=60 ABSTAIN=0",
		"Brier score:",
		"base rate:",
		"AUC:",
		"Brier skill score:",
		"ALLOW vs REJECT mean difference:",
		"95% CI:",
		"zero",
		"horizon: 24 bars",
		"round-trip fee: 40 bps",
		"https://github.com/LevapVeeskela/lv24-vending-machine/discussions",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("share summary misses %q: %q", want, got)
		}
	}
	assertNoCanaries(t, "share stdout", got)
	if strings.Contains(got, "written to") {
		t.Fatalf("share without --out must not print a path: %q", got)
	}
}

func TestGateReportShareRejectsJSON(t *testing.T) {
	env, _, stderr := testEnv(t)
	if code := runArgs(env, "gate", "report", "--share", "--json"); code != 2 {
		t.Fatalf("gate report --share --json = %d, want 2, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot be combined") {
		t.Fatalf("stderr misses the conflict error: %q", stderr.String())
	}
}

func TestGateReportShareOut(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initShareEnv(t)
	seedShareReport(t, env, dataDir)
	// A pre-existing file must not keep its old permissions.
	outPath := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(outPath, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed out file: %v", err)
	}

	if code := runArgs(env, "gate", "report", "--share", "--out", outPath,
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report --share --out = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "written to "+outPath) {
		t.Fatalf("stdout misses the written notice: %q", stdout.String())
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	body := string(raw)
	for _, want := range []string{"# LV24 Vending", "decisions: 120", "days: 7", "discussions"} {
		if !strings.Contains(body, want) {
			t.Fatalf("summary file misses %q: %q", want, body)
		}
	}
	assertNoCanaries(t, "share file", body)
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(outPath); err == nil && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("summary file is group/world readable: %v", info.Mode().Perm())
		}
	}
}

func TestGateReportOutRequiresShare(t *testing.T) {
	env, _, stderr := testEnv(t)
	if code := runArgs(env, "gate", "report", "--out", filepath.Join(t.TempDir(), "x.md")); code != 2 {
		t.Fatalf("gate report --out without share = %d, want 2, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--out requires --share") {
		t.Fatalf("stderr misses the --out hint: %q", stderr.String())
	}
}
