// Package notifytelegram implements ports.Notifier on top of the Telegram Bot
// API sendMessage method.
//
// The bot token is resolved from the secret store at send time and appears
// only in the request URL. It is never returned in an error message, stored as
// an error cause, or logged by this package.
package notifytelegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

const (
	// DefaultSecretRef is the secret reference holding the bot token; its
	// environment form is VENDING_SECRET_CONN_TELEGRAM_BOTTOKEN.
	DefaultSecretRef = "conn/telegram/botToken"
	// DefaultBaseURL is the public Telegram Bot API endpoint.
	DefaultBaseURL = "https://api.telegram.org"
	// DefaultTimeout is the per-send HTTP deadline.
	DefaultTimeout = 10 * time.Second
	// DefaultMinInterval is the local one-message-per-interval rate limit.
	DefaultMinInterval = time.Minute
	// Version is the provider implementation version.
	Version = "0.1.0-m1"
	// kindNotifier marks notification channels. ports declares this family for
	// the notifier capability; no typed constant exists yet.
	kindNotifier ports.Kind = "notifier"
	// maxBodyBytes caps a response body read.
	maxBodyBytes = 8 << 10
	// maxRetryAfterSeconds caps a provider-requested retry delay.
	maxRetryAfterSeconds = 24 * 60 * 60
	// maxMessageUnits is the Bot API per-message text limit in UTF-16 code
	// units (an emoji outside the BMP counts as two).
	maxMessageUnits = 4096
)

// Config holds the provider connection settings.
type Config struct {
	// ChatID is the target chat (numeric id or @channelusername).
	ChatID string
	// SecretRef is the secret reference resolved via Deps.Secrets.
	// Empty means DefaultSecretRef.
	SecretRef string
	// BaseURL overrides the Bot API base URL. Empty means DefaultBaseURL.
	BaseURL string
	// Timeout is the per-send HTTP deadline. Zero means DefaultTimeout.
	Timeout time.Duration
	// MinInterval is the local send interval. Zero means DefaultMinInterval.
	MinInterval time.Duration
}

// Provider delivers notifications through the Telegram Bot API.
type Provider struct {
	chatID      string
	secretRef   string
	baseURL     string
	client      *http.Client
	secrets     ports.SecretStore
	minInterval time.Duration
	now         func() time.Time

	mu   sync.Mutex
	last time.Time
}

var _ ports.Notifier = (*Provider)(nil)

// New builds a Provider. It validates the configuration before any network
// I/O: ChatID must be present, BaseURL must be absolute http(s) (http only for
// loopback hosts) and both durations must be positive after defaults.
func New(cfg Config, deps ports.Deps) (*Provider, error) {
	chatID, err := validateChatID(cfg.ChatID)
	if err != nil {
		return nil, err
	}
	base, err := validateBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, domain.NewError(domain.CodeInvalidRequest,
			"notify-telegram: timeout must be positive")
	}
	minInterval := cfg.MinInterval
	if minInterval == 0 {
		minInterval = DefaultMinInterval
	}
	if minInterval < 0 {
		return nil, domain.NewError(domain.CodeInvalidRequest,
			"notify-telegram: minInterval must be positive")
	}
	secretRef := strings.TrimSpace(cfg.SecretRef)
	if secretRef == "" {
		secretRef = DefaultSecretRef
	}
	client := cloneClient(deps.HTTPClient)
	client.Timeout = timeout
	clock := deps.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Provider{
		chatID:      chatID,
		secretRef:   secretRef,
		baseURL:     base,
		client:      client,
		secrets:     deps.Secrets,
		minInterval: minInterval,
		now:         clock,
	}, nil
}

