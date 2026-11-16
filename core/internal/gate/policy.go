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

// Normalize fills zero-value policy fields with defaults.
//
// NOTE: the task text spells this as a method on domain.GatePolicy, which Go
// forbids (methods cannot be declared on types from another package), so it
// is a package-level function instead.
func Normalize(p domain.GatePolicy) domain.GatePolicy {
	if p.Version == "" {
		p.Version = QuestionsVersion
	}
	if p.MinPass == 0 {
		p.MinPass = 0.55
	}
	if p.MinBlock == 0 {
		p.MinBlock = 0.50
	}
	return p
}

// PolicyHash returns a stable hash of the policy and the check order.
//
// The hashed JSON is deterministic:
// {"version":"gate-v1","minPass":"0.550000","minBlock":"0.500000","checks":["intent",...]}
func PolicyHash(p domain.GatePolicy) string {
	p = Normalize(p)
	var sb strings.Builder
	sb.WriteString(`{"version":`)
	sb.WriteString(strconv.Quote(p.Version))
	sb.WriteString(`,"minPass":`)
	sb.WriteString(strconv.Quote(strconv.FormatFloat(p.MinPass, 'f', 6, 64)))
	sb.WriteString(`,"minBlock":`)
	sb.WriteString(strconv.Quote(strconv.FormatFloat(p.MinBlock, 'f', 6, 64)))
	sb.WriteString(`,"checks":[`)
	for i, c := range CheckOrder {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.Quote(c))
	}
	sb.WriteString(`]}`)
	sum := sha256.Sum256([]byte(sb.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
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
