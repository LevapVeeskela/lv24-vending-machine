// Command vend is the CLI of the vending-machine platform.
//
// This package is the only composition root of the core: it wires
// providers and the journal store into the cli package via factories.
// The cli package itself never imports providers or the store.
package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"time"

	decisiondrex "github.com/LevapVeeskela/lv24-vending-machine/providers/decision-drex"
	marketdatabinance "github.com/LevapVeeskela/lv24-vending-machine/providers/marketdata-binance"

	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/cli"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/registry"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/store/sqlite"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// version is replaced at release time via ldflags (-X main.version=...).
var version = "0.1.0-m0"

// newRegistry constructs both providers from the config without network I/O.
func newRegistry(cfg config.Config, logger *slog.Logger) (*registry.Registry, error) {
	reg := registry.New()
	dec, err := decisiondrex.New(decisiondrex.Config{
		Profile:         decisiondrex.Profile(cfg.Providers.Decision.Profile),
		BaseURL:         cfg.Providers.Decision.BaseURL,
		SecretRef:       cfg.Providers.Decision.SecretRef,
		PricePerBillion: cfg.Providers.Decision.PricePerBillion,
	}, ports.Deps{
		Clock:   time.Now,
		Logger:  logger,
		Secrets: config.EnvSecretStore{Getenv: os.Getenv},
	})
	if err != nil {
		return nil, err
	}
	if err := reg.RegisterDecision(dec); err != nil {
		return nil, err
	}
	md, err := marketdatabinance.New(marketdatabinance.Config{
		BaseURL: cfg.Providers.MarketData.BaseURL,
	}, ports.Deps{Clock: time.Now, Logger: logger})
	if err != nil {
		return nil, err
	}
	if err := reg.RegisterCandle(md); err != nil {
		return nil, err
	}
	return reg, nil
}

// openJournal opens the sqlite journal database at path.
func openJournal(path string) (ports.Journal, io.Closer, error) {
	st, err := sqlite.Open(context.Background(), path)
	if err != nil {
		return nil, nil, err
	}
	return st, st, nil
}

func main() {
	os.Exit(cli.Run(cli.Env{
		Args:        os.Args[1:],
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Getenv:      os.Getenv,
		Version:     version,
		NewRegistry: newRegistry,
		OpenJournal: openJournal,
	}))
}
