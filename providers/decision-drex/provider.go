// Package decisiondrex implements the ports.Decision adapter for the
// Drex/Jev System One decision API.
//
// Only the standard library plus core/domain and core/ports are used.
package decisiondrex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// Profile selects the wire-compatible decision API endpoint family.
type Profile string

// Supported profiles.
const (
	// ProfileDrex talks to the Drex cloud API.
	ProfileDrex Profile = "drex"
	// ProfileJev talks to the Jev (TypeSafe) wire-compatible API.
	ProfileJev Profile = "jev"
)

// Default endpoint parts per profile.
const (
	defaultDrexBase = "https://console.nace.ai"
	drexPath        = "/v1/systemone"
	defaultJevBase  = "https://api.typesafe.ai/v1"
	jevPath         = "/systemone"
)

// defaultTimeout applies when neither the request nor the config sets one.
//
// The vendor recommends at least 60 seconds per attempt: Drex may wait up to
// 55 seconds for the model before answering 529, and a shorter client timeout
// abandons requests that were about to succeed.
// See https://console.nace.ai/docs/drex/guides/errors-and-retries
const defaultTimeout = 60 * time.Second

// maxRetryAfter caps a vendor-requested retry delay so one header cannot stall
// a caller for minutes. It matches the cap in core/internal/resilience.
const maxRetryAfter = 30 * time.Second

// Circuit-breaker tuning. The breaker trips after breakerOpenAfter consecutive
// availability failures, stays open for breakerOpenFor, then lets exactly one
// half-open probe through. Parameters are constants, not config: the vendor
// guide documents no per-account values and the defaults are deliberately
// conservative.
const (
	// breakerOpenAfter is the number of consecutive failures that opens it.
	breakerOpenAfter = 5
	// breakerOpenFor is the cooldown before a single half-open probe.
	breakerOpenFor = 30 * time.Second
)

// maxBodySize caps a single response body at 1 MiB.
const maxBodySize = 1 << 20

// Default input-token prices per 1B tokens, as decimal strings.
const (
	defaultDrexPrice = "60"
	defaultJevPrice  = "42"
)

// billion is the divisor converting a per-1B-token price to a per-token cost.
const billion = "1000000000"

// costScale is the Decimal scale used for costUsd.
const costScale = 12

// Config tunes the decision-drex provider.
type Config struct {
	// Profile selects drex or jev defaults for base URL, path and price.
	Profile Profile
	// BaseURL overrides the profile default base; the profile path is appended.
	BaseURL string
	// SecretRef is the secret reference resolved via Deps.Secrets.
	SecretRef string
	// PricePerBillion is the input-token price per 1B tokens, decimal string.
	PricePerBillion string
	// Timeout is the default per-attempt deadline; 0 means defaultTimeout
	// (60s). An explicit per-request domain.DecisionRequest.Timeout still
	// overrides it, so tests and short-lived callers can shorten one call.
	Timeout time.Duration
}

// Provider is a ports.Decision implementation for Drex/Jev System One.
type Provider struct {
	cfg      Config
	deps     ports.Deps
	endpoint string
	price    domain.Decimal
	client   *http.Client
	breaker  breaker
}

// New builds a Provider from cfg and host deps.
func New(cfg Config, deps ports.Deps) (*Provider, error) {
	var base, path, defaultPrice string
	switch cfg.Profile {
	case ProfileDrex:
		base, path, defaultPrice = defaultDrexBase, drexPath, defaultDrexPrice
	case ProfileJev:
		base, path, defaultPrice = defaultJevBase, jevPath, defaultJevPrice
	default:
		return nil, domain.NewError(domain.CodeInvalidRequest,
			"decision-drex: unknown profile")
	}
	if strings.TrimSpace(cfg.SecretRef) == "" {
		return nil, domain.NewError(domain.CodeInvalidRequest,
			"decision-drex: empty secret reference")
	}
	priceStr := cfg.PricePerBillion
	if priceStr == "" {
		priceStr = defaultPrice
	}
	price, err := domain.ParseDecimal(priceStr)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInvalidRequest,
			"decision-drex: invalid price per billion", err)
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.TrimSpace(cfg.BaseURL) != "" {
		clean, verr := validateBaseURL(cfg.BaseURL)
		if verr != nil {
			return nil, verr
		}
		base = clean
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	return &Provider{
		cfg:      Config{Profile: cfg.Profile, BaseURL: cfg.BaseURL, SecretRef: cfg.SecretRef, PricePerBillion: priceStr, Timeout: timeout},
		deps:     deps,
		endpoint: base + path,
		price:    price,
		client:   cloneClient(deps.HTTPClient),
	}, nil
}

