package domain

import (
	"encoding/json"
	"time"
)

// QuestionType enumerates the supported decision question kinds.
type QuestionType string

// Supported question types.
const (
	// QuestionChoice asks the model to pick one option.
	QuestionChoice QuestionType = "choice"
	// QuestionNoul asks for a normalised -1..+1 assessment.
	QuestionNoul QuestionType = "noul"
	// QuestionScore asks for a numeric score.
	QuestionScore QuestionType = "score"
)

// Option is a single named choice for a choice question.
type Option struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

// Question is one advisory question sent to a decision provider.
type Question struct {
	ID           string       `json:"id"`
	Type         QuestionType `json:"type"`
	Instructions string       `json:"instructions,omitempty"`
	Criteria     string       `json:"criteria,omitempty"`
	Options      []Option     `json:"options,omitempty"`
}

// DecisionRequest is an advisory request to a decision provider.
type DecisionRequest struct {
	Model     string         `json:"model"`
	State     map[string]any `json:"state,omitempty"`
	Questions []Question     `json:"questions"`
	// Timeout is the per-request deadline; 0 means use the provider default.
	Timeout time.Duration `json:"-"`
}

// Answer is the provider response to a single question.
type Answer struct {
	Type          QuestionType       `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Legend        []string           `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

// Usage reports token consumption, cost and latency of a decision call.
type Usage struct {
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CostUSD      Decimal `json:"costUsd"`
	LatencyMS    int64   `json:"latencyMs,omitempty"`
}

// DecisionResponse is the provider answer set for a DecisionRequest.
type DecisionResponse struct {
	Answers        map[string]Answer `json:"answers"`
	Model          string            `json:"model"`
	RequestedModel string            `json:"requestedModel,omitempty"`
	Usage          Usage             `json:"usage"`
	RequestID      string            `json:"requestId,omitempty"`
	Raw            json.RawMessage   `json:"-"`
}
