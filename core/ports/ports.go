// Package ports declares the narrow provider interfaces of the core.
//
// Providers implement these interfaces; the core consumes them.
// This package depends only on the standard library and core/domain.
package ports

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// Kind identifies a provider capability family.
type Kind string

// Supported provider kinds.
const (
	// KindDecision marks advisory decision (LLM) providers.
	KindDecision Kind = "decision"
	// KindMarketData marks OHLCV market data providers.
	KindMarketData Kind = "marketdata"
)

// ConfigField describes one provider connection setting for UIs.
type ConfigField struct {
	// Name is the setting key.
	Name string
	// Type is the UI field type (e.g. "string", "url", "int").
	Type string
	// Description is the human-readable help text.
	Description string
	// Required marks settings the provider needs to operate.
	Required bool
	// Secret marks values that arrive via SecretStore, not plain config.
	Secret bool
}

// Descriptor advertises a provider identity and capabilities.
type Descriptor struct {
	// ID is the stable provider identifier.
	ID string
	// Name is the human-readable provider name.
	Name string
	// Kind is the provider capability family.
	Kind Kind
	// Version is the provider implementation version.
	Version string
	// Capabilities lists fine-grained feature flags.
	Capabilities []string
	// ConfigFields describes the connection form for UIs.
	ConfigFields []ConfigField
	// Degradations lists known graceful degradation modes.
	Degradations []string
}

// SecretStore resolves secret references such as conn/<id>/<field>.
type SecretStore interface {
	// Get fetches the secret value for ref.
	Get(ctx context.Context, ref string) (string, error)
}

// Deps bundles the host-provided dependencies for providers.
type Deps struct {
	// HTTPClient is the shared outbound HTTP client.
	HTTPClient *http.Client
	// Clock returns the current time; nil means time.Now.
	Clock func() time.Time
	// Logger is the host logger.
	Logger *slog.Logger
	// Secrets resolves secret references.
	Secrets SecretStore
	// NewID generates unique identifiers; nil means callers must not rely on it.
	NewID func() string
}

// NextID returns Deps.NewID() or "" when NewID is nil.
func (d Deps) NextID() string {
	if d.NewID == nil {
		return ""
	}
	return d.NewID()
}

// Now returns the current time in UTC via Clock or time.Now.
func (d Deps) Now() time.Time {
	if d.Clock != nil {
		return d.Clock().UTC()
	}
	return time.Now().UTC()
}

// Decision is an advisory provider answering DecisionRequests.
type Decision interface {
	// Descriptor advertises the provider identity and capabilities.
	Descriptor() Descriptor
	// Decide answers the given advisory request.
	Decide(ctx context.Context, req domain.DecisionRequest) (domain.DecisionResponse, error)
}

// CandleProvider serves historical OHLCV candles.
type CandleProvider interface {
	// Descriptor advertises the provider identity and capabilities.
	Descriptor() Descriptor
	// Candles returns the candles selected by req.
	Candles(ctx context.Context, req domain.CandleRequest) ([]domain.Candle, error)
}

// Journal is an append-only hash-chained event store.
//
// Query returns events with Seq greater than from (from == 0 returns all
// events); a non-empty kind filters to that kind only, in ascending Seq
// order.
//
// Append implementations assign Seq, EventID (ULID), TS (UTC when zero),
// PrevHash and Hash themselves and return the stored event; the port only
// documents this contract and performs no field filling.
type Journal interface {
	// Append stores ev and returns it with chain fields assigned.
	Append(ctx context.Context, ev domain.Event) (domain.Event, error)
	// Query lists events with Seq > from, optionally filtered by kind.
	Query(ctx context.Context, from int64, kind string) ([]domain.Event, error)
	// Walk streams events with Seq > from in ascending Seq order, calling
	// fn for each event. fn returning an error stops the walk.
	Walk(ctx context.Context, from int64, kind string, fn func(domain.Event) error) error
	// Head returns the highest-seq event, or false when the journal is empty.
	Head(ctx context.Context) (domain.Event, bool, error)
	// Verify checks the hash chain integrity.
	Verify(ctx context.Context) (domain.VerifyResult, error)
}
