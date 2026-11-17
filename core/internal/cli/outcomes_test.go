package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
)

// writeOutcomeCandles writes a two-bar candles fixture: entry 100, exit 112.
func writeOutcomeCandles(t *testing.T, dir string) string {
	t.Helper()
	sigTS := time.Date(2026, time.November, 10, 6, 0, 0, 0, time.UTC)
	mk := func(open time.Time, o, h, l, c, v string) domain.Candle {
		return domain.Candle{
			Symbol:    "BTCUSDT",
			Interval:  "1h",
			OpenTime:  open,
			CloseTime: open.Add(time.Hour),
			Open:      domain.MustParseDecimal(o),
			High:      domain.MustParseDecimal(h),
			Low:       domain.MustParseDecimal(l),
			Close:     domain.MustParseDecimal(c),
			Volume:    domain.MustParseDecimal(v),
		}
	}
	cs := []domain.Candle{
		mk(sigTS, "100", "110", "90", "105", "1000"),
		mk(sigTS.Add(time.Hour), "105", "115", "95", "112", "2000"),
	}
	raw, err := json.Marshal(cs)
	if err != nil {
		t.Fatalf("encode candles: %v", err)
	}
	path := filepath.Join(dir, "candles.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write candles: %v", err)
	}
	return path
}

// seedOutcomeRequest appends one entry gate.request for the candles fixture.
func seedOutcomeRequest(t *testing.T, env Env, dataDir string) {
	t.Helper()
	sigTS := time.Date(2026, time.November, 10, 6, 0, 0, 0, time.UTC)
	req := domain.GateRequest{
		SignalID:   "sig-out-1",
		Instrument: domain.Instrument{Venue: "binance", Symbol: "BTCUSDT"},
		Side:       domain.SideBuy,
		IntentKind: domain.IntentEntry,
		Timeframe:  "1h",
		TS:         sigTS,
		Price:      domain.MustParseDecimal("100"),
		Source:     "test",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	jr, closer, err := env.OpenJournal(config.DBPath(dataDir))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer func() { _ = closer.Close() }()
	if _, err := jr.Append(context.Background(), domain.Event{
		Kind:          domain.EventGateRequest,
		TS:            sigTS,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       raw,
		CodeVersion:   "v-test",
		DecisionID:    "dec-out-1",
	}); err != nil {
		t.Fatalf("append request: %v", err)
	}
}

func journalCount(t *testing.T, env Env, dataDir string) int64 {
	t.Helper()
	jr, closer, err := env.OpenJournal(config.DBPath(dataDir))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer func() { _ = closer.Close() }()
	evs, err := jr.Query(context.Background(), 0, "")
	if err != nil {
		t.Fatalf("query journal: %v", err)
	}
	return int64(len(evs))
}

func TestOutcomesLabelFromFile(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Outcome.HorizonBars = 2
	cfg.Outcome.FeeBps = 0
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	seedOutcomeRequest(t, env, dataDir)
	candlesPath := writeOutcomeCandles(t, t.TempDir())

	decodeSummary := func() outcomeSummary {
		t.Helper()
		var sum outcomeSummary
		if err := json.Unmarshal(stdout.Bytes(), &sum); err != nil {
			t.Fatalf("decode summary JSON %q: %v", stdout.String(), err)
		}
		return sum
	}

	args := []string{"outcomes", "label", "--json",
		"--config", cfgPath, "--data-dir", dataDir,
		"--candles", candlesPath}
	if code := runArgs(env, args...); code != 0 {
		t.Fatalf("outcomes label = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if sum := decodeSummary(); sum != (outcomeSummary{Labeled: 1}) {
		t.Fatalf("summary = %+v, want {Labeled:1}", sum)
	}
	before := journalCount(t, env, dataDir)
	if before != 2 {
		t.Fatalf("journal events = %d, want 2 (request plus label)", before)
	}

	// Rerun is idempotent: the signal counts as already labeled.
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, args...); code != 0 {
		t.Fatalf("outcomes label rerun = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if sum := decodeSummary(); sum != (outcomeSummary{Already: 1}) {
		t.Fatalf("rerun summary = %+v, want {Already:1}", sum)
	}
	if n := journalCount(t, env, dataDir); n != before {
		t.Fatalf("journal events = %d, want %d (unchanged)", n, before)
	}
}

func TestOutcomesLabelHuman(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Outcome.HorizonBars = 2
	cfg.Outcome.FeeBps = 0
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	seedOutcomeRequest(t, env, dataDir)
	candlesPath := writeOutcomeCandles(t, t.TempDir())

	args := []string{"outcomes", "label",
		"--config", cfgPath, "--data-dir", dataDir,
		"--candles", candlesPath}
	if code := runArgs(env, args...); code != 0 {
		t.Fatalf("outcomes label = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"labeled: 1", "skipped: 0", "already: 0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("human output misses %q: %q", want, got)
		}
	}
}

func TestOutcomesLabelBadCandles(t *testing.T) {
	env, _, stderr, cfgPath, dataDir := initTestEnv(t)

	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`[{"symbol": "BTCUSDT", "bogus": 1}]`), 0o644); err != nil {
		t.Fatalf("write bad candles: %v", err)
	}
	args := []string{"outcomes", "label",
		"--config", cfgPath, "--data-dir", dataDir,
		"--candles", bad}
	if code := runArgs(env, args...); code != 2 {
		t.Fatalf("outcomes label bad candles = %d, want 2, stderr=%s", code, stderr.String())
	}

	stderr.Reset()
	trailing := filepath.Join(t.TempDir(), "trailing.json")
	cs := `[{"symbol":"BTCUSDT","interval":"1h","openTime":"2026-11-10T06:00:00Z","closeTime":"2026-11-10T07:00:00Z","open":"100","high":"110","low":"90","close":"105","volume":"1000"}] garbage`
	if err := os.WriteFile(trailing, []byte(cs), 0o644); err != nil {
		t.Fatalf("write trailing candles: %v", err)
	}
	args = []string{"outcomes", "label",
		"--config", cfgPath, "--data-dir", dataDir,
		"--candles", trailing}
	if code := runArgs(env, args...); code != 2 {
		t.Fatalf("outcomes label trailing data = %d, want 2, stderr=%s", code, stderr.String())
	}
}

