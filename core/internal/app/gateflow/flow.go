// Package gateflow runs one gated decision and persists the linked events.
//
// The flow is idempotent by signal ID: repeating a request reuses the
// recorded verdict without calling the provider again. Every persisted
// event shares one decision ID so request, response, model-change notice
// and verdict stay linked in the journal.
package gateflow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/gate"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/resilience"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// defaultTimeout bounds one provider call including its retries.
const defaultTimeout = 20 * time.Second

// arenaTimeout bounds one arena provider call including its retries.
const arenaTimeout = 20 * time.Second

// ArenaConfig configures the cross-model arena: a second decision provider
// answers the same request in the background after the primary verdict.
type ArenaConfig struct {
	// Provider is the arena decision provider; it is required.
	Provider ports.Decision
	// Model is the arena model id sent in the decision request.
	Model string
}

// Config tunes one gate flow.
type Config struct {
	// Model is the requested decision model id.
	Model string
	// Policy is the gate policy applied to the provider outcome.
	Policy domain.GatePolicy
	// CodeVersion is stamped on every appended event envelope.
	CodeVersion string
	// Timeout bounds the provider call including retries; <=0 means defaultTimeout.
	Timeout time.Duration
	// Arena is the optional cross-model comparison provider.
	Arena *ArenaConfig
}

// arenaPayload mirrors the arena.decision event payload
// (contracts/jsonschema/events/v1.json $defs.ArenaDecision). It is declared
// locally so the app layer keeps depending on domain and ports only.
type arenaPayload struct {
	DecisionID string                  `json:"decisionId"`
	Model      string                  `json:"model"`
	Response   domain.DecisionResponse `json:"response"`
	Verdict    domain.GateVerdict      `json:"verdict"`
}

// Result is the outcome of one Evaluate call.
type Result struct {
	// SignalID echoes the evaluated request.
	SignalID string
	// Verdict is the gate decision (replayed when Reused).
	Verdict domain.GateVerdict
	// Reused reports an idempotent replay: no provider call, no new events.
	Reused bool
	// Response is the provider answer; nil on replay, failure or exit.
	Response *domain.DecisionResponse
	// ProviderErr carries the provider failure; the ABSTAIN verdict is still written.
	ProviderErr error
}

// Flow evaluates gate requests against one provider and one journal.
type Flow struct {
	jr       ports.Journal
	provider ports.Decision
	deps     ports.Deps
	cfg      Config
	timeout  time.Duration

	mu       sync.Mutex
	inflight map[string]*inflightCall
}

// inflightCall tracks one running Evaluate so concurrent duplicates of the
// same signal wait for it instead of calling the provider twice. fingerprint
// binds the call to one request body, so a different body under the same
// signal id is rejected instead of sharing the result.
type inflightCall struct {
	done        chan struct{}
	fingerprint [sha256.Size]byte
	res         Result
	err         error
}

// errSignalBodyConflict marks a reused signal id sent with another body.
func errSignalBodyConflict() error {
	return domain.NewError(domain.CodeConflict, "gateflow: signalId already used with a different request body")
}

// requestFingerprint returns the sha256 of the canonical JSON encoding of req.
//
// json.Marshal is deterministic for GateRequest (sorted state keys, string
// decimals, RFC3339Nano timestamps), so the fingerprint identifies the body
// of a signal id both across restarts and within one process.
func requestFingerprint(req domain.GateRequest) ([sha256.Size]byte, []byte, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return [sha256.Size]byte{}, nil, fmt.Errorf("gateflow: encode gate request: %w", err)
	}
	return sha256.Sum256(raw), raw, nil
}

// sameRequestAs reports a conflict unless payload decodes to a request whose
// fingerprint equals fp. An empty payload cannot be compared and is ignored.
func sameRequestAs(payload []byte, fp [sha256.Size]byte) error {
	if len(payload) == 0 {
		return nil
	}
	var stored domain.GateRequest
	if err := json.Unmarshal(payload, &stored); err != nil {
		return fmt.Errorf("gateflow: decode recorded gate request: %w", err)
	}
	got, _, err := requestFingerprint(stored)
	if err != nil {
		return err
	}
	if got != fp {
		return errSignalBodyConflict()
	}
	return nil
}