// Descriptor advertises the provider identity and capabilities.
func (p *Provider) Descriptor() ports.Descriptor {
	return ports.Descriptor{
		ID:           "notify-telegram",
		Name:         "Telegram notifications",
		Kind:         kindNotifier,
		Version:      Version,
		Capabilities: []string{"sendMessage"},
		ConfigFields: []ports.ConfigField{
			{Name: "chatId", Type: "string", Description: "Target chat id or @channelusername", Required: true},
			{Name: "secretRef", Type: "string", Description: "Secret reference for the bot token", Required: true, Secret: true},
			{Name: "baseUrl", Type: "url", Description: "Bot API base URL override"},
		},
		Degradations: []string{},
	}
}

// Send delivers n through sendMessage.
//
// A local rate limit admits at most one send per MinInterval; a rejected send
// returns CodeRateLimit with the remaining delay and performs no network I/O.
// Text longer than 4096 UTF-16 code units is split on line or word boundaries
// (never inside a rune) and delivered as several messages under the single
// local rate-limit reservation. parse_mode is not set, so text is delivered
// literally and needs no escaping.
func (p *Provider) Send(ctx context.Context, n ports.Notification) error {
	if strings.TrimSpace(n.Text) == "" {
		return domain.NewError(domain.CodeInvalidRequest,
			"notify-telegram: empty notification text")
	}
	if retryAfter, ok := p.reserve(); !ok {
		e := domain.NewError(domain.CodeRateLimit,
			"notify-telegram: local rate limit (one message per interval)")
		e.Retryable = true
		e.RetryAfter = retryAfter
		return e
	}
	token, err := p.botToken(ctx)
	if err != nil {
		return err
	}
	parts := splitMessage(n.Text, maxMessageUnits)
	for i, part := range parts {
		if err := p.sendPart(ctx, token, part); err != nil {
			if len(parts) == 1 {
				return err
			}
			return fmt.Errorf("notify-telegram: part %d of %d: %w", i+1, len(parts), err)
		}
	}
	return nil
}

// sendPart performs one sendMessage call.
func (p *Provider) sendPart(ctx context.Context, token, text string) error {
	payload, err := json.Marshal(sendMessage{
		ChatID:                p.chatID,
		Text:                  text,
		DisableWebPagePreview: true,
	})
	if err != nil {
		return domain.NewError(domain.CodeInvalidRequest,
			"notify-telegram: encode request")
	}
	endpoint, err := p.endpoint(token)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		// The request error embeds the token-bearing URL; drop it.
		return domain.NewError(domain.CodeInvalidRequest,
			"notify-telegram: build request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return transportError(ctx, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		return p.statusError(resp, body)
	}
	if readErr != nil {
		e := domain.NewError(domain.CodeUnavailable,
			"notify-telegram: read response")
		e.Retryable = true
		return e
	}
	return p.checkEnvelope(body)
}

// splitMessage cuts text into chunks of at most limit UTF-16 code units,
// preferring a line break, then a space or tab, and only then a hard cut. Cuts
// always land on a rune boundary, so UTF-8 and surrogate pairs stay intact.
func splitMessage(text string, limit int) []string {
	if limit <= 0 || messageUnits(text) <= limit {
		return []string{text}
	}
	parts := make([]string, 0, messageUnits(text)/limit+1)
	rest := text
	for messageUnits(rest) > limit {
		units := 0
		cut := 0
		lastNewline, lastSpace := 0, 0
		for i := 0; i < len(rest); {
			r, size := utf8.DecodeRuneInString(rest[i:])
			width := utf16.RuneLen(r)
			if width < 0 {
				width = 1
			}
			if units+width > limit {
				break
			}
			units += width
			i += size
			cut = i
			switch r {
			case '\n':
				lastNewline = i
			case ' ', '\t':
				lastSpace = i
			}
		}
		if lastNewline > 0 {
			cut = lastNewline
		} else if lastSpace > 0 {
			cut = lastSpace
		}
		parts = append(parts, rest[:cut])
		rest = rest[cut:]
	}
	if rest != "" {
		parts = append(parts, rest)
	}
	return parts
}

