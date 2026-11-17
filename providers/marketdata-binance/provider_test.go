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

// TestCandlesNormalizesSymbol checks the accepted spot spellings are queried
// and echoed back as BTCUSDT.
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

// TestCandlesRejectsFuturesSymbol checks that a futures spelling is an explicit
// invalid_request instead of silently becoming the spot market.
func TestCandlesRejectsFuturesSymbol(t *testing.T) {
	for _, in := range []string{"BTC/USDT:USDT", "BTCUSDT:USDT", " BTC/USDT:USDT "} {
		t.Run(in, func(t *testing.T) {
			var hits int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				klinesOneRow(t, w)
			}))
			defer srv.Close()
			p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, ports.Deps{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = p.Candles(t.Context(), domain.CandleRequest{Symbol: in, Interval: "1m", Limit: 1})
			if err == nil {
				t.Fatal("Candles = nil error, want invalid_request")
			}
			if got := domain.CodeOf(err); got != domain.CodeInvalidRequest {
				t.Fatalf("code = %q, want invalid_request", got)
			}
			if !strings.Contains(err.Error(), "futures not supported") {
				t.Errorf("error = %q, want it to mention futures not supported", err)
			}
			if hits != 0 {
				t.Errorf("server hits = %d, want 0", hits)
			}
		})
	}
}

// TestCandlesValidatesInterval checks the case-sensitive interval enum: "1H" is
// rejected before any request while "1h" and "1M" pass verbatim.
func TestCandlesValidatesInterval(t *testing.T) {
	valid := []string{"1m", "1h", "4h", "1d", "1w", "1M"}
	for _, interval := range valid {
		t.Run("valid/"+interval, func(t *testing.T) {
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
			got, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: interval, Limit: 1})
			if err != nil {
				t.Fatalf("Candles: %v", err)
			}
			if gotInterval != interval {
				t.Errorf("query interval = %q, want %q verbatim", gotInterval, interval)
			}
			if len(got) != 1 || got[0].Interval != interval {
				t.Errorf("candle interval = %+v, want %q", got, interval)
			}
		})
	}

	invalid := []string{"1H", "1D", "60m", "5", "1 m"}
	for _, interval := range invalid {
		t.Run("invalid/"+interval, func(t *testing.T) {
			var hits int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				klinesOneRow(t, w)
			}))
			defer srv.Close()
			p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, ports.Deps{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: interval, Limit: 1})
			if got := domain.CodeOf(err); got != domain.CodeInvalidRequest {
				t.Fatalf("code = %q, want invalid_request (err %v)", got, err)
			}
			if hits != 0 {
				t.Errorf("server hits = %d, want 0", hits)
			}
		})
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
		if requests > 1 {
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
	got, err := p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: target, From: klinesBase})
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

