package marketdatabinance_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
// echoed back as BTCUSDT, while the interval is left untouched. The futures
// spelling BTC/USDT:USDT resolves to the same spot market.
func TestCandlesNormalizesSymbol(t *testing.T) {
	for _, in := range []string{"BTCUSDT", "BTC/USDT", "BTC-USDT", "btcusdt", " btc/usdt ", "BTC/USDT:USDT"} {
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

// TestCandlesStopsAfterShortPage: a page with fewer candles than requested is
// the end of the data; the provider must not ask for another page.
func TestCandlesStopsAfterShortPage(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeKlinesPage(t, w, [][]any{
			klinesRow(klinesBase, "1.00", "1.10", "0.90", "1.05", "10.0"),
			klinesRow(klinesBase.Add(time.Minute), "2.00", "2.10", "1.90", "2.05", "20.0"),
			klinesRow(klinesBase.Add(2*time.Minute), "3.00", "3.10", "2.90", "3.05", "30.0"),
		})
	}))
	defer srv.Close()
	p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, ports.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 4})
	if err != nil {
		t.Fatalf("Candles: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want exactly 1 HTTP request", requests)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want the 3 returned candles", len(got))
	}
	if !got[0].OpenTime.Equal(klinesBase) || !got[2].OpenTime.Equal(klinesBase.Add(2*time.Minute)) {
		t.Fatalf("candles = %v..%v, want the returned page", got[0].OpenTime, got[2].OpenTime)
	}
}

// TestCandlesPaginatesWhenPageIsFull: a limit above the per-request cap keeps
// the existing multi-page scan while the page stays full.
func TestCandlesPaginatesWhenPageIsFull(t *testing.T) {
	const target = 1005
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil || limit <= 0 {
			t.Errorf("limit = %q, want a positive integer", r.URL.Query().Get("limit"))
			return
		}
		first := 0
		if r.URL.Query().Get("startTime") != "" {
			first = 1000
		}
		rows := make([][]any, 0, limit)
		for i := 0; i < limit && first+i < target; i++ {
			open := klinesBase.Add(time.Duration(first+i) * time.Minute)
			rows = append(rows, klinesRow(open, "1.00", "1.10", "0.90", "1.05", "10.0"))
		}
		writeKlinesPage(t, w, rows)
	}))
	defer srv.Close()
	p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, ports.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: target})
	if err != nil {
		t.Fatalf("Candles: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2 (full page, then the short last page)", requests)
	}
	if len(got) != target {
		t.Fatalf("len = %d, want %d", len(got), target)
	}
	for i, want := 0, klinesBase; i < target; i, want = i+1, want.Add(time.Minute) {
		if !got[i].OpenTime.Equal(want) {
			t.Fatalf("candles[%d].OpenTime = %v, want %v", i, got[i].OpenTime, want)
		}
	}
}
