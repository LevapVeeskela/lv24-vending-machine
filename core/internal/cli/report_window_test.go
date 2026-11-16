package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// appendGateRequest journals one gate request accepted at accepted, carrying
// signalTS in the payload. The two times differ in the tests below so the
// report window cannot accidentally use the payload.
func appendGateRequest(t *testing.T, env Env, dataDir, signalID string, signalTS, accepted time.Time) {
	t.Helper()
	appendReportEvent(t, env, dataDir, domain.Event{
		Kind:       domain.EventGateRequest,
		DecisionID: "dec-" + signalID,
		TS:         accepted,
		Payload: mustJSON(t, domain.GateRequest{
			SignalID:   signalID,
			Instrument: domain.Instrument{Venue: "test-venue", Symbol: "BTC/USDT"},
			Side:       domain.SideBuy,
			IntentKind: domain.IntentEntry,
			Timeframe:  "1h",
			TS:         signalTS,
			Price:      domain.MustParseDecimal("100"),
			Source:     "test",
		}),
	})
}

// runReportWindow runs gate report with and without --share and returns both
// bodies.
func runReportWindow(t *testing.T, env Env, stdout, stderr *bytes.Buffer, cfgPath, dataDir string) (human, share string) {
	t.Helper()
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "gate", "report",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report = %d, stderr=%s", code, stderr.String())
	}
	human = stdout.String()
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "gate", "report", "--share",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report --share = %d, stderr=%s", code, stderr.String())
	}
	return human, stdout.String()
}

func TestGateReportWindowUsesAcceptanceTime(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	accepted := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	signalTS := accepted.AddDate(0, 0, -30)
	appendGateRequest(t, env, dataDir, "sig-1", signalTS, accepted)
	appendGateRequest(t, env, dataDir, "sig-2", signalTS, accepted.Add(time.Hour))

	human, share := runReportWindow(t, env, stdout, stderr, cfgPath, dataDir)
	for _, want := range []string{"- days: 1", "- active days: 1"} {
		if !strings.Contains(share, want) {
			t.Fatalf("share misses %q: %q", want, share)
		}
	}
	if !strings.Contains(human, "window: days=1 active=1") {
		t.Fatalf("human report misses the window: %q", human)
	}
	if strings.Contains(share, "- days: 31") {
		t.Fatalf("share window followed the payload ts: %q", share)
	}
}

func TestGateReportWindowCountsActiveDays(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	day0 := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	appendGateRequest(t, env, dataDir, "sig-1", day0, day0)
	appendGateRequest(t, env, dataDir, "sig-2", day0, day0.Add(13*time.Hour))
	appendGateRequest(t, env, dataDir, "sig-3", day0, day0.AddDate(0, 0, 2))

	human, share := runReportWindow(t, env, stdout, stderr, cfgPath, dataDir)
	for _, want := range []string{"- days: 3", "- active days: 2"} {
		if !strings.Contains(share, want) {
			t.Fatalf("share misses %q: %q", want, share)
		}
	}
	if !strings.Contains(human, "window: days=3 active=2") {
		t.Fatalf("human report misses the window: %q", human)
	}
}

func TestGateReportWindowEmptyJournal(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	human, share := runReportWindow(t, env, stdout, stderr, cfgPath, dataDir)
	for _, want := range []string{"- days: 0", "- active days: 0"} {
		if !strings.Contains(share, want) {
			t.Fatalf("share misses %q: %q", want, share)
		}
	}
	if !strings.Contains(human, "window: days=0 active=0") {
		t.Fatalf("human report misses the window: %q", human)
	}
}

// TestGateReportJSONWindow requires the JSON window fields to mirror the human
// window line.
func TestGateReportJSONWindow(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	day0 := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	appendGateRequest(t, env, dataDir, "sig-1", day0, day0)
	appendGateRequest(t, env, dataDir, "sig-2", day0, day0.Add(13*time.Hour))
	appendGateRequest(t, env, dataDir, "sig-3", day0, day0.AddDate(0, 0, 2))

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "gate", "report", "--json",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("gate report --json = %d, stderr=%s", code, stderr.String())
	}
	var rep struct {
		Days          int            `json:"days"`
		ActiveDays    int            `json:"activeDays"`
		VerdictCounts map[string]int `json:"verdictCounts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("decode report JSON: %v\n%s", err, stdout.String())
	}
	if rep.Days != 3 || rep.ActiveDays != 2 {
		t.Fatalf("json window = days %d active %d, want 3/2", rep.Days, rep.ActiveDays)
	}
	if rep.VerdictCounts == nil {
		t.Fatalf("json report lost verdictCounts: %s", stdout.String())
	}
}
