package marketdatabinance_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
	marketdatabinance "github.com/LevapVeeskela/lv24-vending-machine/providers/marketdata-binance"
)

// klinesBase is the fixed epoch for offline klines pages (2024-01-01T00:00:00Z).
var klinesBase = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

func klinesRow(open time.Time, openPx, high, low, closePx, volume string) []any {
	openMs := open.UnixMilli()
	closeMs := open.Add(59*time.Second + 999*time.Millisecond).UnixMilli()
	return []any{openMs, openPx, high, low, closePx, volume, closeMs, "0.0", 0, "0.0", "0.0", "0"}
}

func writeKlinesPage(t *testing.T, w http.ResponseWriter, rows [][]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(rows); err != nil {
		t.Errorf("encode klines page: %v", err)
	}
}

// TestUnclosedBarFiltered checks that a trailing unclosed bar is dropped and
// the limit compensation still returns N closed bars.
func TestUnclosedBarFiltered(t *testing.T) {
	now := klinesBase.Add(3 * time.Minute)
	deps := ports.Deps{Clock: func() time.Time { return now }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeKlinesPage(t, w, [][]any{
			klinesRow(klinesBase.Add(0*time.Minute), "1.00", "1.10", "0.90", "1.05", "10.0"),
			klinesRow(klinesBase.Add(1*time.Minute), "2.00", "2.10", "1.90", "2.05", "20.0"),
			klinesRow(klinesBase.Add(2*time.Minute), "3.00", "3.10", "2.90", "3.05", "30.0"),
			klinesRow(klinesBase.Add(3*time.Minute), "4.00", "4.10", "3.90", "4.05", "40.0"),
		})
	}))
	defer srv.Close()
	p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.Candles(context.Background(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 3})
	if err != nil {
		t.Fatalf("Candles: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 closed bars", len(got))
	}
	for _, c := range got {
		if !c.CloseTime.Before(now) {
			t.Fatalf("unclosed candle returned: %+v (now %v)", c, now)
		}
	}
	if ms := got[2].OpenTime.UnixMilli(); ms != klinesBase.Add(2*time.Minute).UnixMilli() {
		t.Fatalf("last OpenTime = %d, want bar 2", ms)
	}
}

// TestStatusMapping checks 451/418/429 codes, retryability and Retry-After.
func TestStatusMapping(t *testing.T) {
	fixedNow := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		status      int
		retryAfter  string
		code        domain.ErrorCode
		retryable   bool
		retryAfterW time.Duration
	}{
		{name: "bad_request", status: http.StatusBadRequest, code: domain.CodeInvalidRequest, retryable: false, retryAfterW: 0},
		{name: "region", status: 451, retryAfter: "7", code: domain.CodeRegionUnavailable, retryable: false, retryAfterW: 7 * time.Second},
		{name: "ban", status: http.StatusTeapot, retryAfter: "5", code: domain.CodeIPBanned, retryable: false, retryAfterW: 5 * time.Second},
		{name: "throttle", status: http.StatusTooManyRequests, retryAfter: "5", code: domain.CodeRateLimit, retryable: true, retryAfterW: 5 * time.Second},
		{name: "throttle_date", status: http.StatusTooManyRequests, retryAfter: fixedNow.Add(30 * time.Second).UTC().Format(http.TimeFormat), code: domain.CodeRateLimit, retryable: true, retryAfterW: 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			deps := ports.Deps{Clock: func() time.Time { return fixedNow }}
			p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, deps)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = p.Candles(context.Background(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 1})
			if err == nil {
				t.Fatal("Candles = nil error")
			}
			if domain.CodeOf(err) != tc.code {
				t.Fatalf("code = %q, want %q", domain.CodeOf(err), tc.code)
			}
			var de *domain.Error
			if !errors.As(err, &de) {
				t.Fatalf("type = %T, want *domain.Error", err)
			}
			if de.Retryable != tc.retryable {
				t.Fatalf("Retryable = %v, want %v", de.Retryable, tc.retryable)
			}
			if got := domain.RetryAfterOf(err); got != tc.retryAfterW {
				t.Fatalf("RetryAfter = %v, want %v", got, tc.retryAfterW)
			}
		})
	}
}