// validateBaseURL enforces the base URL policy before any network I/O:
// the override must be an absolute http(s) URL without userinfo, query or
// fragment. https is always allowed; http only for loopback hosts
// (127.0.0.1, ::1). Anything else is CodeInvalidRequest.
func validateBaseURL(raw string) (string, error) {
	invalid := func() (string, error) {
		return "", domain.NewError(domain.CodeInvalidRequest,
			"decision-drex: invalid base URL")
	}
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || !u.IsAbs() {
		return invalid()
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return invalid()
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return invalid()
	}
	host := u.Hostname()
	if host == "" {
		return invalid()
	}
	if scheme == "http" && host != "127.0.0.1" && host != "::1" {
		return invalid()
	}
	return strings.TrimRight(trimmed, "/"), nil
}

// cloneClient copies base (or builds a default client) and disables redirects.
func cloneClient(base *http.Client) *http.Client {
	var c *http.Client
	if base == nil {
		c = &http.Client{}
	} else {
		cp := *base
		c = &cp
	}
	c.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return c
}

// Descriptor advertises the provider identity and capabilities.
func (p *Provider) Descriptor() ports.Descriptor {
	return ports.Descriptor{
		ID:           "decision-drex",
		Name:         "Drex/Jev decision",
		Kind:         ports.KindDecision,
		Version:      "0.1.0-m0",
		Capabilities: []string{"choice", "noul", "score"},
		ConfigFields: []ports.ConfigField{
			{Name: "profile", Type: "string", Description: "API profile: drex or jev", Required: true},
			{Name: "baseUrl", Type: "url", Description: "Override for the profile base URL"},
			{Name: "secretRef", Type: "string", Description: "Secret reference for the API key", Required: true, Secret: true},
			{Name: "pricePerBillion", Type: "string", Description: "Input-token price per 1B tokens, decimal string"},
		},
		Degradations: []string{},
	}
}

