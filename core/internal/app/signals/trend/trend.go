// Package trend generates trend-following gate requests from OHLCV candles.
//
// The generator is a local signal source: it reads closed candles only,
// computes a fast and a slow EMA and emits an entry for every closed bar on
// which the fast EMA crosses the slow EMA. It performs no network I/O of its
// own.
//
// The generator remembers the latest closed bar it has already evaluated per
// symbol, so a repeated scan over the same provider data never replays the
// same bar. The first scan of a symbol evaluates only the last closed bar:
// earlier history is used for EMA warm-up but is never turned into signals.
package trend

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

const (
	// defaultVenue is the instrument venue stamped when Config.Venue is empty.
	defaultVenue = "binance"
	// source is the signal source name stamped on every request.
	source = "trend"
	// barsPerSlow is the bar count requested per unit of Slow; it keeps the
	// EMA warm-up history proportional to the slow period.
	barsPerSlow = 4
	// minBarsAboveSlow is the bar count required above Slow: one bar for the
	// slow EMA to be defined on the previous bar and one to cross on.
	minBarsAboveSlow = 2
)

// Config tunes one trend Generator.
type Config struct {
	// Symbols lists the instruments to scan; entries are canonicalized with
	// domain.CanonicalSymbol and deduplicated in order.
	Symbols []string
	// Timeframe is the candle interval used for EMA computation.
	Timeframe string
	// Fast is the fast EMA period.
	Fast int
	// Slow is the slow EMA period; Fast < Slow is required.
	Slow int
	// Sides restricts the emitted directions to buy and/or sell.
	Sides []string
	// Venue is the instrument venue stamped on every signal; empty means
	// "binance".
	Venue string
	// LabelIntervalSec is the optional outcome-labeling granularity in
	// seconds. When positive, the timeframe interval must be at least twice
	// as long so a label window always fits inside one candle.
	LabelIntervalSec int
}

// Generator scans candle providers for EMA crossovers.
type Generator struct {
	symbols   []string
	timeframe string
	fast      int
	slow      int
	sides     map[domain.Side]bool
	venue     string
	candles   ports.CandleProvider
	deps      ports.Deps
	limit     int
	minBars   int
	// lastSeen holds the close time of the latest evaluated closed bar per
	// symbol. It is the high-water mark that prevents replayed signals.
	lastSeen map[string]time.Time
}

// invalid builds a coded configuration error.
func invalid(msg string) error {
	return domain.NewError(domain.CodeInvalidRequest, "trend: "+msg)
}

// parseInterval converts a candle interval such as "1m", "15m", "4h", "1d"
// or "1w" into a duration. Only a positive integer amount and the m, h, d
// and w units are accepted.
func parseInterval(raw string) (time.Duration, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if len(s) < 2 {
		return 0, fmt.Errorf("interval %q must be an amount followed by m, h, d or w", raw)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("interval %q must start with a positive integer", raw)
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	case 'w':
		unit = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("interval %q unit must be m, h, d or w", raw)
	}
	return time.Duration(n) * unit, nil
}