func TestOutcomesLabelMissingProvider(t *testing.T) {
	env, _, stderr, cfgPath, dataDir := initTestEnv(t)

	// No --candles file and an empty registry: the marketdata id is unknown.
	args := []string{"outcomes", "label",
		"--config", cfgPath, "--data-dir", dataDir}
	if code := runArgs(env, args...); code != 6 {
		t.Fatalf("outcomes label without provider = %d, want 6, stderr=%s", code, stderr.String())
	}
}

func TestReadCandlesFileBOM(t *testing.T) {
	dir := t.TempDir()
	body := `[{"symbol":"BTCUSDT","interval":"1h",` +
		`"openTime":"2026-11-10T06:00:00Z","closeTime":"2026-11-10T07:00:00Z",` +
		`"open":"100","high":"110","low":"90","close":"105","volume":"1000"}]`

	bomPath := filepath.Join(dir, "bom.json")
	if err := os.WriteFile(bomPath, append([]byte("\xef\xbb\xbf"), body...), 0o644); err != nil {
		t.Fatalf("write BOM candles: %v", err)
	}
	cs, code, err := readCandlesFile(bomPath)
	if code != exitOK || err != nil {
		t.Fatalf("read BOM candles = code %d, err %v", code, err)
	}
	if len(cs) != 1 || cs[0].Symbol != "BTCUSDT" {
		t.Fatalf("BOM candles = %+v, want one BTCUSDT candle", cs)
	}

	onlyBOM := filepath.Join(dir, "bom-only.json")
	if err := os.WriteFile(onlyBOM, []byte("\xef\xbb\xbf"), 0o644); err != nil {
		t.Fatalf("write BOM-only file: %v", err)
	}
	if _, code, err := readCandlesFile(onlyBOM); code != exitUsage || err == nil ||
		!strings.Contains(err.Error(), "BOM") {
		t.Fatalf("BOM-only file = code %d, err %v, want a usage error naming the BOM", code, err)
	}

	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	if _, code, err := readCandlesFile(empty); code != exitUsage || err == nil ||
		!strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file = code %d, err %v, want a usage error naming the empty file", code, err)
	}
}
