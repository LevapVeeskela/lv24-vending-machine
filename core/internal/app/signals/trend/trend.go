// Package trend generates trend-following gate requests from OHLCV candles.
//
// The generator is a local signal source: it reads closed candles only,
// computes a fast and a slow EMA and emits an entry only when the fast EMA
// crosses the slow EMA on the last closed bar. It performs no network I/O
// of its own and keeps no state between scans, so repeated scans over the
// same provider data are deterministic.
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
	// venue is the instrument venue stamped on every request.
	venue = "binance"
	// source is the signal source name stamped on every request.
	source = "trend"
	// headroomBars is the extra bar count requested above Slow.
	headroomBars = 3
	// minBarsAboveSlow is the bar count required above Slow.
	minBarsAboveSlow = 2
)

// Config tunes one trend Generator.
type Config struct {
	// Symbols lists the instruments to scan; entries are normalized to
	// uppercase without "/" and "-" and deduplicated in order.
	Symbols []string
	// Timeframe is the candle interval used for EMA computation.
	Timeframe string
	// Fast is the fast EMA period.
	Fast int
	// Slow is the slow EMA period; Fast < Slow is required.
	Slow int
	// Sides restricts the emitted directions to buy and/or sell.
	Sides []string
}

// Generator scans candle providers for EMA crossovers.
type Generator struct {
	symbols   []string
	timeframe string
	fast      int
	slow      int
	sides     map[domain.Side]bool
	candles   ports.CandleProvider
	deps      ports.Deps
	limit     int
	minBars   int
}

// invalid builds a coded configuration error.
func invalid(msg string) error {
	return domain.NewError(domain.CodeInvalidRequest, "trend: "+msg)
}

// normalizeSymbol uppercases s and strips "/" and "-" separators.
func normalizeSymbol(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "/", "")
	s = strings.ReplaceAll(s, "-", "")
	return s
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
		sym := normalizeSymbol(raw)
		if sym == "" {
			return nil, invalid(fmt.Sprintf("symbol %q is empty after normalization", raw))
		}
		if seen[sym] {
			continue
		}
		seen[sym] = true
		symbols = append(symbols, sym)
	}
	return &Generator{
		symbols:   symbols,
		timeframe: timeframe,
		fast:      cfg.Fast,
		slow:      cfg.Slow,
		sides:     sides,
		candles:   candles,
		deps:      deps,
		limit:     cfg.Slow + headroomBars,
		minBars:   cfg.Slow + minBarsAboveSlow,
	}, nil
}

// Scan evaluates every configured symbol and returns the new gate requests
// in configuration order. Candles whose CloseTime is not before now are
// ignored, so an unclosed bar never produces a signal. A provider failure
// for any symbol fails the whole scan.
func (g *Generator) Scan(ctx context.Context) ([]domain.GateRequest, error) {
	now := g.deps.Now()
	var out []domain.GateRequest
	for _, symbol := range g.symbols {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := g.candles.Candles(ctx, domain.CandleRequest{
			Symbol:   symbol,
			Interval: g.timeframe,
			Limit:    g.limit,
		})
		if err != nil {
			return nil, fmt.Errorf("trend: candles for %s: %w", symbol, err)
		}
		req, ok, err := g.evaluate(symbol, raw, now)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, req)
		}
	}
	return out, nil
}

// evaluate derives at most one gate request from the closed bars of symbol.
func (g *Generator) evaluate(symbol string, raw []domain.Candle, now time.Time) (domain.GateRequest, bool, error) {
	bars := closedBars(symbol, g.timeframe, raw, now)
	if len(bars) < g.minBars {
		return domain.GateRequest{}, false, nil
	}
	closes := make([]float64, len(bars))
	for i, bar := range bars {
		f, err := decimalToFloat(bar.Close)
		if err != nil {
			return domain.GateRequest{}, false, fmt.Errorf("trend: %s close: %w", symbol, err)
		}
		closes[i] = f
	}
	fast := emaSeries(closes, g.fast)
	slow := emaSeries(closes, g.slow)
	last := len(bars) - 1
	prev := last - 1

	var side domain.Side
	switch {
	case fast[prev] <= slow[prev] && fast[last] > slow[last]:
		side = domain.SideBuy
	case fast[prev] >= slow[prev] && fast[last] < slow[last]:
		side = domain.SideSell
	default:
		return domain.GateRequest{}, false, nil
	}
	if !g.sides[side] {
		return domain.GateRequest{}, false, nil
	}

	lastBar := bars[last]
	return domain.GateRequest{
		SignalID:   fmt.Sprintf("trend:%s:%s:%d:%s", symbol, g.timeframe, lastBar.CloseTime.Unix(), side),
		Instrument: domain.Instrument{Venue: venue, Symbol: symbol},
		Side:       side,
		IntentKind: domain.IntentEntry,
		Timeframe:  g.timeframe,
		TS:         lastBar.CloseTime.UTC(),
		Price:      lastBar.Close,
		Source:     source,
		State: map[string]float64{
			"ema_fast": fast[last],
			"ema_slow": slow[last],
			"close":    closes[last],
			"fast":     float64(g.fast),
			"slow":     float64(g.slow),
		},
	}, true, nil
}

// closedBars keeps the candles of symbol with the matching interval, a
// positive close and a CloseTime before now; it then orders them by
// CloseTime and keeps the first occurrence of each close time.
func closedBars(symbol, timeframe string, raw []domain.Candle, now time.Time) []domain.Candle {
	bars := make([]domain.Candle, 0, len(raw))
	for _, c := range raw {
		if normalizeSymbol(c.Symbol) != symbol {
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