// Decide sends one advisory request and validates the answer set.
//
// Each call is one HTTP attempt: the retry loop lives in core/internal/resilience
// and sees the typed error plus its RetryAfter hint. The provider breaks the
// circuit itself so an unhealthy vendor is not hammered by independent callers.
func (p *Provider) Decide(ctx context.Context, req domain.DecisionRequest) (domain.DecisionResponse, error) {
	parent := ctx
	timeout := p.cfg.Timeout
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	if err := validateRequest(req); err != nil {
		return domain.DecisionResponse{}, err
	}

	var cancel context.CancelFunc = func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	secret := ""
	if p.deps.Secrets == nil {
		return domain.DecisionResponse{}, domain.NewError(domain.CodeAuth,
			"decision-drex: secret store is not configured")
	}
	var err error
	secret, err = p.deps.Secrets.Get(ctx, p.cfg.SecretRef)
	if err != nil || secret == "" {
		return domain.DecisionResponse{}, domain.WrapError(domain.CodeAuth,
			"decision-drex: cannot resolve API secret", errNoSecret{})
	}

	body, err := buildRequestBody(req)
	if err != nil {
		return domain.DecisionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return domain.DecisionResponse{}, domain.WrapError(domain.CodeInvalidRequest,
			"decision-drex: cannot build request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+secret)
	httpReq.Header.Set("Accept", "application/json")

	if allowed, wait := p.breaker.allow(p.deps.Now()); !allowed {
		e := domain.NewError(domain.CodeUnavailable,
			"decision-drex: circuit breaker is open")
		e.Retryable = true
		e.RetryAfter = wait
		return domain.DecisionResponse{}, e
	}

	call := *p.client
	if timeout > 0 {
		call.Timeout = timeout
	}
	start := time.Now()
	resp, err := call.Do(httpReq)
	if err != nil {
		// Caller cancellation is not a provider-health signal; an attempt
		// killed by the provider deadline is.
		if parent.Err() == nil {
			p.breaker.record(false, p.deps.Now())
		}
		return domain.DecisionResponse{}, mapTransportError(ctx, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		if parent.Err() == nil {
			p.breaker.record(false, p.deps.Now())
		}
		return domain.DecisionResponse{}, mapTransportError(ctx, err)
	}
	elapsed := time.Since(start)
	if elapsed < 0 {
		elapsed = 0
	}
	if len(raw) > maxBodySize {
		// The provider answered, so it is reachable; the body is just unusable.
		p.breaker.record(true, p.deps.Now())
		return domain.DecisionResponse{}, domain.NewError(domain.CodeInvalidOutput,
			"decision-drex: response body exceeds 1 MiB")
	}

	if resp.StatusCode != http.StatusOK {
		now := p.deps.Now()
		p.breaker.record(!transientStatus(resp.StatusCode), now)
		return domain.DecisionResponse{}, mapStatusError(resp.StatusCode, resp.Header, now, raw)
	}
	p.breaker.record(true, p.deps.Now())
	out, err := parseResponse(raw, req, p.price)
	if err != nil {
		return domain.DecisionResponse{}, err
	}
	out.Usage.LatencyMS = int64(elapsed / time.Millisecond)
	return out, nil
}

// transientStatus reports whether the provider itself looked unhealthy. 429 is
// throttling, not an outage: it carries Retry-After and must not open the
// breaker, otherwise the retry loop could never wait it out.
func transientStatus(status int) bool {
	return status == http.StatusInternalServerError ||
		status == http.StatusServiceUnavailable ||
		status == 529
}

// breakerState is the circuit-breaker phase.
type breakerState uint8

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

// breaker is a mutex-guarded consecutive-failure circuit breaker.
//
// It counts availability failures only: transport errors, timeouts and
// 5xx/529 answers. Any other HTTP response proves the provider is reachable
// and resets the streak.
type breaker struct {
	mu       sync.Mutex
	state    breakerState
	failures int
	openedAt time.Time
	probing  bool
}

// allow reports whether a call may start. When the breaker is open and the
// cooldown has elapsed, the caller becomes the single half-open probe. A
// denied caller gets the time left before a probe may run.
func (b *breaker) allow(now time.Time) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerOpen:
		elapsed := now.Sub(b.openedAt)
		if elapsed < breakerOpenFor {
			return false, capRetryAfter(breakerOpenFor - elapsed)
		}
		b.state = breakerHalfOpen
		b.probing = true
		return true, 0
	case breakerHalfOpen:
		if b.probing {
			return false, 0
		}
		b.probing = true
		return true, 0
	default:
		return true, 0
	}
}

// record feeds one call result back. A success closes the breaker or resets the
// streak; a failure counts towards the threshold while closed and re-opens it
// in half-open. Late results of calls that were already in flight when the
// breaker opened are ignored so they cannot flap the state.
func (b *breaker) record(ok bool, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerOpen:
		return
	case breakerHalfOpen:
		b.probing = false
		if ok {
			b.state = breakerClosed
			b.failures = 0
			return
		}
		b.state = breakerOpen
		b.openedAt = now
	case breakerClosed:
		if ok {
			b.failures = 0
			return
		}
		b.failures++
		if b.failures >= breakerOpenAfter {
			b.state = breakerOpen
			b.openedAt = now
			b.failures = breakerOpenAfter
		}
	}
}

// errNoSecret hides the secret-store failure detail from the message chain.
type errNoSecret struct{}

func (errNoSecret) Error() string { return "secret unavailable" }

