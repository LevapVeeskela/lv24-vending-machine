// Package gateflow runs one gated decision and persists the linked events.
//
// The flow is idempotent by signal ID: repeating a request reuses the
// recorded verdict without calling the provider again. Every persisted
// event shares one decision ID so request, response, model-change notice
// and verdict stay linked in the journal.
package gateflow

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/gate"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/resilience"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// defaultTimeout bounds one provider call including its retries.
const defaultTimeout = 20 * time.Second

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
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Flow{jr: jr, provider: provider, deps: deps, cfg: cfg, timeout: timeout}, nil
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

// load walks the whole journal in ascending seq order.
func (f *Flow) load(ctx context.Context) ([]domain.Event, error) {
	var out []domain.Event
	if err := f.jr.Walk(ctx, 0, "", func(ev domain.Event) error {
		out = append(out, ev)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// kindsOf returns the event kinds already stored under decisionID.
func kindsOf(events []domain.Event, decisionID string) map[string]bool {
	kinds := make(map[string]bool)
	for _, ev := range events {
		if ev.DecisionID == decisionID {
			kinds[ev.Kind] = true
		}
	}
	return kinds
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

// storedResponse decodes the recorded decision.response for decisionID.
// It reports false when no such event exists or its payload is corrupt.
func storedResponse(events []domain.Event, decisionID string) (domain.DecisionResponse, bool) {
	var out domain.DecisionResponse
	for _, ev := range events {
		if ev.Kind != domain.EventDecisionResponse || ev.DecisionID != decisionID {
			continue
		}
		if err := json.Unmarshal(ev.Payload, &out); err != nil {
			return domain.DecisionResponse{}, false
		}
		return out, true
	}
	return domain.DecisionResponse{}, false
}

// requestedOf resolves the requested model of a response, falling back to
// the configured model when the provider left it empty.
func (f *Flow) requestedOf(resp domain.DecisionResponse) string {
	if strings.TrimSpace(resp.RequestedModel) != "" {
		return resp.RequestedModel
	}
	return f.cfg.Model
}

// previousModel returns the most recent actual model recorded in earlier
// decision.response events for the same requested model, skipping events
// of the current decision. Empty means no usable predecessor.
func previousModel(events []domain.Event, decisionID, requested string) string {
	prev := ""
	for _, ev := range events {
		if ev.Kind != domain.EventDecisionResponse || ev.DecisionID == decisionID {
			continue
		}
		var resp domain.DecisionResponse
		if err := json.Unmarshal(ev.Payload, &resp); err != nil {
			continue
		}
		rq := resp.RequestedModel
		if strings.TrimSpace(rq) == "" {
			rq = requested
		}
		if rq != requested {
			continue
		}
		if strings.TrimSpace(resp.Model) != "" {
			prev = resp.Model
		}
	}
	return prev
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
// Exit intents never touch the provider. Entry intents call it under a
// single deadline covering all retries; journal appends use the caller
// context so the verdict is still recorded after a provider timeout.
func (f *Flow) Evaluate(ctx context.Context, req domain.GateRequest) (Result, error) {
	if err := validateRequest(req); err != nil {
		return Result{}, err
	}
	events, err := f.load(ctx)
	if err != nil {
		return Result{}, err
	}
	for _, ev := range events {
		if ev.Kind != domain.EventGateRequest {
			continue
		}
		sig, ok := signalOf(ev.Payload)
		if !ok || sig != req.SignalID {
			continue
		}
		decisionID := ev.DecisionID
		for _, v := range events {
			if v.Kind == domain.EventGateVerdict && v.DecisionID == decisionID {
				var verdict domain.GateVerdict
				if err := json.Unmarshal(v.Payload, &verdict); err != nil {
					return Result{}, fmt.Errorf("gateflow: decode replayed verdict: %w", err)
				}
				return Result{SignalID: req.SignalID, Verdict: verdict, Reused: true}, nil
			}
		}
		// The request exists but the verdict does not (crash between
		// appends): continue with the same decision id and append only
		// the missing events, never a second gate.request.
		return f.complete(ctx, req, decisionID, events)
	}
	decisionID := f.deps.NextID()
	if decisionID == "" {
		return Result{}, fmt.Errorf("gateflow: NewID returned an empty decision id")
	}
	return f.complete(ctx, req, decisionID, events)
}

// complete evaluates req and appends the events missing for decisionID.
func (f *Flow) complete(ctx context.Context, req domain.GateRequest, decisionID string, prior []domain.Event) (Result, error) {
	now := f.deps.Now().UTC()
	present := kindsOf(prior, decisionID)

	var outcome domain.DecisionOutcome
	var resp *domain.DecisionResponse
	var providerErr error
	if req.IntentKind != domain.IntentExit {
		if stored, ok := storedResponse(prior, decisionID); ok {
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
			callCtx, cancel := context.WithTimeout(ctx, f.timeout)
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
	verdict.DecidedAt = now

	// Journal appends use the caller context, never the provider deadline.
	if !present[domain.EventGateRequest] {
		raw, err := json.Marshal(req)
		if err != nil {
			return Result{}, fmt.Errorf("gateflow: encode gate request: %w", err)
		}
		if err := f.appendOne(ctx, domain.EventGateRequest, decisionID, "", raw, now); err != nil {
			return Result{}, err
		}
	}
	if resp != nil {
		if !present[domain.EventDecisionResponse] {
			raw, err := json.Marshal(resp)
			if err != nil {
				return Result{}, fmt.Errorf("gateflow: encode decision response: %w", err)
			}
			if err := f.appendOne(ctx, domain.EventDecisionResponse, decisionID, "", raw, now); err != nil {
				return Result{}, err
			}
		}
		requested := f.requestedOf(*resp)
		if prev := previousModel(prior, decisionID, requested); prev != "" && prev != resp.Model {
			if !present[domain.EventDecisionModelChanged] {
				raw, err := json.Marshal(domain.ModelChanged{
					RequestedModel: requested,
					PreviousModel:  prev,
					ActualModel:    resp.Model,
					DetectedAt:     now,
				})
				if err != nil {
					return Result{}, fmt.Errorf("gateflow: encode model change: %w", err)
				}
				if err := f.appendOne(ctx, domain.EventDecisionModelChanged, decisionID, "", raw, now); err != nil {
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
		if err := f.appendOne(ctx, domain.EventGateVerdict, decisionID, verdict.PolicyHash, raw, now); err != nil {
			return Result{}, err
		}
	}

	return Result{
		SignalID:    req.SignalID,
		Verdict:     verdict,
		Response:    resp,
		ProviderErr: providerErr,
	}, nil
}
