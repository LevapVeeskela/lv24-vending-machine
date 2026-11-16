// Package outcome labels gate entry signals with forward candle returns.
//
// The Labeler replays gate.request events from the journal, fetches the
// closed candles that follow each signal, and appends one outcome.labeled
// event per signal. Labeling never looks ahead: only candles with
// CloseTime before now are used, the first used bar always opens at or
// after the signal timestamp and closes after the anchor (the gate verdict
// time when the journal has one, otherwise the gate.request receipt time).
// An entry bar that opened well before the anchor is shifted to the next
// bar and flagged in the outcome, because its open price was already gone
// when the signal became actionable.
package outcome

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// returnScale is the Decimal scale of all computed returns.
const returnScale = 12

// Config tunes one Labeler.
type Config struct {
	// HorizonBars is the number of consecutive timeframe intervals per label.
	HorizonBars int
	// FeeBps is the one-way fee in basis points. NetReturn subtracts twice
	// this rate (entry plus exit: the round trip).
	FeeBps int
	// CodeVersion is stamped on every appended event envelope.
	CodeVersion string
	// PriceSource names the candles used for labeling: the market data
	// provider descriptor id, or "file" for a replayed candles file. It is
	// copied into every outcome so a provider change is visible in reports.
	PriceSource string
}

// CandleSource serves historical OHLCV candles.
type CandleSource interface {
	// Candles returns the candles selected by req.
	Candles(ctx context.Context, req domain.CandleRequest) ([]domain.Candle, error)
}

// Summary counts one Run pass.
type Summary struct {
	// Labeled is the number of newly appended outcome.labeled events.
	Labeled int
	// Skipped is the number of entry signals without enough closed candles.
	Skipped int
	// Already is the number of entry signals labeled on an earlier run.
	Already int
	// Unlabelable is the number of entry signals whose closed candles are
	// missing or off-grid: no honest forward window exists, so nothing is
	// appended and the signal stays retryable.
	Unlabelable int
	// Failed is the number of entry signals whose labeling errored (candle
	// source, bad payload, unsupported timeframe, math). One bad symbol never
	// stops the pass; the signal stays retryable.
	Failed int
}

// labelResult classifies one labeled (or not labeled) entry signal.
type labelResult int

const (
	// resultLabeled means an outcome.labeled event was appended.
	resultLabeled labelResult = iota
	// resultSkipped means the forward window is not complete yet.
	resultSkipped
	// resultUnlabelable means the forward window has gaps or shifts.
	resultUnlabelable
	// resultFailed means labeling this one signal errored.
	resultFailed
	// resultTimedOut means the signal passed its deadline and one
	// outcome.unlabelable event was appended for it.
	resultTimedOut
)

// unlabelableTimeout is how long after the labeling window closes a signal
// may stay unlabeled before it is recorded as a timeout.
const unlabelableTimeout = 7 * 24 * time.Hour

// retryCap bounds the exponential backoff between failed attempts.
const retryCap = time.Hour

// retryState tracks the exponential backoff of one failing signal.
type retryState struct {
	// attempts is the number of consecutive failures.
	attempts int
	// next is the earliest time the signal may be retried.
	next time.Time
}

// Labeler labels gate entry signals with forward candle returns.
type Labeler struct {
	jr   ports.Journal
	src  CandleSource
	deps ports.Deps
	cfg  Config

	mu    sync.Mutex
	retry map[string]retryState
}

// New builds a Labeler. A non-empty CodeVersion is not required.
func New(jr ports.Journal, src CandleSource, deps ports.Deps, cfg Config) (*Labeler, error) {
	if jr == nil {
		return nil, fmt.Errorf("outcome: nil journal")
	}
	if src == nil {
		return nil, fmt.Errorf("outcome: nil candle source")
	}
	if cfg.HorizonBars < 1 || cfg.HorizonBars > 1000 {
		return nil, fmt.Errorf("outcome: horizonBars must be in [1, 1000]")
	}
	if cfg.FeeBps < 0 || cfg.FeeBps > 10000 {
		return nil, fmt.Errorf("outcome: feeBps must be in [0, 10000]")
	}
	return &Labeler{
		jr:    jr,
		src:   src,
		deps:  deps,
		cfg:   cfg,
		retry: make(map[string]retryState),
	}, nil
}