// messageUnits counts UTF-16 code units, the unit of the Bot API 4096 limit.
func messageUnits(s string) int {
	n := 0
	for _, r := range s {
		width := utf16.RuneLen(r)
		if width < 0 {
			width = 1
		}
		n += width
	}
	return n
}

// sendMessage is the Telegram sendMessage request body. parse_mode is left
// unset: the provider does not escape markup, so the text is delivered as-is.
type sendMessage struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

// botToken resolves the bot token, mapping every failure to CodeAuth so the
// secret-store detail never reaches the caller.
func (p *Provider) botToken(ctx context.Context) (string, error) {
	if p.secrets == nil {
		return "", domain.NewError(domain.CodeAuth,
			"notify-telegram: secret store is not configured")
	}
	token, err := p.secrets.Get(ctx, p.secretRef)
	if err != nil || strings.TrimSpace(token) == "" {
		return "", domain.NewError(domain.CodeAuth,
			"notify-telegram: cannot resolve bot token")
	}
	return strings.TrimSpace(token), nil
}

// endpoint builds <BaseURL>/bot<token>/sendMessage. The token is percent-
// escaped by url.URL so it cannot inject query or fragment components.
func (p *Provider) endpoint(token string) (string, error) {
	u, err := url.Parse(p.baseURL)
	if err != nil || u.Host == "" {
		return "", domain.NewError(domain.CodeInvalidRequest,
			"notify-telegram: invalid base URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/bot" + token + "/sendMessage"
	u.RawPath = ""
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), nil
}

// reserve admits one send per MinInterval and returns the remaining delay when
// it rejects the call.
func (p *Provider) reserve() (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.nowUTC()
	if !p.last.IsZero() {
		if elapsed := now.Sub(p.last); elapsed < p.minInterval {
			remaining := p.minInterval - elapsed
			if remaining <= 0 {
				remaining = time.Nanosecond
			}
			if remaining > p.minInterval {
				remaining = p.minInterval
			}
			return remaining, false
		}
	}
	p.last = now
	return 0, true
}

// nowUTC returns the current time in UTC via the injected clock.
func (p *Provider) nowUTC() time.Time {
	if p.now != nil {
		return p.now().UTC()
	}
	return time.Now().UTC()
}

// statusError maps a non-200 response to a coded error. The response body is
// used only for parameters.retry_after and never copied into a message.
func (p *Provider) statusError(resp *http.Response, body []byte) error {
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return domain.NewError(domain.CodeAuth,
			"notify-telegram: bot token rejected")
	case resp.StatusCode == http.StatusBadRequest:
		return domain.NewError(domain.CodeInvalidRequest,
			"notify-telegram: message rejected (400)")
	case resp.StatusCode == http.StatusTooManyRequests:
		e := domain.NewError(domain.CodeRateLimit, "notify-telegram: rate limited")
		e.Retryable = true
		if ra := retryAfter(body, resp.Header.Get("Retry-After"), p.nowUTC()); ra > 0 {
			e.RetryAfter = ra
			e.Message = fmt.Sprintf("notify-telegram: rate limited (retry after %s)", ra)
		}
		return e
	case resp.StatusCode >= 500:
		e := domain.NewError(domain.CodeUnavailable,
			fmt.Sprintf("notify-telegram: upstream status %d", resp.StatusCode))
		e.Retryable = true
		return e
	default:
		return domain.NewError(domain.CodeInvalidOutput,
			fmt.Sprintf("notify-telegram: unexpected status %d", resp.StatusCode))
	}
}

// apiEnvelope is the Bot API response envelope. A 200 with ok=false means the
// message was not delivered, so it must not be reported as success.
type apiEnvelope struct {
	OK        bool `json:"ok"`
	ErrorCode int  `json:"error_code"`
}

