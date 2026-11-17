package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/contracts"
	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/gateflow"
)

const (
	testToken = "test-local-token"
	testAddr  = "127.0.0.1:8420"
)

const validRequest = `{"signalId":"sig-api-001",` +
	`"instrument":{"venue":"binance","symbol":"BTCUSDT"},` +
	`"side":"buy","intentKind":"entry","timeframe":"1h",` +
	`"ts":"2026-11-16T06:00:00Z","price":"67432.50",` +
	`"source":"test","state":{"rsi":55.5}}`

// fakeEvaluator replays scripted results without network I/O.
type fakeEvaluator struct {
	mu    sync.Mutex
	calls int
	fn    func(ctx context.Context, req domain.GateRequest) (gateflow.Result, error)
}

func (f *fakeEvaluator) Evaluate(ctx context.Context, req domain.GateRequest) (gateflow.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.fn(ctx, req)
}

func (f *fakeEvaluator) numCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func testVerdict() domain.GateVerdict {
	return domain.GateVerdict{
		Verdict:    domain.VerdictAllow,
		Code:       "allow",
		Reasons:    []string{"all checks passed"},
		Checks:     []domain.Check{{Name: "schema", Passed: true}},
		DecisionID: "dec-test-001",
		Model:      "drex-max-v1.1",
		PolicyHash: strings.Repeat("ab", 32),
		DecidedAt:  time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC),
	}
}

func newTestHandler(t *testing.T, ev Evaluator, cfg Config) *Handler {
	t.Helper()
	cfg.Token = testToken
	h, err := New(cfg, ev, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func allowEvaluator() *fakeEvaluator {
	return &fakeEvaluator{
		fn: func(_ context.Context, req domain.GateRequest) (gateflow.Result, error) {
			return gateflow.Result{SignalID: req.SignalID, Verdict: testVerdict()}, nil
		},
	}
}

func doGate(t *testing.T, h *Handler, method, body, contentType, auth string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/v1/gate", rd)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode error envelope: %v (%q)", err, rec.Body.String())
	}
	return v
}

