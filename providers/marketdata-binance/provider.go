// Package marketdatabinance implements ports.CandleProvider on top of the
// public Binance Spot klines endpoint (GET /api/v3/klines).
//
// The provider serves spot markets only: futures spellings such as
// BTC/USDT:USDT are rejected instead of being silently folded into the spot
// symbol. Intervals are validated against the case-sensitive Binance enum.
//
// No API keys are used. Prices and volumes are parsed into domain.Decimal;
// float64 is never used for money.
package marketdatabinance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

const (
	// DefaultBaseURL is the public Binance Spot REST endpoint.
	DefaultBaseURL = "https://api.binance.com"
	// Version is the provider implementation version.
	Version = "0.1.0-m0"
	// maxPerRequest caps the limit of a single klines request.
	maxPerRequest = 1000
	// maxIterations caps paginated follow-up requests per Candles call.
	maxIterations = 100
	// maxBodyBytes caps a single klines response body.
	maxBodyBytes = 8 << 20
	// klinesWeight is the Binance request weight of one klines call.
	klinesWeight = 2
	// weightBudgetPerMinute caps locally spent request weight. Binance
	// documents 6000 weight per minute for the public spot API; one fifth of
	// that keeps a single consumer well clear of the shared allowance.
	weightBudgetPerMinute = 1200
	// requestBudgetPerMinute caps klines calls per minute independently of
	// the weight budget.
	requestBudgetPerMinute = 300
	// budgetWindow is the rolling window of the local budgets.
	budgetWindow = time.Minute
	// defaultCooldown is used when a 429/418 carries no Retry-After hint.
	defaultCooldown = time.Minute
	// maxCooldown caps an upstream Retry-After so a broken hint cannot park
	// the provider indefinitely. It is also the longest enforced wait.
	maxCooldown = time.Hour
)

// spotIntervals is the case-sensitive Binance klines interval enum for the
// spot API. "1M" is one month, "1m" is one minute.
var spotIntervals = []string{
	"1s", "1m", "3m", "5m", "15m", "30m",
	"1h", "2h", "4h", "6h", "8h", "12h",
	"1d", "3d", "1w", "1M",
}

// spotIntervalSet is the membership view of spotIntervals.
var spotIntervalSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(spotIntervals))
	for _, iv := range spotIntervals {
		set[iv] = struct{}{}
	}
	return set
}()

// Config holds the provider connection settings.
type Config struct {
	// BaseURL overrides the Binance REST base URL. Empty means DefaultBaseURL.
	BaseURL string
}

// Provider serves historical klines from the public Binance endpoint.
type Provider struct {
	baseURL string
	client  *http.Client
	now     func() time.Time
	// maxPages caps paginated requests per Candles call. It is a field so
	// tests can exercise the truncated-range error without 100 upstream pages.
	maxPages int
	budget   *rateBudget
}

// New builds a Provider. The deps HTTP client is cloned with redirects
// disabled and a 30s timeout; a nil client falls back to http.DefaultClient.
func New(cfg Config, deps ports.Deps) (*Provider, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	if _, err := url.ParseRequestURI(base); err != nil {
		return nil, domain.WrapError(domain.CodeInvalidRequest, "marketdata-binance: invalid baseUrl", err)
	}
	src := deps.HTTPClient
	if src == nil {
		src = http.DefaultClient
	}
	clone := *src
	clone.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	clone.Timeout = 30 * time.Second
	clock := deps.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Provider{
		baseURL:  base,
		client:   &clone,
		now:      clock,
		maxPages: maxIterations,
		budget:   newRateBudget(clock),
	}, nil
}

// Descriptor advertises the provider identity and capabilities.
func (p *Provider) Descriptor() ports.Descriptor {
	return ports.Descriptor{
		ID:           "marketdata-binance",
		Name:         "Binance Spot Market Data",
		Kind:         ports.KindMarketData,
		Version:      Version,
		Capabilities: []string{"klines"},
		ConfigFields: []ports.ConfigField{
			{
				Name:        "baseUrl",
				Type:        "url",
				Description: "Binance REST base URL",
			},
		},
		Degradations: []string{},
	}
}