// mapTransportError classifies a failed HTTP round trip.
func mapTransportError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		e := domain.WrapError(domain.CodeTimeout, "decision-drex: request canceled or timed out", err)
		e.Retryable = true
		return e
	}
	if ctx != nil && ctx.Err() != nil {
		e := domain.WrapError(domain.CodeTimeout, "decision-drex: request canceled or timed out", err)
		e.Retryable = true
		return e
	}
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Timeout() {
		e := domain.WrapError(domain.CodeTimeout, "decision-drex: request timed out", err)
		e.Retryable = true
		return e
	}
	e := domain.WrapError(domain.CodeUnavailable, "decision-drex: transport error", err)
	e.Retryable = true
	return e
}

// vendorErrorEnvelope carries the vendor error identity, if present.
//
// The vendor documents error.type; error.code is kept as a fallback for older
// servers and existing fixtures.
type vendorErrorEnvelope struct {
	Error *struct {
		Type string `json:"type"`
		Code string `json:"code"`
	} `json:"error"`
}

// vendorCode extracts the sanitized vendor error identity from an error body.
func vendorCode(raw []byte) string {
	var env vendorErrorEnvelope
	if json.Unmarshal(raw, &env) != nil || env.Error == nil {
		return ""
	}
	if t := sanitizeVendorCode(strings.TrimSpace(env.Error.Type)); t != "" {
		return t
	}
	return sanitizeVendorCode(strings.TrimSpace(env.Error.Code))
}

// vendorCodeError is a *domain.Error that also exposes the sanitized vendor
// error.type. domain.Error has no Details field yet, so flow and CLI code can
// read the value with VendorCodeOf instead of parsing the message.
type vendorCodeError struct {
	err  *domain.Error
	code string
}

// Error renders the wrapped domain error unchanged.
func (e *vendorCodeError) Error() string { return e.err.Error() }

// Unwrap returns the wrapped domain error; codes and RetryAfter stay visible
// to domain.CodeOf, domain.IsRetryable and domain.RetryAfterOf.
func (e *vendorCodeError) Unwrap() error { return e.err }

// VendorCode returns the sanitized vendor error.type.
func (e *vendorCodeError) VendorCode() string { return e.code }

// VendorCodeOf returns the sanitized vendor error.type carried by err, or ""
// when there is none. This is the value meant for details.vendorCode.
func VendorCodeOf(err error) string {
	var v interface{ VendorCode() string }
	if errors.As(err, &v) {
		return v.VendorCode()
	}
	return ""
}

// withVendorCode attaches code to a domain error without changing its code,
// retryability or message.
func withVendorCode(e *domain.Error, code string) error {
	if e == nil || code == "" {
		return e
	}
	return &vendorCodeError{err: e, code: code}
}

// sanitizeVendorCode keeps only [A-Za-z0-9_.-], capped at 64 characters.
// Control characters and newlines never survive: they are simply not in the
// allowlist, so the result is safe to embed in messages, logs and journals.
func sanitizeVendorCode(s string) string {
	const maxLen = 64
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			b.WriteRune(r)
			n++
			if n >= maxLen {
				return b.String()
			}
		}
	}
	return b.String()
}

// statusError builds a typed status error. Only the status, the sanitized
// vendor type and the retry hint enter the message: headers, the full body and
// the secret never do.
func statusError(code domain.ErrorCode, prefix, vendor string, retryAfter time.Duration, retryable bool) error {
	msg := "decision-drex: " + prefix
	if retryAfter > 0 {
		msg += ", retry after " + retryAfter.Round(time.Millisecond).String()
	}
	if vendor != "" {
		msg += " (" + vendor + ")"
	}
	e := domain.NewError(code, msg)
	e.Retryable = retryable
	e.RetryAfter = retryAfter
	return withVendorCode(e, vendor)
}