// TestCandlesUnboundedLimitPaginatesBackward checks the unbounded case on a
// server that behaves like Binance: the first page is the newest one and a
// forward follow-up is empty, so the provider must continue backwards instead
// of returning fewer candles than requested.
func TestCandlesUnboundedLimitPaginatesBackward(t *testing.T) {
	const (
		total  = 1500
		target = 1200
	)
	openAt := func(i int) time.Time {
		return klinesBase.Add(time.Duration(i) * time.Minute)
	}
	stepMs := int64(time.Minute / time.Millisecond)
	floorIndex := func(ms int64) int {
		if ms < klinesBase.UnixMilli() {
			return -1
		}
		return int((ms - klinesBase.UnixMilli()) / stepMs)
	}
	ceilIndex := func(ms int64) int {
		if ms <= klinesBase.UnixMilli() {
			return 0
		}
		return int((ms - klinesBase.UnixMilli() + stepMs - 1) / stepMs)
	}
	var requests int
	var sawEndTime bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		q := r.URL.Query()
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil || limit <= 0 {
			t.Errorf("limit = %q, want a positive integer", q.Get("limit"))
			return
		}
		end := total - 1
		if raw := q.Get("endTime"); raw != "" {
			sawEndTime = true
			ms, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				t.Errorf("endTime = %q: %v", raw, err)
				return
			}
			end = floorIndex(ms)
		}
		start := 0
		if raw := q.Get("startTime"); raw != "" {
			ms, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				t.Errorf("startTime = %q: %v", raw, err)
				return
			}
			start = ceilIndex(ms)
		} else {
			start = end - limit + 1
			if start < 0 {
				start = 0
			}
		}
		rows := make([][]any, 0, limit)
		for i := start; i <= end && i < total && len(rows) < limit; i++ {
			rows = append(rows, klinesRow(openAt(i), "1.00", "1.10", "0.90", "1.05", "10.0"))
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
	if !sawEndTime {
		t.Error("no request carried endTime: the scan did not fall back backwards")
	}
	if len(got) != target {
		t.Fatalf("len = %d, want %d (no page may be lost)", len(got), target)
	}
	if !got[0].OpenTime.Equal(openAt(total - target)) {
		t.Errorf("first = %v, want %v", got[0].OpenTime, openAt(total-target))
	}
	if !got[len(got)-1].OpenTime.Equal(openAt(total - 1)) {
		t.Errorf("last = %v, want the newest candle %v", got[len(got)-1].OpenTime, openAt(total-1))
	}
}

// TestCandlesToWithoutFromPaginatesBackward guards the audit finding that
// To without From plus Limit > 1000 silently dropped the older pages: Binance
// answers with the newest candles before endTime, so the provider must walk
// backward instead of paging forward past them.
func TestCandlesToWithoutFromPaginatesBackward(t *testing.T) {
	const (
		total  = 1500
		target = 1200
	)
	openAt := func(i int) time.Time {
		return klinesBase.Add(time.Duration(i) * time.Minute)
	}
	indexAt := func(ms int64) int {
		if ms < klinesBase.UnixMilli() {
			return -1
		}
		return int((ms - klinesBase.UnixMilli()) / int64(time.Minute/time.Millisecond))
	}
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		q := r.URL.Query()
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil || limit <= 0 {
			t.Errorf("limit = %q, want a positive integer", q.Get("limit"))
			return
		}
		if got := q.Get("startTime"); got != "" {
			t.Errorf("startTime = %q, want backward pagination without From", got)
		}
		end := total - 1
		if raw := q.Get("endTime"); raw != "" {
			ms, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				t.Errorf("endTime = %q: %v", raw, err)
				return
			}
			end = indexAt(ms)
		}
		if end >= total {
			end = total - 1
		}
		start := end - limit + 1
		if start < 0 {
			start = 0
		}
		rows := make([][]any, 0, limit)
		for i := start; i <= end && len(rows) < limit; i++ {
			rows = append(rows, klinesRow(openAt(i), "1.00", "1.10", "0.90", "1.05", "10.0"))
		}
		writeKlinesPage(t, w, rows)
	}))
	defer srv.Close()
	p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, ports.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	to := openAt(total - 1)
	got, err := p.Candles(t.Context(), domain.CandleRequest{
		Symbol: "BTCUSDT", Interval: "1m", Limit: target, To: to,
	})
	if err != nil {
		t.Fatalf("Candles: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2 (backward pages)", requests)
	}
	if len(got) != target {
		t.Fatalf("len = %d, want %d (no page may be lost)", len(got), target)
	}
	if !got[0].OpenTime.Equal(openAt(total - target)) {
		t.Errorf("first = %v, want %v", got[0].OpenTime, openAt(total-target))
	}
	if !got[len(got)-1].OpenTime.Equal(to) {
		t.Errorf("last = %v, want To %v", got[len(got)-1].OpenTime, to)
	}
}

