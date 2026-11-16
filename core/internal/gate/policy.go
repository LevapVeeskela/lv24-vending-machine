package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// QuestionsVersion is the frozen gate question set identifier.
const QuestionsVersion = "gate-v1"

// CheckOrder is the fixed evaluation order. It is part of the policy hash.
// Do not mutate this slice.
var CheckOrder = []string{"intent", "provider", "answers", "data_quality", "risk_check", "directional", "confidence"}

// DefaultPolicy returns the baseline gate policy.
func DefaultPolicy() domain.GatePolicy {
	return domain.DefaultGatePolicy()
}

// Normalize fills an empty policy version with the frozen default.
//
// NOTE: the task text spells this as a method on domain.GatePolicy, which Go
// forbids (methods cannot be declared on types from another package), so it
// is a package-level function instead.
//
// Thresholds are never defaulted here: a MinPass or MinBlock outside (0,1]
// (including zero, NaN and infinities) is invalid and must be rejected by
// Evaluate, not silently replaced.
func Normalize(p domain.GatePolicy) domain.GatePolicy {
	if p.Version == "" {
		p.Version = QuestionsVersion
	}
	return p
}

// PolicyHash returns a stable 64-char lowercase hex hash of the normalized
// policy and the full frozen question set: version, thresholds, check order,
// and for every question its id, type, instructions and ordered options
// (key plus description). Deterministic serialization with stable field
// order and question/option order; thresholds use the shortest exact
// round-trip form (strconv.FormatFloat with 'g', -1).
func PolicyHash(p domain.GatePolicy) string {
	return policyHashWithQuestions(Normalize(p), Questions())
}

// policyHashWithQuestions is the pure hashing core behind PolicyHash. The
// question list is a parameter so tests can prove sensitivity to question
// text, option keys, option descriptions and option order.
func policyHashWithQuestions(p domain.GatePolicy, qs []domain.Question) string {
	var sb strings.Builder
	sb.WriteString(`{"version":`)
	sb.WriteString(strconv.Quote(p.Version))
	sb.WriteString(`,"minPass":`)
	sb.WriteString(strconv.Quote(strconv.FormatFloat(p.MinPass, 'g', -1, 64)))
	sb.WriteString(`,"minBlock":`)
	sb.WriteString(strconv.Quote(strconv.FormatFloat(p.MinBlock, 'g', -1, 64)))
	sb.WriteString(`,"checks":[`)
	for i, c := range CheckOrder {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.Quote(c))
	}
	sb.WriteString(`],"questions":[`)
	for i, q := range qs {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"id":`)
		sb.WriteString(strconv.Quote(q.ID))
		sb.WriteString(`,"type":`)
		sb.WriteString(strconv.Quote(string(q.Type)))
		sb.WriteString(`,"instructions":`)
		sb.WriteString(strconv.Quote(q.Instructions))
		sb.WriteString(`,"options":[`)
		for j, o := range q.Options {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`{"key":`)
			sb.WriteString(strconv.Quote(o.Key))
			sb.WriteString(`,"description":`)
			sb.WriteString(strconv.Quote(o.Description))
			sb.WriteByte('}')
		}
		sb.WriteString(`]}`)
	}
	sb.WriteString(`]}`)
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

// Questions returns the frozen gate-v1 question set in fixed order.
func Questions() []domain.Question {
	return []domain.Question{
		{
			ID:           "data_quality",
			Type:         domain.QuestionChoice,
			Instructions: "Assess whether the available market data is sufficient for a decision.",
			Options: []domain.Option{
				{Key: "ok", Description: "Data is sufficient for a reliable decision."},
				{Key: "insufficient", Description: "Data is missing or too noisy for a reliable decision."},
			},
		},
		{
			ID:           "signal_alignment",
			Type:         domain.QuestionChoice,
			Instructions: "Assess how the trading signal aligns with the current market context.",
			Options: []domain.Option{
				{Key: "aligned", Description: "Signal agrees with the market context."},
				{Key: "neutral", Description: "Signal is neither supported nor contradicted by the market context."},
				{Key: "conflict", Description: "Signal contradicts the market context."},
			},
		},
		{
			ID:           "market_regime",
			Type:         domain.QuestionChoice,
			Instructions: "Classify the current market regime for the instrument.",
			Options: []domain.Option{
				{Key: "favorable", Description: "Regime supports taking new positions."},
				{Key: "neutral", Description: "Regime is neutral for new positions."},
				{Key: "adverse", Description: "Regime is adverse for new positions."},
			},
		},
		{
			ID:           "risk_check",
			Type:         domain.QuestionChoice,
			Instructions: "Apply the risk checklist to the proposed intent.",
			Options: []domain.Option{
				{Key: "pass", Description: "All risk checks pass."},
				{Key: "block", Description: "At least one risk check blocks execution."},
			},
		},
	}
}
