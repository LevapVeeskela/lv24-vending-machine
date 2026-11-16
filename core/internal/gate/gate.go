package gate

import (
	"fmt"
	"math"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// questionIDs lists the expected gate-v1 answers in order.
var questionIDs = []string{"data_quality", "signal_alignment", "market_regime", "risk_check"}

// allowedChoices mirrors Questions() option keys.
var allowedChoices = map[string]map[string]bool{
	"data_quality":     {"ok": true, "insufficient": true},
	"signal_alignment": {"aligned": true, "neutral": true, "conflict": true},
	"market_regime":    {"favorable": true, "neutral": true, "adverse": true},
	"risk_check":       {"pass": true, "block": true},
}

// Evaluate applies the gate-v1 policy to a provider outcome.
//
// It performs no I/O and reads no clock: DecidedAt stays zero and DecisionID
// stays empty; the caller fills them. Checks and Reasons are always non-nil.
func Evaluate(req domain.GateRequest, outcome domain.DecisionOutcome, policy domain.GatePolicy) domain.GateVerdict {
	policy = Normalize(policy)
	hash := PolicyHash(policy)
	model := ""
	if outcome.Response != nil {
		model = outcome.Response.Model
	}

	checks := []domain.Check{}
	reasons := []string{}

	pass := func(name, detail string) {
		checks = append(checks, domain.Check{Name: name, Passed: true, Detail: detail})
	}
	fail := func(verdict domain.Verdict, code, checkName, detail, reason string) domain.GateVerdict {
		checks = append(checks, domain.Check{Name: checkName, Passed: false, Detail: detail})
		reasons = append(reasons, reason)
		return domain.GateVerdict{
			Verdict:    verdict,
			Code:       code,
			Reasons:    reasons,
			Checks:     checks,
			Model:      model,
			PolicyHash: hash,
		}
	}

	// 0. policy (validated as given; only an empty version is defaulted).
	if !validThreshold(policy.MinPass) || !validThreshold(policy.MinBlock) {
		detail := fmt.Sprintf("minPass=%.4g minBlock=%.4g must both be in (0,1]", policy.MinPass, policy.MinBlock)
		return fail(domain.VerdictAbstain, "invalid_policy", "policy",
			detail,
			"invalid policy: "+detail)
	}

	// 1. intent
	if req.IntentKind == domain.IntentExit {
		return fail(domain.VerdictSkip, "exit_not_gated", "intent",
			"intentKind=exit",
			"intentKind=exit: exits are not gated")
	}
	pass("intent", fmt.Sprintf("intentKind=%s", string(req.IntentKind)))

	// 2a. provider error
	if outcome.Err != nil {
		code := "provider_unavailable"
		if domain.CodeOf(outcome.Err) == domain.CodeInvalidOutput {
			code = "invalid_output"
		}
		detail := outcome.Err.Error()
		return fail(domain.VerdictAbstain, code, "provider",
			detail,
			"provider error: "+detail)
	}
	if outcome.Response == nil {
		detail := "nil decision response"
		return fail(domain.VerdictAbstain, "invalid_output", "provider",
			detail,
			"invalid provider output: "+detail)
	}
	pass("provider", "provider response ok")

	// 2b. answers validation
	answers := outcome.Response.Answers
	if msg, ok := validateAnswers(answers); !ok {
		return fail(domain.VerdictAbstain, "invalid_output", "answers",
			msg,
			"invalid provider output: "+msg)
	}
	pass("answers", "all 4 gate-v1 answers valid")

	prob := func(question, key string) (float64, bool) {
		v, ok := answers[question].Probabilities[key]
		if !ok {
			return 0, false
		}
		return v, true
	}
	maxProb := func(question string) float64 {
		m := 0.0
		allowed := allowedChoices[question]
		for k, v := range answers[question].Probabilities {
			if !allowed[k] {
				continue
			}
			if v > m {
				m = v
			}
		}
		return m
	}

	// 3. data_quality: p(insufficient) >= minBlock -> ABSTAIN
	pIns, ok := prob("data_quality", "insufficient")
	if !ok {
		msg := `missing probability "insufficient" for question "data_quality"`
		return fail(domain.VerdictAbstain, "invalid_output", "answers", msg, "invalid provider output: "+msg)
	}
	if pIns >= policy.MinBlock {
		detail := fmt.Sprintf("p(insufficient)=%.4f threshold=%.4f", pIns, policy.MinBlock)
		return fail(domain.VerdictAbstain, "insufficient_data", "data_quality",
			detail,
			"insufficient data: "+detail)
	}
	pass("data_quality", fmt.Sprintf("p(insufficient)=%.4f threshold=%.4f", pIns, policy.MinBlock))

	// 4. risk_check: p(block) >= minBlock -> REJECT
	pBlock, ok := prob("risk_check", "block")
	if !ok {
		msg := `missing probability "block" for question "risk_check"`
		return fail(domain.VerdictAbstain, "invalid_output", "answers", msg, "invalid provider output: "+msg)
	}
	if pBlock >= policy.MinBlock {
		detail := fmt.Sprintf("p(block)=%.4f threshold=%.4f", pBlock, policy.MinBlock)
		return fail(domain.VerdictReject, "risk_block", "risk_check",
			detail,
			"risk blocked: "+detail)
	}
	pass("risk_check", fmt.Sprintf("p(block)=%.4f threshold=%.4f", pBlock, policy.MinBlock))

	// 5. directional: conflict + adverse chosen, both p_max >= minBlock -> REJECT
	sigChoice := answers["signal_alignment"].Choice
	regChoice := answers["market_regime"].Choice
	sigMax := maxProb("signal_alignment")
	regMax := maxProb("market_regime")
	dirDetail := fmt.Sprintf("signal_alignment=%s(p_max=%.4f) market_regime=%s(p_max=%.4f) threshold=%.4f",
		sigChoice, sigMax, regChoice, regMax, policy.MinBlock)
	if sigChoice == "conflict" && regChoice == "adverse" && sigMax >= policy.MinBlock && regMax >= policy.MinBlock {
		return fail(domain.VerdictReject, "directional_block", "directional",
			dirDetail,
			"directional blocked: "+dirDetail)
	}
	pass("directional", dirDetail)

	// 6. confidence: p(pass) < minPass -> ABSTAIN
	pPass, ok := prob("risk_check", "pass")
	if !ok {
		msg := `missing probability "pass" for question "risk_check"`
		return fail(domain.VerdictAbstain, "invalid_output", "answers", msg, "invalid provider output: "+msg)
	}
	if pPass < policy.MinPass {
		detail := fmt.Sprintf("p(pass)=%.4f threshold=%.4f", pPass, policy.MinPass)
		return fail(domain.VerdictAbstain, "low_confidence", "confidence",
			detail,
			"low confidence: "+detail)
	}
	pass("confidence", fmt.Sprintf("p(pass)=%.4f threshold=%.4f", pPass, policy.MinPass))

	// 7. allow
	reasons = append(reasons, "all gate checks passed")
	return domain.GateVerdict{
		Verdict:    domain.VerdictAllow,
		Code:       "allow",
		Reasons:    reasons,
		Checks:     checks,
		Model:      model,
		PolicyHash: hash,
	}
}

// validThreshold reports whether a gate threshold is usable: finite and in (0,1].
func validThreshold(v float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	return v > 0 && v <= 1
}

// validateAnswers checks presence, choice type, and that every gate-v1
// answer carries exactly the question's option keys (no missing, no extra)
// with finite [0,1] values, and a choice that is one of the option keys.
// The gate is the last line of defense and never trusts the provider adapter.
func validateAnswers(answers map[string]domain.Answer) (string, bool) {
	if answers == nil {
		return "missing answers", false
	}
	for _, id := range questionIDs {
		a, ok := answers[id]
		if !ok {
			return fmt.Sprintf("missing answer for question %q", id), false
		}
		if a.Type != domain.QuestionChoice {
			return fmt.Sprintf("question %q: expected type choice, got %q", id, string(a.Type)), false
		}
		allowed := allowedChoices[id]
		if len(a.Probabilities) != len(allowed) {
			return fmt.Sprintf("question %q: probability keys do not exactly match options", id), false
		}
		for k, v := range a.Probabilities {
			if !allowed[k] {
				return fmt.Sprintf("question %q: unexpected probability key %q", id, k), false
			}
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
				return fmt.Sprintf("question %q: probability %q out of range [0, 1]", id, k), false
			}
		}
		for k := range allowed {
			if _, ok := a.Probabilities[k]; !ok {
				return fmt.Sprintf("question %q: missing probability %q", id, k), false
			}
		}
		if _, ok := a.Probabilities[a.Choice]; !ok {
			return fmt.Sprintf("question %q: choice %q not in probabilities", id, a.Choice), false
		}
		if !allowed[a.Choice] {
			return fmt.Sprintf("question %q: unknown choice %q", id, a.Choice), false
		}
	}
	return "", true
}