// New builds a Flow. A nil provider is accepted because exit intents never
// call it; entry intents fail at Evaluate time when it is missing.
func New(jr ports.Journal, provider ports.Decision, deps ports.Deps, cfg Config) (*Flow, error) {
	if jr == nil {
		return nil, fmt.Errorf("gateflow: nil journal")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("gateflow: empty model")
	}
	if cfg.Arena != nil {
		if cfg.Arena.Provider == nil {
			return nil, fmt.Errorf("gateflow: nil arena provider")
		}
		if strings.TrimSpace(cfg.Arena.Model) == "" {
			return nil, fmt.Errorf("gateflow: empty arena model")
		}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Flow{jr: jr, provider: provider, deps: deps, cfg: cfg, timeout: timeout, inflight: make(map[string]*inflightCall)}, nil
}

// validateRequest rejects malformed requests before any journal I/O.
func validateRequest(req domain.GateRequest) error {
	invalid := func(msg string) error {
		return domain.NewError(domain.CodeInvalidRequest, "gateflow: "+msg)
	}
	if strings.TrimSpace(req.SignalID) == "" {
		return invalid("signalId must not be empty")
	}
	if strings.TrimSpace(req.Instrument.Venue) == "" || strings.TrimSpace(req.Instrument.Symbol) == "" {
		return invalid("instrument venue and symbol must not be empty")
	}
	if strings.TrimSpace(req.Timeframe) == "" {
		return invalid("timeframe must not be empty")
	}
	if strings.TrimSpace(req.Source) == "" {
		return invalid("source must not be empty")
	}
	if req.Side != domain.SideBuy && req.Side != domain.SideSell {
		return invalid("side must be buy or sell")
	}
	if req.IntentKind != domain.IntentEntry && req.IntentKind != domain.IntentExit {
		return invalid("intentKind must be entry or exit")
	}
	if req.TS.IsZero() {
		return invalid("ts must be set")
	}
	if req.Price.Sign() <= 0 {
		return invalid("price must be > 0")
	}
	if req.State == nil {
		return invalid("state must be an object")
	}
	for k, v := range req.State {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return invalid(fmt.Sprintf("state %q must be finite", k))
		}
	}
	return nil
}

// signalOf decodes the signalId carried by a gate.request payload.
func signalOf(payload []byte) (string, bool) {
	var v struct {
		SignalID string `json:"signalId"`
	}
	if err := json.Unmarshal(payload, &v); err != nil {
		return "", false
	}
	return v.SignalID, true
}

// scanState is the O(1)-memory result of one journal walk for complete.
type scanState struct {
	// present lists event kinds already stored under the decision id.
	present map[string]bool
	// storedRaw is the recorded decision.response payload, if any.
	storedRaw []byte
	// lastByKey tracks the newest actual model per non-empty requested model.
	lastByKey map[string]modelMark
	// lastBare tracks the newest actual model of responses with no
	// requested model recorded; they count as the current request.
	lastBare modelMark
}

// modelMark is one observed actual model with its sequence number.
type modelMark struct {
	seq   int64
	model string
}

// scan walks the whole journal once in ascending seq order, keeping only
// what complete needs for decisionID: present kinds, the stored response
// payload, and model candidates for the model_changed check.
func (f *Flow) scan(ctx context.Context, decisionID string) (scanState, error) {
	s := scanState{present: make(map[string]bool), lastByKey: make(map[string]modelMark)}
	err := f.jr.Walk(ctx, 0, "", func(ev domain.Event) error {
		if ev.DecisionID == decisionID {
			s.present[ev.Kind] = true
			if ev.Kind == domain.EventDecisionResponse && s.storedRaw == nil {
				s.storedRaw = append([]byte(nil), ev.Payload...)
			}
		}
		if ev.Kind != domain.EventDecisionResponse || ev.DecisionID == decisionID {
			return nil
		}
		var resp domain.DecisionResponse
		if err := json.Unmarshal(ev.Payload, &resp); err != nil {
			return nil
		}
		if strings.TrimSpace(resp.Model) == "" {
			return nil
		}
		mark := modelMark{seq: ev.Seq, model: resp.Model}
		if strings.TrimSpace(resp.RequestedModel) == "" {
			if ev.Seq >= s.lastBare.seq {
				s.lastBare = mark
			}
			return nil
		}
		if prev, ok := s.lastByKey[resp.RequestedModel]; !ok || ev.Seq >= prev.seq {
			s.lastByKey[resp.RequestedModel] = mark
		}
		return nil
	})
	if err != nil {
		return scanState{}, err
	}
	return s, nil
}

