package notifytelegram_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
	notifytelegram "github.com/LevapVeeskela/lv24-vending-machine/providers/notify-telegram"
)

const testChatID = "-1001234567890"

// testToken is assembled at runtime so no repository file holds a literal
// bot-token-shaped string (digits, colon, 35 token characters).
var testToken = fmt.Sprintf("%d:%s", 123456789,
	strings.Join([]string{"AAH", "secret", "token", "value", "do", "not", "leak"}, "-"))

// fakeSecrets is an in-memory ports.SecretStore.
type fakeSecrets struct {
	value string
	err   error
	calls atomic.Int64
}

func (f *fakeSecrets) Get(_ context.Context, _ string) (string, error) {
	f.calls.Add(1)
	if f.err != nil {
		return "", f.err
	}
	return f.value, nil
}

// newProvider builds a provider with test defaults.
func newProvider(t *testing.T, baseURL string, deps ports.Deps) *notifytelegram.Provider {
	t.Helper()
	p, err := notifytelegram.New(notifytelegram.Config{
		ChatID:      testChatID,
		SecretRef:   "conn/telegram/botToken",
		BaseURL:     baseURL,
		Timeout:     5 * time.Second,
		MinInterval: time.Nanosecond,
	}, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// assertSanitized fails when s leaks the token or a URL.
func assertSanitized(t *testing.T, label, s string) {
	t.Helper()
	for _, bad := range []string{testToken, "http://", "https://", "127.0.0.1"} {
		if strings.Contains(s, bad) {
			t.Fatalf("%s leaks %q: %s", label, bad, s)
		}
	}
}

// TestSendSuccess checks the request shape and the happy path.
func TestSendSuccess(t *testing.T) {
	var gotPath, gotContentType, gotRaw string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		gotRaw = string(raw)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
	}))
	defer srv.Close()

	secrets := &fakeSecrets{value: testToken}
	p := newProvider(t, srv.URL, ports.Deps{Secrets: secrets})
	if err := p.Send(t.Context(), ports.Notification{Kind: "verdict", Text: "gate verdict: long"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if want := "/bot" + testToken + "/sendMessage"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if secrets.calls.Load() != 1 {
		t.Errorf("secret calls = %d, want 1", secrets.calls.Load())
	}
	if gotBody["chat_id"] != testChatID {
		t.Errorf("chat_id = %v, want %q", gotBody["chat_id"], testChatID)
	}
	if gotBody["text"] != "gate verdict: long" {
		t.Errorf("text = %v", gotBody["text"])
	}
	if gotBody["disable_web_page_preview"] != true {
		t.Errorf("disable_web_page_preview = %v, want true", gotBody["disable_web_page_preview"])
	}
	if len(gotBody) != 3 {
		t.Errorf("body fields = %v, want 3 fields", gotBody)
	}
	if strings.Contains(gotRaw, testToken) {
		t.Error("request body contains the bot token")
	}
}

// TestSendStatusMapping checks non-200 mapping and retry metadata.
func TestSendStatusMapping(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		header     map[string]string
		code       domain.ErrorCode
		retryable  bool
		retryAfter time.Duration
	}{
		{name: "unauthorized", status: 401, body: `{"ok":false}`, code: domain.CodeAuth},
		{name: "forbidden", status: 403, body: `{"ok":false}`, code: domain.CodeAuth},
		{
			name: "rate limit from json", status: 429,
			body: `{"ok":false,"error_code":429,"parameters":{"retry_after":7}}`,
			code: domain.CodeRateLimit, retryable: true, retryAfter: 7 * time.Second,
		},
		{
			name: "rate limit from header", status: 429,
			body:   `{"ok":false}`,
			header: map[string]string{"Retry-After": "3"},
			code:   domain.CodeRateLimit, retryable: true, retryAfter: 3 * time.Second,
		},
		{name: "rate limit without hint", status: 429, body: `{"ok":false}`, code: domain.CodeRateLimit, retryable: true},
		{name: "server error", status: 500, body: `{"ok":false}`, code: domain.CodeUnavailable, retryable: true},
		{name: "service unavailable", status: 503, body: `{"ok":false}`, code: domain.CodeUnavailable, retryable: true},
		{name: "bad request", status: 400, body: `{"ok":false}`, code: domain.CodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			p := newProvider(t, srv.URL, ports.Deps{Secrets: &fakeSecrets{value: testToken}})
			err := p.Send(t.Context(), ports.Notification{Text: "hello"})
			if err == nil {
				t.Fatal("Send = nil error, want failure")
			}
			if got := domain.CodeOf(err); got != tc.code {
				t.Fatalf("code = %q, want %q (err %v)", got, tc.code, err)
			}
			var de *domain.Error
			if !errors.As(err, &de) {
				t.Fatalf("error %T is not *domain.Error", err)
			}
			if de.Retryable != tc.retryable {
				t.Errorf("Retryable = %v, want %v", de.Retryable, tc.retryable)
			}
			if got := domain.RetryAfterOf(err); got != tc.retryAfter {
				t.Errorf("RetryAfter = %s, want %s", got, tc.retryAfter)
			}
			if domain.IsRetryable(err) != tc.retryable {
				t.Errorf("IsRetryable = %v, want %v", domain.IsRetryable(err), tc.retryable)
			}
			assertSanitized(t, "error", err.Error())
		})
	}
}