// Run labels every entry gate.request in the journal and returns the counts.
func (l *Labeler) Run(ctx context.Context) (Summary, error) {
	var out Summary
	labeled := make(map[string]bool)
	if err := l.jr.Walk(ctx, 0, domain.EventOutcomeLabeled, func(ev domain.Event) error {
		var v struct {
			SignalID string `json:"signalId"`
		}
		if err := json.Unmarshal(ev.Payload, &v); err != nil {
			return nil
		}
		if v.SignalID != "" {
			labeled[v.SignalID] = true
		}
		return nil
	}); err != nil {
		return Summary{}, err
	}
	// finished holds signals that must never be queried again: already
	// labeled, or recorded as unlabelable on an earlier run.
	finished := make(map[string]bool, len(labeled))
	for id := range labeled {
		finished[id] = true
	}
	if err := l.jr.Walk(ctx, 0, domain.EventOutcomeUnlabelable, func(ev domain.Event) error {
		var v domain.OutcomeUnlabelable
		if err := json.Unmarshal(ev.Payload, &v); err != nil {
			return nil
		}
		if v.SignalID != "" {
			finished[v.SignalID] = true
		}
		return nil
	}); err != nil {
		return Summary{}, err
	}
	// verdicts maps decisionId to the gate verdict time: the moment the
	// signal became actionable. A missing verdict falls back to the
	// gate.request receipt time.
	verdicts := make(map[string]time.Time)
	if err := l.jr.Walk(ctx, 0, domain.EventGateVerdict, func(ev domain.Event) error {
		var v domain.GateVerdict
		if err := json.Unmarshal(ev.Payload, &v); err != nil {
			// One corrupt verdict must not stop the pass.
			return nil
		}
		id := v.DecisionID
		if id == "" {
			id = ev.DecisionID
		}
		if id == "" || v.DecidedAt.IsZero() {
			return nil
		}
		// Later verdicts for the same decision supersede earlier ones.
		verdicts[id] = v.DecidedAt.UTC()
		return nil
	}); err != nil {
		return Summary{}, err
	}
	now := l.deps.Now().UTC()
	// Collect the requests first: appending inside Walk would deadlock
	// against the open listing cursor (single-connection journal).
	var reqs []domain.Event
	if err := l.jr.Walk(ctx, 0, domain.EventGateRequest, func(ev domain.Event) error {
		reqs = append(reqs, ev)
		return nil
	}); err != nil {
		return Summary{}, err
	}
	for _, ev := range reqs {
		var req domain.GateRequest
		if err := json.Unmarshal(ev.Payload, &req); err != nil {
			// One corrupt payload must not stop an unattended pass.
			out.Failed++
			if l.deps.Logger != nil {
				l.deps.Logger.Warn("outcome: decode gate request",
					"seq", ev.Seq, "err", err.Error())
			}
			continue
		}
		if req.IntentKind != domain.IntentEntry {
			continue
		}
		if labeled[req.SignalID] {
			out.Already++
			continue
		}
		if finished[req.SignalID] {
			// Recorded as unlabelable on an earlier run: never queried again.
			continue
		}
		if !l.due(req.SignalID, now) {
			// Backing off after a failure: skip without touching the source.
			out.Skipped++
			continue
		}
		res, err := l.labelOne(ctx, ev, req, verdicts, now)
		if err != nil {
			// Only journal failures are fatal: losing the journal is not a
			// per-signal problem.
			return Summary{}, err
		}
		switch res {
		case resultLabeled:
			labeled[req.SignalID] = true
			out.Labeled++
		case resultSkipped:
			out.Skipped++
		case resultUnlabelable:
			out.Unlabelable++
		case resultFailed:
			out.Failed++
		case resultTimedOut:
			// The timeout event is the record; nothing else to count.
			finished[req.SignalID] = true
		}
	}
	return out, nil
}