// Candles returns the candles selected by req, oldest first.
//
// The symbol must be a spot market: a futures suffix (everything from the
// first ":") is rejected with invalid_request. The interval must be one of the
// case-sensitive Binance klines intervals; "1H" is rejected, "1h" is accepted.
//
// With req.From set the scan walks forward from From, one millisecond past the
// latest candle seen. When From is empty and req.To is set it walks backward
// from To so a Limit above 1000 cannot silently lose the older pages. With
// neither bound the endpoint's newest page is fetched and the scan falls back
// to backward paging when a forward follow-up comes back empty. Pages are
// sorted ascending by OpenTime, de-duplicated by OpenTime (keeping the first
// occurrence), unclosed candles (CloseTime >= now) and candles past req.To are
// dropped, and the result is truncated to the requested limit (the earliest
// candles after From, or the latest candles when scanning backwards).
//
// Every request spends the local weight and request budget; the call returns
// rate_limit with RetryAfter instead of hammering the endpoint. A 429/418
// starts a cooldown capped at one hour. Reaching maxIterations while the range
// is still incomplete returns invalid_request naming the collected range
// instead of a silently truncated result.
func (p *Provider) Candles(ctx context.Context, req domain.CandleRequest) ([]domain.Candle, error) {
	symbol, err := validateSpotSymbol(req.Symbol)
	if err != nil {
		return nil, err
	}
	interval, err := validateInterval(req.Interval)
	if err != nil {
		return nil, err
	}
	target := req.Limit
	switch {
	case target < 0:
		return nil, domain.NewError(domain.CodeInvalidRequest,
			"marketdata-binance: limit must not be negative")
	case target == 0:
		target = maxPerRequest
	}
	if !req.From.IsZero() && !req.To.IsZero() && req.From.After(req.To) {
		return nil, domain.NewError(domain.CodeInvalidRequest,
			"marketdata-binance: from must not be after to")
	}
	var startMs *int64
	if !req.From.IsZero() {
		ms := req.From.UnixMilli()
		startMs = &ms
	}
	var endMs *int64
	if !req.To.IsZero() {
		ms := req.To.UnixMilli()
		endMs = &ms
	}
	// Without From but with To, Binance answers with the newest candles before
	// endTime; paging forward from them would never reach older data, so walk
	// backward with endTime instead.
	backward := startMs == nil && endMs != nil
	// unbounded marks a request with neither From nor To: Binance answers it
	// with the newest page, so a forward follow-up may be empty and the scan
	// then has to continue backwards.
	unbounded := startMs == nil && endMs == nil
	var all []domain.Candle
	var prev int64
	hasPrev := false
	done := false
	iter := 0
	now := p.nowUTC()
	for ; iter < p.maxPages && len(all) < target; iter++ {
		per := target - len(all) + 1
		if per > maxPerRequest {
			per = maxPerRequest
		}
		if per < 1 {
			per = 1
		}
		page, err := p.fetchPage(ctx, symbol, interval, per, startMs, endMs)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			if unbounded && !backward && len(all) > 0 && len(all) < target {
				// An unbounded request answered with the newest page cannot be
				// extended forward. Continue backwards from the oldest candle
				// collected so far instead of returning a short slice.
				oldest := minOpenTime(all)
				prev = oldest
				hasPrev = true
				next := oldest - 1
				startMs = nil
				endMs = &next
				backward = true
				continue
			}
			done = true
			break
		}
		// A page shorter than the requested limit is the last page: the
		// endpoint has no more candles in this direction. Do not spend a
		// second request to confirm the end of the data.
		shortPage := len(page) < per
		for _, c := range page {
			if !c.CloseTime.Before(now) {
				continue
			}
			all = append(all, c)
		}
		if shortPage {
			done = true
			break
		}
		// The page is full: advance one millisecond past its edge. A page
		// that does not move the edge would loop forever and would also mean
		// the range cannot be covered, so fail loudly instead of dropping it.
		if backward {
			minMs := minOpenTime(page)
			if hasPrev && minMs >= prev {
				return nil, domain.NewError(domain.CodeInvalidOutput,
					"marketdata-binance: pagination made no progress")
			}
			prev = minMs
			hasPrev = true
			next := minMs - 1
			endMs = &next
			continue
		}
		maxMs := maxOpenTime(page)
		if hasPrev && maxMs <= prev {
			return nil, domain.NewError(domain.CodeInvalidOutput,
				"marketdata-binance: pagination made no progress")
		}
		prev = maxMs
		hasPrev = true
		next := maxMs + 1
		startMs = &next
	}
	if !done && len(all) < target {
		return nil, paginationLimitError(all, target, p.maxPages)
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].OpenTime.Before(all[j].OpenTime)
	})
	res := make([]domain.Candle, 0, len(all))
	for _, c := range all {
		if n := len(res); n > 0 && c.OpenTime.Equal(res[n-1].OpenTime) {
			continue
		}
		if !c.CloseTime.Before(now) {
			continue
		}
		if !req.To.IsZero() && c.OpenTime.After(req.To) {
			continue
		}
		res = append(res, c)
	}
	if len(res) > target {
		if backward {
			res = res[len(res)-target:]
		} else {
			res = res[:target]
		}
	}
	return res, nil
}

