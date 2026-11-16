// Package api serves the local shadow gate HTTP API on stdlib net/http.
//
// The server binds loopback only and guards POST /v1/gate with a bearer
// token. Successful evaluations return the contract flat wrapper
// {mode, signalId, verdict}; failures use the CLI envelope shape.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/gateflow"
)

// Defaults applied when Config carries non-positive values.
const (
	defaultMaxBodyBytes = 64 << 10
	defaultMaxConc      = 8
)

// Config tunes the local HTTP API.
type Config struct {
	// Token is the required bearer token; empty rejects New.
	Token string
	// Version is reported by GET /healthz.
	Version string
	// MaxBodyBytes caps one request body; <=0 means defaultMaxBodyBytes.
	MaxBodyBytes int64
	// MaxConcurrent caps concurrent evaluations; <=0 means defaultMaxConc.
	MaxConcurrent int
}

// Evaluator runs one gate request. *gateflow.Flow satisfies it.
type Evaluator interface {
	Evaluate(ctx context.Context, req domain.GateRequest) (gateflow.Result, error)
}

// Handler serves the local API. Build it with New.
type Handler struct {
	version   string
	ev        Evaluator
	logger    *slog.Logger
	tokenHash [32]byte
	sem       chan struct{}
	maxBody   int64
}

// New builds a Handler. An empty token or nil evaluator is an error so a
// server without auth can never start by accident.
func New(cfg Config, ev Evaluator, logger *slog.Logger) (*Handler, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("api: empty token")
	}
	if ev == nil {
		return nil, fmt.Errorf("api: nil evaluator")
	}
	if logger == nil {
		logger = slog.Default()
	}
	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}
	maxConc := cfg.MaxConcurrent
	if maxConc <= 0 {
		maxConc = defaultMaxConc
	}
	return &Handler{
		version:   cfg.Version,
		ev:        ev,
		logger:    logger,
		tokenHash: sha256.Sum256([]byte(cfg.Token)),
		sem:       make(chan struct{}, maxConc),
		maxBody:   maxBody,
	}, nil
}

// ValidateAddr rejects anything but 127.0.0.1 with a numeric port 1..65535.
func ValidateAddr(addr string) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("api: invalid address %q: %w", addr, err)
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("api: address host must be 127.0.0.1, got %q", host)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("api: invalid port %q: must be 1..65535", portStr)
	}
	return nil
}

// Serve listens on addr and serves h until ctx ends. It returns nil after a
// graceful shutdown.
func Serve(ctx context.Context, addr string, h http.Handler, logger *slog.Logger) error {
	if err := ValidateAddr(addr); err != nil {
		return err
	}
	if h == nil {
		return fmt.Errorf("api: nil handler")
	}
	if logger == nil {
		logger = slog.Default()
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.Serve(ln); err != nil {
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
	return nil
}

// errBody is the machine-readable error envelope.
type errBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Hint      string `json:"hint"`
	Retryable bool   `json:"retryable"`
	Details   any    `json:"details,omitempty"`
}

// healthBody is the GET /healthz payload.
type healthBody struct {
	Status  string `json:"status"`
	Mode    string `json:"mode"`
	Version string `json:"version"`
}

// gateResponse is the contract flat wrapper for a verdict.
type gateResponse struct {
	Mode     string             `json:"mode"`
	SignalID string             `json:"signalId"`
	Verdict  domain.GateVerdict `json:"verdict"`
}

// statusRecorder captures the status for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status before delegating.
func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// ServeHTTP routes health, gate and unknown paths with access logging.
// Logged fields are method, path, status and duration only.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	switch r.URL.Path {
	case "/healthz":
		h.serveHealth(rec, r)
	case "/v1/gate":
		h.serveGate(rec, r)
	default:
		writeError(rec, http.StatusNotFound, "not_found",
			fmt.Sprintf("unknown path %q", r.URL.Path),
			"use GET /healthz or POST /v1/gate", false)
	}
	h.logger.Info("request",
		"method", r.Method,
		"path", r.URL.Path,
		"status", rec.status,
		"duration_ms", time.Since(start).Milliseconds())
}

// serveHealth answers the unauthenticated liveness probe.
func (h *Handler) serveHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"method must be GET", "use GET /healthz", false)
		return
	}
	writeJSON(w, http.StatusOK, healthBody{Status: "ok", Mode: "shadow", Version: h.version})
}

// serveGate evaluates one gate request behind bearer auth.
func (h *Handler) serveGate(w *statusRecorder, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"method must be POST", "use POST /v1/gate", false)
		return
	}
	if !h.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized",
			"missing or invalid bearer token",
			"send Authorization: Bearer <token>", false)
		return
	}
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "busy",
			"server is busy", "retry later", true)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"content type must be application/json",
			"set Content-Type: application/json", false)
		return
	}
	req, ok := decodeGateRequest(w, r, h.maxBody)
	if !ok {
		return
	}
	res, err := h.ev.Evaluate(r.Context(), req)
	if err != nil {
		if domain.CodeOf(err) == domain.CodeInvalidRequest {
			writeError(w, http.StatusBadRequest, "bad_request",
				"invalid gate request", "fix the request fields and retry", false)
			return
		}
		h.logger.Error("gate evaluate error",
			"method", r.Method, "path", r.URL.Path,
			"code", string(domain.CodeOf(err)))
		writeError(w, http.StatusInternalServerError, "internal",
			"gate evaluation failed", "retry later", domain.IsRetryable(err))
		return
	}
	if res.ProviderErr != nil {
		h.logger.Warn("gate provider error",
			"method", r.Method, "path", r.URL.Path,
			"code", string(domain.CodeOf(res.ProviderErr)))
	}
	signalID := res.SignalID
	if signalID == "" {
		signalID = req.SignalID
	}
	writeJSON(w, http.StatusOK, gateResponse{
		Mode:     "shadow",
		SignalID: signalID,
		Verdict:  res.Verdict,
	})
}

// authorized compares the bearer token by SHA-256 digest in constant time.
func (h *Handler) authorized(r *http.Request) bool {
	raw := r.Header.Get("Authorization")
	parts := strings.SplitN(raw, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return false
	}
	if strings.TrimSpace(parts[1]) == "" {
		return false
	}
	got := sha256.Sum256([]byte(parts[1]))
	return subtle.ConstantTimeCompare(got[:], h.tokenHash[:]) == 1
}

// decodeGateRequest reads exactly one strict JSON object within maxBody.
func decodeGateRequest(w http.ResponseWriter, r *http.Request, maxBody int64) (domain.GateRequest, bool) {
	var req domain.GateRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var over *http.MaxBytesError
		if errors.As(err, &over) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large",
				"request body exceeds the size limit",
				"reduce the request size and retry", false)
			return req, false
		}
		writeError(w, http.StatusBadRequest, "bad_request",
			"invalid request body", "send exactly one JSON gate request object", false)
		return req, false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		var over *http.MaxBytesError
		if errors.As(err, &over) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large",
				"request body exceeds the size limit",
				"reduce the request size and retry", false)
			return req, false
		}
		writeError(w, http.StatusBadRequest, "bad_request",
			"request body must contain exactly one JSON value",
			"remove trailing data from the request body", false)
		return req, false
	}
	return req, true
}

// writeJSON encodes v as application/json with status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError encodes the envelope with status.
func writeError(w http.ResponseWriter, status int, code, message, hint string, retryable bool) {
	writeJSON(w, status, errBody{
		Code: code, Message: message, Hint: hint, Retryable: retryable,
	})
}
