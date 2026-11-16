// Package registry holds the M0 provider registry.
//
// The registry is keyed by provider ID; the core never branches on
// provider identity outside this composition helper.
package registry

import (
	"fmt"
	"sort"

	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// NotifierProvider is the registry-facing view of a notification channel:
// the ports.Notifier capability plus its descriptor.
type NotifierProvider interface {
	ports.Notifier
	Descriptor() ports.Descriptor
}

// Registry maps provider IDs to provider implementations.
type Registry struct {
	decisions map[string]ports.Decision
	candles   map[string]ports.CandleProvider
	notifiers map[string]NotifierProvider
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		decisions: make(map[string]ports.Decision),
		candles:   make(map[string]ports.CandleProvider),
		notifiers: make(map[string]NotifierProvider),
	}
}

// RegisterDecision adds a decision provider. A duplicate ID is an error.
func (r *Registry) RegisterDecision(p ports.Decision) error {
	if p == nil {
		return fmt.Errorf("registry: nil decision provider")
	}
	id := p.Descriptor().ID
	if id == "" {
		return fmt.Errorf("registry: decision provider has empty ID")
	}
	if _, ok := r.decisions[id]; ok {
		return fmt.Errorf("registry: duplicate decision provider %q", id)
	}
	r.decisions[id] = p
	return nil
}

// RegisterCandle adds a market data provider. A duplicate ID is an error.
func (r *Registry) RegisterCandle(p ports.CandleProvider) error {
	if p == nil {
		return fmt.Errorf("registry: nil candle provider")
	}
	id := p.Descriptor().ID
	if id == "" {
		return fmt.Errorf("registry: candle provider has empty ID")
	}
	if _, ok := r.candles[id]; ok {
		return fmt.Errorf("registry: duplicate candle provider %q", id)
	}
	r.candles[id] = p
	return nil
}

// RegisterNotifier adds a notification channel. A duplicate ID is an error.
func (r *Registry) RegisterNotifier(p NotifierProvider) error {
	if p == nil {
		return fmt.Errorf("registry: nil notifier provider")
	}
	id := p.Descriptor().ID
	if id == "" {
		return fmt.Errorf("registry: notifier provider has empty ID")
	}
	if _, ok := r.notifiers[id]; ok {
		return fmt.Errorf("registry: duplicate notifier provider %q", id)
	}
	r.notifiers[id] = p
	return nil
}

// Decision returns the decision provider with the given ID.
func (r *Registry) Decision(id string) (ports.Decision, error) {
	p, ok := r.decisions[id]
	if !ok {
		return nil, fmt.Errorf("registry: unknown decision provider %q", id)
	}
	return p, nil
}

// Candle returns the market data provider with the given ID.
func (r *Registry) Candle(id string) (ports.CandleProvider, error) {
	p, ok := r.candles[id]
	if !ok {
		return nil, fmt.Errorf("registry: unknown candle provider %q", id)
	}
	return p, nil
}

// Notifier returns the notification channel with the given ID.
func (r *Registry) Notifier(id string) (ports.Notifier, error) {
	p, ok := r.notifiers[id]
	if !ok {
		return nil, fmt.Errorf("registry: unknown notifier provider %q", id)
	}
	return p, nil
}

// Descriptors returns all registered provider descriptors sorted by ID.
func (r *Registry) Descriptors() []ports.Descriptor {
	var out []ports.Descriptor
	for _, p := range r.decisions {
		out = append(out, p.Descriptor())
	}
	for _, p := range r.candles {
		out = append(out, p.Descriptor())
	}
	for _, p := range r.notifiers {
		out = append(out, p.Descriptor())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