// validateSpotSymbol rejects futures spellings and returns the canonical spot
// symbol used both in the query and on the returned candles.
func validateSpotSymbol(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", domain.NewError(domain.CodeInvalidRequest,
			"marketdata-binance: symbol is required")
	}
	if strings.ContainsRune(trimmed, ':') {
		// BTC/USDT:USDT is a perpetual/futures spelling. Folding it into
		// BTCUSDT would silently query a different market.
		return "", domain.NewError(domain.CodeInvalidRequest,
			fmt.Sprintf("marketdata-binance: futures not supported (spot endpoint only): %q", trimmed))
	}
	symbol := domain.CanonicalSymbol(trimmed)
	if symbol == "" {
		return "", domain.NewError(domain.CodeInvalidRequest,
			"marketdata-binance: symbol is required")
	}
	return symbol, nil
}

// validateInterval enforces the case-sensitive spot klines interval enum.
// The value is otherwise passed to Binance verbatim.
func validateInterval(raw string) (string, error) {
	interval := strings.TrimSpace(raw)
	if interval == "" {
		return "", domain.NewError(domain.CodeInvalidRequest,
			"marketdata-binance: interval is required")
	}
	if _, ok := spotIntervalSet[interval]; !ok {
		return "", domain.NewError(domain.CodeInvalidRequest,
			fmt.Sprintf("marketdata-binance: unsupported interval %q (want one of %s)",
				interval, strings.Join(spotIntervals, ",")))
	}
	return interval, nil
}

// minOpenTime returns the earliest OpenTime in milliseconds.
func minOpenTime(candles []domain.Candle) int64 {
	minMs := candles[0].OpenTime.UnixMilli()
	for _, c := range candles[1:] {
		if ms := c.OpenTime.UnixMilli(); ms < minMs {
			minMs = ms
		}
	}
	return minMs
}

// maxOpenTime returns the latest OpenTime in milliseconds.
func maxOpenTime(candles []domain.Candle) int64 {
	maxMs := candles[0].OpenTime.UnixMilli()
	for _, c := range candles[1:] {
		if ms := c.OpenTime.UnixMilli(); ms > maxMs {
			maxMs = ms
		}
	}
	return maxMs
}