// previousModelOf resolves the predecessor actual model for requested from
// the scan: the newest of the same-key mark and the bare mark.
func (s scanState) previousModelOf(requested string) string {
	best := s.lastBare
	if m, ok := s.lastByKey[requested]; ok && m.seq >= best.seq {
		best = m
	}
	return best.model
}

// composeState copies the numeric request state and adds routing context.
func composeState(req domain.GateRequest) map[string]any {
	state := make(map[string]any, len(req.State)+7)
	for k, v := range req.State {
		state[k] = v
	}
	state["symbol"] = req.Instrument.Symbol
	state["venue"] = req.Instrument.Venue
	state["timeframe"] = req.Timeframe
	state["side"] = string(req.Side)
	state["intent_kind"] = string(req.IntentKind)
	state["close"] = req.Price
	state["timestamp"] = req.TS.UTC().Format(time.RFC3339Nano)
	return state
}

// requestedOf resolves the requested model of a response, falling back to
// the configured model when the provider left it empty.
func (f *Flow) requestedOf(resp domain.DecisionResponse) string {
	if strings.TrimSpace(resp.RequestedModel) != "" {
		return resp.RequestedModel
	}
	return f.cfg.Model
}

// appendOne stores ev with the flow envelope fields.
func (f *Flow) appendOne(ctx context.Context, kind, decisionID, policyHash string, payload []byte, ts time.Time) error {
	_, err := f.jr.Append(ctx, domain.Event{
		Kind:          kind,
		TS:            ts,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       payload,
		CodeVersion:   f.cfg.CodeVersion,
		DecisionID:    decisionID,
		PolicyHash:    policyHash,
	})
	return err
}