// TestNetworkErrorSanitized checks that a transport failure and its slog
// rendering carry neither the token nor the request URL.
func TestNetworkErrorSanitized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	baseURL := srv.URL
	srv.Close()

	p := newProvider(t, baseURL, ports.Deps{Secrets: &fakeSecrets{value: testToken}})
	err := p.Send(t.Context(), ports.Notification{Text: "hello"})
	if err == nil {
		t.Fatal("Send = nil error, want transport failure")
	}
	if got := domain.CodeOf(err); got != domain.CodeUnavailable {
		t.Fatalf("code = %q, want unavailable", got)
	}
	if !domain.IsRetryable(err) {
		t.Error("IsRetryable = false, want true")
	}
	assertSanitized(t, "error", err.Error())
	assertSanitized(t, "formatted error", fmt.Sprintf("%v %+v", err, err))

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		t.Fatalf("error chain wraps *url.Error: %v", urlErr)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Error("send failed", "err", err)
	assertSanitized(t, "slog output", buf.String())
}

// TestTimeout checks that a slow upstream maps to a sanitized timeout.
func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, err := notifytelegram.New(notifytelegram.Config{
		ChatID:      testChatID,
		BaseURL:     srv.URL,
		Timeout:     30 * time.Millisecond,
		MinInterval: time.Nanosecond,
	}, ports.Deps{Secrets: &fakeSecrets{value: testToken}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sendErr := p.Send(t.Context(), ports.Notification{Text: "hello"})
	if sendErr == nil {
		t.Fatal("Send = nil error, want timeout")
	}
	if got := domain.CodeOf(sendErr); got != domain.CodeTimeout {
		t.Fatalf("code = %q, want timeout (err %v)", got, sendErr)
	}
	if !domain.IsRetryable(sendErr) {
		t.Error("IsRetryable = false, want true")
	}
	assertSanitized(t, "error", sendErr.Error())

	var urlErr *url.Error
	if errors.As(sendErr, &urlErr) {
		t.Fatalf("error chain wraps *url.Error: %v", urlErr)
	}
}

// TestLocalRateLimit checks that only one request reaches the network per
// interval.
func TestLocalRateLimit(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
	}))
	defer srv.Close()

	p, err := notifytelegram.New(notifytelegram.Config{
		ChatID:      testChatID,
		BaseURL:     srv.URL,
		Timeout:     5 * time.Second,
		MinInterval: time.Minute,
	}, ports.Deps{Secrets: &fakeSecrets{value: testToken}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Send(t.Context(), ports.Notification{Text: "first"}); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	for i := 0; i < 2; i++ {
		rateErr := p.Send(t.Context(), ports.Notification{Text: "again"})
		if got := domain.CodeOf(rateErr); got != domain.CodeRateLimit {
			t.Fatalf("code = %q, want rate_limit", got)
		}
		if !domain.IsRetryable(rateErr) {
			t.Error("IsRetryable = false, want true")
		}
		if ra := domain.RetryAfterOf(rateErr); ra <= 0 || ra > time.Minute {
			t.Errorf("RetryAfter = %s, want (0, 1m]", ra)
		}
		assertSanitized(t, "error", rateErr.Error())
	}
	if hits.Load() != 1 {
		t.Errorf("server hits = %d, want 1", hits.Load())
	}
}

// TestRateLimitWindowAdvances checks that the injected clock releases the
// limit after MinInterval.
func TestRateLimitWindowAdvances(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
	}))
	defer srv.Close()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p, err := notifytelegram.New(notifytelegram.Config{
		ChatID:      testChatID,
		BaseURL:     srv.URL,
		Timeout:     5 * time.Second,
		MinInterval: time.Minute,
	}, ports.Deps{
		Secrets: &fakeSecrets{value: testToken},
		Clock:   func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Send(t.Context(), ports.Notification{Text: "first"}); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if got := domain.CodeOf(p.Send(t.Context(), ports.Notification{Text: "second"})); got != domain.CodeRateLimit {
		t.Fatalf("code = %q, want rate_limit", got)
	}
	now = now.Add(time.Minute)
	if err := p.Send(t.Context(), ports.Notification{Text: "third"}); err != nil {
		t.Fatalf("Send after window: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("server hits = %d, want 2", hits.Load())
	}
}

// TestSecretsFailures checks that secret failures map to auth and never touch
// the network.
func TestSecretsFailures(t *testing.T) {
	cases := []struct {
		name    string
		secrets ports.SecretStore
	}{
		{name: "unconfigured", secrets: nil},
		{name: "store error", secrets: &fakeSecrets{err: errors.New("keychain locked")}},
		{name: "empty token", secrets: &fakeSecrets{value: "  "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			p := newProvider(t, srv.URL, ports.Deps{Secrets: tc.secrets})
			err := p.Send(t.Context(), ports.Notification{Text: "hello"})
			if got := domain.CodeOf(err); got != domain.CodeAuth {
				t.Fatalf("code = %q, want auth (err %v)", got, err)
			}
			if domain.IsRetryable(err) {
				t.Error("IsRetryable = true, want false")
			}
			assertSanitized(t, "error", err.Error())
			if hits.Load() != 0 {
				t.Errorf("server hits = %d, want 0", hits.Load())
			}
		})
	}
}

// TestEmptyTextRejected checks cheap validation without network or quota use.
func TestEmptyTextRejected(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
	}))
	defer srv.Close()

	p, err := notifytelegram.New(notifytelegram.Config{
		ChatID:      testChatID,
		BaseURL:     srv.URL,
		Timeout:     5 * time.Second,
		MinInterval: time.Minute,
	}, ports.Deps{Secrets: &fakeSecrets{value: testToken}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := domain.CodeOf(p.Send(t.Context(), ports.Notification{Text: "   "})); got != domain.CodeInvalidRequest {
		t.Fatalf("code = %q, want invalid_request", got)
	}
	if err := p.Send(t.Context(), ports.Notification{Text: "hello"}); err != nil {
		t.Fatalf("Send after rejection: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("server hits = %d, want 1", hits.Load())
	}
}

// TestNewValidation checks constructor validation without network.
func TestNewValidation(t *testing.T) {
	invalid := []struct {
		name string
		cfg  notifytelegram.Config
	}{
		{name: "empty chat id", cfg: notifytelegram.Config{}},
		{name: "blank chat id", cfg: notifytelegram.Config{ChatID: "   "}},
		{name: "chat id with space", cfg: notifytelegram.Config{ChatID: "12 34"}},
		{name: "chat id control char", cfg: notifytelegram.Config{ChatID: "12\n34"}},
		{name: "malformed url", cfg: notifytelegram.Config{ChatID: testChatID, BaseURL: "://bad"}},
		{name: "relative url", cfg: notifytelegram.Config{ChatID: testChatID, BaseURL: "api.telegram.org"}},
		{name: "ftp url", cfg: notifytelegram.Config{ChatID: testChatID, BaseURL: "ftp://api.telegram.org"}},
		{name: "plain http remote", cfg: notifytelegram.Config{ChatID: testChatID, BaseURL: "http://api.telegram.org"}},
		{name: "plain http localhost", cfg: notifytelegram.Config{ChatID: testChatID, BaseURL: "http://localhost:8080"}},
		{name: "userinfo", cfg: notifytelegram.Config{ChatID: testChatID, BaseURL: "https://user:pass@api.telegram.org"}},
		{name: "query", cfg: notifytelegram.Config{ChatID: testChatID, BaseURL: "https://api.telegram.org/?a=1"}},
		{name: "negative timeout", cfg: notifytelegram.Config{ChatID: testChatID, Timeout: -time.Second}},
		{name: "negative min interval", cfg: notifytelegram.Config{ChatID: testChatID, MinInterval: -time.Second}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			p, err := notifytelegram.New(tc.cfg, ports.Deps{})
			if err == nil {
				t.Fatalf("New = %v, want invalid_request", p)
			}
			if got := domain.CodeOf(err); got != domain.CodeInvalidRequest {
				t.Fatalf("code = %q, want invalid_request", got)
			}
			assertSanitized(t, "error", err.Error())
		})
	}

	valid := []struct {
		name string
		cfg  notifytelegram.Config
	}{
		{name: "defaults", cfg: notifytelegram.Config{ChatID: testChatID}},
		{name: "loopback http", cfg: notifytelegram.Config{ChatID: testChatID, BaseURL: "http://127.0.0.1:8080"}},
		{name: "loopback ipv6 http", cfg: notifytelegram.Config{ChatID: testChatID, BaseURL: "http://[::1]:8080"}},
		{name: "https with path", cfg: notifytelegram.Config{ChatID: "@mychannel", BaseURL: "https://api.telegram.org/"}},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			p, err := notifytelegram.New(tc.cfg, ports.Deps{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if p == nil {
				t.Fatal("New = nil provider")
			}
		})
	}
}

// TestDescriptor checks the provider identity.
func TestDescriptor(t *testing.T) {
	p, err := notifytelegram.New(notifytelegram.Config{ChatID: testChatID}, ports.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := p.Descriptor()
	if d.ID != "notify-telegram" {
		t.Errorf("ID = %q, want notify-telegram", d.ID)
	}
	if string(d.Kind) != "notifier" {
		t.Errorf("Kind = %q, want notifier", d.Kind)
	}
	if d.Version != notifytelegram.Version {
		t.Errorf("Version = %q, want %q", d.Version, notifytelegram.Version)
	}
	if len(d.ConfigFields) != 3 {
		t.Errorf("ConfigFields = %+v, want 3", d.ConfigFields)
	}
}

// testUnits counts UTF-16 code units, the unit of the Bot API 4096 limit.
func testUnits(s string) int {
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

// longMultipartText is well above 4096 UTF-16 units and contains Cyrillic and
// an emoji so a broken split would show up as invalid UTF-8.
func longMultipartText() string {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "Строка %03d: цена выросла на 1.5%% 🙂\n", i)
	}
	return b.String()
}

// TestSendDeliversOKFalseAsError checks that a 200 response with ok=false is
// never reported as a delivered message and is mapped by its error_code.
func TestSendDeliversOKFalseAsError(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		code       domain.ErrorCode
		retryable  bool
		retryAfter time.Duration
	}{
		{
			name: "bad request",
			body: `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`,
			code: domain.CodeInvalidRequest,
		},
		{
			name: "rate limit",
			body: `{"ok":false,"error_code":429,"parameters":{"retry_after":9}}`,
			code: domain.CodeRateLimit, retryable: true, retryAfter: 9 * time.Second,
		},
		{name: "unauthorized", body: `{"ok":false,"error_code":401}`, code: domain.CodeAuth},
		{name: "no error code", body: `{"ok":false}`, code: domain.CodeInvalidOutput},
		{name: "malformed json", body: `not json`, code: domain.CodeInvalidOutput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			p := newProvider(t, srv.URL, ports.Deps{Secrets: &fakeSecrets{value: testToken}})
			err := p.Send(t.Context(), ports.Notification{Text: "hello"})
			if err == nil {
				t.Fatal("Send = nil error, want a delivery failure")
			}
			if got := domain.CodeOf(err); got != tc.code {
				t.Fatalf("code = %q, want %q (err %v)", got, tc.code, err)
			}
			if domain.IsRetryable(err) != tc.retryable {
				t.Errorf("IsRetryable = %v, want %v", domain.IsRetryable(err), tc.retryable)
			}
			if got := domain.RetryAfterOf(err); got != tc.retryAfter {
				t.Errorf("RetryAfter = %s, want %s", got, tc.retryAfter)
			}
			assertSanitized(t, "error", err.Error())
		})
	}
}