// paginationLimitError reports a range that cannot be covered within
// maxIterations pages. It names the collected range so the caller can narrow
// From/To instead of receiving a silently truncated slice.
func paginationLimitError(collected []domain.Candle, target, pages int) error {
	first, last := "-", "-"
	if len(collected) > 0 {
		minT, maxT := collected[0].OpenTime, collected[0].OpenTime
		for _, c := range collected[1:] {
			if c.OpenTime.Before(minT) {
				minT = c.OpenTime
			}
			if c.OpenTime.After(maxT) {
				maxT = c.OpenTime
			}
		}
		first = minT.UTC().Format(time.RFC3339)
		last = maxT.UTC().Format(time.RFC3339)
	}
	return domain.NewError(domain.CodeInvalidRequest,
		fmt.Sprintf("marketdata-binance: candle range too large: collected %d of %d candles from %s to %s in %d pages; narrow from/to",
			len(collected), target, first, last, pages))
}

// fetchPage performs one GET /api/v3/klines request.
func (p *Provider) fetchPage(ctx context.Context, symbol, interval string, limit int, startMs, endMs *int64) ([]domain.Candle, error) {
	if err := p.budget.reserve(klinesWeight); err != nil {
		return nil, err
	}
	u, err := url.Parse(p.baseURL + "/api/v3/klines")
	if err != nil {
		return nil, domain.WrapError(domain.CodeInvalidRequest, "marketdata-binance: invalid baseUrl", err)
	}
	q := u.Query()
	q.Set("symbol", symbol)
	q.Set("interval", interval)
	q.Set("limit", strconv.Itoa(limit))
	if startMs != nil {
		q.Set("startTime", strconv.FormatInt(*startMs, 10))
	}
	if endMs != nil {
		q.Set("endTime", strconv.FormatInt(*endMs, 10))
	}
	u.RawQuery = q.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInvalidRequest, "marketdata-binance: build request", err)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, domain.WrapError(domain.CodeTimeout, "marketdata-binance: request canceled", err)
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			return nil, domain.WrapError(domain.CodeTimeout, "marketdata-binance: request timeout", err)
		}
		return nil, domain.WrapError(domain.CodeUnavailable, "marketdata-binance: transport error", err)
	}
	defer resp.Body.Close()
	now := p.nowUTC()
	switch {
	case resp.StatusCode == http.StatusOK:
		// Parse below.
	case resp.StatusCode == http.StatusTooManyRequests:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		// Enforce the provider-requested pause locally: the next calls fail
		// fast with the remaining delay instead of hammering the endpoint.
		wait := p.budget.cooldown(parseRetryAfter(resp.Header.Get("Retry-After"), now), false)
		rateErr := domain.NewError(domain.CodeRateLimit,
			fmt.Sprintf("marketdata-binance: rate limited (retry after %s)", wait))
		rateErr.Retryable = true
		rateErr.RetryAfter = wait
		return nil, rateErr
	case resp.StatusCode == http.StatusTeapot:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		wait := p.budget.cooldown(parseRetryAfter(resp.Header.Get("Retry-After"), now), true)
		banErr := domain.NewError(domain.CodeIPBanned,
			fmt.Sprintf("marketdata-binance: IP banned (418, retry after %s)", wait))
		banErr.Retryable = false
		banErr.RetryAfter = wait
		return nil, banErr
	case resp.StatusCode == 451:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		regionErr := domain.NewError(domain.CodeRegionUnavailable, "marketdata-binance: not available in region (451)")
		regionErr.Retryable = false
		if ra := parseRetryAfter(resp.Header.Get("Retry-After"), now); ra > 0 {
			regionErr.RetryAfter = ra
		}
		return nil, regionErr
	case resp.StatusCode == http.StatusBadRequest:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, domain.NewError(domain.CodeInvalidRequest, "marketdata-binance: unknown symbol or interval (400)")
	case resp.StatusCode >= 500 && resp.StatusCode <= 599:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		unavail := domain.NewError(domain.CodeUnavailable, fmt.Sprintf("marketdata-binance: upstream status %d", resp.StatusCode))
		unavail.Retryable = true
		return nil, unavail
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, domain.NewError(domain.CodeInvalidOutput, fmt.Sprintf("marketdata-binance: unexpected status %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, domain.WrapError(domain.CodeInvalidOutput, "marketdata-binance: read response", err)
	}
	return parseKlinesPage(body, symbol, interval)
}

// nowUTC returns the current time in UTC via the injected clock.
func (p *Provider) nowUTC() time.Time {
	if p.now != nil {
		return p.now().UTC()
	}
	return time.Now().UTC()
}

// rateBudget is the provider-local request budget. It spends Binance request
// weight inside a rolling one-minute window, caps the request count per minute
// and keeps a cooldown after an upstream 429/418 so repeated calls fail fast
// with the remaining delay instead of hammering the endpoint.
type rateBudget struct {
	mu  sync.Mutex
	now func() time.Time
	// weightPerMinute and requestsPerMinute are the local ceilings; tests
	// lower them to exercise both gates without hundreds of requests.
	weightPerMinute   int
	requestsPerMinute int

	windowStart   time.Time
	weight        int
	requests      int
	cooldownUntil time.Time
	banned        bool
}

// newRateBudget builds a budget with the default public-API ceilings.
func newRateBudget(now func() time.Time) *rateBudget {
	if now == nil {
		now = time.Now
	}
	return &rateBudget{
		now:               now,
		weightPerMinute:   weightBudgetPerMinute,
		requestsPerMinute: requestBudgetPerMinute,
	}
}

// reserve spends one request of the given weight or returns a retryable
// rate_limit / ip_banned error while the budget or cooldown blocks the call.
func (b *rateBudget) reserve(weight int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().UTC()
	if b.cooldownUntil.After(now) {
		remaining := b.cooldownUntil.Sub(now)
		if b.banned {
			e := domain.NewError(domain.CodeIPBanned,
				fmt.Sprintf("marketdata-binance: IP banned by upstream (%s of cooldown remain)", remaining))
			e.RetryAfter = remaining
			return e
		}
		e := domain.NewError(domain.CodeRateLimit,
			fmt.Sprintf("marketdata-binance: cooling down after upstream throttle (%s remain)", remaining))
		e.Retryable = true
		e.RetryAfter = remaining
		return e
	}
	b.cooldownUntil = time.Time{}
	b.banned = false
	if b.windowStart.IsZero() || now.Sub(b.windowStart) >= budgetWindow {
		b.windowStart = now
		b.weight = 0
		b.requests = 0
	}
	if b.requests+1 > b.requestsPerMinute {
		return b.budgetError(now, "request",
			fmt.Sprintf("%d/%d per minute", b.requests, b.requestsPerMinute))
	}
	if b.weight+weight > b.weightPerMinute {
		return b.budgetError(now, "weight",
			fmt.Sprintf("%d/%d per minute", b.weight, b.weightPerMinute))
	}
	b.weight += weight
	b.requests++
	return nil
}

// budgetError builds the error for an exhausted local budget.
func (b *rateBudget) budgetError(now time.Time, kind, used string) error {
	wait := b.windowStart.Add(budgetWindow).Sub(now)
	if wait <= 0 {
		wait = time.Millisecond
	}
	e := domain.NewError(domain.CodeRateLimit,
		fmt.Sprintf("marketdata-binance: local %s budget exhausted (%s); retry after %s", kind, used, wait))
	e.Retryable = true
	e.RetryAfter = wait
	return e
}

// cooldown records an upstream-requested pause, capped at maxCooldown, and
// returns the effective duration. A missing hint falls back to the default
// minute so a 429/418 still stops the next call from hammering.
func (b *rateBudget) cooldown(d time.Duration, banned bool) time.Duration {
	if d <= 0 {
		d = defaultCooldown
	}
	if d > maxCooldown {
		d = maxCooldown
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().UTC()
	until := now.Add(d)
	if until.After(b.cooldownUntil) {
		b.cooldownUntil = until
		b.banned = banned
	} else if banned {
		// A longer throttle is already active; escalate its severity without
		// shortening the enforced pause.
		b.banned = true
	}
	return b.cooldownUntil.Sub(now)
}

// parseRetryAfter decodes a Retry-After header value (delta seconds or HTTP
// date) into a non-negative delay computed against now.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	for _, layout := range []string{http.TimeFormat, time.RFC1123, time.RFC1123Z} {
		if t, err := time.Parse(layout, v); err == nil {
			d := t.Sub(now)
			if d < 0 {
				return 0
			}
			return d
		}
	}
	return 0
}