func TestNewEmptyToken(t *testing.T) {
	_, err := New(Config{}, allowEvaluator(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("New with empty token must fail")
	}
	_, err = New(Config{Token: testToken}, nil, slog.Default())
	if err == nil {
		t.Fatal("New with nil evaluator must fail")
	}
}

func TestAuth(t *testing.T) {
	h := newTestHandler(t, allowEvaluator(), Config{})
	for _, tc := range []struct {
		name string
		auth string
	}{
		{"missing", ""},
		{"malformed", "Token abc"},
		{"bare", "Bearer"},
		{"empty", "Bearer "},
		{"wrong", "Bearer wrong-token"},
	} {
		rec := doGate(t, h, http.MethodPost, validRequest, "application/json", tc.auth)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", tc.name, rec.Code)
		}
		if got := decodeErr(t, rec)["code"]; got != "unauthorized" {
			t.Fatalf("%s: code = %v, want unauthorized", tc.name, got)
		}
	}
	rec := doGate(t, h, http.MethodPost, validRequest, "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

func TestContentType(t *testing.T) {
	h := newTestHandler(t, allowEvaluator(), Config{})
	auth := "Bearer " + testToken
	for _, tc := range []struct {
		name string
		ct   string
	}{
		{"missing", ""},
		{"text", "text/plain"},
	} {
		rec := doGate(t, h, http.MethodPost, validRequest, tc.ct, auth)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("%s: status = %d, want 415", tc.name, rec.Code)
		}
		if got := decodeErr(t, rec)["code"]; got != "unsupported_media_type" {
			t.Fatalf("%s: code = %v", tc.name, got)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	h := newTestHandler(t, allowEvaluator(), Config{MaxBodyBytes: 16})
	rec := doGate(t, h, http.MethodPost, validRequest, "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if got := decodeErr(t, rec)["code"]; got != "payload_too_large" {
		t.Fatalf("code = %v, want payload_too_large", got)
	}
}

func TestTrailingJSON(t *testing.T) {
	h := newTestHandler(t, allowEvaluator(), Config{})
	rec := doGate(t, h, http.MethodPost, validRequest+" {}", "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUnknownField(t *testing.T) {
	h := newTestHandler(t, allowEvaluator(), Config{})
	bad := strings.Replace(validRequest, `"source":"test"`, `"source":"test","bogus":1`, 1)
	rec := doGate(t, h, http.MethodPost, bad, "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestEmptyBody(t *testing.T) {
	h := newTestHandler(t, allowEvaluator(), Config{})
	rec := doGate(t, h, http.MethodPost, "", "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestMethod(t *testing.T) {
	h := newTestHandler(t, allowEvaluator(), Config{})
	rec := doGate(t, h, http.MethodGet, "", "", "Bearer "+testToken)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", allow)
	}
	if got := decodeErr(t, rec)["code"]; got != "method_not_allowed" {
		t.Fatalf("code = %v", got)
	}
}

func TestNotFound(t *testing.T) {
	h := newTestHandler(t, allowEvaluator(), Config{})
	req := httptest.NewRequest(http.MethodGet, "/v1/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := decodeErr(t, rec)["code"]; got != "not_found" {
		t.Fatalf("code = %v", got)
	}
}

func TestHealthz(t *testing.T) {
	h := newTestHandler(t, allowEvaluator(), Config{Version: "v-test"})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var v struct {
		Status  string `json:"status"`
		Mode    string `json:"mode"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	if v.Status != "ok" || v.Mode != "shadow" || v.Version != "v-test" {
		t.Fatalf("healthz = %+v", v)
	}
}

func TestModeReporting(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  string
		want string
	}{
		{"explicit", "paper", "paper"},
		{"default", "", "shadow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(t, allowEvaluator(), Config{Version: "v-test", Mode: tc.cfg})
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("healthz status = %d, want 200", rec.Code)
			}
			var hb struct {
				Mode string `json:"mode"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &hb); err != nil {
				t.Fatalf("decode healthz: %v", err)
			}
			if hb.Mode != tc.want {
				t.Fatalf("healthz mode = %q, want %q", hb.Mode, tc.want)
			}
			rec = doGate(t, h, http.MethodPost, validRequest, "application/json", "Bearer "+testToken)
			if rec.Code != http.StatusOK {
				t.Fatalf("gate status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}
			// The contract enum now covers shadow|paper|sandbox, so the response
			// decodes into GateVerdictResponse directly.
			var gb contracts.GateVerdictResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &gb); err != nil {
				t.Fatalf("decode gate: %v", err)
			}
			if string(gb.Mode) != tc.want {
				t.Fatalf("gate mode = %q, want %q", gb.Mode, tc.want)
			}
		})
	}
}

func TestHappyPath(t *testing.T) {
	h := newTestHandler(t, allowEvaluator(), Config{Version: "v-test"})
	rec := doGate(t, h, http.MethodPost, validRequest, "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	var v contracts.GateVerdictResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode GateVerdictResponse: %v (%s)", err, rec.Body.String())
	}
	if string(v.Mode) != "shadow" {
		t.Fatalf("mode = %q", v.Mode)
	}
	if v.SignalID != "sig-api-001" {
		t.Fatalf("signalId = %q", v.SignalID)
	}
	if string(v.Verdict.Verdict) != "ALLOW" {
		t.Fatalf("verdict = %q", v.Verdict.Verdict)
	}
}

func TestReplay(t *testing.T) {
	ev := &fakeEvaluator{
		fn: func(_ context.Context, req domain.GateRequest) (gateflow.Result, error) {
			return gateflow.Result{SignalID: req.SignalID, Verdict: testVerdict(), Reused: true}, nil
		},
	}
	h := newTestHandler(t, ev, Config{})
	rec := doGate(t, h, http.MethodPost, validRequest, "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var v contracts.GateVerdictResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if v.SignalID != "sig-api-001" || string(v.Verdict.Verdict) != "ALLOW" {
		t.Fatalf("replay = %+v", v)
	}
}

func TestProviderErrorStill200(t *testing.T) {
	abstain := testVerdict()
	abstain.Verdict = domain.VerdictAbstain
	abstain.Code = "provider_unavailable"
	ev := &fakeEvaluator{
		fn: func(_ context.Context, req domain.GateRequest) (gateflow.Result, error) {
			return gateflow.Result{
				SignalID:    req.SignalID,
				Verdict:     abstain,
				ProviderErr: domain.NewError(domain.CodeUnavailable, "provider is down"),
			}, nil
		},
	}
	h := newTestHandler(t, ev, Config{})
	rec := doGate(t, h, http.MethodPost, validRequest, "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var v contracts.GateVerdictResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(v.Verdict.Verdict) != "ABSTAIN" {
		t.Fatalf("verdict = %q, want ABSTAIN", v.Verdict.Verdict)
	}
}

func TestFlowErrorMapping(t *testing.T) {
	mk := func(err error) *Handler {
		return newTestHandler(t, &fakeEvaluator{
			fn: func(_ context.Context, req domain.GateRequest) (gateflow.Result, error) {
				return gateflow.Result{}, err
			},
		}, Config{})
	}
	rec := doGate(t, mk(domain.NewError(domain.CodeInvalidRequest, "bad signal")),
		http.MethodPost, validRequest, "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid_request: status = %d, want 400", rec.Code)
	}
	env := decodeErr(t, rec)
	if got := env["code"]; got != "bad_request" {
		t.Fatalf("invalid_request: code = %v", got)
	}
	if got := env["message"]; got != "invalid gate request: bad signal" {
		t.Fatalf("invalid_request: message = %v", got)
	}
	rec = doGate(t, mk(domain.NewError(domain.CodeUnavailable, "store is down")),
		http.MethodPost, validRequest, "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unavailable: status = %d, want 500", rec.Code)
	}
	if got := decodeErr(t, rec)["code"]; got != "internal" {
		t.Fatalf("unavailable: code = %v", got)
	}
}