// TestSendSplitsLongText checks that text above 4096 UTF-16 units is split on
// line boundaries, never breaks UTF-8, and is sent under a single local
// rate-limit reservation.
func TestSendSplitsLongText(t *testing.T) {
	text := longMultipartText()
	if got := testUnits(text); got <= 4096 {
		t.Fatalf("fixture units = %d, want > 4096", got)
	}
	var mu sync.Mutex
	var parts []string
	var parseModes []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		part, _ := body["text"].(string)
		mu.Lock()
		parts = append(parts, part)
		parseModes = append(parseModes, body["parse_mode"])
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
	}))
	defer srv.Close()

	// A one-minute interval proves the split parts share one reservation: a
	// per-part reserve would rate-limit the second message.
	p, err := notifytelegram.New(notifytelegram.Config{
		ChatID:      testChatID,
		BaseURL:     srv.URL,
		Timeout:     5 * time.Second,
		MinInterval: time.Minute,
	}, ports.Deps{Secrets: &fakeSecrets{value: testToken}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Send(t.Context(), ports.Notification{Text: text}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(parts) < 2 {
		t.Fatalf("parts = %d, want at least 2", len(parts))
	}
	var joined strings.Builder
	for i, part := range parts {
		if units := testUnits(part); units > 4096 {
			t.Errorf("part %d units = %d, want <= 4096", i, units)
		}
		if !utf8.ValidString(part) {
			t.Errorf("part %d is not valid UTF-8", i)
		}
		if strings.ContainsRune(part, utf8.RuneError) {
			t.Errorf("part %d contains a replacement rune", i)
		}
		joined.WriteString(part)
	}
	if got := joined.String(); got != text {
		t.Errorf("joined text differs from the original (%d vs %d bytes)", len(got), len(text))
	}
	if !strings.HasSuffix(parts[0], "\n") {
		t.Errorf("first part does not end on a line boundary: %q", parts[0][len(parts[0])-20:])
	}
	for i, mode := range parseModes {
		if mode != nil {
			t.Errorf("part %d parse_mode = %v, want unset", i, mode)
		}
	}
}

// TestSendSplitsEmojiWithoutBreakingRunes checks a hard split of a string
// without line or word breaks: surrogate pairs must stay intact.
func TestSendSplitsEmojiWithoutBreakingRunes(t *testing.T) {
	const emojiCount = 3000 // 6000 UTF-16 units
	text := strings.Repeat("🙂", emojiCount)
	var mu sync.Mutex
	var parts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		mu.Lock()
		parts = append(parts, body.Text)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
	}))
	defer srv.Close()

	p := newProvider(t, srv.URL, ports.Deps{Secrets: &fakeSecrets{value: testToken}})
	if err := p.Send(t.Context(), ports.Notification{Text: text}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	var joined strings.Builder
	for i, part := range parts {
		if units := testUnits(part); units > 4096 {
			t.Errorf("part %d units = %d, want <= 4096", i, units)
		}
		if !utf8.ValidString(part) {
			t.Errorf("part %d is not valid UTF-8", i)
		}
		if got := strings.Count(part, "🙂"); got*2 != testUnits(part) {
			t.Errorf("part %d split an emoji (runes %d, units %d)", i, got, testUnits(part))
		}
		joined.WriteString(part)
	}
	if got := joined.String(); got != text {
		t.Errorf("joined text differs from the original")
	}
}

// TestSendMultipartFailurePropagates checks that a failed later part is an
// error and does not hide which part failed.
func TestSendMultipartFailurePropagates(t *testing.T) {
	var mu sync.Mutex
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		attempt := hits
		mu.Unlock()
		if attempt == 1 {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request"}`)
	}))
	defer srv.Close()

	p := newProvider(t, srv.URL, ports.Deps{Secrets: &fakeSecrets{value: testToken}})
	err := p.Send(t.Context(), ports.Notification{Text: longMultipartText()})
	if err == nil {
		t.Fatal("Send = nil error, want the second part to fail")
	}
	if got := domain.CodeOf(err); got != domain.CodeInvalidRequest {
		t.Fatalf("code = %q, want invalid_request (err %v)", got, err)
	}
	if !strings.Contains(err.Error(), "part 2 of 2") {
		t.Errorf("error = %q, want it to name the failed part", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
	assertSanitized(t, "error", err.Error())
}