// mapStatusError maps an HTTP failure status to a domain error.
//
// 429, 503 and 529 carry retry-after-ms and retry-after; the hint is clamped to
// maxRetryAfter and returned on the error for the retry loop. Only the status
// and the sanitized vendor error type enter the message.
func mapStatusError(status int, header http.Header, now time.Time, raw []byte) error {
	vendor := vendorCode(raw)
	retryAfter, _ := retryAfterHint(header, now)
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return statusError(domain.CodeAuth,
			"authentication failed with status "+strconv.Itoa(status), vendor, 0, false)
	case http.StatusPaymentRequired:
		return statusError(domain.CodePaymentRequired,
			"payment required with status 402", vendor, 0, false)
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge:
		return statusError(domain.CodeInvalidRequest,
			"invalid request with status "+strconv.Itoa(status), vendor, 0, false)
	case http.StatusTooManyRequests:
		return statusError(domain.CodeRateLimit,
			"rate limited with status 429", vendor, retryAfter, true)
	case http.StatusInternalServerError, http.StatusServiceUnavailable, 529:
		return statusError(domain.CodeUnavailable,
			"provider unavailable with status "+strconv.Itoa(status), vendor, retryAfter, true)
	default:
		if status >= 300 && status < 400 {
			// Redirects are never followed; retrying the same URL is futile.
			return statusError(domain.CodeInvalidOutput,
				"redirect not followed with status "+strconv.Itoa(status), vendor, 0, false)
		}
		if status >= 400 && status < 500 {
			return statusError(domain.CodeInvalidRequest,
				"request failed with status "+strconv.Itoa(status), vendor, 0, false)
		}
		return statusError(domain.CodeUnavailable,
			"provider failed with status "+strconv.Itoa(status), vendor, retryAfter, true)
	}
}

// retryAfterHint reads the vendor retry hint, preferring retry-after-ms (the
// vendor asks clients to read it when they can) over retry-after, and clamps it
// to maxRetryAfter.
func retryAfterHint(header http.Header, now time.Time) (time.Duration, bool) {
	if d, ok := parseRetryAfterMS(header.Get("Retry-After-Ms")); ok {
		return capRetryAfter(d), true
	}
	if d, ok := parseRetryAfter(header.Get("Retry-After"), now); ok {
		return capRetryAfter(d), true
	}
	return 0, false
}

// parseRetryAfterMS parses retry-after-ms as a positive millisecond delay.
func parseRetryAfterMS(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	// Clamp before multiplying so a hostile header cannot overflow Duration.
	if n > int64(maxRetryAfter/time.Millisecond) {
		return maxRetryAfter, true
	}
	return time.Duration(n) * time.Millisecond, true
}

// capRetryAfter clamps d to [0, maxRetryAfter].
func capRetryAfter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// parseRetryAfter parses a Retry-After header as a delay: either delta
// seconds or an HTTP date (delay until then from now, clamped to >= 0).
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
		// Clamp before multiplying so a hostile header cannot overflow Duration.
		if n > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter, true
		}
		return time.Duration(n) * time.Second, true
	}
	if tm, err := http.ParseTime(v); err == nil {
		d := tm.Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// validateRequest rejects malformed advisory requests before any I/O.
func validateRequest(req domain.DecisionRequest) error {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return domain.NewError(domain.CodeInvalidRequest,
			"decision-drex: request model is empty")
	}
	lowered := strings.ToLower(model)
	if strings.HasSuffix(lowered, "-latest") || strings.HasSuffix(lowered, "-preview") {
		return domain.NewError(domain.CodeInvalidRequest,
			"decision-drex: request model alias is not allowed")
	}
	if len(req.Questions) == 0 {
		return domain.NewError(domain.CodeInvalidRequest,
			"decision-drex: request has no questions")
	}
	seen := make(map[string]struct{}, len(req.Questions))
	for _, q := range req.Questions {
		if strings.TrimSpace(q.ID) == "" {
			return domain.NewError(domain.CodeInvalidRequest,
				"decision-drex: question id is empty")
		}
		if _, dup := seen[q.ID]; dup {
			return domain.NewError(domain.CodeInvalidRequest,
				"decision-drex: duplicate question id")
		}
		seen[q.ID] = struct{}{}
		if len(q.Options) > 0 {
			oseen := make(map[string]struct{}, len(q.Options))
			for _, o := range q.Options {
				if strings.TrimSpace(o.Key) == "" {
					return domain.NewError(domain.CodeInvalidRequest,
						"decision-drex: option key is empty")
				}
				if _, dup := oseen[o.Key]; dup {
					return domain.NewError(domain.CodeInvalidRequest,
						"decision-drex: duplicate option key")
				}
				oseen[o.Key] = struct{}{}
			}
		}
		switch q.Type {
		case domain.QuestionChoice:
			if len(q.Options) < 2 {
				return domain.NewError(domain.CodeInvalidRequest,
					"decision-drex: choice needs at least two options")
			}
		case domain.QuestionScore:
			if len(q.Options) < 1 {
				return domain.NewError(domain.CodeInvalidRequest,
					"decision-drex: score needs at least one option")
			}
		case domain.QuestionNoul:
		default:
			return domain.NewError(domain.CodeInvalidRequest,
				"decision-drex: unknown question type")
		}
	}
	return nil
}

