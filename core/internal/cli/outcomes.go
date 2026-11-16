package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/outcome"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// outcomeSummary is the machine-readable outcomes label result.
type outcomeSummary struct {
	Labeled     int `json:"labeled"`
	Skipped     int `json:"skipped"`
	Already     int `json:"already"`
	Failed      int `json:"failed"`
	Unlabelable int `json:"unlabelable"`
}

func (r *runner) cmdOutcomes(args []string) int {
	if len(args) < 1 {
		r.fail(false, exitUsage, codeUsage, "missing outcomes subcommand",
			"run vend outcomes label [--candles FILE]", false, nil)
		return exitUsage
	}
	switch args[0] {
	case "label":
		return r.cmdOutcomesLabel(args[1:])
	default:
		r.fail(false, exitUsage, codeUsage,
			fmt.Sprintf("unknown outcomes subcommand %q", args[0]),
			"run vend outcomes label [--candles FILE]", false, nil)
		return exitUsage
	}
}

// fileCandles replays one candles file as a CandleSource. Filtering by
// symbol, interval and time window is done by the labeler itself.
type fileCandles struct {
	candles []domain.Candle
}

func (f fileCandles) Candles(_ context.Context, _ domain.CandleRequest) ([]domain.Candle, error) {
	return f.candles, nil
}

func (r *runner) cmdOutcomesLabel(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend outcomes label [--candles FILE]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("outcomes label")
	candles := fs.String("candles", "", "candles JSON file")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend outcomes label [--candles FILE]", false, nil)
	}
	cfgPath, dataDir, err := r.resolvePaths(g.config, g.dataDir)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"set VENDING_CONFIG_FILE and VENDING_DATA_DIR explicitly", false, nil)
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
	dbPath := config.DBPath(dataDir)
	if err := statMissing(dbPath); err != nil {
		if isNotFound(err) {
			return r.fail(jsonOut, exitNotFound, codeNotFound,
				fmt.Sprintf("database not found at %s", dbPath),
				"run vend init to create it", false, nil)
		}
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the data directory is readable and retry", false, nil)
	}
	jr, closer, err := r.openJournalAt(dbPath)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	defer func() { _ = closer.Close() }()

	var src outcome.CandleSource
	priceSource := "file"
	if strings.TrimSpace(*candles) != "" {
		cs, failCode, failErr := readCandlesFile(*candles)
		if failErr != nil {
			hint := "fix the candles file"
			errCode := codeUsage
			if failCode == exitNotFound {
				errCode = codeNotFound
			}
			return r.fail(jsonOut, failCode, errCode, failErr.Error(), hint, false, nil)
		}
		src = fileCandles{candles: cs}
	} else {
		reg, err := r.newRegistry(cfg,
			slog.New(slog.NewTextHandler(r.stderr, nil)))
		if err != nil {
			return r.fail(jsonOut, exitProvider, codeProvider, err.Error(),
				"fix the provider settings via vend config validate", false, nil)
		}
		p, err := reg.Candle(cfg.Providers.MarketData.ID)
		if err != nil {
			return r.fail(jsonOut, exitProvider, codeProvider, err.Error(),
				"align providers.marketdata.id with a registered provider", false, nil)
		}
		src = p
		priceSource = p.Descriptor().ID
	}

	lb, err := outcome.New(jr, src, ports.Deps{
		Clock:  r.clock,
		NewID:  r.newID,
		Logger: slog.New(slog.NewTextHandler(r.stderr, nil)),
	}, outcome.Config{
		HorizonBars: cfg.Outcome.HorizonBars,
		FeeBps:      cfg.Outcome.FeeBps,
		CodeVersion: r.ver,
		PriceSource: priceSource,
	})
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"fix the config file or run vend init --force to recreate it", false, nil)
	}
	sum, err := lb.Run(context.Background())
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	res := outcomeSummary{
		Labeled:     sum.Labeled,
		Skipped:     sum.Skipped,
		Already:     sum.Already,
		Failed:      sum.Failed,
		Unlabelable: sum.Unlabelable,
	}
	if jsonOut {
		r.printJSON(res)
		return exitOK
	}
	fmt.Fprintf(r.stdout, "labeled: %d\n", res.Labeled)
	fmt.Fprintf(r.stdout, "skipped: %d\n", res.Skipped)
	fmt.Fprintf(r.stdout, "already: %d\n", res.Already)
	fmt.Fprintf(r.stdout, "failed: %d\n", res.Failed)
	fmt.Fprintf(r.stdout, "unlabelable: %d\n", res.Unlabelable)
	return exitOK
}

// readCandlesFile parses a candles JSON file strictly: unknown fields and
// trailing data are rejected.
func readCandlesFile(path string) ([]domain.Candle, int, error) {
	var cs []domain.Candle
	if err := statMissing(path); err != nil {
		if isNotFound(err) {
			return nil, exitNotFound, fmt.Errorf("candles file not found at %s", path)
		}
		return nil, exitGeneral, err
	}
	f, err := os.Open(path)
	if err != nil {
		if isNotFound(err) {
			return nil, exitNotFound, fmt.Errorf("candles file not found at %s", path)
		}
		return nil, exitGeneral, err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cs); err != nil {
		return nil, exitUsage, fmt.Errorf("invalid candles JSON: %v", firstLine(err))
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, exitUsage, fmt.Errorf("invalid candles JSON: trailing data")
	}
	return cs, exitOK, nil
}
