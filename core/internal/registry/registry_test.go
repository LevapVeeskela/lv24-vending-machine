package registry

import (
	"context"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

type fakeDecision struct{ id string }

func (f *fakeDecision) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: f.id, Kind: ports.KindDecision}
}

func (f *fakeDecision) Decide(context.Context, domain.DecisionRequest) (domain.DecisionResponse, error) {
	return domain.DecisionResponse{}, nil
}

type fakeCandle struct{ id string }

func (f *fakeCandle) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: f.id, Kind: ports.KindMarketData}
}

func (f *fakeCandle) Candles(context.Context, domain.CandleRequest) ([]domain.Candle, error) {
	return nil, nil
}

var (
	_ ports.Decision       = (*fakeDecision)(nil)
	_ ports.CandleProvider = (*fakeCandle)(nil)
	_                      = time.Now
)

func TestRegisterLookup(t *testing.T) {
	r := New()
	if err := r.RegisterDecision(&fakeDecision{id: "decision-drex"}); err != nil {
		t.Fatalf("RegisterDecision() = %v", err)
	}
	if err := r.RegisterCandle(&fakeCandle{id: "marketdata-binance"}); err != nil {
		t.Fatalf("RegisterCandle() = %v", err)
	}
	if _, err := r.Decision("decision-drex"); err != nil {
		t.Fatalf("Decision() = %v", err)
	}
	if _, err := r.Candle("marketdata-binance"); err != nil {
		t.Fatalf("Candle() = %v", err)
	}
	if _, err := r.Decision("nope"); err == nil {
		t.Fatalf("Decision(nope) = nil")
	}
	if _, err := r.Candle("nope"); err == nil {
		t.Fatalf("Candle(nope) = nil")
	}
	if err := r.RegisterDecision(&fakeDecision{id: "decision-drex"}); err == nil {
		t.Fatalf("duplicate RegisterDecision = nil")
	}
	if err := r.RegisterCandle(&fakeCandle{id: "marketdata-binance"}); err == nil {
		t.Fatalf("duplicate RegisterCandle = nil")
	}
}

func TestDescriptorsSorted(t *testing.T) {
	r := New()
	_ = r.RegisterDecision(&fakeDecision{id: "z-decision"})
	_ = r.RegisterDecision(&fakeDecision{id: "a-decision"})
	_ = r.RegisterCandle(&fakeCandle{id: "m-candles"})
	got := r.Descriptors()
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID > got[i].ID {
			t.Fatalf("not sorted: %v", got)
		}
	}
}