// buildRequestBody encodes the wire JSON manually to preserve the option
// order: top-level keys are model, state and questions; per question the
// criteria hold an object (key -> description) for choice and an array of
// descriptions for score. There is no options field on the wire.
func buildRequestBody(req domain.DecisionRequest) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	buf.WriteString(`"model":`)
	writeJSONString(&buf, req.Model)
	buf.WriteString(`,"state":`)
	state := []byte("{}")
	if req.State != nil {
		var err error
		state, err = json.Marshal(req.State)
		if err != nil {
			return nil, domain.WrapError(domain.CodeInvalidRequest,
				"decision-drex: cannot encode state", err)
		}
	}
	buf.Write(state)
	buf.WriteString(`,"questions":{`)
	for i, q := range req.Questions {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeJSONString(&buf, q.ID)
		buf.WriteByte(':')
		buf.WriteString(`{"type":`)
		writeJSONString(&buf, string(q.Type))
		buf.WriteString(`,"instructions":`)
		writeJSONString(&buf, q.Instructions)
		switch q.Type {
		case domain.QuestionChoice:
			buf.WriteString(`,"criteria":{`)
			for j, o := range q.Options {
				if j > 0 {
					buf.WriteByte(',')
				}
				writeJSONString(&buf, o.Key)
				buf.WriteByte(':')
				writeJSONString(&buf, o.Description)
			}
			buf.WriteByte('}')
		case domain.QuestionScore:
			buf.WriteString(`,"criteria":[`)
			for j, o := range q.Options {
				if j > 0 {
					buf.WriteByte(',')
				}
				writeJSONString(&buf, o.Description)
			}
			buf.WriteByte(']')
		case domain.QuestionNoul:
			// No criteria on the wire for noul.
		default:
			return nil, domain.NewError(domain.CodeInvalidRequest,
				"decision-drex: unknown question type")
		}
		buf.WriteByte('}')
	}
	buf.WriteString(`}}`)
	return buf.Bytes(), nil
}

// writeJSONString appends the JSON-quoted string s to buf.
func writeJSONString(buf *bytes.Buffer, s string) {
	b, err := json.Marshal(s)
	if err != nil {
		buf.WriteString(`""`)
		return
	}
	buf.Write(b)
}

