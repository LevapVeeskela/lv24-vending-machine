package marketdatabinance_test

import (
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
	marketdatabinance "github.com/LevapVeeskela/lv24-vending-machine/providers/marketdata-binance"
)

// TestDescriptor checks the frozen provider identity.
func TestDescriptor(t *testing.T) {
	p, err := marketdatabinance.New(marketdatabinance.Config{}, ports.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := p.Descriptor()
	if d.ID != "marketdata-binance" {
		t.Errorf("ID = %q, want %q", d.ID, "marketdata-binance")
	}
	if d.Kind != ports.KindMarketData {
		t.Errorf("Kind = %q, want %q", d.Kind, ports.KindMarketData)
	}
	if d.Version != "0.1.0-m0" {
		t.Errorf("Version = %q, want 0.1.0-m0", d.Version)
	}
	if len(d.Capabilities) != 1 || d.Capabilities[0] != "klines" {
		t.Errorf("Capabilities = %v, want [klines]", d.Capabilities)
	}
	if len(d.ConfigFields) != 1 || d.ConfigFields[0].Name != "baseUrl" {
		t.Errorf("ConfigFields = %+v, want [baseUrl]", d.ConfigFields)
	}
	if len(d.Degradations) != 0 {
		t.Errorf("Degradations = %v, want empty", d.Degradations)
	}
}

// TestNewRejectsBadBaseURL checks constructor validation without network.
func TestNewRejectsBadBaseURL(t *testing.T) {
	if _, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: "://bad"}, ports.Deps{}); err == nil {
		t.Fatal("New = nil error, want invalid_request")
	} else if domain.CodeOf(err) != domain.CodeInvalidRequest {
		t.Fatalf("code = %q, want invalid_request", domain.CodeOf(err))
	}
}

// TestCandlesRejectsEmptySymbol checks request validation without network.
func TestCandlesRejectsEmptySymbol(t *testing.T) {
	p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: "http://127.0.0.1:1"}, ports.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Candles(t.Context(), domain.CandleRequest{Interval: "1m"}); domain.CodeOf(err) != domain.CodeInvalidRequest {
		t.Fatalf("code = %q, want invalid_request", domain.CodeOf(err))
	}
}
