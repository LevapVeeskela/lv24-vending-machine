// Package outcome labels gate entry signals with forward candle returns.
//
// The Labeler replays gate.request events from the journal, fetches the
// closed candles that follow each signal, and appends one outcome.labeled
// event per signal. Labeling never looks ahead: only candles with
// CloseTime before now are used, the first used bar always opens at or
// after the signal timestamp, and a bar that closed before the gate
// accepted the signal is only allowed for a late (backfill) signal, which
// is flagged in the outcome.
package outcome

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
)

// Labeler labels gate entry signals with forward candle returns.
type Labeler struct {
	jr   ports.Journal
	src  CandleSource
	deps ports.Deps
	cfg  Config
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
	return &Labeler{jr: jr, src: src, deps: deps, cfg: cfg}, nil
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
		res, err := l.labelOne(ctx, ev, req, now)
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
		}
	}
	return out, nil
}

// fail logs one per-signal labeling error and asks Run to continue. The
// message carries ids only: no payload, price or secret ever reaches the log.
func (l *Labeler) fail(req domain.GateRequest, err error) (labelResult, error) {
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
func (l *Labeler) labelOne(ctx context.Context, ev domain.Event, req domain.GateRequest, now time.Time) (labelResult, error) {
	interval, err := parseInterval(req.Timeframe)
	if err != nil {
		return l.fail(req, err)
	}
	// receivedAt is when the gate accepted the signal, not when the signal
	// claims to have fired: the difference decides whether the signal is a
	// late (backfill) sample.
	receivedAt := ev.TS.UTC()
	backfill := !req.TS.IsZero() && receivedAt.Sub(req.TS.UTC()) > interval
	candles, err := l.src.Candles(ctx, domain.CandleRequest{
		Symbol:   req.Instrument.Symbol,
		Interval: req.Timeframe,
		From:     req.TS.UTC(),
		Limit:    l.cfg.HorizonBars + 2,
	})
	if err != nil {
		return l.fail(req, fmt.Errorf("outcome: candles for signal %q: %w", req.SignalID, err))
	}
	bars := selectBars(candles, req, receivedAt, backfill, now)
	if len(bars) < l.cfg.HorizonBars {
		return resultSkipped, nil
	}
	bars = bars[:l.cfg.HorizonBars]
	if !contiguous(bars, interval) {
		// A missing or shifted bar breaks the horizon: labeling over a
		// gappy window would silently change the holding period.
		return resultUnlabelable, nil
	}
	oc, err := l.buildOutcome(req, bars, now)
	if err != nil {
		return l.fail(req, err)
	}
	oc.DecisionID = ev.DecisionID
	oc.ReceivedAt = receivedAt
	oc.Backfill = backfill
	oc.PriceSource = l.cfg.PriceSource
	raw, err := json.Marshal(oc)
	if err != nil {
		return l.fail(req, fmt.Errorf("outcome: encode outcome for signal %q: %w", req.SignalID, err))
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
	return resultLabeled, nil
}

// selectBars keeps the usable candles for req: same symbol and interval,
// opening at or after the signal, already closed as of now.
//
// A signal accepted while its own bar was still open may enter at that bar's
// open, so the bar must close after receipt. A late (backfill) signal may
// enter a bar that had already closed when it arrived; the outcome then
// records backfill=true so reports can keep those samples apart.
//
// The result is sorted by open time ascending with duplicates removed (first
// wins).
func selectBars(all []domain.Candle, req domain.GateRequest, receivedAt time.Time, backfill bool, now time.Time) []domain.Candle {
	sigTS := req.TS.UTC()
	var bars []domain.Candle
	for _, c := range all {
		if c.Symbol != req.Instrument.Symbol || c.Interval != req.Timeframe {
			continue
		}
		open, closed := c.OpenTime.UTC(), c.CloseTime.UTC()
		if open.Before(sigTS) {
			continue
		}
		if !closed.Before(now) {
			continue
		}
		if !backfill && !closed.After(receivedAt) {
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