// parseKlinesPage decodes one klines response body.
//
// Each kline is an array:
//
//	[openTimeMs, "open", "high", "low", "close", "volume", closeTimeMs, ...]
func parseKlinesPage(data []byte, symbol, interval string) ([]domain.Candle, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, domain.WrapError(domain.CodeInvalidOutput, "marketdata-binance: invalid klines JSON", err)
	}
	out := make([]domain.Candle, 0, len(rows))
	for i, r := range rows {
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.UseNumber()
		var cols []json.RawMessage
		if err := dec.Decode(&cols); err != nil {
			return nil, domain.WrapError(domain.CodeInvalidOutput, fmt.Sprintf("marketdata-binance: invalid kline row %d", i), err)
		}
		if len(cols) < 7 {
			return nil, domain.NewError(domain.CodeInvalidOutput, fmt.Sprintf("marketdata-binance: invalid kline row %d: want at least 7 columns", i))
		}
		openMs, err := parseMs(cols[0])
		if err != nil {
			return nil, domain.WrapError(domain.CodeInvalidOutput, fmt.Sprintf("marketdata-binance: invalid kline row %d open time", i), err)
		}
		closeMs, err := parseMs(cols[6])
		if err != nil {
			return nil, domain.WrapError(domain.CodeInvalidOutput, fmt.Sprintf("marketdata-binance: invalid kline row %d close time", i), err)
		}
		open, err := parseDecimalField(cols[1])
		if err != nil {
			return nil, domain.WrapError(domain.CodeInvalidOutput, fmt.Sprintf("marketdata-binance: invalid kline row %d open price", i), err)
		}
		high, err := parseDecimalField(cols[2])
		if err != nil {
			return nil, domain.WrapError(domain.CodeInvalidOutput, fmt.Sprintf("marketdata-binance: invalid kline row %d high price", i), err)
		}
		low, err := parseDecimalField(cols[3])
		if err != nil {
			return nil, domain.WrapError(domain.CodeInvalidOutput, fmt.Sprintf("marketdata-binance: invalid kline row %d low price", i), err)
		}
		closePx, err := parseDecimalField(cols[4])
		if err != nil {
			return nil, domain.WrapError(domain.CodeInvalidOutput, fmt.Sprintf("marketdata-binance: invalid kline row %d close price", i), err)
		}
		volume, err := parseDecimalField(cols[5])
		if err != nil {
			return nil, domain.WrapError(domain.CodeInvalidOutput, fmt.Sprintf("marketdata-binance: invalid kline row %d volume", i), err)
		}
		out = append(out, domain.Candle{
			Symbol:    symbol,
			Interval:  interval,
			OpenTime:  time.UnixMilli(openMs).UTC(),
			CloseTime: time.UnixMilli(closeMs).UTC(),
			Open:      open,
			High:      high,
			Low:       low,
			Close:     closePx,
			Volume:    volume,
		})
	}
	return out, nil
}

// parseMs decodes a millisecond epoch timestamp without float64.
func parseMs(raw json.RawMessage) (int64, error) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, err
	}
	return n.Int64()
}

// parseDecimalField decodes a JSON string into a domain.Decimal.
func parseDecimalField(raw json.RawMessage) (domain.Decimal, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return domain.Decimal{}, err
	}
	return domain.ParseDecimal(s)
}