// due reports whether a signal may be retried now. A signal that never failed
// is always due.
func (l *Labeler) due(signalID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.retry[signalID]
	return !ok || !now.Before(st.next)
}

// recordFailure schedules the next retry of a failed signal: one label
// interval after the first failure, doubling per consecutive failure, capped
// at one hour. An unknown interval falls back to the cap.
func (l *Labeler) recordFailure(signalID string, interval time.Duration, now time.Time) {
	if interval <= 0 {
		interval = retryCap
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.retry[signalID]
	st.attempts++
	delay := interval
	for i := 1; i < st.attempts; i++ {
		if delay >= retryCap {
			break
		}
		delay *= 2
	}
	if delay > retryCap {
		delay = retryCap
	}
	st.next = now.Add(delay)
	l.retry[signalID] = st
}

// resetRetry forgets the backoff of a signal that labeled successfully.
func (l *Labeler) resetRetry(signalID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.retry, signalID)
}

// fail logs one per-signal labeling error, schedules its retry and asks Run to
// continue. The message carries ids only: no payload, price or secret ever
// reaches the log.
func (l *Labeler) fail(req domain.GateRequest, interval time.Duration, err error) (labelResult, error) {
	l.recordFailure(req.SignalID, interval, l.deps.Now().UTC())
	if l.deps.Logger != nil {
		l.deps.Logger.Warn("outcome: label failed",
			"signalId", req.SignalID,
			"symbol", req.Instrument.Symbol,
			"err", err.Error())
	}
	return resultFailed, nil
}

// labelOne fetches the candles following req and appends the outcome.labeled
// event. Per-signal failures come back as resultFailed with a nil error; only
// a journal append error is returned so Run can stop.
func (l *Labeler) labelOne(ctx context.Context, ev domain.Event, req domain.GateRequest, verdicts map[string]time.Time, now time.Time) (labelResult, error) {
	interval, err := parseInterval(req.Timeframe)
	if err != nil {
		return l.fail(req, 0, err)
	}
	// receivedAt is when the gate accepted the signal, not when the signal
	// claims to have fired: the difference decides whether the signal is a
	// late (backfill) sample.
	receivedAt := ev.TS.UTC()
	backfill := !req.TS.IsZero() && receivedAt.Sub(req.TS.UTC()) > interval
	// anchor is when the signal became actionable: the verdict time when the
	// journal has one, otherwise the request receipt time. Only candles that
	// closed after it may be entered.
	anchor := receivedAt
	if decidedAt, ok := verdicts[ev.DecisionID]; ok {
		anchor = decidedAt
	}
	// The forward window cannot be complete before anchor plus the horizon
	// and one slack bar, so the source is not asked earlier: an early fetch
	// would return the same incomplete window at a cost.
	windowEnd := anchor.Add(time.Duration(l.cfg.HorizonBars+1) * interval)
	// A signal that never produced an outcome by the deadline is recorded
	// once and then dropped from labeling for good.
	if !now.Before(windowEnd.Add(unlabelableTimeout)) {
		if err := l.appendUnlabelable(ctx, req, ev.DecisionID, now, "timeout"); err != nil {
			return resultFailed, err
		}
		return resultTimedOut, nil
	}
	if now.Before(windowEnd) {
		return resultSkipped, nil
	}
	candles, err := l.src.Candles(ctx, domain.CandleRequest{
		Symbol:   req.Instrument.Symbol,
		Interval: req.Timeframe,
		From:     req.TS.UTC(),
		Limit:    l.cfg.HorizonBars + 2,
	})
	if err != nil {
		return l.fail(req, interval, fmt.Errorf("outcome: candles for signal %q: %w", req.SignalID, err))
	}
	bars := selectBars(candles, req, anchor, now)
	if len(bars) == 0 {
		// The source answered but nothing was usable: log why and stay
		// retryable. An empty response is normal provider lag, not a fault.
		if len(candles) > 0 && l.deps.Logger != nil {
			l.deps.Logger.Warn("outcome: no usable candle",
				"signalId", req.SignalID,
				"symbol", req.Instrument.Symbol,
				"candles", len(candles),
				"reason", "no candle opens at or after the signal and closes after the gate decision")
		}
		return resultSkipped, nil
	}
	if len(bars) < l.cfg.HorizonBars {
		return resultSkipped, nil
	}
	bars, lagMS, shifted := shiftEntry(bars, anchor, interval, l.cfg.HorizonBars)
	bars = bars[:l.cfg.HorizonBars]
	if !contiguous(bars, interval) {
		// A missing or shifted bar breaks the horizon: labeling over a
		// gappy window would silently change the holding period.
		return resultUnlabelable, nil
	}
	oc, err := l.buildOutcome(req, bars, now)
	if err != nil {
		return l.fail(req, interval, err)
	}
	oc.DecisionID = ev.DecisionID
	oc.ReceivedAt = receivedAt
	oc.Backfill = backfill
	oc.PriceSource = l.cfg.PriceSource
	oc.EntryLagMS = lagMS
	oc.EntryShifted = shifted
	raw, err := json.Marshal(oc)
	if err != nil {
		return l.fail(req, interval, fmt.Errorf("outcome: encode outcome for signal %q: %w", req.SignalID, err))
	}
	if _, err := l.jr.Append(ctx, domain.Event{
		Kind:          domain.EventOutcomeLabeled,
		TS:            now,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       raw,
		CodeVersion:   l.cfg.CodeVersion,
		DecisionID:    ev.DecisionID,
	}); err != nil {
		return resultFailed, err
	}
	l.resetRetry(req.SignalID)
	return resultLabeled, nil
}

