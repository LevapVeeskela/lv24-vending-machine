package domain

import (
	"encoding/json"
	"time"
)

// Event kind constants for the journal stream.
const (
	EventGateRequest          = "gate.request"
	EventDecisionResponse     = "decision.response"
	EventGateVerdict          = "gate.verdict"
	EventOutcomeLabeled       = "outcome.labeled"
	EventDecisionModelChanged = "decision.model_changed"
	EventArenaDecision        = "arena.decision"
	EventNotifySent           = "notify.sent"
)

// EventSchemaVersion is the current journal payload schema version.
const EventSchemaVersion = 1

// Event is one hash-chained journal record.
type Event struct {
	Seq           int64           `json:"seq"`
	EventID       string          `json:"eventId"`
	Kind          string          `json:"kind"`
	TS            time.Time       `json:"ts"`
	SchemaVersion int             `json:"schemaVersion"`
	Payload       json.RawMessage `json:"payload"`
	CodeVersion   string          `json:"codeVersion"`
	PrevHash      string          `json:"prevHash"`
	Hash          string          `json:"hash"`
	RunID         string          `json:"runId,omitempty"`
	DecisionID    string          `json:"decisionId,omitempty"`
	PolicyHash    string          `json:"policyHash,omitempty"`
	SnapshotHash  string          `json:"snapshotHash,omitempty"`
}

// VerifyResult reports the outcome of a journal chain verification.
type VerifyResult struct {
	OK        bool   `json:"ok"`
	Count     int64  `json:"count"`
	BrokenSeq int64  `json:"brokenSeq,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
