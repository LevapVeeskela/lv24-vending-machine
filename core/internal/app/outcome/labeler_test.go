package outcome

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// memJournal is an in-memory ports.Journal for offline tests.
type memJournal struct {
	mu        sync.Mutex
	events    []domain.Event
	walkErr   error
	appendErr error
}

func (m *memJournal) Append(_ context.Context, ev domain.Event) (domain.Event, error) {
	if m.appendErr != nil {
		return domain.Event{}, m.appendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ev.Seq = int64(len(m.events)) + 1
	if ev.EventID == "" {
		ev.EventID = "evt-test"
	}
	m.events = append(m.events, ev)
	return ev, nil
}

func (m *memJournal) Query(_ context.Context, from int64, kind string) ([]domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Event
	for _, ev := range m.events {
		if ev.Seq > from && (kind == "" || ev.Kind == kind) {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (m *memJournal) Walk(ctx context.Context, from int64, kind string, fn func(domain.Event) error) error {
	if m.walkErr != nil {
		return m.walkErr
	}
	evs, err := m.Query(ctx, from, kind)
	if err != nil {
		return err
	}
	for _, ev := range evs {
		if err := fn(ev); err != nil {
			return err
		}
	}
	return nil
}

func (m *memJournal) Head(_ context.Context) (domain.Event, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return domain.Event{}, false, nil
	}
	return m.events[len(m.events)-1], true, nil
}

func (m *memJournal) Verify(_ context.Context) (domain.VerifyResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return domain.VerifyResult{OK: true, Count: int64(len(m.events))}, nil
}

func (m *memJournal) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

// stubSource is a scripted CandleSource for offline tests.
type stubSource struct {
	candles []domain.Candle
	err     error
	lastReq domain.CandleRequest
}

func (s *stubSource) Candles(_ context.Context, req domain.CandleRequest) ([]domain.Candle, error) {
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.candles, nil
}

var (
	fixedNow = time.Date(2026, time.November, 16, 8, 0, 0, 0, time.UTC)
	sigTS    = time.Date(2026, time.November, 10, 6, 0, 0, 0, time.UTC)
)

func testDeps() ports.Deps {
	return ports.Deps{
		Clock: func() time.Time { return fixedNow },
		NewID: func() string { return "evt-test" },
	}
}

func testConfig() Config {
	return Config{HorizonBars: 2, FeeBps: 0, CodeVersion: "v-test"}
}

func candle(symbol, interval string, openTime, closeTime time.Time, open, high, low, close, volume string) domain.Candle {
	return domain.Candle{
		Symbol:    symbol,
		Interval:  interval,
		OpenTime:  openTime,
		CloseTime: closeTime,
		Open:      domain.MustParseDecimal(open),
		High:      domain.MustParseDecimal(high),
		Low:       domain.MustParseDecimal(low),
		Close:     domain.MustParseDecimal(close),
		Volume:    domain.MustParseDecimal(volume),
	}
}

// twoBars returns the shared deterministic fixture: entry 100, exit 112,
// maxHigh 115, minLow 90.
func twoBars() []domain.Candle {
	return []domain.Candle{
		candle("BTCUSDT", "1h",
			time.Date(2026, time.November, 10, 6, 0, 0, 0, time.UTC),
			time.Date(2026, time.November, 10, 7, 0, 0, 0, time.UTC),
			"100", "110", "90", "105", "1000"),
		candle("BTCUSDT", "1h",
			time.Date(2026, time.November, 10, 7, 0, 0, 0, time.UTC),
			time.Date(2026, time.November, 10, 8, 0, 0, 0, time.UTC),
			"105", "115", "95", "112", "2000"),
	}
}

func entryReq(signalID string, side domain.Side) domain.GateRequest {
	return domain.GateRequest{
		SignalID:   signalID,
		Instrument: domain.Instrument{Venue: "binance", Symbol: "BTCUSDT"},
		Side:       side,
		IntentKind: domain.IntentEntry,
		Timeframe:  "1h",
		TS:         sigTS,
		Price:      domain.MustParseDecimal("100"),
		Source:     "test",
	}
}

func seedRequest(t *testing.T, jr *memJournal, req domain.GateRequest, decisionID string) {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if _, err := jr.Append(context.Background(), domain.Event{
		Kind:          domain.EventGateRequest,
		TS:            req.TS,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       raw,
		CodeVersion:   "v-test",
		DecisionID:    decisionID,
	}); err != nil {
		t.Fatalf("seed request: %v", err)
	}
}

func labeledOutcome(t *testing.T, jr *memJournal) domain.Outcome {
	t.Helper()
	evs, err := jr.Query(context.Background(), 0, domain.EventOutcomeLabeled)
	if err != nil {
		t.Fatalf("query labeled: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("labeled events = %d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.DecisionID != "dec-1" {
		t.Fatalf("envelope decisionId = %q, want dec-1", ev.DecisionID)
	}
	if ev.SchemaVersion != domain.EventSchemaVersion || ev.CodeVersion != "v-test" {
		t.Fatalf("envelope = %+v, want schema %d code v-test", ev, domain.EventSchemaVersion)
	}
	if !ev.TS.Equal(fixedNow) {
		t.Fatalf("envelope ts = %v, want %v", ev.TS, fixedNow)
	}
	var oc domain.Outcome
	if err := json.Unmarshal(ev.Payload, &oc); err != nil {
		t.Fatalf("decode outcome: %v", err)
	}
	return oc
}

func TestNewValidation(t *testing.T) {
	jr := &memJournal{}
	src := &stubSource{}
	if _, err := New(nil, src, testDeps(), testConfig()); err == nil {
		t.Fatalf("New(nil journal) = nil, want error")
	}
	if _, err := New(jr, nil, testDeps(), testConfig()); err == nil {
		t.Fatalf("New(nil source) = nil, want error")
	}
	for _, h := range []int{0, -1, 1001} {
		cfg := testConfig()
		cfg.HorizonBars = h
		if _, err := New(jr, src, testDeps(), cfg); err == nil {
			t.Fatalf("New(horizonBars=%d) = nil, want error", h)
		}
	}
	for _, f := range []int{-1, 10001} {
		cfg := testConfig()
		cfg.FeeBps = f
		if _, err := New(jr, src, testDeps(), cfg); err == nil {
			t.Fatalf("New(feeBps=%d) = nil, want error", f)
		}
	}
	// Empty CodeVersion is allowed.
	cfg := testConfig()
	cfg.CodeVersion = ""
	if _, err := New(jr, src, testDeps(), cfg); err != nil {
		t.Fatalf("New(empty code version) = %v, want nil", err)
	}
}

func TestBuyExactNumbers(t *testing.T) {
	jr := &memJournal{}
	src := &stubSource{candles: twoBars()}
	seedRequest(t, jr, entryReq("sig-buy", domain.SideBuy), "dec-1")
	lb, err := New(jr, src, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	sum, err := lb.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if sum != (Summary{Labeled: 1}) {
		t.Fatalf("summary = %+v, want {Labeled:1}", sum)
	}
	oc := labeledOutcome(t, jr)
	cases := map[string][2]string{
		"entryPrice": {"100", oc.EntryPrice.String()},
		"exitPrice":  {"112", oc.ExitPrice.String()},
		"gross":      {"0.120000000000", oc.GrossReturn.String()},
		"net":        {"0.120000000000", oc.NetReturn.String()},
		"mfe":        {"0.150000000000", oc.MFE.String()},
		"mae":        {"-0.100000000000", oc.MAE.String()},
	}
	for name, c := range cases {
		if c[0] != c[1] {
			t.Fatalf("%s = %q, want %q", name, c[1], c[0])
		}
	}
	if oc.SignalID != "sig-buy" || oc.DecisionID != "dec-1" {
		t.Fatalf("ids = %+v, want sig-buy/dec-1", oc)
	}
	if oc.Side != domain.SideBuy || oc.Timeframe != "1h" {
		t.Fatalf("side/timeframe = %q/%q", oc.Side, oc.Timeframe)
	}
	if oc.HorizonBars != 2 || oc.FeeBps != 0 {
		t.Fatalf("horizon/fee = %d/%d", oc.HorizonBars, oc.FeeBps)
	}
	if !oc.EntryTS.Equal(sigTS) {
		t.Fatalf("entryTs = %v, want %v", oc.EntryTS, sigTS)
	}
	if want := time.Date(2026, time.November, 10, 8, 0, 0, 0, time.UTC); !oc.ExitTS.Equal(want) {
		t.Fatalf("exitTs = %v, want %v", oc.ExitTS, want)
	}
	if !oc.LabeledAt.Equal(fixedNow) {
		t.Fatalf("labeledAt = %v, want %v", oc.LabeledAt, fixedNow)
	}
	if oc.Instrument != (domain.Instrument{Venue: "binance", Symbol: "BTCUSDT"}) {
		t.Fatalf("instrument = %+v", oc.Instrument)
	}
	// The fetch window must cover the horizon plus slack.
	if src.lastReq.Symbol != "BTCUSDT" || src.lastReq.Interval != "1h" {
		t.Fatalf("candle request = %+v", src.lastReq)
	}
	if !src.lastReq.From.Equal(sigTS) {
		t.Fatalf("candle request from = %v, want %v", src.lastReq.From, sigTS)
	}
	if src.lastReq.Limit != 4 {
		t.Fatalf("candle request limit = %d, want 4", src.lastReq.Limit)
	}
}

func TestSellExactNumbersWithFee(t *testing.T) {
	jr := &memJournal{}
	src := &stubSource{candles: twoBars()}
	seedRequest(t, jr, entryReq("sig-sell", domain.SideSell), "dec-1")
	cfg := testConfig()
	cfg.FeeBps = 20
	lb, err := New(jr, src, testDeps(), cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	sum, err := lb.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if sum != (Summary{Labeled: 1}) {
		t.Fatalf("summary = %+v, want {Labeled:1}", sum)
	}
	oc := labeledOutcome(t, jr)
	cases := map[string][2]string{
		"gross": {"-0.120000000000", oc.GrossReturn.String()},
		"net":   {"-0.124000000000", oc.NetReturn.String()},
		"mfe":   {"0.100000000000", oc.MFE.String()},
		"mae":   {"-0.150000000000", oc.MAE.String()},
	}
	for name, c := range cases {
		if c[0] != c[1] {
			t.Fatalf("%s = %q, want %q", name, c[1], c[0])
		}
	}
	if oc.FeeBps != 20 {
		t.Fatalf("feeBps = %d, want 20", oc.FeeBps)
	}
}

func TestNoLookahead(t *testing.T) {
	jr := &memJournal{}
	// The spanning candle opens before the signal and closes after it:
	// it must be ignored even though it contains the signal ts.
	bars := []domain.Candle{
		candle("BTCUSDT", "1h",
			sigTS.Add(-30*time.Minute), sigTS.Add(30*time.Minute),
			"99", "101", "98", "100", "500"),
		candle("BTCUSDT", "1h",
			time.Date(2026, time.November, 10, 7, 0, 0, 0, time.UTC),
			time.Date(2026, time.November, 10, 8, 0, 0, 0, time.UTC),
			"105", "115", "95", "112", "2000"),
	}
	src := &stubSource{candles: bars}
	seedRequest(t, jr, entryReq("sig-nola", domain.SideBuy), "dec-1")
	cfg := testConfig()
	cfg.HorizonBars = 1
	lb, err := New(jr, src, testDeps(), cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	sum, err := lb.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if sum != (Summary{Labeled: 1}) {
		t.Fatalf("summary = %+v, want {Labeled:1}", sum)
	}
	oc := labeledOutcome(t, jr)
	if want := time.Date(2026, time.November, 10, 7, 0, 0, 0, time.UTC); !oc.EntryTS.Equal(want) {
		t.Fatalf("entryTs = %v, want %v (spanning candle ignored)", oc.EntryTS, want)
	}
	if oc.EntryPrice.String() != "105" {
		t.Fatalf("entryPrice = %q, want 105", oc.EntryPrice.String())
	}
}

func TestUnclosedCandleIgnored(t *testing.T) {
	jr := &memJournal{}
	bars := []domain.Candle{
		candle("BTCUSDT", "1h", sigTS, sigTS.Add(time.Hour), "100", "110", "90", "105", "1000"),
		// Still open as of the fake now: CloseTime >= now.
		candle("BTCUSDT", "1h", sigTS.Add(time.Hour), fixedNow, "105", "115", "95", "112", "2000"),
	}
	src := &stubSource{candles: bars}
	seedRequest(t, jr, entryReq("sig-open", domain.SideBuy), "dec-1")
	lb, err := New(jr, src, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	sum, err := lb.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if sum != (Summary{Skipped: 1}) {
		t.Fatalf("summary = %+v, want {Skipped:1}", sum)
	}
	if n := jr.count(); n != 1 {
		t.Fatalf("events = %d, want 1 (no label appended)", n)
	}
}

func TestInsufficientBarsSkipped(t *testing.T) {
	jr := &memJournal{}
	src := &stubSource{candles: twoBars()[:1]}
	seedRequest(t, jr, entryReq("sig-short", domain.SideBuy), "dec-1")
	lb, err := New(jr, src, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	sum, err := lb.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if sum != (Summary{Skipped: 1}) {
		t.Fatalf("summary = %+v, want {Skipped:1}", sum)
	}
	if n := jr.count(); n != 1 {
		t.Fatalf("events = %d, want 1 (no label appended)", n)
	}
}

func TestRerunAlready(t *testing.T) {
	jr := &memJournal{}
	src := &stubSource{candles: twoBars()}
	seedRequest(t, jr, entryReq("sig-re", domain.SideBuy), "dec-1")
	lb, err := New(jr, src, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if sum, err := lb.Run(context.Background()); err != nil {
		t.Fatalf("first Run() = %v", err)
	} else if sum != (Summary{Labeled: 1}) {
		t.Fatalf("first summary = %+v, want {Labeled:1}", sum)
	}
	before := jr.count()
	sum, err := lb.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() = %v", err)
	}
	if sum != (Summary{Already: 1}) {
		t.Fatalf("second summary = %+v, want {Already:1}", sum)
	}
	if n := jr.count(); n != before {
		t.Fatalf("events = %d, want %d (journal unchanged)", n, before)
	}
}

func TestSymbolIntervalFiltering(t *testing.T) {
	jr := &memJournal{}
	bars := append([]domain.Candle{
		candle("ETHUSDT", "1h", sigTS, sigTS.Add(time.Hour), "1", "2", "1", "2", "10"),
		candle("BTCUSDT", "15m", sigTS, sigTS.Add(15*time.Minute), "1", "2", "1", "2", "10"),
	}, twoBars()...)
	src := &stubSource{candles: bars}
	seedRequest(t, jr, entryReq("sig-filter", domain.SideBuy), "dec-1")
	lb, err := New(jr, src, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	sum, err := lb.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if sum != (Summary{Labeled: 1}) {
		t.Fatalf("summary = %+v, want {Labeled:1}", sum)
	}
	oc := labeledOutcome(t, jr)
	if oc.EntryPrice.String() != "100" {
		t.Fatalf("entryPrice = %q, want 100 (foreign candles filtered)", oc.EntryPrice.String())
	}
}

func TestDedupKeepsFirst(t *testing.T) {
	jr := &memJournal{}
	dup := twoBars()[0]
	dup.Close = domain.MustParseDecimal("999")
	bars := []domain.Candle{twoBars()[0], dup}
	src := &stubSource{candles: bars}
	seedRequest(t, jr, entryReq("sig-dedup", domain.SideBuy), "dec-1")
	cfg := testConfig()
	cfg.HorizonBars = 1
	lb, err := New(jr, src, testDeps(), cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if sum, err := lb.Run(context.Background()); err != nil {
		t.Fatalf("Run() = %v", err)
	} else if sum != (Summary{Labeled: 1}) {
		t.Fatalf("summary = %+v, want {Labeled:1}", sum)
	}
	oc := labeledOutcome(t, jr)
	if oc.ExitPrice.String() != "105" {
		t.Fatalf("exitPrice = %q, want 105 (first of the duplicates wins)", oc.ExitPrice.String())
	}
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestCandlesHash(t *testing.T) {
	jr := &memJournal{}
	src := &stubSource{candles: twoBars()}
	seedRequest(t, jr, entryReq("sig-hash", domain.SideBuy), "dec-1")
	lb, err := New(jr, src, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := lb.Run(context.Background()); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	first := labeledOutcome(t, jr)
	if !hex64.MatchString(first.CandlesHash) {
		t.Fatalf("candlesHash = %q, want 64 lowercase hex", first.CandlesHash)
	}

	jr2 := &memJournal{}
	changed := twoBars()
	changed[1].Close = domain.MustParseDecimal("113")
	src2 := &stubSource{candles: changed}
	seedRequest(t, jr2, entryReq("sig-hash", domain.SideBuy), "dec-1")
	lb2, err := New(jr2, src2, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := lb2.Run(context.Background()); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	second := labeledOutcome(t, jr2)
	if second.CandlesHash == first.CandlesHash {
		t.Fatalf("candlesHash did not change after a used candle changed")
	}
	if !hex64.MatchString(second.CandlesHash) {
		t.Fatalf("candlesHash = %q, want 64 lowercase hex", second.CandlesHash)
	}
}

func TestZeroEntryPriceErrors(t *testing.T) {
	jr := &memJournal{}
	bars := twoBars()
	bars[0].Open = domain.MustParseDecimal("0")
	src := &stubSource{candles: bars}
	seedRequest(t, jr, entryReq("sig-zero", domain.SideBuy), "dec-1")
	lb, err := New(jr, src, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := lb.Run(context.Background()); err == nil {
		t.Fatalf("Run() = nil, want zero entry price error")
	}
	if n := jr.count(); n != 1 {
		t.Fatalf("events = %d, want 1 (no label appended)", n)
	}
}

func TestExitIntentsIgnored(t *testing.T) {
	jr := &memJournal{}
	req := entryReq("sig-exit", domain.SideSell)
	req.IntentKind = domain.IntentExit
	seedRequest(t, jr, req, "dec-1")
	lb, err := New(jr, &stubSource{candles: twoBars()}, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	sum, err := lb.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if sum != (Summary{}) {
		t.Fatalf("summary = %+v, want zero", sum)
	}
	if n := jr.count(); n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
}

func TestErrorsPropagate(t *testing.T) {
	boom := errors.New("boom")
	jr := &memJournal{walkErr: boom}
	lb, err := New(jr, &stubSource{}, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := lb.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Run() walk err = %v, want boom", err)
	}

	jr2 := &memJournal{}
	src := &stubSource{err: boom}
	seedRequest(t, jr2, entryReq("sig-err", domain.SideBuy), "dec-1")
	lb2, err := New(jr2, src, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := lb2.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Run() source err = %v, want boom", err)
	}

	jr3 := &memJournal{}
	seedRequest(t, jr3, entryReq("sig-err", domain.SideBuy), "dec-1")
	lb3, err := New(jr3, &stubSource{candles: twoBars()}, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	jr3.appendErr = boom
	if _, err := lb3.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Run() append err = %v, want boom", err)
	}
}

func TestMissingBarSkipped(t *testing.T) {
	jr := &memJournal{}
	bars := []domain.Candle{
		candle("BTCUSDT", "1h", sigTS, sigTS.Add(time.Hour), "100", "110", "90", "105", "1000"),
		// The 07:00 bar is missing: the window no longer spans N intervals.
		candle("BTCUSDT", "1h", sigTS.Add(2*time.Hour), sigTS.Add(3*time.Hour),
			"108", "112", "100", "110", "1500"),
	}
	src := &stubSource{candles: bars}
	seedRequest(t, jr, entryReq("sig-gap", domain.SideBuy), "dec-1")
	lb, err := New(jr, src, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	sum, err := lb.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if sum != (Summary{Skipped: 1}) {
		t.Fatalf("summary = %+v, want {Skipped:1}", sum)
	}
	if n := jr.count(); n != 1 {
		t.Fatalf("events = %d, want 1 (no outcome event on a gappy window)", n)
	}
}

func TestShiftedBarSkipped(t *testing.T) {
	jr := &memJournal{}
	bars := []domain.Candle{
		candle("BTCUSDT", "1h", sigTS, sigTS.Add(time.Hour), "100", "110", "90", "105", "1000"),
		// Opens half an hour late: not on the 1h grid.
		candle("BTCUSDT", "1h", sigTS.Add(90*time.Minute), sigTS.Add(150*time.Minute),
			"105", "115", "95", "112", "2000"),
	}
	src := &stubSource{candles: bars}
	seedRequest(t, jr, entryReq("sig-shift", domain.SideBuy), "dec-1")
	lb, err := New(jr, src, testDeps(), testConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	sum, err := lb.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if sum != (Summary{Skipped: 1}) {
		t.Fatalf("summary = %+v, want {Skipped:1}", sum)
	}
	if n := jr.count(); n != 1 {
		t.Fatalf("events = %d, want 1 (no outcome event on a shifted window)", n)
	}
}

func TestCandlesHashRepresentationIndependent(t *testing.T) {
	rep := func(o, h, l, c, v string, openHour int) domain.Candle {
		return candle("BTCUSDT", "1h",
			time.Date(2026, time.November, 10, openHour, 0, 0, 0, time.UTC),
			time.Date(2026, time.November, 10, openHour+1, 0, 0, 0, time.UTC),
			o, h, l, c, v)
	}
	plain := []domain.Candle{rep("100", "110", "90", "105", "1000", 6), rep("105", "115", "95", "112", "2000", 7)}
	scaled := []domain.Candle{
		rep("100.0", "110.00", "90.000", "105.0000", "1000.00000", 6),
		rep("105.0", "115.00", "95.000", "112.0000", "2000.00000", 7),
	}
	// Guard the test itself: the two fixtures must be numerically equal.
	for i := range plain {
		for _, pair := range [][2]domain.Decimal{
			{plain[i].Open, scaled[i].Open},
			{plain[i].High, scaled[i].High},
			{plain[i].Low, scaled[i].Low},
			{plain[i].Close, scaled[i].Close},
			{plain[i].Volume, scaled[i].Volume},
		} {
			if pair[0].Cmp(pair[1]) != 0 {
				t.Fatalf("fixtures differ numerically: %q vs %q", pair[0], pair[1])
			}
		}
	}

	label := func(candles []domain.Candle) domain.Outcome {
		t.Helper()
		jr := &memJournal{}
		seedRequest(t, jr, entryReq("sig-rep", domain.SideBuy), "dec-1")
		lb, err := New(jr, &stubSource{candles: candles}, testDeps(), testConfig())
		if err != nil {
			t.Fatalf("New() = %v", err)
		}
		if sum, err := lb.Run(context.Background()); err != nil {
			t.Fatalf("Run() = %v", err)
		} else if sum != (Summary{Labeled: 1}) {
			t.Fatalf("summary = %+v, want {Labeled:1}", sum)
		}
		return labeledOutcome(t, jr)
	}
	first, second := label(plain), label(scaled)
	if !hex64.MatchString(first.CandlesHash) || !hex64.MatchString(second.CandlesHash) {
		t.Fatalf("hashes = %q, %q, want 64 lowercase hex", first.CandlesHash, second.CandlesHash)
	}
	if first.CandlesHash != second.CandlesHash {
		t.Fatalf("hash depends on representation: %q vs %q", first.CandlesHash, second.CandlesHash)
	}
}

func TestUnsupportedTimeframeErrors(t *testing.T) {
	for _, tf := range []string{"", "1M", "xyz", "h", "0h", "1H", "1.5h"} {
		jr := &memJournal{}
		req := entryReq("sig-badtf", domain.SideBuy)
		req.Timeframe = tf
		seedRequest(t, jr, req, "dec-1")
		lb, err := New(jr, &stubSource{candles: twoBars()}, testDeps(), testConfig())
		if err != nil {
			t.Fatalf("New() = %v", err)
		}
		_, err = lb.Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "unsupported timeframe") {
			t.Fatalf("Run() with timeframe %q = %v, want a clear unsupported-timeframe error", tf, err)
		}
		if n := jr.count(); n != 1 {
			t.Fatalf("events = %d, want 1 (no outcome event on bad timeframe)", n)
		}
	}
}
