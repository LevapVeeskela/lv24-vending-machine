package marketdatabinance

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
)

// limitBase is the fixed epoch for the pagination-limit test.
var limitBase = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

// limitRow renders one closed 1m kline row.
func limitRow(open time.Time) []any {
	return []any{
		open.UnixMilli(), "1.00", "1.10", "0.90", "1.05", "10.0",
		open.Add(59*time.Second + 999*time.Millisecond).UnixMilli(),
		"0.0", 0, "0.0", "0.0", "0",
	}
}

// TestCandlesPaginationLimitIsAnError checks that a range larger than the page
// cap fails with the collected range instead of silently returning a partial
// slice.
func TestCandlesPaginationLimitIsAnError(t *testing.T) {
	const pageSize = maxPerRequest
	indexAt := func(ms int64) int {
		if ms <= limitBase.UnixMilli() {
			return 0
		}
		step := int64(time.Minute / time.Millisecond)
		return int((ms - limitBase.UnixMilli() + step - 1) / step)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil || limit <= 0 {
			t.Errorf("limit = %q, want a positive integer", q.Get("limit"))
			return
		}
		first := 0
		if raw := q.Get("startTime"); raw != "" {
			ms, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				t.Errorf("startTime = %q: %v", raw, err)
				return
			}
			first = indexAt(ms)
		}
		rows := make([][]any, 0, limit)
		for i := 0; i < limit; i++ {
			rows = append(rows, limitRow(limitBase.Add(time.Duration(first+i)*time.Minute)))
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(rows); err != nil {
			t.Errorf("encode klines page: %v", err)
		}
	}))
	defer srv.Close()

	now := limitBase.Add(48 * time.Hour)
	p, err := New(Config{BaseURL: srv.URL}, ports.Deps{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.maxPages = 2
	_, err = p.Candles(t.Context(), domain.CandleRequest{
		Symbol:   "BTCUSDT",
		Interval: "1m",
		From:     limitBase,
		Limit:    5000,
	})
	if err == nil {
		t.Fatal("Candles = nil error, want the pagination-limit error")
	}
	if got := domain.CodeOf(err); got != domain.CodeInvalidRequest {
		t.Fatalf("code = %q, want invalid_request (err %v)", got, err)
	}
	if !strings.Contains(err.Error(), "range too large") {
		t.Errorf("error = %q, want it to mention the range", err)
	}
	// Two full pages of 1000 rows cover minutes 0..1999.
	first := limitBase.UTC().Format(time.RFC3339)
	last := limitBase.Add(1999 * time.Minute).UTC().Format(time.RFC3339)
	if !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), last) {
		t.Errorf("error = %q, want the collected range %s..%s", err, first, last)
	}
	if !strings.Contains(err.Error(), "2000 of 5000") {
		t.Errorf("error = %q, want the collected and requested counts", err)
	}
}
