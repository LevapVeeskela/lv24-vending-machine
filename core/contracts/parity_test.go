package contracts

// Domain↔contracts parity: domain is the runtime model, contracts/* is the
// wire boundary. Marshal a domain value and decode it into the matching
// generated contracts type (and back for requests). Any future field drift
// that breaks wire compatibility fails here.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

var parityTS = time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC)

const (
	parityPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"
	parityHash     = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	parityPolicy   = "sha256:b9ae3472b8ff26cc2889f5db91eb555ed09a935a112f16984ea1014a69e59b4f"
	parityULID     = "01J9ZQ8W3E12AB3456789CDEFG"
)

func fptr(f float64) *float64 { return &f }

func mustWire(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return b
}

func TestParityDecisionRequest(t *testing.T) {
	d := domain.DecisionRequest{
		Model: "dre-x-1",
		State: map[string]any{"rsi14": 62.5},
		Questions: []domain.Question{
			{
				ID:   "direction",
				Type: domain.QuestionChoice,
				Options: []domain.Option{
					{Key: "long", Description: "Open or hold a long position"},
					{Key: "flat", Description: "Stay out of the market"},
				},
			},
			{ID: "volatility", Type: domain.QuestionNoul},
		},
	}
	var c DecisionRequest
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.DecisionRequest not wire-compatible: %v", err)
	}
	if c.Model != d.Model || len(c.Questions) != 2 || c.Questions[0].ID != "direction" {
		t.Fatalf("round-trip mismatch: %+v", c)
	}

	var back domain.DecisionRequest
	if err := json.Unmarshal(mustWire(t, c), &back); err != nil {
		t.Fatalf("contracts.DecisionRequest not domain-compatible: %v", err)
	}
	if back.Model != d.Model || len(back.Questions) != len(d.Questions) {
		t.Fatalf("reverse mismatch: %+v", back)
	}
}

func TestParityDecisionResponse(t *testing.T) {
	d := domain.DecisionResponse{
		Answers: map[string]domain.Answer{
			"direction": {
				Type:          domain.QuestionChoice,
				Choice:        "long",
				Probabilities: map[string]float64{"long": 0.6, "flat": 0.3, "short": 0.1},
				Confidence:    fptr(0.4),
			},
		},
		Model:          "dre-x-1",
		RequestedModel: "dre-x-1",
		Usage: domain.Usage{
			InputTokens:  120,
			OutputTokens: 40,
			CostUSD:      domain.MustParseDecimal("0.0000072"),
			LatencyMS:    12,
		},
		RequestID: "req-001",
	}
	var c DecisionResponse
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.DecisionResponse not wire-compatible: %v", err)
	}
	if c.Model != "dre-x-1" || c.Usage.InputTokens != 120 {
		t.Fatalf("round-trip mismatch: %+v", c)
	}
	if c.Usage.LatencyMs == nil || *c.Usage.LatencyMs != 12 {
		t.Fatalf("latencyMs lost on the wire: %+v", c.Usage)
	}
}

func TestParityGateRequest(t *testing.T) {
	d := domain.GateRequest{
		SignalID:   "sig-001",
		Instrument: domain.Instrument{Venue: "binance", Symbol: "BTCUSDT"},
		Side:       domain.SideBuy,
		IntentKind: domain.IntentEntry,
		Timeframe:  "1h",
		TS:         parityTS,
		Price:      domain.MustParseDecimal("97500.50"),
		Source:     "strategy:trend-v1",
		State:      map[string]float64{"rsi14": 62.5},
	}
	var c GateRequest
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.GateRequest not wire-compatible: %v", err)
	}
	if string(c.Side) != "buy" || string(c.Price) != "97500.50" {
		t.Fatalf("round-trip mismatch: %+v", c)
	}

	var back domain.GateRequest
	if err := json.Unmarshal(mustWire(t, c), &back); err != nil {
		t.Fatalf("contracts.GateRequest not domain-compatible: %v", err)
	}
	if back.SignalID != d.SignalID || back.Instrument.Symbol != "BTCUSDT" {
		t.Fatalf("reverse mismatch: %+v", back)
	}
}

func TestParityGateVerdict(t *testing.T) {
	d := domain.GateVerdict{
		Verdict:    domain.VerdictAllow,
		Code:       "allow",
		Reasons:    []string{"all gate checks passed"},
		Checks:     []domain.Check{{Name: "risk_check", Passed: true, Detail: "within daily loss limit"}},
		DecisionID: parityULID,
		Model:      "dre-x-1",
		PolicyHash: parityPolicy,
		DecidedAt:  parityTS,
	}
	var c GateVerdict
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.GateVerdict not wire-compatible: %v", err)
	}
	if string(c.Verdict) != "ALLOW" || c.PolicyHash != parityPolicy {
		t.Fatalf("round-trip mismatch: %+v", c)
	}
}

func TestParityEvent(t *testing.T) {
	d := domain.Event{
		Seq:           0,
		EventID:       parityULID,
		Kind:          "run.started",
		TS:            parityTS,
		SchemaVersion: 1,
		Payload:       json.RawMessage(`{"mode":"shadow"}`),
		CodeVersion:   "v0.0.0-m0",
		PrevHash:      parityPrevHash,
		Hash:          parityHash,
		RunID:         "run-001",
		PolicyHash:    parityPolicy,
	}
	var c Event
	if err := json.Unmarshal(mustWire(t, d), &c); err != nil {
		t.Fatalf("domain.Event not wire-compatible: %v", err)
	}
	if c.Seq != 0 || c.Kind != "run.started" || c.SchemaVersion != 1 {
		t.Fatalf("round-trip mismatch: %+v", c)
	}
}
