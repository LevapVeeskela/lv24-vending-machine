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

	"github.com/oklog/ulid/v2"

	decisiondrex "github.com/LevapVeeskela/lv24-vending-machine/providers/decision-drex"
	marketdatabinance "github.com/LevapVeeskela/lv24-vending-machine/providers/marketdata-binance"
	notifytelegram "github.com/LevapVeeskela/lv24-vending-machine/providers/notify-telegram"

	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/cli"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/registry"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/store/sqlite"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// version is replaced at release time via ldflags (-X main.version=...).
var version = "0.1.0-m0"

// arenaDecision pins a decision provider under the arena registry id while
// keeping the wrapped provider's own identity for its outbound calls.
type arenaDecision struct {
	ports.Decision
}

// Descriptor reports the arena registry id.
func (a arenaDecision) Descriptor() ports.Descriptor {
	d := a.Decision.Descriptor()
	d.ID = "decision-arena"
	return d
}

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
	if cfg.Arena.Enabled {
		// The arena is a second connection to the same provider protocol,
		// registered under its own id so the core never branches on it.
		arenaDec, err := decisiondrex.New(decisiondrex.Config{
			Profile:         decisiondrex.Profile(cfg.Arena.Profile),
			BaseURL:         cfg.Arena.BaseURL,
			SecretRef:       cfg.Arena.SecretRef,
			PricePerBillion: cfg.Arena.PricePerBillion,
		}, ports.Deps{
			Clock:   time.Now,
			Logger:  logger,
			Secrets: config.EnvSecretStore{Getenv: os.Getenv},
		})
		if err != nil {
			return nil, err
		}
		if err := reg.RegisterDecision(arenaDecision{Decision: arenaDec}); err != nil {
			return nil, err
		}
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
	if cfg.Notify.Telegram.Enabled {
		// The bot token is resolved from the secret store at send time; only
		// the chat id and the secret reference are wired here.
		tg, err := notifytelegram.New(notifytelegram.Config{
			ChatID:    cfg.Notify.Telegram.ChatID,
			SecretRef: cfg.Notify.Telegram.SecretRef,
		}, ports.Deps{
			Clock:   time.Now,
			Logger:  logger,
			Secrets: config.EnvSecretStore{Getenv: os.Getenv},
		})
		if err != nil {
			return nil, err
		}
		if err := reg.RegisterNotifier(tg); err != nil {
			return nil, err
		}
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
		Clock:       time.Now,
		NewID:       func() string { return ulid.Make().String() },
	}))
}
