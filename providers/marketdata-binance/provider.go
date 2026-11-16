// Package marketdatabinance implements ports.CandleProvider on top of the
// public Binance Spot klines endpoint (GET /api/v3/klines).
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
)

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
	return &Provider{baseURL: base, client: &clone, now: clock}, nil
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
// It paginates with startTime set to one millisecond past the latest candle
// seen so far, then sorts ascending by OpenTime, drops duplicates by
// OpenTime (keeping the first occurrence), drops unclosed candles
// (CloseTime >= now), drops candles past req.To and truncates to the
// requested limit. No retries are performed.
func (p *Provider) Candles(ctx context.Context, req domain.CandleRequest) ([]domain.Candle, error) {
	if strings.TrimSpace(req.Symbol) == "" || strings.TrimSpace(req.Interval) == "" {
		return nil, domain.NewError(domain.CodeInvalidRequest, "marketdata-binance: symbol and interval are required")
	}
	target := req.Limit
	if target <= 0 {
		target = 1000
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
	var all []domain.Candle
	var prevMax int64
	hasPrev := false
	now := p.nowUTC()
	for iter := 0; iter < maxIterations && len(all) < target; iter++ {
		per := target - len(all) + 1
		if per > maxPerRequest {
			per = maxPerRequest
		}
		if per < 1 {
			per = 1
		}
		page, err := p.fetchPage(ctx, req.Symbol, req.Interval, per, startMs, endMs)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		maxMs := page[0].OpenTime.UnixMilli()
		for _, c := range page[1:] {
			if ms := c.OpenTime.UnixMilli(); ms > maxMs {
				maxMs = ms
			}
		}
		for _, c := range page {
			if !c.CloseTime.Before(now) {
				continue
			}
			all = append(all, c)
		}
		if hasPrev && maxMs <= prevMax {
			break
		}
		prevMax = maxMs
		hasPrev = true
		next := maxMs + 1
		startMs = &next
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
		res = res[:target]
	}
	return res, nil
}

// fetchPage performs one GET /api/v3/klines request.
func (p *Provider) fetchPage(ctx context.Context, symbol, interval string, limit int, startMs, endMs *int64) ([]domain.Candle, error) {
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
		rateErr := domain.NewError(domain.CodeRateLimit, "marketdata-binance: rate limited")
		rateErr.Retryable = true
		if ra := parseRetryAfter(resp.Header.Get("Retry-After"), now); ra > 0 {
			rateErr.RetryAfter = ra
			rateErr.Message = fmt.Sprintf("marketdata-binance: rate limited (retry after %s)", ra)
		}
		return nil, rateErr
	case resp.StatusCode == http.StatusTeapot:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		banErr := domain.NewError(domain.CodeIPBanned, "marketdata-binance: IP banned (418)")
		banErr.Retryable = false
		if ra := parseRetryAfter(resp.Header.Get("Retry-After"), now); ra > 0 {
			banErr.RetryAfter = ra
			banErr.Message = fmt.Sprintf("marketdata-binance: IP banned (418, retry after %s)", ra)
		}
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
		return nil, domain.NewError(domain.CodeInvalidRequest, "marketdata-binance: bad request")
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
