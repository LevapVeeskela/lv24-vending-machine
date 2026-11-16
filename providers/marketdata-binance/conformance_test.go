package marketdatabinance_test

import (
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
	"github.com/LevapVeeskela/lv24-vending-machine/core/porttest"
	marketdatabinance "github.com/LevapVeeskela/lv24-vending-machine/providers/marketdata-binance"
)

// TestCandleConformance runs the shared candle conformance suite against a
// local httptest server. No network access is used.
func TestCandleConformance(t *testing.T) {
	porttest.CandleConformance(t, func(t *testing.T, baseURL string, deps ports.Deps) ports.CandleProvider {
		t.Helper()
		p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: baseURL}, deps)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p
	})
}
