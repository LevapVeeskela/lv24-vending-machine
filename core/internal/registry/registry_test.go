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

type fakeNotifier struct{ id string }

func (f *fakeNotifier) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: f.id, Kind: "notifier"}
}

func (f *fakeNotifier) Send(context.Context, ports.Notification) error { return nil }

var (
	_ ports.Decision       = (*fakeDecision)(nil)
	_ ports.CandleProvider = (*fakeCandle)(nil)
	_ NotifierProvider     = (*fakeNotifier)(nil)
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

func TestRegisterNotifier(t *testing.T) {
	r := New()
	if err := r.RegisterNotifier(&fakeNotifier{id: "notify-telegram"}); err != nil {
		t.Fatalf("RegisterNotifier() = %v", err)
	}
	p, err := r.Notifier("notify-telegram")
	if err != nil {
		t.Fatalf("Notifier() = %v", err)
	}
	if p == nil {
		t.Fatal("Notifier() returned nil")
	}
	if _, err := r.Notifier("nope"); err == nil {
		t.Fatal("Notifier(nope) = nil error")
	}
	if err := r.RegisterNotifier(&fakeNotifier{id: "notify-telegram"}); err == nil {
		t.Fatal("duplicate RegisterNotifier = nil")
	}
	if err := r.RegisterNotifier(&fakeNotifier{id: ""}); err == nil {
		t.Fatal("empty ID RegisterNotifier = nil")
	}
	if err := r.RegisterNotifier(nil); err == nil {
		t.Fatal("nil RegisterNotifier = nil")
	}
}

func TestDescriptorsIncludeNotifiers(t *testing.T) {
	r := New()
	_ = r.RegisterNotifier(&fakeNotifier{id: "notify-telegram"})
	got := r.Descriptors()
	if len(got) != 1 || got[0].ID != "notify-telegram" {
		t.Fatalf("Descriptors() = %v, want notify-telegram", got)
	}
}