// Evaluate runs one gate request through the provider and the journal.
//
// Concurrent duplicates of the same signal id share one execution: the
// second caller waits for the first and receives its Result. A duplicate with
// a different request body is a CodeConflict and writes nothing. Different
// signals never block each other.
//
// Exit intents never touch the provider. Entry intents call it under a
// single deadline covering all retries; journal I/O and the provider call are
// detached from caller cancellation so a client disconnect cannot lose a
// recorded verdict or abort a paid call.
func (f *Flow) Evaluate(ctx context.Context, req domain.GateRequest) (Result, error) {
	if err := validateRequest(req); err != nil {
		return Result{}, err
	}
	fp, raw, err := requestFingerprint(req)
	if err != nil {
		return Result{}, err
	}
	// Only the wait for an in-flight duplicate honours caller cancellation.
	workCtx := context.WithoutCancel(ctx)

	f.mu.Lock()
	if c, ok := f.inflight[req.SignalID]; ok {
		if c.fingerprint != fp {
			f.mu.Unlock()
			return Result{}, errSignalBodyConflict()
		}
		f.mu.Unlock()
		select {
		case <-c.done:
			return c.res, c.err
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	c := &inflightCall{done: make(chan struct{}), fingerprint: fp}
	f.inflight[req.SignalID] = c
	f.mu.Unlock()

	res, err := f.evaluate(workCtx, req, fp, raw)

	c.res, c.err = res, err
	close(c.done)
	f.mu.Lock()
	delete(f.inflight, req.SignalID)
	f.mu.Unlock()
	return res, err
}

// evaluate runs one gate request; it assumes no duplicate is in flight.
func (f *Flow) evaluate(ctx context.Context, req domain.GateRequest, fp [sha256.Size]byte, raw []byte) (Result, error) {
	decisionID := ""
	var storedRequest []byte
	if err := f.jr.Walk(ctx, 0, domain.EventGateRequest, func(ev domain.Event) error {
		sig, ok := signalOf(ev.Payload)
		if ok && sig == req.SignalID {
			decisionID = ev.DecisionID
			storedRequest = append([]byte(nil), ev.Payload...)
		}
		return nil
	}); err != nil {
		return Result{}, err
	}
	if decisionID == "" {
		decisionID = f.deps.NextID()
		if decisionID == "" {
			return Result{}, fmt.Errorf("gateflow: NewID returned an empty decision id")
		}
		return f.complete(ctx, req, decisionID, raw)
	}
	// The signal id is already recorded: only the identical body may reuse it.
	if err := sameRequestAs(storedRequest, fp); err != nil {
		return Result{}, err
	}
	var verdict *domain.GateVerdict
	if err := f.jr.Walk(ctx, 0, domain.EventGateVerdict, func(ev domain.Event) error {
		if ev.DecisionID != decisionID {
			return nil
		}
		var v domain.GateVerdict
		if err := json.Unmarshal(ev.Payload, &v); err != nil {
			return fmt.Errorf("gateflow: decode replayed verdict: %w", err)
		}
		vCopy := v
		verdict = &vCopy
		return nil
	}); err != nil {
		return Result{}, err
	}
	if verdict != nil {
		return Result{SignalID: req.SignalID, Verdict: *verdict, Reused: true}, nil
	}
	// The request exists but the verdict does not (crash between
	// appends): continue with the same decision id and append only
	// the missing events, never a second gate.request.
	return f.complete(ctx, req, decisionID, raw)
}

// complete evaluates req and appends the events missing for decisionID.
//
// Timestamps are split by phase: a fresh decision stamps gate.request with
// its admission time, and every event produced by the decision (response,
// model change, verdict) carries decidedAt. A crash retry never rewrites
// gate.request, so its original admission time survives.
func (f *Flow) complete(ctx context.Context, req domain.GateRequest, decisionID string, reqRaw []byte) (Result, error) {
	sc, err := f.scan(ctx, decisionID)
	if err != nil {
		return Result{}, err
	}
	var admittedAt time.Time
	if !sc.present[domain.EventGateRequest] {
		admittedAt = f.deps.Now()
	}

	var outcome domain.DecisionOutcome
	var resp *domain.DecisionResponse
	var providerErr error
	if req.IntentKind != domain.IntentExit {
		if len(sc.storedRaw) > 0 {
			var stored domain.DecisionResponse
			if err := json.Unmarshal(sc.storedRaw, &stored); err != nil {
				return Result{}, fmt.Errorf("gateflow: decode recorded response: %w", err)
			}
			// A response is already recorded (crash after its append):
			// replay it instead of calling the provider again.
			rCopy := stored
			resp = &rCopy
			outcome = domain.DecisionOutcome{Response: resp}
		} else {
			if f.provider == nil {
				return Result{}, fmt.Errorf("gateflow: nil decision provider")
			}
			decReq := domain.DecisionRequest{
				Model:     f.cfg.Model,
				State:     composeState(req),
				Questions: gate.Questions(),
				Timeout:   f.timeout,
			}
			callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), f.timeout)
			callErr := resilience.Do(callCtx, resilience.DefaultPolicy(), func(cctx context.Context) error {
				r, err := f.provider.Decide(cctx, decReq)
				if err != nil {
					return err
				}
				rCopy := r
				resp = &rCopy
				return nil
			})
			cancel()
			if callErr != nil {
				outcome = domain.DecisionOutcome{Err: callErr}
				providerErr = callErr
				resp = nil
			} else {
				outcome = domain.DecisionOutcome{Response: resp}
			}
		}
	}
	// IntentExit skips the provider: Evaluate short-circuits to SKIP.

	verdict := gate.Evaluate(req, outcome, f.cfg.Policy)
	verdict.DecisionID = decisionID
	decidedAt := f.deps.Now()
	verdict.DecidedAt = decidedAt

	// Journal appends use a detached context: a client disconnect after
	// the provider answered must not lose the recorded verdict.
	appendCtx := context.WithoutCancel(ctx)
	present := sc.present
	if !present[domain.EventGateRequest] {
		if len(reqRaw) == 0 {
			reqRaw, err = json.Marshal(req)
			if err != nil {
				return Result{}, fmt.Errorf("gateflow: encode gate request: %w", err)
			}
		}
		if err := f.appendOne(appendCtx, domain.EventGateRequest, decisionID, "", reqRaw, admittedAt); err != nil {
			return Result{}, err
		}
	}
	if resp != nil {
		if !present[domain.EventDecisionResponse] {
			raw, err := json.Marshal(resp)
			if err != nil {
				return Result{}, fmt.Errorf("gateflow: encode decision response: %w", err)
			}
			if err := f.appendOne(appendCtx, domain.EventDecisionResponse, decisionID, "", raw, decidedAt); err != nil {
				return Result{}, err
			}
		}
		requested := f.requestedOf(*resp)
		if prev := sc.previousModelOf(requested); prev != "" && prev != resp.Model {
			if !present[domain.EventDecisionModelChanged] {
				raw, err := json.Marshal(domain.ModelChanged{
					RequestedModel: requested,
					PreviousModel:  prev,
					ActualModel:    resp.Model,
					DetectedAt:     decidedAt,
				})
				if err != nil {
					return Result{}, fmt.Errorf("gateflow: encode model change: %w", err)
				}
				if err := f.appendOne(appendCtx, domain.EventDecisionModelChanged, decisionID, "", raw, decidedAt); err != nil {
					return Result{}, err
				}
			}
		}
	}
	if !present[domain.EventGateVerdict] {
		raw, err := json.Marshal(verdict)
		if err != nil {
			return Result{}, fmt.Errorf("gateflow: encode gate verdict: %w", err)
		}
		if err := f.appendOne(appendCtx, domain.EventGateVerdict, decisionID, verdict.PolicyHash, raw, decidedAt); err != nil {
			return Result{}, err
		}
	}
	// The arena is advisory and must never delay or fail the primary answer:
	// it runs detached, writes only its own event, and logs failures.
	if req.IntentKind != domain.IntentExit && !present[domain.EventArenaDecision] {
		f.launchArena(appendCtx, req, decisionID)
	}

	return Result{
		SignalID:    req.SignalID,
		Verdict:     verdict,
		Response:    resp,
		ProviderErr: providerErr,
	}, nil
}