// TestCandlesBacksOffAfterThrottle checks that a 429 with Retry-After starts a
// cooldown: further calls fail fast with the remaining delay and perform no
// network I/O until the window passes.
func TestCandlesBacksOffAfterThrottle(t *testing.T) {
	now := klinesBase.Add(time.Hour)
	throttled := true
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if throttled {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":-1003,"msg":"Too much request weight used."}`))
			return
		}
		klinesOneRow(t, w)
	}))
	defer srv.Close()
	deps := ports.Deps{Clock: func() time.Time { return now }}
	p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 1}
	_, err = p.Candles(t.Context(), req)
	if got := domain.CodeOf(err); got != domain.CodeRateLimit {
		t.Fatalf("code = %q, want rate_limit (err %v)", got, err)
	}
	if got := domain.RetryAfterOf(err); got != 5*time.Second {
		t.Fatalf("RetryAfter = %s, want 5s", got)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}

	_, err = p.Candles(t.Context(), req)
	if got := domain.CodeOf(err); got != domain.CodeRateLimit {
		t.Fatalf("code = %q, want rate_limit during cooldown (err %v)", got, err)
	}
	if got := domain.RetryAfterOf(err); got != 5*time.Second {
		t.Fatalf("RetryAfter = %s, want the remaining 5s", got)
	}
	if !domain.IsRetryable(err) {
		t.Error("IsRetryable = false, want true")
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1: cooldown must not hit the network", hits)
	}

	throttled = false
	now = now.Add(5 * time.Second)
	if _, err := p.Candles(t.Context(), req); err != nil {
		t.Fatalf("Candles after cooldown: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 after the cooldown", hits)
	}
}

// TestCandlesCapsRetryAfter checks that an absurd upstream Retry-After cannot
// park the provider longer than the one-hour cap.
func TestCandlesCapsRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":-1003,"msg":"Too much request weight used."}`))
	}))
	defer srv.Close()
	p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, ports.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Candles(t.Context(), domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 1})
	if got := domain.CodeOf(err); got != domain.CodeRateLimit {
		t.Fatalf("code = %q, want rate_limit (err %v)", got, err)
	}
	if got := domain.RetryAfterOf(err); got != time.Hour {
		t.Fatalf("RetryAfter = %s, want the 1h cap", got)
	}
	if !domain.IsRetryable(err) {
		t.Error("IsRetryable = false, want true")
	}
}

// TestCandlesIPBanCooldown checks that a 418 keeps its non-retryable ip_banned
// code and blocks further requests during the cooldown.
func TestCandlesIPBanCooldown(t *testing.T) {
	now := klinesBase.Add(time.Hour)
	banned := true
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if banned {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte(`{"code":-1004,"msg":"IP banned."}`))
			return
		}
		klinesOneRow(t, w)
	}))
	defer srv.Close()
	deps := ports.Deps{Clock: func() time.Time { return now }}
	p, err := marketdatabinance.New(marketdatabinance.Config{BaseURL: srv.URL}, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := domain.CandleRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 1}
	_, err = p.Candles(t.Context(), req)
	if got := domain.CodeOf(err); got != domain.CodeIPBanned {
		t.Fatalf("code = %q, want ip_banned (err %v)", got, err)
	}
	if domain.IsRetryable(err) {
		t.Error("IsRetryable = true, want false for an IP ban")
	}
	if got := domain.RetryAfterOf(err); got != 5*time.Second {
		t.Fatalf("RetryAfter = %s, want 5s", got)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}

	_, err = p.Candles(t.Context(), req)
	if got := domain.CodeOf(err); got != domain.CodeIPBanned {
		t.Fatalf("code = %q, want ip_banned during cooldown (err %v)", got, err)
	}
	if got := domain.RetryAfterOf(err); got <= 0 || got > 5*time.Second {
		t.Fatalf("RetryAfter = %s, want (0, 5s]", got)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1: cooldown must not hit the network", hits)
	}

	banned = false
	now = now.Add(5 * time.Second)
	if _, err := p.Candles(t.Context(), req); err != nil {
		t.Fatalf("Candles after cooldown: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 after the cooldown", hits)
	}
}