// appendUnlabelable records one signal that will never be labeled. The reason
// is a short machine-readable code, not free text.
func (l *Labeler) appendUnlabelable(ctx context.Context, req domain.GateRequest, decisionID string, now time.Time, reason string) error {
	raw, err := json.Marshal(domain.OutcomeUnlabelable{
		SignalID:   req.SignalID,
		DecisionID: decisionID,
		Reason:     reason,
	})
	if err != nil {
		return fmt.Errorf("outcome: encode unlabelable for signal %q: %w", req.SignalID, err)
	}
	if _, err := l.jr.Append(ctx, domain.Event{
		Kind:          domain.EventOutcomeUnlabelable,
		TS:            now,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       raw,
		CodeVersion:   l.cfg.CodeVersion,
		DecisionID:    decisionID,
	}); err != nil {
		return err
	}
	return nil
}

// selectBars keeps the usable candles for req: same canonical symbol and
// interval, opening at or after the signal, closing after the anchor, already
// closed as of now.
//
// A bar that closed at or before the anchor cannot be entered: its close
// price was already known when the gate decided, so using it would credit the
// signal with a move it could not have captured. The symbol comparison goes
// through domain.CanonicalSymbol so BTC/USDT and BTCUSDT are the same market.
//
// The result is sorted by open time ascending with duplicates removed (first
// wins).
func selectBars(all []domain.Candle, req domain.GateRequest, anchor, now time.Time) []domain.Candle {
	sigTS := req.TS.UTC()
	want := domain.CanonicalSymbol(req.Instrument.Symbol)
	var bars []domain.Candle
	for _, c := range all {
		if domain.CanonicalSymbol(c.Symbol) != want || c.Interval != req.Timeframe {
			continue
		}
		open, closed := c.OpenTime.UTC(), c.CloseTime.UTC()
		if open.Before(sigTS) {
			continue
		}
		if !closed.After(anchor) || !closed.Before(now) {
			continue
		}
		bars = append(bars, c)
	}
	sort.SliceStable(bars, func(i, j int) bool {
		return bars[i].OpenTime.UTC().Before(bars[j].OpenTime.UTC())
	})
	out := bars[:0]
	var last time.Time
	for i, c := range bars {
		t := c.OpenTime.UTC()
		if i > 0 && t.Equal(last) {
			continue
		}
		out = append(out, c)
		last = t
	}
	return out
}