// launchArena starts the detached arena call for one recorded decision.
//
// The state snapshot is taken synchronously, before the goroutine starts, so
// the caller may reuse or mutate its request map once Evaluate returns.
func (f *Flow) launchArena(ctx context.Context, req domain.GateRequest, decisionID string) {
	arena := f.cfg.Arena
	if arena == nil || arena.Provider == nil {
		return
	}
	state := composeState(req)
	go f.runArena(ctx, arena, req, state, decisionID)
}

// runArena calls the arena provider and appends one arena.decision event.
//
// It runs off the request path with its own deadline: a slow or failing arena
// never delays or changes the primary verdict, and failures are logged by
// error code only (never the response or the request state).
func (f *Flow) runArena(ctx context.Context, arena *ArenaConfig, req domain.GateRequest, state map[string]any, decisionID string) {
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), arenaTimeout)
	decReq := domain.DecisionRequest{
		Model:     arena.Model,
		State:     state,
		Questions: gate.Questions(),
		Timeout:   arenaTimeout,
	}
	var resp *domain.DecisionResponse
	err := resilience.Do(callCtx, resilience.DefaultPolicy(), func(cctx context.Context) error {
		r, ierr := arena.Provider.Decide(cctx, decReq)
		if ierr != nil {
			return ierr
		}
		rCopy := r
		resp = &rCopy
		return nil
	})
	cancel()
	if err != nil {
		f.logArenaError(arena, decisionID, err)
		return
	}
	if resp == nil {
		return
	}
	// The arena evaluation never records decision.response or gate.verdict:
	// one arena.decision event carries both answers under the primary id.
	verdict := gate.Evaluate(req, domain.DecisionOutcome{Response: resp}, f.cfg.Policy)
	verdict.DecisionID = decisionID
	ts := f.deps.Now()
	verdict.DecidedAt = ts
	raw, merr := json.Marshal(arenaPayload{
		DecisionID: decisionID,
		Model:      resp.Model,
		Response:   *resp,
		Verdict:    verdict,
	})
	if merr != nil {
		f.logArenaError(arena, decisionID, fmt.Errorf("gateflow: encode arena decision: %w", merr))
		return
	}
	appendCtx := context.WithoutCancel(ctx)
	if aerr := f.appendOne(appendCtx, domain.EventArenaDecision, decisionID, verdict.PolicyHash, raw, ts); aerr != nil {
		f.logArenaError(arena, decisionID, aerr)
	}
}

// logArenaError records an arena failure without response or state content.
func (f *Flow) logArenaError(arena *ArenaConfig, decisionID string, err error) {
	logger := f.deps.Logger
	if logger == nil {
		return
	}
	providerID := ""
	if arena != nil && arena.Provider != nil {
		providerID = arena.Provider.Descriptor().ID
	}
	logger.Warn("gateflow: arena decision failed",
		"decisionId", decisionID,
		"provider", providerID,
		"code", string(domain.CodeOf(err)))
}
