package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/gateflow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/outcome"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/shadow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/store/sqlite"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
	"github.com/LevapVeeskela/lv24-vending-machine/providers/decision-drex"
	marketdata "github.com/LevapVeeskela/lv24-vending-machine/providers/marketdata-binance"
	notifytelegram "github.com/LevapVeeskela/lv24-vending-machine/providers/notify-telegram"
)

// rcAllowResponse and rcRejectResponse are vendor-shaped decision responses:
// camelCase answer objects and snake_case usage fields.
const (
	rcAllowResponse = `{
		"model": "drex-test-1",
		"answers": {
			"data_quality": {"type": "choice", "choice": "ok",
				"probabilities": {"ok": 0.9, "insufficient": 0.1}, "confidence": 0.8},
			"signal_alignment": {"type": "choice", "choice": "aligned",
				"probabilities": {"aligned": 0.8, "neutral": 0.1, "conflict": 0.1}, "confidence": 0.7},
			"market_regime": {"type": "choice", "choice": "favorable",
				"probabilities": {"favorable": 0.7, "neutral": 0.2, "adverse": 0.1}, "confidence": 0.55},
			"risk_check": {"type": "choice", "choice": "pass",
				"probabilities": {"pass": 0.9, "block": 0.1}, "confidence": 0.8}
		},
		"usage": {"input_tokens": 120, "output_tokens": 40},
		"request_id": "rc-req-allow",
		"evaluation_time_ms": 5
	}`

	rcRejectResponse = `{
		"model": "drex-test-1",
		"answers": {
			"data_quality": {"type": "choice", "choice": "ok",
				"probabilities": {"ok": 0.9, "insufficient": 0.1}, "confidence": 0.8},
			"signal_alignment": {"type": "choice", "choice": "neutral",
				"probabilities": {"aligned": 0.2, "neutral": 0.6, "conflict": 0.2}, "confidence": 0.4},
			"market_regime": {"type": "choice", "choice": "neutral",
				"probabilities": {"favorable": 0.2, "neutral": 0.6, "adverse": 0.2}, "confidence": 0.4},
			"risk_check": {"type": "choice", "choice": "block",
				"probabilities": {"pass": 0.05, "block": 0.95}, "confidence": 0.9}
		},
		"usage": {"input_tokens": 130, "output_tokens": 35},
		"request_id": "rc-req-reject",
		"evaluation_time_ms": 6
	}`
)

// rcClock is the fake clock shared by every collaborator.
type rcClock struct {
	mu  sync.Mutex
	now time.Time
}

func newRCClock(start time.Time) *rcClock {
	return &rcClock{now: start}
}

func (c *rcClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *rcClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// rcSecrets is an in-memory secret store; no real credential is used.
type rcSecrets struct {
	value string
}

func (s rcSecrets) Get(context.Context, string) (string, error) {
	return s.value, nil
}

// rcIDs is a deterministic decision-id generator.
func rcIDs() func() string {
	var mu sync.Mutex
	n := 0
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("rc-dec-%04d", n)
	}
}

// rcDecisionServer alternates ALLOW and REJECT vendor responses and counts
// decision calls.
type rcDecisionServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls int
}

func newRCDecisionServer(t *testing.T) *rcDecisionServer {
	t.Helper()
	s := &rcDecisionServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		s.calls++
		body := rcAllowResponse
		if s.calls%2 == 0 {
			body = rcRejectResponse
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *rcDecisionServer) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// rcCandleServer serves Binance-shaped klines and counts requests.
type rcCandleServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
}

func newRCCandleServer(t *testing.T, start time.Time, interval time.Duration) *rcCandleServer {
	t.Helper()
	row := func(open time.Time, o, h, l, c, v string) []any {
		return []any{
			open.UTC().UnixMilli(), o, h, l, c, v,
			open.UTC().Add(interval).UnixMilli(), 0, 0, "0", "0", "0", "0",
		}
	}
	rows := [][]any{
		row(start, "100", "110", "90", "105", "1000"),
		row(start.Add(interval), "105", "115", "95", "112", "2000"),
		row(start.Add(2*interval), "112", "120", "100", "118", "1500"),
		row(start.Add(3*interval), "118", "125", "110", "120", "1200"),
	}
	body, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("encode klines: %v", err)
	}
	s := &rcCandleServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/klines" {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		s.requests++
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *rcCandleServer) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// rcTelegramServer accepts sendMessage calls and counts them.
type rcTelegramServer struct {
	*httptest.Server
	mu    sync.Mutex
	sends int
}

func newRCTelegramServer(t *testing.T) *rcTelegramServer {
	t.Helper()
	s := &rcTelegramServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		s.sends++
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *rcTelegramServer) Sends() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends
}

// rcReporter is a fake shadow reporter with a fixed labeled count.
type rcReporter struct {
	labeled int
}

func (r rcReporter) Build(context.Context) (domain.Report, error) {
	return domain.Report{
		LabeledCount:  r.labeled,
		VerdictCounts: map[string]int{"ALLOW": 1, "REJECT": 1},
	}, nil
}

