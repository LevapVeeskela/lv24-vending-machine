package domain

import (
	"encoding/json"
	"time"
)

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