// New validates cfg and builds a Generator over candles.
func New(cfg Config, candles ports.CandleProvider, deps ports.Deps) (*Generator, error) {
	if candles == nil {
		return nil, invalid("nil candle provider")
	}
	timeframe := strings.TrimSpace(cfg.Timeframe)
	if timeframe == "" {
		return nil, invalid("timeframe must not be empty")
	}
	if cfg.Fast <= 0 || cfg.Slow <= 0 || cfg.Fast >= cfg.Slow {
		return nil, invalid(fmt.Sprintf("fast and slow must satisfy 0 < fast < slow, got fast=%d slow=%d", cfg.Fast, cfg.Slow))
	}
	if len(cfg.Sides) == 0 {
		return nil, invalid("sides must not be empty")
	}
	sides := make(map[domain.Side]bool, len(cfg.Sides))
	for _, raw := range cfg.Sides {
		side := domain.Side(strings.ToLower(strings.TrimSpace(raw)))
		switch side {
		case domain.SideBuy, domain.SideSell:
			sides[side] = true
		default:
			return nil, invalid(fmt.Sprintf("side %q must be buy or sell", raw))
		}
	}
	if len(cfg.Symbols) == 0 {
		return nil, invalid("symbols must not be empty")
	}
	seen := make(map[string]bool, len(cfg.Symbols))
	symbols := make([]string, 0, len(cfg.Symbols))
	for _, raw := range cfg.Symbols {
		sym := domain.CanonicalSymbol(raw)
		if sym == "" {
			return nil, invalid(fmt.Sprintf("symbol %q is empty after normalization", raw))
		}
		if seen[sym] {
			continue
		}
		seen[sym] = true
		symbols = append(symbols, sym)
	}
	if cfg.LabelIntervalSec < 0 {
		return nil, invalid(fmt.Sprintf("label interval sec must not be negative, got %d", cfg.LabelIntervalSec))
	}
	if cfg.LabelIntervalSec > 0 {
		interval, err := parseInterval(timeframe)
		if err != nil {
			return nil, invalid(fmt.Sprintf("timeframe %q: %v", timeframe, err))
		}
		if min := 2 * time.Duration(cfg.LabelIntervalSec) * time.Second; interval < min {
			return nil, invalid(fmt.Sprintf("timeframe %q is shorter than twice the label interval %s", timeframe, min))
		}
	}
	venue := strings.TrimSpace(cfg.Venue)
	if venue == "" {
		venue = defaultVenue
	}
	return &Generator{
		symbols:   symbols,
		timeframe: timeframe,
		fast:      cfg.Fast,
		slow:      cfg.Slow,
		sides:     sides,
		venue:     venue,
		candles:   candles,
		deps:      deps,
		limit:     max(barsPerSlow*cfg.Slow, cfg.Slow+minBarsAboveSlow),
		minBars:   cfg.Slow + minBarsAboveSlow,
		lastSeen:  make(map[string]time.Time, len(cfg.Symbols)),
	}, nil
}

// Scan evaluates every configured symbol and returns the new gate requests in
// configuration order. Candles whose CloseTime is not before now are ignored,
// so an unclosed bar never produces a signal. A failing symbol is logged and
// skipped so the remaining symbols still produce their signals.
func (g *Generator) Scan(ctx context.Context) ([]domain.GateRequest, error) {
	now := g.deps.Now()
	var out []domain.GateRequest
	for _, symbol := range g.symbols {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		raw, err := g.candles.Candles(ctx, domain.CandleRequest{
			Symbol:   symbol,
			Interval: g.timeframe,
			Limit:    g.limit,
		})
		if err != nil {
			g.warn("candles", symbol, err)
			continue
		}
		reqs, err := g.evaluate(symbol, raw, now)
		if err != nil {
			g.warn("evaluate", symbol, err)
			continue
		}
		out = append(out, reqs...)
	}
	return out, nil
}

// warn logs a per-symbol failure without stopping the scan. Only the symbol
// and the error are logged; candle payloads are never included.
func (g *Generator) warn(op, symbol string, err error) {
	if g.deps.Logger == nil {
		return
	}
	g.deps.Logger.Warn("trend: "+op+" failed", "symbol", symbol, "err", err)
}

// evaluate derives the gate requests of the closed bars of symbol that were
// not evaluated before. The first scan of a symbol evaluates only its last
// closed bar, so pre-existing history is never replayed.
func (g *Generator) evaluate(symbol string, raw []domain.Candle, now time.Time) ([]domain.GateRequest, error) {
	bars := closedBars(symbol, g.timeframe, raw, now)
	if len(bars) < g.minBars {
		return nil, nil
	}
	closes := make([]float64, len(bars))
	for i, bar := range bars {
		f, err := decimalToFloat(bar.Close)
		if err != nil {
			return nil, fmt.Errorf("trend: %s close: %w", symbol, err)
		}
		closes[i] = f
	}
	fast := emaSeries(closes, g.fast)
	slow := emaSeries(closes, g.slow)
	last := len(bars) - 1
	seen, known := g.lastSeen[symbol]
	start := -1
	if !known {
		start = last
	} else {
		for i, bar := range bars {
			if bar.CloseTime.After(seen) {
				start = i
				break
			}
		}
	}
	latest := bars[last].CloseTime
	if !known || latest.After(seen) {
		g.lastSeen[symbol] = latest
	}
	if start < 0 {
		// No new closed bar since the previous scan.
		return nil, nil
	}
	var out []domain.GateRequest
	for i := start; i <= last; i++ {
		side, ok := crossSide(fast, slow, i)
		if !ok || !g.sides[side] {
			continue
		}
		out = append(out, g.request(symbol, bars[i], side, fast[i], slow[i], closes[i]))
	}
	return out, nil
}