// rcHeadSeq returns the highest journal sequence number.
func rcHeadSeq(t *testing.T, jr ports.Journal) int64 {
	t.Helper()
	head, ok, err := jr.Head(context.Background())
	if err != nil {
		t.Fatalf("journal head: %v", err)
	}
	if !ok {
		return 0
	}
	return head.Seq
}

// rcLabeledBySignal counts outcome.labeled events per signalId.
func rcLabeledBySignal(t *testing.T, jr ports.Journal) map[string]int {
	t.Helper()
	out := make(map[string]int)
	if err := jr.Walk(context.Background(), 0, domain.EventOutcomeLabeled, func(ev domain.Event) error {
		var oc domain.Outcome
		if err := json.Unmarshal(ev.Payload, &oc); err != nil {
			return nil
		}
		out[oc.SignalID]++
		return nil
	}); err != nil {
		t.Fatalf("walk labeled outcomes: %v", err)
	}
	return out
}

// rcNotifySent decodes every notify.sent payload.
func rcNotifySent(t *testing.T, jr ports.Journal) []struct {
	Kind    string `json:"kind"`
	Channel string `json:"channel"`
	OK      bool   `json:"ok"`
} {
	t.Helper()
	var out []struct {
		Kind    string `json:"kind"`
		Channel string `json:"channel"`
		OK      bool   `json:"ok"`
	}
	if err := jr.Walk(context.Background(), 0, domain.EventNotifySent, func(ev domain.Event) error {
		var p struct {
			Kind    string `json:"kind"`
			Channel string `json:"channel"`
			OK      bool   `json:"ok"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return nil
		}
		out = append(out, p)
		return nil
	}); err != nil {
		t.Fatalf("walk notify.sent: %v", err)
	}
	return out
}

// TestRCScenario is the offline end-to-end release check: a real Drex decision
// adapter against an alternating local server, idempotent replay and body
// conflict, outcome labeling from local klines, and one weekly shadow
// notification across a Runner restart. No real network is used.
func TestRCScenario(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, time.November, 2, 6, 0, 0, 0, time.UTC)
	clock := newRCClock(start)

	decisionSrv := newRCDecisionServer(t)
	candleSrv := newRCCandleServer(t, start, time.Hour)
	telegramSrv := newRCTelegramServer(t)

	deps := ports.Deps{
		Clock:   clock.Now,
		Secrets: rcSecrets{value: "rc-test-secret"},
		NewID:   rcIDs(),
	}

	decision, err := decisiondrex.New(decisiondrex.Config{
		Profile:   decisiondrex.ProfileDrex,
		BaseURL:   decisionSrv.URL,
		SecretRef: "conn/rc/decision",
	}, deps)
	if err != nil {
		t.Fatalf("build decision provider: %v", err)
	}

	candles, err := marketdata.New(marketdata.Config{BaseURL: candleSrv.URL}, deps)
	if err != nil {
		t.Fatalf("build candle provider: %v", err)
	}

	telegram, err := notifytelegram.New(notifytelegram.Config{
		ChatID:    "123456",
		SecretRef: "conn/rc/telegram",
		BaseURL:   telegramSrv.URL,
	}, deps)
	if err != nil {
		t.Fatalf("build telegram notifier: %v", err)
	}

	jr, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "rc.sqlite"))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(func() { _ = jr.Close() })

	flow, err := gateflow.New(jr, decision, deps, gateflow.Config{
		Model:       "drex-test-1",
		Policy:      domain.DefaultGatePolicy(),
		CodeVersion: "rc-test",
	})
	if err != nil {
		t.Fatalf("build gate flow: %v", err)
	}

	mkReq := func(signalID, price string) domain.GateRequest {
		return domain.GateRequest{
			SignalID:   signalID,
			Instrument: domain.Instrument{Venue: "binance", Symbol: "BTC/USDT"},
			Side:       domain.SideBuy,
			IntentKind: domain.IntentEntry,
			Timeframe:  "1h",
			TS:         start,
			Price:      domain.MustParseDecimal(price),
			Source:     "rc-fixture",
			State:      map[string]float64{"rsi": 55.5, "volatility": 0.02},
		}
	}

	allowReq := mkReq("rc-sig-allow", "100")
	allowRes, err := flow.Evaluate(ctx, allowReq)
	if err != nil {
		t.Fatalf("evaluate allow: %v", err)
	}
	if allowRes.Verdict.Verdict != domain.VerdictAllow {
		t.Fatalf("allow verdict = %s (%s), want ALLOW", allowRes.Verdict.Verdict, allowRes.Verdict.Code)
	}
	if allowRes.Verdict.DecisionID == "" {
		t.Fatal("allow verdict has no decision id")
	}
	if got := decisionSrv.Calls(); got != 1 {
		t.Fatalf("decision calls = %d, want 1", got)
	}

	rejectRes, err := flow.Evaluate(ctx, mkReq("rc-sig-reject", "101"))
	if err != nil {
		t.Fatalf("evaluate reject: %v", err)
	}
	if rejectRes.Verdict.Verdict != domain.VerdictReject {
		t.Fatalf("reject verdict = %s (%s), want REJECT", rejectRes.Verdict.Verdict, rejectRes.Verdict.Code)
	}
	if got := decisionSrv.Calls(); got != 2 {
		t.Fatalf("decision calls = %d, want 2", got)
	}

	// Idempotent replay: same signal body, same decision id, no provider call.
	replay, err := flow.Evaluate(ctx, allowReq)
	if err != nil {
		t.Fatalf("replay allow: %v", err)
	}
	if !replay.Reused {
		t.Fatal("replay reused = false, want true")
	}
	if replay.Verdict.DecisionID != allowRes.Verdict.DecisionID {
		t.Fatalf("replay decision id = %q, want %q", replay.Verdict.DecisionID, allowRes.Verdict.DecisionID)
	}
	if got := decisionSrv.Calls(); got != 2 {
		t.Fatalf("decision calls after replay = %d, want 2", got)
	}

	// Same signal id with a changed body is a conflict and writes nothing.
	before := rcHeadSeq(t, jr)
	if _, err := flow.Evaluate(ctx, mkReq("rc-sig-allow", "123")); err == nil {
		t.Fatal("conflicting body was accepted")
	} else if code := domain.CodeOf(err); code != domain.CodeConflict {
		t.Fatalf("conflict code = %q, want %q", code, domain.CodeConflict)
	}
	if after := rcHeadSeq(t, jr); after != before {
		t.Fatalf("journal seq = %d after conflict, want %d", after, before)
	}

	// Advance past horizonBars (1) + (N+1)=2 intervals, so the forward window
	// of every signal is closed and labelable.
	clock.Advance(3*time.Hour + time.Second)

	labeler, err := outcome.New(jr, candles, deps, outcome.Config{
		HorizonBars: 1,
		FeeBps:      0,
		CodeVersion: "rc-test",
		PriceSource: "fixture",
	})
	if err != nil {
		t.Fatalf("build labeler: %v", err)
	}
	sum, err := labeler.Run(ctx)
	if err != nil {
		t.Fatalf("label outcomes: %v", err)
	}
	if sum.Labeled != 2 {
		t.Fatalf("labeled = %d, want 2 (%+v)", sum.Labeled, sum)
	}
	if got := candleSrv.Requests(); got > 2 {
		t.Fatalf("candle requests = %d, want <= 2 (no trend scan)", got)
	}

	labeled := rcLabeledBySignal(t, jr)
	if len(labeled) != 2 || labeled["rc-sig-allow"] != 1 || labeled["rc-sig-reject"] != 1 {
		t.Fatalf("labeled outcomes = %v, want one per signal", labeled)
	}

	// Move well past seven days from the first gate.request so the weekly
	// summary is due, then run the shadow loop twice on one journal.
	clock.Advance(8 * 24 * time.Hour)

	statusPath := filepath.Join(t.TempDir(), "shadow-status.json")
	cfg := shadow.Config{
		LabelInterval: time.Minute,
		StatusPath:    statusPath,
		CodeVersion:   "rc-test",
	}
	reporter := rcReporter{labeled: sum.Labeled}

	first, err := shadow.New(jr, nil, nil, nil, reporter, telegram, deps, cfg)
	if err != nil {
		t.Fatalf("build first shadow runner: %v", err)
	}
	if err := first.Step(ctx); err != nil {
		t.Fatalf("first shadow step: %v", err)
	}

	// A fresh Runner on the same journal/status file must not repeat the send:
	// the schedule is derived from the journal, not from process memory.
	second, err := shadow.New(jr, nil, nil, nil, reporter, telegram, deps, cfg)
	if err != nil {
		t.Fatalf("build second shadow runner: %v", err)
	}
	if err := second.Step(ctx); err != nil {
		t.Fatalf("second shadow step: %v", err)
	}

	sent := rcNotifySent(t, jr)
	if len(sent) != 1 {
		t.Fatalf("notify.sent events = %d (%+v), want 1", len(sent), sent)
	}
	if sent[0].Kind != "gate.weekly" {
		t.Fatalf("notify kind = %q, want gate.weekly", sent[0].Kind)
	}
	if !sent[0].OK {
		t.Fatal("weekly notify ok = false, want true")
	}
	if got := telegramSrv.Sends(); got != 1 {
		t.Fatalf("telegram sends = %d, want 1", got)
	}

	if ver, err := jr.Verify(ctx); err != nil {
		t.Fatalf("journal verify: %v", err)
	} else if !ver.OK {
		t.Fatalf("journal verify not ok: %+v", ver)
	}

	labeled = rcLabeledBySignal(t, jr)
	if len(labeled) != 2 || labeled["rc-sig-allow"] != 1 || labeled["rc-sig-reject"] != 1 {
		t.Fatalf("labeled outcomes after shadow = %v, want no duplicates", labeled)
	}
}
