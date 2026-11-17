package decisiondrex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// testSecrets is a static secret store for provider tests.
type testSecrets map[string]string

func (m testSecrets) Get(_ context.Context, ref string) (string, error) {
	if v, ok := m[ref]; ok && v != "" {
		return v, nil
	}
	return "", context.DeadlineExceeded
}

// testClock is a concurrency-safe manual clock for breaker tests.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// noulRequest is the smallest valid request: one noul question.
func noulRequest(model string) domain.DecisionRequest {
	return domain.DecisionRequest{
		Model:     model,
		Questions: []domain.Question{{ID: "q", Type: domain.QuestionNoul, Instructions: "well?"}},
	}
}

// noulOKBody is a valid vendor answer for noulRequest.
const noulOKBody = `{"model":"m","answers":{"q":{"type":"noul","noul":0.5}}}`

func newRetryTestProvider(t *testing.T, baseURL string, clock func() time.Time) *Provider {
	t.Helper()
	p, err := New(Config{
		Profile:   ProfileDrex,
		BaseURL:   baseURL,
		SecretRef: "conn/test/decision",
	}, ports.Deps{
		Secrets: testSecrets{"conn/test/decision": "unit-test-token"},
		Clock:   clock,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func startStatusServer(t *testing.T, status int, headers map[string]string, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDefaultTimeoutAtLeastSixtySeconds pins the vendor minimum and the config
// override semantics: 0 means the 60s default, an explicit value is honoured
// (the conformance suite and short callers rely on it).
func TestDefaultTimeoutAtLeastSixtySeconds(t *testing.T) {
	if defaultTimeout < 60*time.Second {
		t.Fatalf("defaultTimeout = %v, want >= 60s", defaultTimeout)
	}
	p := newRetryTestProvider(t, "https://example.com", nil)
	if p.cfg.Timeout != defaultTimeout {
		t.Fatalf("New(Timeout 0) = %v, want %v", p.cfg.Timeout, defaultTimeout)
	}
	p, err := New(Config{
		Profile:   ProfileDrex,
		SecretRef: "conn/test/decision",
		Timeout:   5 * time.Second,
	}, ports.Deps{Secrets: testSecrets{"conn/test/decision": "s"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.cfg.Timeout != 5*time.Second {
		t.Fatalf("explicit timeout = %v, want 5s", p.cfg.Timeout)
	}
}

func TestParseRetryAfterMS(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"-10", 0, false},
		{"abc", 0, false},
		{"1.5", 0, false},
		{"2500", 2500 * time.Millisecond, true},
		{"1000", time.Second, true},
		{"600000", maxRetryAfter, true},
		{"9223372036854775807", maxRetryAfter, true},
	}
	for _, tc := range cases {
		got, ok := parseRetryAfterMS(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseRetryAfterMS(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRetryAfterHintPrefersMillisecondsAndCaps(t *testing.T) {
	now := baseTime
	cases := []struct {
		name   string
		header map[string]string
		want   time.Duration
		ok     bool
	}{
		{"both prefers ms", map[string]string{"Retry-After-Ms": "1500", "Retry-After": "9"}, 1500 * time.Millisecond, true},
		{"seconds only", map[string]string{"Retry-After": "7"}, 7 * time.Second, true},
		{"ms capped", map[string]string{"Retry-After-Ms": "600000"}, maxRetryAfter, true},
		{"seconds capped", map[string]string{"Retry-After": "1200"}, maxRetryAfter, true},
		{"ms wins even when larger", map[string]string{"Retry-After-Ms": "3000", "Retry-After": "1"}, 3 * time.Second, true},
		{"absent", nil, 0, false},
		{"garbage", map[string]string{"Retry-After-Ms": "soon", "Retry-After": "later"}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			for k, v := range tc.header {
				header.Set(k, v)
			}
			got, ok := retryAfterHint(header, now)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("retryAfterHint = (%v, %v), want (%v, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestStatusErrorsHonorRetryAfter covers both vendor headers on every status
// that documents them, plus the cap for oversized hints.
func TestStatusErrorsHonorRetryAfter(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		header  map[string]string
		code    domain.ErrorCode
		want    time.Duration
		message string
	}{
		{
			name: "429 retry-after-ms", status: http.StatusTooManyRequests,
			header:  map[string]string{"Retry-After-Ms": "2500"},
			code:    domain.CodeRateLimit,
			want:    2500 * time.Millisecond,
			message: "retry after 2.5s",
		},
		{
			name: "429 retry-after seconds", status: http.StatusTooManyRequests,
			header:  map[string]string{"Retry-After": "6"},
			code:    domain.CodeRateLimit,
			want:    6 * time.Second,
			message: "retry after 6s",
		},
		{
			name: "503 retry-after-ms", status: http.StatusServiceUnavailable,
			header:  map[string]string{"Retry-After-Ms": "5000"},
			code:    domain.CodeUnavailable,
			want:    5 * time.Second,
			message: "retry after 5s",
		},
		{
			name: "529 retry-after seconds", status: 529,
			header:  map[string]string{"Retry-After": "2"},
			code:    domain.CodeUnavailable,
			want:    2 * time.Second,
			message: "retry after 2s",
		},
		{
			name: "503 ms capped", status: http.StatusServiceUnavailable,
			header: map[string]string{"Retry-After-Ms": "600000"},
			code:   domain.CodeUnavailable,
			want:   maxRetryAfter,
		},
		{
			name: "529 seconds capped", status: 529,
			header: map[string]string{"Retry-After": "1200"},
			code:   domain.CodeUnavailable,
			want:   maxRetryAfter,
		},
		{
			name: "503 without hint", status: http.StatusServiceUnavailable,
			code: domain.CodeUnavailable,
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startStatusServer(t, tc.status, tc.header, `{"error":{"type":"service_unavailable"}}`)
			p := newRetryTestProvider(t, srv.URL, nil)
			_, err := p.Decide(context.Background(), noulRequest("m"))
			if domain.CodeOf(err) != tc.code {
				t.Fatalf("code = %q, want %q (%v)", domain.CodeOf(err), tc.code, err)
			}
			if !domain.IsRetryable(err) {
				t.Fatalf("error is not retryable: %v", err)
			}
			if got := domain.RetryAfterOf(err); got != tc.want {
				t.Fatalf("RetryAfter = %v, want %v", got, tc.want)
			}
			if tc.message != "" && !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("message %q does not contain %q", err.Error(), tc.message)
			}
		})
	}
}

// TestVendorErrorType checks error.type parsing, the typed code kept for every
// status, and the vendor code exposed for details.vendorCode.
func TestVendorErrorType(t *testing.T) {
	cases := []struct {
		status  int
		vendor  string
		code    domain.ErrorCode
		retries bool
	}{
		{http.StatusUnauthorized, "authentication_error", domain.CodeAuth, false},
		{http.StatusPaymentRequired, "insufficient_credit", domain.CodePaymentRequired, false},
		{http.StatusUnprocessableEntity, "invalid_request_error", domain.CodeInvalidRequest, false},
		{http.StatusTooManyRequests, "rate_limit_error", domain.CodeRateLimit, true},
		{http.StatusInternalServerError, "internal_error", domain.CodeUnavailable, true},
		{http.StatusServiceUnavailable, "service_unavailable", domain.CodeUnavailable, true},
		{529, "overloaded", domain.CodeUnavailable, true},
	}
	for _, tc := range cases {
		t.Run(tc.vendor, func(t *testing.T) {
			body := `{"error":{"type":"` + tc.vendor + `","message":"opaque"}}`
			srv := startStatusServer(t, tc.status, nil, body)
			p := newRetryTestProvider(t, srv.URL, nil)
			_, err := p.Decide(context.Background(), noulRequest("m"))
			if domain.CodeOf(err) != tc.code {
				t.Fatalf("code = %q, want %q (%v)", domain.CodeOf(err), tc.code, err)
			}
			if got := domain.IsRetryable(err); got != tc.retries {
				t.Fatalf("retryable = %v, want %v", got, tc.retries)
			}
			if got := VendorCodeOf(err); got != tc.vendor {
				t.Fatalf("VendorCodeOf = %q, want %q", got, tc.vendor)
			}
			if !strings.Contains(err.Error(), "("+tc.vendor+")") {
				t.Fatalf("message %q does not carry the vendor type", err.Error())
			}
			// The vendor message must never be copied into the error.
			if strings.Contains(err.Error(), "opaque") {
				t.Fatalf("message leaks the vendor body: %v", err)
			}
		})
	}
}

func TestVendorCodeFallbackAndSanitizing(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"legacy code", `{"error":{"code":"rate_limit_error"}}`, "rate_limit_error"},
		{"type wins over code", `{"error":{"type":"overloaded","code":"rate_limit_error"}}`, "overloaded"},
		{"sanitized", "{\"error\":{\"type\":\"bad\\ntype X-Injected: yes\"}}", "badtypeX-Injectedyes"},
		{"empty", `{"error":{}}`, ""},
		{"not an envelope", `not json`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := vendorCode([]byte(tc.body)); got != tc.want {
				t.Fatalf("vendorCode = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUsageOptional proves a missing or empty usage block is accepted and
// reported as unknown zeros instead of failing or inventing token counts.
func TestUsageOptional(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"absent", `{"model":"m","answers":{"q":{"type":"noul","noul":0.5}}}`},
		{"null", `{"model":"m","answers":{"q":{"type":"noul","noul":0.5}},"usage":null}`},
		{"empty", `{"model":"m","answers":{"q":{"type":"noul","noul":0.5}},"usage":{}}`},
	}
	price := domain.MustParseDecimal("60")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := parseResponse([]byte(tc.body), noulRequest("m"), price)
			if err != nil {
				t.Fatalf("parseResponse: %v", err)
			}
			if resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != 0 {
				t.Fatalf("usage = %+v, want unknown zeros", resp.Usage)
			}
			if !resp.Usage.CostUSD.IsZero() {
				t.Fatalf("cost = %s, want zero for unknown usage", resp.Usage.CostUSD.String())
			}
		})
	}
}

func TestVendorLatencyOptional(t *testing.T) {
	price := domain.MustParseDecimal("60")
	withEval := `{"model":"m","answers":{"q":{"type":"noul","noul":0.5}},"evaluation_time_ms":104}`
	resp, err := parseResponse([]byte(withEval), noulRequest("m"), price)
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Usage.VendorLatencyMS != 104 {
		t.Fatalf("vendor latency = %d, want 104", resp.Usage.VendorLatencyMS)
	}
	absent := `{"model":"m","answers":{"q":{"type":"noul","noul":0.5}}}`
	resp, err = parseResponse([]byte(absent), noulRequest("m"), price)
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Usage.VendorLatencyMS != 0 {
		t.Fatalf("vendor latency = %d, want 0 (unknown) when absent", resp.Usage.VendorLatencyMS)
	}
}

func TestDecideRespectsParentContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(noulOKBody))
	}))
	t.Cleanup(srv.Close)
	p := newRetryTestProvider(t, srv.URL, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Decide(ctx, noulRequest("m"))
	if domain.CodeOf(err) != domain.CodeTimeout {
		t.Fatalf("code = %q, want timeout (%v)", domain.CodeOf(err), err)
	}
}

func TestDecidePerRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(noulOKBody))
	}))
	t.Cleanup(srv.Close)
	p := newRetryTestProvider(t, srv.URL, nil)

	req := noulRequest("m")
	req.Timeout = 30 * time.Millisecond
	_, err := p.Decide(context.Background(), req)
	if domain.CodeOf(err) != domain.CodeTimeout {
		t.Fatalf("code = %q, want timeout (%v)", domain.CodeOf(err), err)
	}
}