// shiftEntry moves the entry to the next bar when the first usable bar opened
// more than 10% of one interval before the anchor: entering that bar would
// measure the return from an open price the signal could no longer get.
//
// The shift needs a spare bar so the full horizon still fits; without one the
// entry stays and only its lag is recorded. The returned lag always belongs to
// the final entry bar.
func shiftEntry(bars []domain.Candle, anchor time.Time, interval time.Duration, horizon int) ([]domain.Candle, int64, bool) {
	lag := entryLagMS(anchor, bars[0].OpenTime)
	if lag <= interval.Milliseconds()/10 || len(bars) <= horizon {
		return bars, lag, false
	}
	bars = bars[1:]
	return bars, entryLagMS(anchor, bars[0].OpenTime), true
}

// entryLagMS is how long after the entry bar open the signal became
// actionable, in milliseconds. A bar that opens at or after the anchor has no
// lag: it is clamped to zero.
func entryLagMS(anchor, open time.Time) int64 {
	d := anchor.UTC().Sub(open.UTC())
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// parseInterval parses a timeframe of integer magnitude plus unit m/h/d/w
// (minutes, hours, days, weeks) into a duration. Anything else is a clear
// error: the horizon is N intervals, so an unknown bar length cannot label.
func parseInterval(tf string) (time.Duration, error) {
	bad := func() (time.Duration, error) {
		return 0, fmt.Errorf("outcome: unsupported timeframe %q (want e.g. 5m, 1h, 1d, 1w)", tf)
	}
	i := 0
	for i < len(tf) && tf[i] >= '0' && tf[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(tf) {
		return bad()
	}
	n, err := strconv.Atoi(tf[:i])
	if err != nil || n <= 0 {
		return bad()
	}
	var unit time.Duration
	switch tf[i:] {
	case "m":
		unit = time.Minute
	case "h":
		unit = time.Hour
	case "d":
		unit = 24 * time.Hour
	case "w":
		unit = 7 * 24 * time.Hour
	default:
		return bad()
	}
	if d := time.Duration(n) * unit; d > 0 {
		return d, nil
	}
	return bad()
}

// contiguous reports whether bars open exactly one interval apart starting
// at bars[0].OpenTime.
func contiguous(bars []domain.Candle, interval time.Duration) bool {
	base := bars[0].OpenTime.UTC()
	for i := 1; i < len(bars); i++ {
		if want := base.Add(time.Duration(i) * interval); !bars[i].OpenTime.UTC().Equal(want) {
			return false
		}
	}
	return true
}

// buildOutcome computes the labeled outcome over exactly the used bars.
func (l *Labeler) buildOutcome(req domain.GateRequest, bars []domain.Candle, now time.Time) (domain.Outcome, error) {
	entry := bars[0].Open
	if entry.Sign() == 0 {
		return domain.Outcome{}, fmt.Errorf("outcome: zero entry price for signal %q", req.SignalID)
	}
	exit := bars[len(bars)-1].Close
	var gross domain.Decimal
	var err error
	switch req.Side {
	case domain.SideBuy:
		gross, err = exit.Sub(entry).QuoRound(entry, returnScale)
	case domain.SideSell:
		gross, err = entry.Sub(exit).QuoRound(entry, returnScale)
	default:
		return domain.Outcome{}, fmt.Errorf("outcome: unknown side %q for signal %q", req.Side, req.SignalID)
	}
	if err != nil {
		return domain.Outcome{}, fmt.Errorf("outcome: gross return for signal %q: %w", req.SignalID, err)
	}
	// Round-trip fee: FeeBps is one-way, so the label pays it twice
	// (entry plus exit).
	fee, err := domain.MustParseDecimal(strconv.Itoa(2*l.cfg.FeeBps)).QuoRound(domain.MustParseDecimal("10000"), returnScale)
	if err != nil {
		return domain.Outcome{}, fmt.Errorf("outcome: fee: %w", err)
	}
	maxHigh, minLow := bars[0].High, bars[0].Low
	for _, b := range bars[1:] {
		if b.High.Cmp(maxHigh) > 0 {
			maxHigh = b.High
		}
		if b.Low.Cmp(minLow) < 0 {
			minLow = b.Low
		}
	}
	var mfe, mae domain.Decimal
	if req.Side == domain.SideBuy {
		if mfe, err = maxHigh.Sub(entry).QuoRound(entry, returnScale); err != nil {
			return domain.Outcome{}, fmt.Errorf("outcome: mfe for signal %q: %w", req.SignalID, err)
		}
		if mae, err = minLow.Sub(entry).QuoRound(entry, returnScale); err != nil {
			return domain.Outcome{}, fmt.Errorf("outcome: mae for signal %q: %w", req.SignalID, err)
		}
	} else {
		if mfe, err = entry.Sub(minLow).QuoRound(entry, returnScale); err != nil {
			return domain.Outcome{}, fmt.Errorf("outcome: mfe for signal %q: %w", req.SignalID, err)
		}
		if mae, err = entry.Sub(maxHigh).QuoRound(entry, returnScale); err != nil {
			return domain.Outcome{}, fmt.Errorf("outcome: mae for signal %q: %w", req.SignalID, err)
		}
	}
	return domain.Outcome{
		SignalID:    req.SignalID,
		DecisionID:  "",
		Instrument:  req.Instrument,
		Side:        req.Side,
		Timeframe:   req.Timeframe,
		EntryTS:     bars[0].OpenTime.UTC(),
		EntryPrice:  entry,
		ExitTS:      bars[len(bars)-1].CloseTime.UTC(),
		ExitPrice:   exit,
		HorizonBars: l.cfg.HorizonBars,
		FeeBps:      l.cfg.FeeBps,
		GrossReturn: gross,
		NetReturn:   gross.Sub(fee),
		MFE:         mfe,
		MAE:         mae,
		CandlesHash: hashCandles(bars),
		LabeledAt:   now,
	}, nil
}

// candleHash is the canonical JSON shape of one used candle: fixed field
// order, RFC3339Nano UTC timestamps, Decimals as strings.
type candleHash struct {
	Symbol    string `json:"symbol"`
	Interval  string `json:"interval"`
	OpenTime  string `json:"openTime"`
	CloseTime string `json:"closeTime"`
	Open      string `json:"open"`
	High      string `json:"high"`
	Low       string `json:"low"`
	Close     string `json:"close"`
	Volume    string `json:"volume"`
}

// decOne is the QuoRound divisor for scale normalization.
var decOne = domain.MustParseDecimal("1")

// norm12 renders d with exactly 12 fractional digits so the hash is
// representation-independent: "105" and "105.0" hash identically.
func norm12(d domain.Decimal) string {
	q, err := d.QuoRound(decOne, returnScale)
	if err != nil {
		return d.String()
	}
	return q.String()
}

// hashCandles returns the lowercase hex SHA-256 of the canonical JSON of
// exactly the used candles.
func hashCandles(bars []domain.Candle) string {
	items := make([]candleHash, 0, len(bars))
	for _, b := range bars {
		items = append(items, candleHash{
			Symbol:    b.Symbol,
			Interval:  b.Interval,
			OpenTime:  b.OpenTime.UTC().Format(time.RFC3339Nano),
			CloseTime: b.CloseTime.UTC().Format(time.RFC3339Nano),
			Open:      norm12(b.Open),
			High:      norm12(b.High),
			Low:       norm12(b.Low),
			Close:     norm12(b.Close),
			Volume:    norm12(b.Volume),
		})
	}
	raw, err := json.Marshal(items)
	if err != nil {
		raw = []byte("[]")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
