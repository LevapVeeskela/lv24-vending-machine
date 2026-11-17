package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/signals/trend"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

const (
	// signalsTargetPerDay is the G1 volume target: 100 labeled decisions in
	// seven days is about 15 per day.
	signalsTargetPerDay = 15
	// estimateMinDays and estimateMaxDays bound the history window.
	estimateMinDays = 1
	estimateMaxDays = 30
	// estimateDefaultDays is the G1 week.
	estimateDefaultDays = 7
)

// estimateSymbol is one symbol row of the signals estimate JSON.
type estimateSymbol struct {
	Symbol  string   `json:"symbol"`
	Signals *int     `json:"signals,omitempty"`
	PerDay  *float64 `json:"perDay,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// estimateTotal is the summed row of the signals estimate JSON.
type estimateTotal struct {
	Signals int     `json:"signals"`
	PerDay  float64 `json:"perDay"`
}

// estimateResult is the machine-readable signals estimate. The fields mirror
// the human lines, including per-symbol provider errors.
type estimateResult struct {
	Days         int              `json:"days"`
	TargetPerDay float64          `json:"targetPerDay"`
	Symbols      []estimateSymbol `json:"symbols"`
	Total        estimateTotal    `json:"total"`
	Verdict      string           `json:"verdict"`
}

func (r *runner) cmdSignals(args []string) int {
	if len(args) < 1 {
		r.fail(false, exitUsage, codeUsage, "missing signals subcommand",
			"run vend signals estimate [--days N]", false, nil)
		return exitUsage
	}
	switch args[0] {
	case "estimate":
		return r.cmdSignalsEstimate(args[1:])
	default:
		r.fail(false, exitUsage, codeUsage,
			fmt.Sprintf("unknown signals subcommand %q", args[0]),
			"run vend signals estimate [--days N]", false, nil)
		return exitUsage
	}
}

// cmdSignalsEstimate counts the signals the configured trend source would have
// produced over the last days. It only reads candles: it opens no journal,
// calls no decision provider and needs no secrets.
func (r *runner) cmdSignalsEstimate(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend signals estimate [--days N]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("signals estimate")
	days := fs.Int("days", estimateDefaultDays, "history window in days (1..30)")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend signals estimate [--days N]", false, nil)
	}
	if *days < estimateMinDays || *days > estimateMaxDays {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("invalid --days %d", *days),
			fmt.Sprintf("pass --days between %d and %d", estimateMinDays, estimateMaxDays), false, nil)
	}
	cfgPath, err := r.configPath(g.config)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"set VENDING_CONFIG_FILE explicitly", false, nil)
	}
	cfg, err := r.loadConfig(cfgPath)
	if err != nil {
		code, errCode := loadFailureCode(err)
		hint := "run vend init to create the config"
		if code == exitUsage {
			hint = "fix the config file or run vend init --force to recreate it"
		}
		return r.fail(jsonOut, code, errCode,
			fmt.Sprintf("cannot load config at %s: %v", cfgPath, firstLine(err)), hint, false, nil)
	}
	tr := cfg.Signals.Trend
	if !tr.Enabled {
		return r.fail(jsonOut, exitUsage, codeUsage,
			"signals.trend is disabled",
			"set signals.trend.enabled=true and list symbols in the config", false, nil)
	}
	if len(tr.Symbols) == 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			"signals.trend.symbols is empty",
			"add at least one symbol to signals.trend.symbols", false, nil)
	}
	reg, err := r.newRegistry(cfg, slog.New(slog.NewTextHandler(r.stderr, nil)))
	if err != nil {
		return r.fail(jsonOut, exitProvider, codeProvider, err.Error(),
			"fix the provider settings via vend config validate", domain.IsRetryable(err), nil)
	}
	candles, err := reg.Candle(cfg.Providers.MarketData.ID)
	if err != nil {
		return r.fail(jsonOut, exitProvider, codeProvider, err.Error(),
			"align providers.marketdata.id with a registered provider", false, nil)
	}
	gen, err := trend.New(trend.Config{
		Symbols:   tr.Symbols,
		Timeframe: tr.Timeframe,
		Fast:      tr.Fast,
		Slow:      tr.Slow,
		Sides:     tr.Sides,
		Venue:     tr.Venue,
	}, candles, ports.Deps{Clock: r.clock})
	if err != nil {
		return r.fail(jsonOut, exitUsage, codeUsage, err.Error(),
			"fix signals.trend settings via vend config validate", false, nil)
	}
	estimates, err := gen.Estimate(context.Background(), *days)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"retry the command", false, nil)
	}

	res := estimateResult{
		Days:         *days,
		TargetPerDay: signalsTargetPerDay,
		Symbols:      make([]estimateSymbol, 0, len(estimates)),
		Verdict:      "low",
	}
	total := 0
	failed := 0
	for _, est := range estimates {
		row := estimateSymbol{Symbol: est.Symbol}
		if est.Err != nil {
			row.Error = string(domain.CodeOf(est.Err))
			failed++
			res.Symbols = append(res.Symbols, row)
			continue
		}
		signals := est.Signals
		perDay := estimatePerDay(signals, *days)
		row.Signals = &signals
		row.PerDay = &perDay
		res.Symbols = append(res.Symbols, row)
		total += signals
	}
	res.Total = estimateTotal{Signals: total, PerDay: estimatePerDay(total, *days)}
	if res.Total.PerDay >= signalsTargetPerDay {
		res.Verdict = "enough"
	}

	if jsonOut {
		r.printJSON(res)
	} else {
		printEstimateHuman(r.stdout, res)
	}
	if failed > 0 {
		return r.fail(jsonOut, exitProvider, codeProvider,
			fmt.Sprintf("signals estimate failed for %d of %d symbols", failed, len(estimates)),
			"check the failing symbols and the marketdata provider, or lower --days when a symbol's history is shorter than the window, then retry", false, nil)
	}
	return exitOK
}

// estimatePerDay returns signals per day rounded to one decimal, the precision
// printed by the human output and the JSON.
func estimatePerDay(signals, days int) float64 {
	if days <= 0 {
		return 0
	}
	return math.Round(float64(signals)/float64(days)*10) / 10
}

// printEstimateHuman prints one row per symbol, the total, the G1 target, the
// verdict and the disclaimer, in that order.
func printEstimateHuman(w io.Writer, res estimateResult) {
	for _, row := range res.Symbols {
		if row.Error != "" {
			fmt.Fprintf(w, "%s error=%s\n", row.Symbol, row.Error)
			continue
		}
		if row.Signals == nil || row.PerDay == nil {
			continue
		}
		fmt.Fprintf(w, "%s signals=%d per_day=%.1f\n", row.Symbol, *row.Signals, *row.PerDay)
	}
	fmt.Fprintf(w, "total: signals=%d per_day=%.1f\n", res.Total.Signals, res.Total.PerDay)
	fmt.Fprintf(w, "target: %d per day (100 labeled in 7 days)\n", signalsTargetPerDay)
	fmt.Fprintf(w, "verdict: %s\n", res.Verdict)
	fmt.Fprintln(w, "past frequency does not predict future frequency")
}