// checkEnvelope turns a 200 body into an error unless it carries ok=true.
// error_code is mapped like the matching HTTP status (a 400 is a caller
// invalid_request, a 429 a retryable rate_limit, 401/403 auth).
func (p *Provider) checkEnvelope(body []byte) error {
	var env apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return domain.NewError(domain.CodeInvalidOutput,
			"notify-telegram: invalid response JSON")
	}
	if env.OK {
		return nil
	}
	switch env.ErrorCode {
	case http.StatusBadRequest:
		return domain.NewError(domain.CodeInvalidRequest,
			"notify-telegram: message rejected (400)")
	case http.StatusUnauthorized, http.StatusForbidden:
		return domain.NewError(domain.CodeAuth,
			"notify-telegram: bot token rejected")
	case http.StatusTooManyRequests:
		e := domain.NewError(domain.CodeRateLimit, "notify-telegram: rate limited")
		e.Retryable = true
		if ra := retryAfter(body, "", p.nowUTC()); ra > 0 {
			e.RetryAfter = ra
			e.Message = fmt.Sprintf("notify-telegram: rate limited (retry after %s)", ra)
		}
		return e
	default:
		return domain.NewError(domain.CodeInvalidOutput,
			fmt.Sprintf("notify-telegram: message not delivered (error_code %d)", env.ErrorCode))
	}
}

// transportError classifies a failed round trip without wrapping err: the
// *url.Error message carries the token-bearing URL.
func transportError(ctx context.Context, err error) error {
	timedOut := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	var urlErr *url.Error
	if !timedOut && errors.As(err, &urlErr) && urlErr.Timeout() {
		timedOut = true
	}
	if ctx != nil && ctx.Err() != nil {
		timedOut = true
	}
	if timedOut {
		e := domain.NewError(domain.CodeTimeout,
			"notify-telegram: request canceled or timed out")
		e.Retryable = true
		return e
	}
	e := domain.NewError(domain.CodeUnavailable, "notify-telegram: transport error")
	e.Retryable = true
	return e
}

// retryAfter resolves the provider-requested delay from the JSON body
// parameters.retry_after, then from the Retry-After header.
func retryAfter(body []byte, header string, now time.Time) time.Duration {
	var envelope struct {
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		secs := envelope.Parameters.RetryAfter
		if secs > 0 {
			if secs > maxRetryAfterSeconds {
				secs = maxRetryAfterSeconds
			}
			return time.Duration(secs) * time.Second
		}
	}
	return parseRetryAfterHeader(header, now)
}

// parseRetryAfterHeader decodes a Retry-After header (delta seconds or HTTP
// date) into a non-negative delay computed against now.
func parseRetryAfterHeader(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		if secs > maxRetryAfterSeconds {
			secs = maxRetryAfterSeconds
		}
		return time.Duration(secs) * time.Second
	}
	for _, layout := range []string{http.TimeFormat, time.RFC1123, time.RFC1123Z} {
		if t, err := time.Parse(layout, v); err == nil {
			if d := t.Sub(now); d > 0 {
				return d
			}
			return 0
		}
	}
	return 0
}

// validateChatID rejects empty chat ids and ids with whitespace or control
// characters.
func validateChatID(raw string) (string, error) {
	invalid := func() (string, error) {
		return "", domain.NewError(domain.CodeInvalidRequest,
			"notify-telegram: invalid chat id")
	}
	id := strings.TrimSpace(raw)
	if id == "" {
		return invalid()
	}
	for _, r := range id {
		if r <= ' ' || r == 0x7f {
			return invalid()
		}
	}
	return id, nil
}

// validateBaseURL enforces the base URL policy before any network I/O: the
// value must be an absolute http(s) URL without userinfo, query or fragment.
// https is always allowed; http only for loopback hosts (127.0.0.1, ::1).
func validateBaseURL(raw string) (string, error) {
	invalid := func() (string, error) {
		return "", domain.NewError(domain.CodeInvalidRequest,
			"notify-telegram: invalid base URL")
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		trimmed = DefaultBaseURL
	}
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
