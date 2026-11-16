package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/api"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/gateflow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/outcome"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/report"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/shadow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/signals/trend"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// localAPITokenEnv holds the bearer token of the loopback gate API.
const localAPITokenEnv = "VENDING_SECRET_CONN_LOCAL_APITOKEN"

func (r *runner) cmdServe(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend serve [--addr 127.0.0.1:8420]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("serve")
	addr := fs.String("addr", "127.0.0.1:8420", "listen address")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend serve [--addr 127.0.0.1:8420]", false, nil)
	}
	if err := api.ValidateAddr(*addr); err != nil {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("invalid --addr %q: %v", *addr, err),
			"run vend serve --addr 127.0.0.1:8420 with a port 1..65535", false, nil)
	}
	token := ""
	if r.getenv != nil {
		token = r.getenv(localAPITokenEnv)
	} else {
		token = os.Getenv(localAPITokenEnv)
	}
	if token == "" {
		return r.fail(jsonOut, exitForbidden, codeForbidden,
			"local API token is not set",
			fmt.Sprintf("set %s and retry", localAPITokenEnv), false, nil)
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

	logger := slog.New(slog.NewTextHandler(r.stderr, nil))
	reg, err := r.newRegistry(cfg, logger)
	if err != nil {
		return r.fail(jsonOut, exitProvider, codeProvider, err.Error(),
			"fix the provider settings via vend config validate", domain.IsRetryable(err), nil)
	}
	provider, err := reg.Decision(cfg.Providers.Decision.ID)
	if err != nil {
		return r.fail(jsonOut, exitProvider, codeProvider, err.Error(),
			"align providers.decision.id with a registered provider", false, nil)
	}
	var arena *gateflow.ArenaConfig
	if cfg.Arena.Enabled {
		arenaProvider, aerr := reg.Decision("decision-arena")
		if aerr != nil {
			return r.fail(jsonOut, exitProvider, codeProvider, aerr.Error(),
				"the arena provider is not registered; fix arena settings or set arena.enabled=false", false, nil)
		}
		arena = &gateflow.ArenaConfig{Provider: arenaProvider, Model: cfg.Arena.Model}
	}
	jr, closer, err := r.openJournalAt(config.DBPath(dataDir))
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the data directory is writable and retry", false, nil)
	}
	defer func() { _ = closer.Close() }()

	flow, err := gateflow.New(jr, provider, ports.Deps{
		Clock:  r.clock,
		NewID:  r.newID,
		Logger: logger,
	}, gateflow.Config{
		Model:       cfg.Providers.Decision.Model,
		Policy:      domain.GatePolicy{Version: cfg.Gate.Version, MinPass: cfg.Gate.MinPass, MinBlock: cfg.Gate.MinBlock},
		CodeVersion: r.ver,
		Arena:       arena,
	})
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the vend wiring and retry", false, nil)
	}
	candle, err := reg.Candle(cfg.Providers.MarketData.ID)
	if err != nil {
		return r.fail(jsonOut, exitProvider, codeProvider, err.Error(),
			"align providers.marketdata.id with a registered provider", false, nil)
	}
	deps := ports.Deps{
		Clock:  r.clock,
		NewID:  r.newID,
		Logger: logger,
	}
	labeler, err := outcome.New(jr, candle, deps, outcome.Config{
		HorizonBars: cfg.Outcome.HorizonBars,
		FeeBps:      cfg.Outcome.FeeBps,
		CodeVersion: r.ver,
		PriceSource: cfg.Providers.MarketData.ID,
	})
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"fix outcome.horizonBars/outcome.feeBps and retry", false, nil)
	}
	reporter, err := report.New(jr, deps, report.Config{})
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the vend wiring and retry", false, nil)
	}
	signals, err := shadowSignals(cfg.Signals.Trend, candle, deps)
	if err != nil {
		return r.fail(jsonOut, exitUsage, codeUsage, err.Error(),
			"fix signals.trend settings via vend config validate", false, nil)
	}
	var notifier ports.Notifier
	if cfg.Notify.Telegram.Enabled {
		notifier, err = reg.Notifier("notify-telegram")
		if err != nil {
			return r.fail(jsonOut, exitProvider, codeProvider, err.Error(),
				"check notify.telegram settings or disable it", false, nil)
		}
	}
	runner, err := shadow.New(jr, labeler, signals, flow, reporter, notifier, deps, shadow.Config{
		LabelInterval: time.Duration(cfg.Outcome.LabelIntervalSec) * time.Second,
		StatusPath:    filepath.Join(dataDir, "shadow-status.json"),
		CodeVersion:   r.ver,
	})
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the shadow wiring and retry", false, nil)
	}
	h, err := api.New(api.Config{
		Token:         token,
		Version:       r.ver,
		Mode:          cfg.Mode,
		MaxBodyBytes:  64 << 10,
		MaxConcurrent: 8,
	}, flow, logger)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the server wiring and retry", false, nil)
	}
	fmt.Fprintf(r.stderr, "listening on http://%s\n", *addr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(ctx)
	}()
	serveErr := api.Serve(ctx, *addr, h, logger)
	// Cancel the shadow loop and wait for an in-flight step to finish, then
	// drain detached arena calls: the journal is closed by the deferred
	// closer after this function returns.
	stop()
	<-done
	flow.Wait()
	if serveErr != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, serveErr.Error(),
			"check the address is free and retry", false, nil)
	}
	return exitOK
}

// shadowSignals builds the optional signal source for the shadow runner. It
// returns a nil interface when the trend source is disabled: assigning a
// typed nil *trend.Generator would make the interface non-nil and panic on
// the first Step.
func shadowSignals(tr config.TrendConfig, candle ports.CandleProvider, deps ports.Deps) (shadow.SignalSource, error) {
	if !tr.Enabled {
		return nil, nil
	}
	gen, err := trend.New(trend.Config{
		Symbols:   tr.Symbols,
		Timeframe: tr.Timeframe,
		Fast:      tr.Fast,
		Slow:      tr.Slow,
		Sides:     tr.Sides,
	}, candle, deps)
	if err != nil {
		return nil, err
	}
	return gen, nil
}
