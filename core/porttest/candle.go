// Package porttest provides conformance suites for provider implementations.
package porttest

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// CandleFactory builds a CandleProvider bound to baseURL for conformance tests.
type CandleFactory func(t *testing.T, baseURL string, deps ports.Deps) ports.CandleProvider

// candleBase is the fixed epoch for conformance klines (2024-01-01T00:00:00Z).
var candleBase = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

// candleOpenMs returns the open time in milliseconds for candle index i
// on a 1-minute grid.
func candleOpenMs(i int) int64 {
	return candleBase.Add(time.Duration(i) * time.Minute).UnixMilli()
}

// candleRow renders one Binance-style kline row with string prices.
func candleRow(openMs int64, open, high, low, close, volume string, closeMs int64) []any {
	return []any{openMs, open, high, low, close, volume, closeMs, "0.0", 0, "0.0", "0.0", "0"}
}

// candleWritePage encodes rows as a JSON klines page.
func candleWritePage(t *testing.T, w http.ResponseWriter, rows [][]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(rows); err != nil {
		t.Errorf("encode klines page: %v", err)
	}
}

// candleCheckParams validates the klines query parameters of r.
// Handlers run on the server goroutine, so only t.Errorf is used here.
func candleCheckParams(t *testing.T, r *http.Request, wantSymbol, wantInterval string) {
	t.Helper()
	if r.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", r.Method)
	}
	if r.URL.Path != "/api/v3/klines" {
		t.Errorf("path = %s, want /api/v3/klines", r.URL.Path)
	}
	q := r.URL.Query()
	if got := q.Get("symbol"); got != wantSymbol {
		t.Errorf("symbol = %q, want %q", got, wantSymbol)
	}
	if got := q.Get("interval"); got != wantInterval {
		t.Errorf("interval = %q, want %q", got, wantInterval)
	}
	lim, err := strconv.Atoi(q.Get("limit"))
	if err != nil || lim <= 0 || lim > 1000 {
		t.Errorf("limit = %q, want an integer in 1..1000", q.Get("limit"))
	}
}