// wireUsage mirrors the vendor usage block with snake_case fields. The block
// is optional in the vendor response.
type wireUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// wireAnswer mirrors one vendor answer object.
type wireAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Legend        []string           `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

// wireResponse mirrors the vendor decision response envelope.
//
// Usage, evaluation_time_ms and latency_ms are optional: the port has no
// optional numeric fields, so a missing value is reported as 0 and means
// "unknown", never a measured zero.
type wireResponse struct {
	Model            string                `json:"model"`
	Answers          map[string]wireAnswer `json:"answers"`
	Usage            *wireUsage            `json:"usage"`
	RequestID        string                `json:"request_id"`
	EvaluationTimeMS *float64              `json:"evaluation_time_ms"`
	LatencyMS        *float64              `json:"latency_ms"`
}

// parseResponse decodes, validates and converts a vendor response body.
func parseResponse(raw []byte, req domain.DecisionRequest, price domain.Decimal) (domain.DecisionResponse, error) {
	var wr wireResponse
	if err := json.Unmarshal(raw, &wr); err != nil {
		return domain.DecisionResponse{}, domain.WrapError(domain.CodeInvalidOutput,
			"decision-drex: invalid response JSON", err)
	}
	if strings.TrimSpace(wr.Model) == "" {
		return domain.DecisionResponse{}, domain.NewError(domain.CodeInvalidOutput,
			"decision-drex: response model is empty")
	}
	byID := make(map[string]domain.Question, len(req.Questions))
	for _, q := range req.Questions {
		byID[q.ID] = q
	}
	if len(wr.Answers) != len(req.Questions) {
		return domain.DecisionResponse{}, domain.NewError(domain.CodeInvalidOutput,
			"decision-drex: answer count does not match question count")
	}
	if wr.Usage != nil && (wr.Usage.InputTokens < 0 || wr.Usage.OutputTokens < 0) {
		return domain.DecisionResponse{}, domain.NewError(domain.CodeInvalidOutput,
			"decision-drex: usage token count is negative")
	}
	answers := make(map[string]domain.Answer, len(req.Questions))
	for id, wa := range wr.Answers {
		q, ok := byID[id]
		if !ok {
			return domain.DecisionResponse{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: answer for unknown question")
		}
		if wa.Type != string(q.Type) {
			return domain.DecisionResponse{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: answer type does not match question type")
		}
		a, err := convertAnswer(q, wa)
		if err != nil {
			return domain.DecisionResponse{}, err
		}
		answers[id] = a
	}
	// A missing usage block is valid: the response still carries answers.
	// Zero tokens and a zero cost then mean "not reported", not "free".
	var inputTokens, outputTokens int
	cost := domain.Decimal{}
	if wr.Usage != nil {
		inputTokens = wr.Usage.InputTokens
		outputTokens = wr.Usage.OutputTokens
		var cerr error
		cost, cerr = costUSD(inputTokens, price)
		if cerr != nil {
			return domain.DecisionResponse{}, domain.WrapError(domain.CodeInvalidOutput,
				"decision-drex: cannot compute cost", cerr)
		}
	}
	var vendorLatency int64
	switch {
	case wr.EvaluationTimeMS != nil:
		vendorLatency = int64(math.Round(*wr.EvaluationTimeMS))
	case wr.LatencyMS != nil:
		vendorLatency = int64(math.Round(*wr.LatencyMS))
	}
	out := domain.DecisionResponse{
		Answers:        answers,
		Model:          wr.Model,
		RequestedModel: req.Model,
		Usage: domain.Usage{
			InputTokens:     inputTokens,
			OutputTokens:    outputTokens,
			CostUSD:         cost,
			VendorLatencyMS: vendorLatency,
		},
		RequestID: wr.RequestID,
		Raw:       append(json.RawMessage(nil), raw...),
	}
	return out, nil
}

// convertAnswer validates one answer against its question.
func convertAnswer(q domain.Question, wa wireAnswer) (domain.Answer, error) {
	a := domain.Answer{Type: q.Type}
	switch q.Type {
	case domain.QuestionNoul:
		if wa.Noul == nil || math.IsNaN(*wa.Noul) || math.IsInf(*wa.Noul, 0) ||
			*wa.Noul < 0 || *wa.Noul > 1 {
			return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: noul out of range [0, 1]")
		}
		v := *wa.Noul
		a.Noul = &v
		if wa.Confidence != nil {
			if math.IsNaN(*wa.Confidence) || math.IsInf(*wa.Confidence, 0) ||
				*wa.Confidence < 0 || *wa.Confidence > 1 {
				return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
					"decision-drex: noul confidence out of range [0, 1]")
			}
			c := *wa.Confidence
			a.Confidence = &c
		}
		return a, nil
	case domain.QuestionScore:
		if wa.Score == nil || math.IsNaN(*wa.Score) || math.IsInf(*wa.Score, 0) {
			return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: score is missing or not finite")
		}
		if len(wa.Legend) == 0 {
			return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: score legend is empty")
		}
		if len(wa.Legend) != len(q.Options) {
			return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: score legend length does not match options")
		}
		if len(wa.Probabilities) == 0 {
			return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: score probabilities are empty")
		}
		if len(wa.Probabilities) != len(wa.Legend) {
			return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: score probability count does not match legend")
		}
		sum := 0.0
		for i := range wa.Legend {
			p, ok := wa.Probabilities[strconv.Itoa(i)]
			if !ok {
				return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
					"decision-drex: score probabilities miss an index")
			}
			if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
				return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
					"decision-drex: score probability out of range [0, 1]")
			}
			sum += p
		}
		if math.Abs(sum-1) > 0.001 {
			return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: score probabilities do not sum to 1")
		}
		s := *wa.Score
		a.Score = &s
		a.Legend = append([]string(nil), wa.Legend...)
		a.Probabilities = copyProbs(wa.Probabilities)
		if wa.Confidence != nil {
			if math.IsNaN(*wa.Confidence) || math.IsInf(*wa.Confidence, 0) ||
				*wa.Confidence < 0 || *wa.Confidence > 1 {
				return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
					"decision-drex: score confidence out of range [0, 1]")
			}
			c := *wa.Confidence
			a.Confidence = &c
		}
		return a, nil
	case domain.QuestionChoice:
		return convertChoice(q, wa)
	default:
		return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
			"decision-drex: unknown question type")
	}
}

// convertChoice validates a choice answer: exact key match, finite [0,1]
// values, a unit sum, argmax choice and a matching confidence.
func convertChoice(q domain.Question, wa wireAnswer) (domain.Answer, error) {
	invalid := func(msg string) (domain.Answer, error) {
		return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput, "decision-drex: "+msg)
	}
	n := len(q.Options)
	if n < 2 {
		return invalid("choice needs at least two options")
	}
	if len(wa.Probabilities) != n {
		return invalid("choice probability count does not match options")
	}
	max := math.Inf(-1)
	sum := 0.0
	for _, o := range q.Options {
		p, ok := wa.Probabilities[o.Key]
		if !ok {
			return invalid("choice probabilities miss an option")
		}
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return invalid("choice probability out of range [0, 1]")
		}
		sum += p
		if p > max {
			max = p
		}
	}
	if math.Abs(sum-1) > 0.001 {
		return invalid("choice probabilities do not sum to 1")
	}
	got, ok := wa.Probabilities[wa.Choice]
	if !ok || got != max {
		return invalid("choice is not the argmax")
	}
	if wa.Confidence == nil {
		return invalid("choice confidence is missing")
	}
	if math.IsNaN(*wa.Confidence) || math.IsInf(*wa.Confidence, 0) {
		return invalid("choice confidence is not finite")
	}
	want := recomputeConfidence(n, max)
	if math.Abs(*wa.Confidence-want) > 0.02 {
		return invalid("choice confidence does not match recomputed value")
	}
	a := domain.Answer{
		Type:          domain.QuestionChoice,
		Choice:        wa.Choice,
		Probabilities: copyProbs(wa.Probabilities),
	}
	c := *wa.Confidence
	a.Confidence = &c
	return a, nil
}

// recomputeConfidence rescales the winning probability above a uniform
// baseline: C = (p_max - 1/n) / (1 - 1/n). Callers must pass n >= 2.
func recomputeConfidence(n int, max float64) float64 {
	return (max - 1/float64(n)) / (1 - 1/float64(n))
}

// copyProbs duplicates a probability map.
func copyProbs(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// costUSD computes inputTokens * pricePerBillion / 1e9 rounded to 12 places.
func costUSD(inputTokens int, price domain.Decimal) (domain.Decimal, error) {
	tokens, err := domain.ParseDecimal(strconv.Itoa(inputTokens))
	if err != nil {
		return domain.Decimal{}, err
	}
	bn, err := domain.ParseDecimal(billion)
	if err != nil {
		return domain.Decimal{}, err
	}
	return tokens.Mul(price).QuoRound(bn, costScale)
}
