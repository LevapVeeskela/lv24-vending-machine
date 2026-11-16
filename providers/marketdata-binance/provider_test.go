package marketdatabinance_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// klinesOneRow writes a single closed 1m kline page around klinesBase.
func klinesOneRow(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	row := []any{
		klinesBase.UnixMilli(), "1.00", "1.10", "0.90", "1.05", "10.0",
		klinesBase.Add(59*time.Second + 999*time.Millisecond).UnixMilli(),
		"0.0", 0, "0.0", "0.0", "0",
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode([][]any{row}); err != nil {
		t.Errorf("encode klines page: %v", err)
	}
}

// TestCandlesNormalizesSymbol checks the accepted spellings are queried and
// echoed back as BTCUSDT, while the interval is left untouched.
func TestCandlesNormalizesSymbol(t *testing.T) {
	for _, in := range []string{"BTCUSDT", "BTC/USDT", "BTC-USDT", "btcusdt", " btc/usdt "} {
		t.Run(in, func(t *testing.T) {
			var gotSymbol, gotInterval string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotSymbol = r.URL.Query().Get("symbol")
				gotInterval = r.URL.Query().Get("interval")
				klinesOneRow(t, w)
			}))
			defer srv.Close()
			p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, ports.Deps{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: in, Interval: "1m", Limit: 1})
			if err != nil {
				t.Fatalf("Candles: %v", err)
			}
			if gotSymbol != "BTCUSDT" {
				t.Errorf("query symbol = %q, want BTCUSDT", gotSymbol)
			}
			if gotInterval != "1m" {
				t.Errorf("query interval = %q, want 1m verbatim", gotInterval)
			}
			if len(got) != 1 || got[0].Symbol != "BTCUSDT" {
				t.Errorf("candle identity = %+v, want symbol BTCUSDT", got)
			}
		})
	}
}

// TestCandlesKeepsIntervalVerbatim checks that the interval is not normalized.
func TestCandlesKeepsIntervalVerbatim(t *testing.T) {
	var gotInterval string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInterval = r.URL.Query().Get("interval")
		klinesOneRow(t, w)
	}))
	defer srv.Close()
	p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, ports.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1H", Limit: 1})
	if err != nil {
		t.Fatalf("Candles: %v", err)
	}
	if gotInterval != "1H" {
		t.Errorf("query interval = %q, want 1H verbatim", gotInterval)
	}
	if len(got) != 1 || got[0].Interval != "1H" {
		t.Errorf("candle interval = %+v, want 1H", got)
	}
}

// TestCandlesBadRequest checks that HTTP 400 maps to a non-retryable
// invalid_request with a clear message.
func TestCandlesBadRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":-1121,"msg":"Invalid symbol."}`))
	}))
	defer srv.Close()
	p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, ports.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Candles(t.Context(), domain.CandleRequest{Symbol: "NOPEUSDT", Interval: "1m", Limit: 1})
	if err == nil {
		t.Fatal("Candles = nil error, want invalid_request")
	}
	if code := domain.CodeOf(err); code != domain.CodeInvalidRequest {
		t.Fatalf("code = %q, want %q", code, domain.CodeInvalidRequest)
	}
	de, ok := err.(*domain.Error)
	if !ok {
		t.Fatalf("type = %T, want *domain.Error", err)
	}
	if de.Retryable {
		t.Error("Retryable = true, want false")
	}
	if !strings.Contains(de.Message, "unknown symbol or interval") {
		t.Errorf("message = %q, want it to mention unknown symbol or interval", de.Message)
	}
}