// CandleConformance checks a ports.CandleProvider against httptest servers:
// ordering, dedup, pagination, rate limiting, To filtering and Decimal prices.
func CandleConformance(t *testing.T, newProvider CandleFactory) {
	t.Run("order", func(t *testing.T) {
		var requests int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			candleCheckParams(t, r, "BTCUSDT", "1m")
			candleWritePage(t, w, [][]any{
				candleRow(candleOpenMs(2), "3.00", "3.10", "2.90", "3.05", "30.0", candleOpenMs(2)+59999),
				candleRow(candleOpenMs(1), "2.00", "2.10", "1.90", "2.05", "20.0", candleOpenMs(1)+59999),
				candleRow(candleOpenMs(0), "1.00", "1.10", "0.90", "1.05", "10.0", candleOpenMs(0)+59999),
			})
		}))
		defer srv.Close()
		p := newProvider(t, srv.URL, ports.Deps{})
		got, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 3})
		if err != nil {
			t.Fatalf("Candles: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
		for i, want := range []int64{candleOpenMs(0), candleOpenMs(1), candleOpenMs(2)} {
			if ms := got[i].OpenTime.UnixMilli(); ms != want {
				t.Fatalf("candles[%d].OpenTime = %d, want %d (ascending order)", i, ms, want)
			}
		}
		if got[0].Open.String() != "1.00" || got[2].Close.String() != "3.05" {
			t.Fatalf("prices not preserved: %+v", got)
		}
	})

	t.Run("dedup", func(t *testing.T) {
		var requests int
		var secondStart string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			candleCheckParams(t, r, "BTCUSDT", "1m")
			if r.URL.Query().Get("startTime") == "" {
				candleWritePage(t, w, [][]any{
					candleRow(candleOpenMs(0), "1.00", "1.10", "0.90", "1.05", "10.0", candleOpenMs(0)+59999),
					candleRow(candleOpenMs(1), "2.00", "2.10", "1.90", "2.05", "20.0", candleOpenMs(1)+59999),
					candleRow(candleOpenMs(2), "3.00", "3.10", "2.90", "3.05", "30.0", candleOpenMs(2)+59999),
				})
				return
			}
			secondStart = r.URL.Query().Get("startTime")
			candleWritePage(t, w, [][]any{
				candleRow(candleOpenMs(2), "3.00", "3.10", "2.90", "3.05", "30.0", candleOpenMs(2)+59999),
				candleRow(candleOpenMs(3), "4.00", "4.10", "3.90", "4.05", "40.0", candleOpenMs(3)+59999),
			})
		}))
		defer srv.Close()
		p := newProvider(t, srv.URL, ports.Deps{})
		got, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 4})
		if err != nil {
			t.Fatalf("Candles: %v", err)
		}
		if requests < 2 {
			t.Fatalf("requests = %d, want at least 2", requests)
		}
		if want := strconv.FormatInt(candleOpenMs(2)+1, 10); secondStart != want {
			t.Errorf("second startTime = %q, want %q", secondStart, want)
		}
		if len(got) != 4 {
			t.Fatalf("len = %d, want 4 unique candles", len(got))
		}
		for i, want := range []int64{candleOpenMs(0), candleOpenMs(1), candleOpenMs(2), candleOpenMs(3)} {
			if ms := got[i].OpenTime.UnixMilli(); ms != want {
				t.Fatalf("candles[%d].OpenTime = %d, want %d", i, ms, want)
			}
		}
	})

	t.Run("pagination", func(t *testing.T) {
		var requests int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			candleCheckParams(t, r, "BTCUSDT", "1m")
			if r.URL.Query().Get("startTime") == "" {
				candleWritePage(t, w, [][]any{
					candleRow(candleOpenMs(0), "1.00", "1.10", "0.90", "1.05", "10.0", candleOpenMs(0)+59999),
					candleRow(candleOpenMs(1), "2.00", "2.10", "1.90", "2.05", "20.0", candleOpenMs(1)+59999),
					candleRow(candleOpenMs(2), "3.00", "3.10", "2.90", "3.05", "30.0", candleOpenMs(2)+59999),
				})
				return
			}
			if want := strconv.FormatInt(candleOpenMs(2)+1, 10); r.URL.Query().Get("startTime") != want {
				t.Errorf("second startTime = %q, want %q", r.URL.Query().Get("startTime"), want)
			}
			candleWritePage(t, w, [][]any{
				candleRow(candleOpenMs(3), "4.00", "4.10", "3.90", "4.05", "40.0", candleOpenMs(3)+59999),
				candleRow(candleOpenMs(4), "5.00", "5.10", "4.90", "5.05", "50.0", candleOpenMs(4)+59999),
			})
		}))
		defer srv.Close()
		p := newProvider(t, srv.URL, ports.Deps{})
		got, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 5})
		if err != nil {
			t.Fatalf("Candles: %v", err)
		}
		if requests < 2 {
			t.Fatalf("requests = %d, want at least 2", requests)
		}
		if len(got) != 5 {
			t.Fatalf("len = %d, want 5", len(got))
		}
		for i := 0; i < 5; i++ {
			if ms := got[i].OpenTime.UnixMilli(); ms != candleOpenMs(i) {
				t.Fatalf("candles[%d].OpenTime = %d, want %d", i, ms, candleOpenMs(i))
			}
		}
	})

	t.Run("rate_limit", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			candleCheckParams(t, r, "BTCUSDT", "1m")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":-1003,"msg":"Too much request weight used."}`))
		}))
		defer srv.Close()
		p := newProvider(t, srv.URL, ports.Deps{})
		_, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 5})
		if err == nil {
			t.Fatal("Candles = nil error, want rate_limit")
		}
		if domain.CodeOf(err) != domain.CodeRateLimit {
			t.Fatalf("code = %q, want %q", domain.CodeOf(err), domain.CodeRateLimit)
		}
		var de *domain.Error
		if !errors.As(err, &de) {
			t.Fatalf("error type = %T, want *domain.Error", err)
		}
		if !de.Retryable {
			t.Fatal("Retryable = false, want true")
		}
	})

	t.Run("to_filter", func(t *testing.T) {
		to := candleBase.Add(time.Minute)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			candleCheckParams(t, r, "BTCUSDT", "1m")
			if got := r.URL.Query().Get("endTime"); got != strconv.FormatInt(to.UnixMilli(), 10) {
				t.Errorf("endTime = %q, want To in ms", got)
			}
			if r.URL.Query().Get("startTime") != "" {
				candleWritePage(t, w, [][]any{})
				return
			}
			// The server ignores endTime on purpose: the provider must
			// filter client-side.
			candleWritePage(t, w, [][]any{
				candleRow(candleOpenMs(0), "1.00", "1.10", "0.90", "1.05", "10.0", candleOpenMs(0)+59999),
				candleRow(candleOpenMs(1), "2.00", "2.10", "1.90", "2.05", "20.0", candleOpenMs(1)+59999),
				candleRow(candleOpenMs(2), "3.00", "3.10", "2.90", "3.05", "30.0", candleOpenMs(2)+59999),
				candleRow(candleOpenMs(3), "4.00", "4.10", "3.90", "4.05", "40.0", candleOpenMs(3)+59999),
			})
		}))
		defer srv.Close()
		p := newProvider(t, srv.URL, ports.Deps{})
		got, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 10, To: to})
		if err != nil {
			t.Fatalf("Candles: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (candles past To dropped)", len(got))
		}
		for _, c := range got {
			if c.OpenTime.After(to) {
				t.Fatalf("candle %v past To %v", c.OpenTime, to)
			}
		}
	})

	t.Run("decimal_prices", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			candleCheckParams(t, r, "BTCUSDT", "1m")
			candleWritePage(t, w, [][]any{
				candleRow(candleOpenMs(0), "43210.12", "43250.00", "43100.55", "43200.01", "12.345", candleOpenMs(0)+59999),
			})
		}))
		defer srv.Close()
		p := newProvider(t, srv.URL, ports.Deps{})
		got, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 1})
		if err != nil {
			t.Fatalf("Candles: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		c := got[0]
		for field, gw := range map[string][2]string{
			"open":   {c.Open.String(), "43210.12"},
			"high":   {c.High.String(), "43250.00"},
			"low":    {c.Low.String(), "43100.55"},
			"close":  {c.Close.String(), "43200.01"},
			"volume": {c.Volume.String(), "12.345"},
		} {
			if gw[0] != gw[1] {
				t.Errorf("%s = %q, want %q", field, gw[0], gw[1])
			}
		}
		if c.Open.Cmp(domain.MustParseDecimal("43210.12")) != 0 {
			t.Errorf("open Cmp != 0")
		}
		if c.Symbol != "BTCUSDT" || c.Interval != "1m" {
			t.Errorf("identity = %s/%s, want BTCUSDT/1m", c.Symbol, c.Interval)
		}
		if !c.OpenTime.Equal(candleBase) {
			t.Errorf("OpenTime = %v, want %v", c.OpenTime, candleBase)
		}
	})
}