// crossSide reports the direction of the EMA cross on bar i, if any.
func crossSide(fast, slow []float64, i int) (domain.Side, bool) {
	if i < 1 || i >= len(fast) || i >= len(slow) {
		return "", false
	}
	prev := i - 1
	switch {
	case fast[prev] <= slow[prev] && fast[i] > slow[i]:
		return domain.SideBuy, true
	case fast[prev] >= slow[prev] && fast[i] < slow[i]:
		return domain.SideSell, true
	default:
		return "", false
	}
}

// request builds the gate request of one crossed bar.
func (g *Generator) request(symbol string, bar domain.Candle, side domain.Side, emaFast, emaSlow, close float64) domain.GateRequest {
	return domain.GateRequest{
		SignalID:   fmt.Sprintf("trend:%s:%s:%d:%s", symbol, g.timeframe, bar.CloseTime.Unix(), side),
		Instrument: domain.Instrument{Venue: g.venue, Symbol: symbol},
		Side:       side,
		IntentKind: domain.IntentEntry,
		Timeframe:  g.timeframe,
		TS:         bar.CloseTime.UTC(),
		Price:      bar.Close,
		Source:     source,
		State: map[string]float64{
			"ema_fast": emaFast,
			"ema_slow": emaSlow,
			"close":    close,
			"fast":     float64(g.fast),
			"slow":     float64(g.slow),
		},
	}
}

// closedBars keeps the candles of symbol with the matching interval, a
// positive close and a CloseTime before now; it then orders them by
// CloseTime and keeps the first occurrence of each close time.
func closedBars(symbol, timeframe string, raw []domain.Candle, now time.Time) []domain.Candle {
	bars := make([]domain.Candle, 0, len(raw))
	for _, c := range raw {
		if domain.CanonicalSymbol(c.Symbol) != symbol {
			continue
		}
		if strings.TrimSpace(c.Interval) != timeframe {
			continue
		}
		if !c.CloseTime.Before(now) {
			continue
		}
		if c.Close.Sign() <= 0 {
			continue
		}
		bars = append(bars, c)
	}
	sort.SliceStable(bars, func(i, j int) bool {
		return bars[i].CloseTime.Before(bars[j].CloseTime)
	})
	uniq := make([]domain.Candle, 0, len(bars))
	for _, c := range bars {
		if n := len(uniq); n > 0 && uniq[n-1].CloseTime.Equal(c.CloseTime) {
			continue
		}
		uniq = append(uniq, c)
	}
	return uniq
}

// emaSeries returns the exponential moving average of closes with
// alpha = 2/(n+1), seeded with the first close.
func emaSeries(closes []float64, n int) []float64 {
	out := make([]float64, len(closes))
	if len(closes) == 0 {
		return out
	}
	alpha := 2.0 / float64(n+1)
	out[0] = closes[0]
	for i := 1; i < len(closes); i++ {
		out[i] = alpha*closes[i] + (1-alpha)*out[i-1]
	}
	return out
}

// decimalToFloat converts d for EMA math and state reporting. Money stays
// Decimal in the request; this conversion is for indicator values only.
func decimalToFloat(d domain.Decimal) (float64, error) {
	f, err := strconv.ParseFloat(d.String(), 64)
	if err != nil {
		return 0, fmt.Errorf("not a finite decimal: %w", err)
	}
	return f, nil
}
