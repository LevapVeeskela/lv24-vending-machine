package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/api"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/gateflow"
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
	})
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the vend wiring and retry", false, nil)
	}
	h, err := api.New(api.Config{
		Token:         token,
		Version:       r.ver,
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
	if err := api.Serve(ctx, *addr, h, logger); err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the address is free and retry", false, nil)
	}
	return exitOK
}
