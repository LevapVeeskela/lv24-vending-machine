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

// shareSeed describes the synthetic journal behind a --share test.
type shareSeed struct {
	total      int
	labeled    int
	days       int
	allowEvery int
	withPairs  bool
}

// seedShareReportShape writes a journal with the requested verdict mix and
// calibration coverage. 120 decisions over 7 days; 100 of them carry a
// labeled outcome, which unlocks the comparison metrics by default.
func seedShareReportShape(t *testing.T, env Env, dataDir string, s shareSeed) {
	t.Helper()
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	jr, closer, err := env.OpenJournal(config.DBPath(dataDir))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer func() { _ = closer.Close() }()
	for i := 0; i < s.total; i++ {
		sig := fmt.Sprintf("CANARYSIGNAL-%d", i)
		dec := "CANARYDECISION-" + sig
		ts := now.AddDate(0, 0, i%s.days)
		verdict := domain.VerdictAllow
		net := "0.01"
		if i%s.allowEvery != 0 {
			verdict = domain.VerdictReject
			net = "-0.01"
		}
		answers := map[string]domain.Answer{
			"data_quality": {
				Type:          domain.QuestionChoice,
				Choice:        "ok",
				Probabilities: map[string]float64{"ok": 1, "insufficient": 0},
			},
		}
		if s.withPairs {
			answers = map[string]domain.Answer{
				"risk_check": {
					Type:          domain.QuestionChoice,
					Choice:        "pass",
					Probabilities: map[string]float64{"pass": 0.6, "block": 0.4},
				},
			}
		}
		events := []domain.Event{
			{
				Kind:       domain.EventGateRequest,
				DecisionID: dec,
				TS:         ts,
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
					Answers: answers,
					Model:   "CANARYMODEL",
					Usage:   domain.Usage{CostUSD: domain.MustParseDecimal("0.01"), LatencyMS: 120},
				}),
			},
			{
				Kind:       domain.EventGateVerdict,
				DecisionID: dec,
				TS:         ts,
				Payload: mustJSON(t, domain.GateVerdict{
					Verdict:    verdict,
					Code:       "test",
					Reasons:    []string{"test"},
					Checks:     []domain.Check{},
					PolicyHash: strings.Repeat("ab", 32),
					DecidedAt:  ts,
				}),
			},
		}
		if i < s.labeled {
			events = append(events, domain.Event{
				Kind:       domain.EventOutcomeLabeled,
				DecisionID: dec,
				Payload: mustJSON(t, domain.Outcome{
					SignalID:     sig,
					DecisionID:   dec,
					Instrument:   domain.Instrument{Venue: "CANARYVENUE", Symbol: "CANARYSYMBOL"},
					Side:         domain.SideBuy,
					Timeframe:    "1h",
					EntryTS:      ts,
					EntryPrice:   domain.MustParseDecimal("12345.67"),
					ExitPrice:    domain.MustParseDecimal("12346.67"),
					NetReturn:    domain.MustParseDecimal(net),
					GrossReturn:  domain.MustParseDecimal(net),
					LabeledAt:    ts.Add(24 * time.Hour),
					HorizonBars:  24,
					FeeBps:       20,
					PriceSource:  `C:\CANARYPATH\candles.csv`,
					EntryLagMS:   900000,
					EntryShifted: i%10 == 0,
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

// seedShareReport writes the baseline 120-decision journal: 100 labeled
// outcomes over 7 days with calibration pairs for every labeled outcome.
func seedShareReport(t *testing.T, env Env, dataDir string) {
	t.Helper()
	seedShareReportShape(t, env, dataDir, shareSeed{
		total: 120, labeled: 100, days: 7, allowEvery: 2, withPairs: true,
	})
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
		"- active days: 7",
		"labeled outcomes: 100",
		"- awaiting labeling: 20",
		"- no outcome by deadline: 0",
		"- source rejected: 0",
		"- g1 labels: 100/100",
		"- g1 active days: 7/7",
		"- g1 max gap hours: 24",
		"- g1 eligible: yes",
		"- g1 labels due:",
		"- g1 last label age hours:",
		"- g1 expected label interval hours: 13",
		"ALLOW=60 REJECT=60 ABSTAIN=0",
		"calibration pairs: 100",
		"Brier score:",
		"base rate:",
		"AUC:",
		"Brier skill score:",
		"ALLOW vs REJECT mean difference:",
		"95% CI:",
		"zero",
		"horizon: 24 bars",
		"round-trip fee: 40 bps",
		"entry lag median: 0.2500 bars (900.000 s)",
		"entry lag p95: 0.2500 bars (900.000 s)",
		"entry shifted: 10",
		"https://github.com/LevapVeeskela/lv24-vending-machine/discussions",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("share summary misses %q: %q", want, got)
		}
	}
	if strings.Contains(got, "price source") {
		t.Fatalf("share summary must not print a price source: %q", got)
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
	// A pre-existing file must not be replaced without --force.
	outPath := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(outPath, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed out file: %v", err)
	}

	if code := runArgs(env, "gate", "report", "--share", "--out", outPath,
		"--config", cfgPath, "--data-dir", dataDir); code != 2 {
		t.Fatalf("gate report --share --out without --force = %d, want 2, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("stderr misses the existing-file hint: %q", stderr.String())
	}
	if raw, err := os.ReadFile(outPath); err != nil || string(raw) != "old\n" {
		t.Fatalf("existing file was modified without --force: %q, err=%v", raw, err)
	}
	stdout.Reset()
	stderr.Reset()

	if code := runArgs(env, "gate", "report", "--share", "--out", outPath, "--force",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report --share --out --force = %d, stderr=%s", code, stderr.String())
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
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("summary file does not exist: %v", err)
	}
	// The atomic write must leave no temp file behind.
	entries, err := os.ReadDir(filepath.Dir(outPath))
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "summary.md" {
			t.Fatalf("stray file after atomic write: %q", e.Name())
		}
	}
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

// TestGateReportShareOutInsideDataDir: --out must never write into the
// directory that holds the journal and the config, even with --force.
func TestGateReportShareOutInsideDataDir(t *testing.T) {
	env, _, stderr, cfgPath, dataDir := initShareEnv(t)
	seedShareReport(t, env, dataDir)
	outPath := filepath.Join(dataDir, "summary.md")

	if code := runArgs(env, "gate", "report", "--share", "--out", outPath, "--force",
		"--config", cfgPath, "--data-dir", dataDir); code != 2 {
		t.Fatalf("gate report --out inside data dir = %d, want 2, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "data directory") {
		t.Fatalf("stderr misses the data-dir hint: %q", stderr.String())
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("summary was written inside the data dir: err=%v", err)
	}
}

// TestGateReportShareInsufficientCalibration: 100 labeled outcomes without a
// probability pair must not publish Brier, AUC or a skill score.
func TestGateReportShareInsufficientCalibration(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initShareEnv(t)
	seedShareReportShape(t, env, dataDir, shareSeed{
		total: 120, labeled: 100, days: 7, allowEvery: 2, withPairs: false,
	})

	if code := runArgs(env, "gate", "report", "--share",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report --share = %d, stderr=%s", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"labeled outcomes: 100",
		"- calibration pairs: 0",
		"- metrics: insufficient (0 pairs, need 30)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("share summary misses %q: %q", want, got)
		}
	}
	for _, forbidden := range []string{"Brier score:", "- AUC:", "Brier skill score:", "base rate:"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("share summary publishes %q without calibration pairs: %q", forbidden, got)
		}
	}
	assertNoCanaries(t, "share stdout", got)
}

// TestGateReportShareGroupTooSmall: one ALLOW among 99 REJECT has enough
// calibration pairs but no publishable group interval.
func TestGateReportShareGroupTooSmall(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initShareEnv(t)
	seedShareReportShape(t, env, dataDir, shareSeed{
		total: 100, labeled: 100, days: 7, allowEvery: 100, withPairs: true,
	})

	if code := runArgs(env, "gate", "report", "--share",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report --share = %d, stderr=%s", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"ALLOW=1 REJECT=99",
		"- calibration pairs: 100",
		"- Brier score:",
		"insufficient group size",
		"ALLOW=1, REJECT=99",
		"need 30 per group",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("share summary misses %q: %q", want, got)
		}
	}
	if strings.Contains(got, "95% CI") {
		t.Fatalf("share summary prints a CI for one ALLOW: %q", got)
	}
	assertNoCanaries(t, "share stdout", got)
}

// TestGateReportShareNoComparisons still publishes the calibration-pair count
// when the journal has too few labeled outcomes.
func TestGateReportShareNoComparisons(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initShareEnv(t)
	seedSmallReport(t, env, dataDir)

	if code := runArgs(env, "gate", "report", "--share",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report --share = %d, stderr=%s", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"- calibration pairs: 0",
		"- insufficient data: 2 labeled outcomes, need 100",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("share summary misses %q: %q", want, got)
		}
	}
	if strings.Contains(got, "Brier score:") {
		t.Fatalf("share summary publishes metrics without comparisons: %q", got)
	}
}