// TestDecideBreakerOpensAndRecovers drives the provider through the full
// closed -> open -> half-open -> closed cycle without real waiting.
func TestDecideBreakerOpensAndRecovers(t *testing.T) {
	clock := &testClock{now: baseTime}
	var hits atomic.Int64
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if !healthy.Load() {
			w.Header().Set("Retry-After-Ms", "2000")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"type":"service_unavailable"}}`))
			return
		}
		_, _ = w.Write([]byte(noulOKBody))
	}))
	t.Cleanup(srv.Close)
	p := newRetryTestProvider(t, srv.URL, clock.Now)

	for i := 0; i < breakerOpenAfter; i++ {
		if _, err := p.Decide(context.Background(), noulRequest("m")); domain.CodeOf(err) != domain.CodeUnavailable {
			t.Fatalf("failure %d: code = %q, want unavailable", i+1, domain.CodeOf(err))
		}
	}
	if got := hits.Load(); got != breakerOpenAfter {
		t.Fatalf("server hits = %d, want %d", got, breakerOpenAfter)
	}

	_, err := p.Decide(context.Background(), noulRequest("m"))
	if domain.CodeOf(err) != domain.CodeUnavailable || !domain.IsRetryable(err) {
		t.Fatalf("open breaker: code = %q retryable = %v", domain.CodeOf(err), domain.IsRetryable(err))
	}
	if got := domain.RetryAfterOf(err); got <= 0 || got > maxRetryAfter {
		t.Fatalf("open breaker RetryAfter = %v, want (0, %v]", got, maxRetryAfter)
	}
	if got := hits.Load(); got != breakerOpenAfter {
		t.Fatalf("open breaker still reached the server: hits = %d", got)
	}

	healthy.Store(true)
	clock.Advance(breakerOpenFor)
	if _, err := p.Decide(context.Background(), noulRequest("m")); err != nil {
		t.Fatalf("half-open probe failed: %v", err)
	}
	if got := hits.Load(); got != breakerOpenAfter+1 {
		t.Fatalf("half-open probe hits = %d, want %d", got, breakerOpenAfter+1)
	}
	if _, err := p.Decide(context.Background(), noulRequest("m")); err != nil {
		t.Fatalf("closed breaker rejected a call: %v", err)
	}
	if got := hits.Load(); got != breakerOpenAfter+2 {
		t.Fatalf("post-recovery hits = %d, want %d", got, breakerOpenAfter+2)
	}
}

// TestDecideBreakerHalfOpenFailureReopens proves a failed probe keeps the
// breaker open for another full cooldown.
func TestDecideBreakerHalfOpenFailureReopens(t *testing.T) {
	clock := &testClock{now: baseTime}
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(529)
		_, _ = w.Write([]byte(`{"error":{"type":"overloaded"}}`))
	}))
	t.Cleanup(srv.Close)
	p := newRetryTestProvider(t, srv.URL, clock.Now)

	for i := 0; i < breakerOpenAfter; i++ {
		_, _ = p.Decide(context.Background(), noulRequest("m"))
	}
	clock.Advance(breakerOpenFor)
	if _, err := p.Decide(context.Background(), noulRequest("m")); domain.CodeOf(err) != domain.CodeUnavailable {
		t.Fatalf("probe code = %q, want unavailable", domain.CodeOf(err))
	}
	if got := hits.Load(); got != breakerOpenAfter+1 {
		t.Fatalf("probe hits = %d, want %d", got, breakerOpenAfter+1)
	}
	if _, err := p.Decide(context.Background(), noulRequest("m")); domain.CodeOf(err) != domain.CodeUnavailable {
		t.Fatalf("after failed probe: code = %q, want unavailable", domain.CodeOf(err))
	}
	if got := hits.Load(); got != breakerOpenAfter+1 {
		t.Fatalf("reopened breaker reached the server: hits = %d", got)
	}
}
