// Package outcome labels gate entry signals with forward candle returns.
//
// The Labeler replays gate.request events from the journal, fetches the
// closed candles that follow each signal, and appends one outcome.labeled
// event per signal. Labeling never looks ahead: only candles with
// CloseTime before now are used, and the first used bar always opens at or
// after the signal timestamp.
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
	// HorizonBars is the number of closed candles per label.
	HorizonBars int
	// FeeBps is the one-way fee in basis points; the round trip is 2x.
	FeeBps int
	// CodeVersion is stamped on every appended event envelope.
	CodeVersion string
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
}

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
			return Summary{}, fmt.Errorf("outcome: decode gate request at seq %d: %w", ev.Seq, err)
		}
		if req.IntentKind != domain.IntentEntry {
			continue
		}
		if labeled[req.SignalID] {
			out.Already++
			continue
		}
		done, err := l.labelOne(ctx, ev, req, now)
		if err != nil {
			return Summary{}, err
		}
		if done {
			labeled[req.SignalID] = true
			out.Labeled++
		} else {
			out.Skipped++
		}
	}
	return out, nil
}

// labelOne fetches the candles following req and appends the outcome.labeled
// event. It reports false when fewer than HorizonBars closed candles exist
// yet, in which case nothing is appended.
func (l *Labeler) labelOne(ctx context.Context, ev domain.Event, req domain.GateRequest, now time.Time) (bool, error) {
	candles, err := l.src.Candles(ctx, domain.CandleRequest{
		Symbol:   req.Instrument.Symbol,
		Interval: req.Timeframe,
		From:     req.TS,
		Limit:    l.cfg.HorizonBars + 2,
	})
	if err != nil {
		return false, err
	}
	bars := selectBars(candles, req, now)
	if len(bars) < l.cfg.HorizonBars {
		return false, nil
	}
	bars = bars[:l.cfg.HorizonBars]
	oc, err := l.buildOutcome(req, bars, now)
	if err != nil {
		return false, err
	}
	oc.DecisionID = ev.DecisionID
	raw, err := json.Marshal(oc)
	if err != nil {
		return false, fmt.Errorf("outcome: encode outcome for signal %q: %w", req.SignalID, err)
	}
	if _, err := l.jr.Append(ctx, domain.Event{
		Kind:          domain.EventOutcomeLabeled,
		TS:            now,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       raw,
		CodeVersion:   l.cfg.CodeVersion,
		DecisionID:    ev.DecisionID,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// selectBars keeps the usable candles for req: same symbol and interval,
// opening at or after the signal, already closed as of now. The result is
// sorted by open time ascending with duplicates removed (first wins).
func selectBars(all []domain.Candle, req domain.GateRequest, now time.Time) []domain.Candle {
	sigTS := req.TS.UTC()
	var bars []domain.Candle
	for _, c := range all {
		if c.Symbol != req.Instrument.Symbol || c.Interval != req.Timeframe {
			continue
		}
		if c.OpenTime.UTC().Before(sigTS) {
			continue
		}
		if !c.CloseTime.UTC().Before(now) {
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
			Open:      b.Open.String(),
			High:      b.High.String(),
			Low:       b.Low.String(),
			Close:     b.Close.String(),
			Volume:    b.Volume.String(),
		})
	}
	raw, err := json.Marshal(items)
	if err != nil {
		raw = []byte("[]")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