func TestInvalidGateMessageDropsInternalPrefix(t *testing.T) {
	h := newTestHandler(t, &fakeEvaluator{
		fn: func(_ context.Context, _ domain.GateRequest) (gateflow.Result, error) {
			return gateflow.Result{}, domain.NewError(domain.CodeInvalidRequest,
				"gateflow: state must be an object")
		},
	}, Config{})
	rec := doGate(t, h, http.MethodPost, validRequest, "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := decodeErr(t, rec)["message"]; got != "invalid gate request: state must be an object" {
		t.Fatalf("message = %v", got)
	}
}

func TestConflictMapping(t *testing.T) {
	h := newTestHandler(t, &fakeEvaluator{
		fn: func(_ context.Context, _ domain.GateRequest) (gateflow.Result, error) {
			return gateflow.Result{}, domain.NewError(domain.CodeConflict, "signalId reused")
		},
	}, Config{})
	rec := doGate(t, h, http.MethodPost, validRequest, "application/json", "Bearer "+testToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict: status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	env := decodeErr(t, rec)
	if env["code"] != "conflict" {
		t.Fatalf("conflict: code = %v", env["code"])
	}
	if env["message"] != "signalId already used with a different request body" {
		t.Fatalf("conflict: message = %v", env["message"])
	}
	if env["hint"] != "use a new signalId or send the identical body" {
		t.Fatalf("conflict: hint = %v", env["hint"])
	}
	if env["retryable"] != false {
		t.Fatalf("conflict: retryable = %v, want false", env["retryable"])
	}
}

func TestConcurrencyLimit(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	release := make(chan struct{})
	ev := &fakeEvaluator{
		fn: func(_ context.Context, req domain.GateRequest) (gateflow.Result, error) {
			once.Do(func() { close(entered) })
			<-release
			return gateflow.Result{SignalID: req.SignalID, Verdict: testVerdict()}, nil
		},
	}
	h := newTestHandler(t, ev, Config{MaxConcurrent: 1})
	auth := "Bearer " + testToken
	mkReq := func() (*httptest.ResponseRecorder, *http.Request) {
		req := httptest.NewRequest(http.MethodPost, "/v1/gate", strings.NewReader(validRequest))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", auth)
		return httptest.NewRecorder(), req
	}
	rec1, req1 := mkReq()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec1, req1)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never entered the evaluator")
	}
	rec2, req2 := mkReq()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want 503", rec2.Code)
	}
	if got := decodeErr(t, rec2)["code"]; got != "busy" {
		t.Fatalf("second code = %v, want busy", got)
	}
	if ra := rec2.Header().Get("Retry-After"); ra != "1" {
		t.Fatalf("Retry-After = %q, want 1", ra)
	}
	if n := ev.numCalls(); n != 1 {
		t.Fatalf("evaluator calls = %d, want 1 (busy must shed load)", n)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never finished")
	}
	if rec1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", rec1.Code)
	}
}

func TestValidateAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8420", "127.0.0.1:1", "127.0.0.1:65535"} {
		if err := ValidateAddr(addr); err != nil {
			t.Fatalf("ValidateAddr(%q) = %v, want nil", addr, err)
		}
	}
	for _, addr := range []string{
		"", "127.0.0.1", "127.0.0.1:", "127.0.0.1:0", "127.0.0.1:65536",
		"127.0.0.1:notaport", "localhost:8420", "0.0.0.0:8420", "::1:8420",
		"[::1]:8420", "example.com:8420",
	} {
		if err := ValidateAddr(addr); err == nil {
			t.Fatalf("ValidateAddr(%q) = nil, want error", addr)
		}
	}
}

func TestServeRoundTrip(t *testing.T) {
	h := newTestHandler(t, allowEvaluator(), Config{})
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/gate", bytes.NewBufferString(validRequest))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var v contracts.GateVerdictResponse
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.SignalID != "sig-api-001" {
		t.Fatalf("signalId = %q", v.SignalID)
	}
}

func TestServeWaitsForInflight(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	release := make(chan struct{})
	canceled := make(chan error, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		canceled <- r.Context().Err()
		w.WriteHeader(http.StatusOK)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	serveDone := make(chan error, 1)
	go func() { serveDone <- Serve(ctx, addr, h, logger) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server never came up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	respCh := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			respCh <- resp
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never entered")
	}
	cancel()
	select {
	case err := <-serveDone:
		t.Fatalf("Serve returned %v before the in-flight handler finished", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	var herr error
	select {
	case herr = <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never finished")
	}
	if herr != nil {
		t.Fatalf("handler context canceled before finish: %v", herr)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the handler finished")
	}
	select {
	case resp := <-respCh:
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("client status = %d, want 200", resp.StatusCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client never got a response")
	}
}
