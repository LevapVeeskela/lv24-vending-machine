package domain

import "time"

// Side is the trade direction of a gate request.
type Side string

// Supported sides.
const (
	// SideBuy requests a long position.
	SideBuy Side = "buy"
	// SideSell requests a short position.
	SideSell Side = "sell"
)

// IntentKind distinguishes entries from exits.
type IntentKind string

// Supported intent kinds.
const (
	// IntentEntry opens or scales a position.
	IntentEntry IntentKind = "entry"
	// IntentExit closes or reduces a position.
	IntentExit IntentKind = "exit"
)

// Instrument identifies a tradable symbol on a venue.
type Instrument struct {
	Venue  string `json:"venue"`
	Symbol string `json:"symbol"`
}

// GateRequest is the input evaluated by the risk gate.
type GateRequest struct {
	SignalID   string             `json:"signalId"`
	Instrument Instrument         `json:"instrument"`
	Side       Side               `json:"side"`
	IntentKind IntentKind         `json:"intentKind"`
	Timeframe  string             `json:"timeframe"`
	TS         time.Time          `json:"ts"`
	Price      Decimal            `json:"price"`
	Source     string             `json:"source"`
	State      map[string]float64 `json:"state"`
}

// Verdict is the gate decision outcome.
type Verdict string

// Supported verdicts.
const (
	// VerdictAllow permits the intent to proceed.
	VerdictAllow Verdict = "ALLOW"
	// VerdictReject blocks the intent.
	VerdictReject Verdict = "REJECT"
	// VerdictAbstain records provider indecision.
	VerdictAbstain Verdict = "ABSTAIN"
	// VerdictSkip bypasses evaluation without a decision.
	VerdictSkip Verdict = "SKIP"
)

// Check is a single named gate sub-result.
type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// GateVerdict is the evaluated result for a GateRequest.
type GateVerdict struct {
	Verdict    Verdict   `json:"verdict"`
	Code       string    `json:"code"`
	Reasons    []string  `json:"reasons"`
	Checks     []Check   `json:"checks"`
	DecisionID string    `json:"decisionId,omitempty"`
	Model      string    `json:"model,omitempty"`
	PolicyHash string    `json:"policyHash"`
	DecidedAt  time.Time `json:"decidedAt"`
}

// GatePolicy tunes the gate decision thresholds.
type GatePolicy struct {
	Version  string  `json:"version"`
	MinPass  float64 `json:"minPass"`
	MinBlock float64 `json:"minBlock"`
}

// DefaultGatePolicy returns the baseline gate policy.
func DefaultGatePolicy() GatePolicy {
	return GatePolicy{Version: "gate-v1", MinPass: 0.55, MinBlock: 0.50}
}

// DecisionOutcome carries either a response or a provider failure.
//
// A non-nil Err means the provider call itself failed.
type DecisionOutcome struct {
	Response *DecisionResponse
	Err      error
}
