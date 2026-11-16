package gate

import (
	"errors"
	"reflect"
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

func entryReq() domain.GateRequest {
	return domain.GateRequest{
		SignalID:   "sig-1",
		Instrument: domain.Instrument{Venue: "binance", Symbol: "BTCUSDT"},
		Side:       domain.SideBuy,
		IntentKind: domain.IntentEntry,
		Timeframe:  "1h",
		Source:     "test",
	}
}

func choiceAnswer(choice string, probs map[string]float64) domain.Answer {
	return domain.Answer{Type: domain.QuestionChoice, Choice: choice, Probabilities: probs}
}

// goodAnswers returns an answer set that sails through to ALLOW.
func goodAnswers() map[string]domain.Answer {
	return map[string]domain.Answer{
		"data_quality":     choiceAnswer("ok", map[string]float64{"ok": 0.9, "insufficient": 0.1}),
		"signal_alignment": choiceAnswer("aligned", map[string]float64{"aligned": 0.8, "neutral": 0.1, "conflict": 0.1}),
		"market_regime":    choiceAnswer("favorable", map[string]float64{"favorable": 0.7, "neutral": 0.2, "adverse": 0.1}),
		"risk_check":       choiceAnswer("pass", map[string]float64{"pass": 0.9, "block": 0.1}),
	}
}

func okOutcome(mut func(map[string]domain.Answer)) domain.DecisionOutcome {
	a := goodAnswers()
	if mut != nil {
		mut(a)
	}
	return domain.DecisionOutcome{Response: &domain.DecisionResponse{Answers: a, Model: "test-model"}}
}

func TestEvaluateTable(t *testing.T) {
	pol := DefaultPolicy()
	cases := []struct {
		name        string
		req         domain.GateRequest
		outcome     domain.DecisionOutcome
		policy      domain.GatePolicy
		wantVerdict domain.Verdict
		wantCode    string
		wantChecks  []string
	}{
		{
			name:        "exit_not_gated",
			req:         func() domain.GateRequest { r := entryReq(); r.IntentKind = domain.IntentExit; return r }(),
			outcome:     domain.DecisionOutcome{},
			policy:      pol,
			wantVerdict: domain.VerdictSkip,
			wantCode:    "exit_not_gated",
			wantChecks:  []string{"intent"},
		},
		{
			name:        "provider_unavailable",
			req:         entryReq(),
			outcome:     domain.DecisionOutcome{Err: errors.New("boom")},
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "provider_unavailable",
			wantChecks:  []string{"intent", "provider"},
		},
		{
			name:        "provider invalid_output code",
			req:         entryReq(),
			outcome:     domain.DecisionOutcome{Err: domain.NewError(domain.CodeInvalidOutput, "bad json")},
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_output",
			wantChecks:  []string{"intent", "provider"},
		},
		{
			name:        "nil response invalid_output",
			req:         entryReq(),
			outcome:     domain.DecisionOutcome{},
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_output",
			wantChecks:  []string{"intent", "provider"},
		},
		{
			name: "answers missing question",
			req:  entryReq(),
			outcome: func() domain.DecisionOutcome {
				o := okOutcome(nil)
				delete(o.Response.Answers, "risk_check")
				return o
			}(),
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_output",
			wantChecks:  []string{"intent", "provider", "answers"},
		},
		{
			name: "answers wrong type",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				an := a["risk_check"]
				an.Type = domain.QuestionScore
				a["risk_check"] = an
			}),
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_output",
			wantChecks:  []string{"intent", "provider", "answers"},
		},
		{
			name: "answers choice not in probabilities",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				an := a["risk_check"]
				an.Choice = "pass"
				an.Probabilities = map[string]float64{"block": 1.0}
				a["risk_check"] = an
			}),
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_output",
			wantChecks:  []string{"intent", "provider", "answers"},
		},
		{
			name: "answers unknown choice",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				an := a["risk_check"]
				an.Choice = "maybe"
				an.Probabilities = map[string]float64{"maybe": 0.6, "pass": 0.4}
				a["risk_check"] = an
			}),
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_output",
			wantChecks:  []string{"intent", "provider", "answers"},
		},
		{
			name: "insufficient_data",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				a["data_quality"] = choiceAnswer("insufficient", map[string]float64{"ok": 0.39, "insufficient": 0.61})
			}),
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "insufficient_data",
			wantChecks:  []string{"intent", "provider", "answers", "data_quality"},
		},
		{
			name: "risk_block",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				a["risk_check"] = choiceAnswer("block", map[string]float64{"pass": 0.4, "block": 0.6})
			}),
			policy:      pol,
			wantVerdict: domain.VerdictReject,
			wantCode:    "risk_block",
			wantChecks:  []string{"intent", "provider", "answers", "data_quality", "risk_check"},
		},
		{
			name: "directional_block",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				a["signal_alignment"] = choiceAnswer("conflict", map[string]float64{"aligned": 0.1, "neutral": 0.2, "conflict": 0.7})
				a["market_regime"] = choiceAnswer("adverse", map[string]float64{"favorable": 0.1, "neutral": 0.2, "adverse": 0.7})
				a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 0.9, "block": 0.1})
			}),
			policy:      pol,
			wantVerdict: domain.VerdictReject,
			wantCode:    "directional_block",
			wantChecks:  []string{"intent", "provider", "answers", "data_quality", "risk_check", "directional"},
		},
		{
			name: "directional passes when only one side adverse",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				a["signal_alignment"] = choiceAnswer("conflict", map[string]float64{"aligned": 0.1, "neutral": 0.2, "conflict": 0.7})
				a["market_regime"] = choiceAnswer("favorable", map[string]float64{"favorable": 0.7, "neutral": 0.2, "adverse": 0.1})
			}),
			policy:      pol,
			wantVerdict: domain.VerdictAllow,
			wantCode:    "allow",
			wantChecks:  []string{"intent", "provider", "answers", "data_quality", "risk_check", "directional", "confidence"},
		},
		{
			name: "low_confidence",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 0.5, "block": 0.1, "other": 0.4})
			}),
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "low_confidence",
			wantChecks:  []string{"intent", "provider", "answers", "data_quality", "risk_check", "directional", "confidence"},
		},
		{
			name:        "allow",
			req:         entryReq(),
			outcome:     okOutcome(nil),
			policy:      pol,
			wantVerdict: domain.VerdictAllow,
			wantCode:    "allow",
			wantChecks:  []string{"intent", "provider", "answers", "data_quality", "risk_check", "directional", "confidence"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.req, tc.outcome, tc.policy)
			if got.Verdict != tc.wantVerdict {
				t.Fatalf("verdict = %q, want %q (code %q reasons %v)", got.Verdict, tc.wantVerdict, got.Code, got.Reasons)
			}
			if got.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", got.Code, tc.wantCode)
			}
			if got.Checks == nil || got.Reasons == nil {
				t.Fatalf("checks/reasons must be non-nil")
			}
			names := make([]string, len(got.Checks))
			for i, c := range got.Checks {
				names[i] = c.Name
			}
			if !reflect.DeepEqual(names, tc.wantChecks) {
				t.Fatalf("checks = %v, want %v", names, tc.wantChecks)
			}
			// Order must be a prefix of CheckOrder.
			for i, n := range names {
				if CheckOrder[i] != n {
					t.Fatalf("check %d = %q, want CheckOrder %q", i, n, CheckOrder[i])
				}
			}
			// Last check fails except on ALLOW; earlier checks pass.
			for i, c := range got.Checks {
				wantPassed := tc.wantCode == "allow" || i < len(got.Checks)-1
				if tc.wantCode == "allow" && !c.Passed {
					t.Fatalf("allow check %q not passed", c.Name)
				}
				if tc.wantCode != "allow" && c.Passed != wantPassed {
					t.Fatalf("check %q passed=%v, want %v", c.Name, c.Passed, wantPassed)
				}
			}
			if len(got.Reasons) == 0 {
				t.Fatalf("reasons empty")
			}
			if !got.DecidedAt.IsZero() {
				t.Fatalf("DecidedAt must be zero")
			}
			if got.DecisionID != "" {
				t.Fatalf("DecisionID must be empty")
			}
			if got.PolicyHash == "" {
				t.Fatalf("PolicyHash empty")
			}
		})
	}
}

func TestEvaluateThresholdBoundaries(t *testing.T) {
	pol := DefaultPolicy() // minPass 0.55, minBlock 0.50

	// Exactly minBlock triggers insufficient_data (>=).
	got := Evaluate(entryReq(), okOutcome(func(a map[string]domain.Answer) {
		a["data_quality"] = choiceAnswer("insufficient", map[string]float64{"ok": 0.5, "insufficient": 0.5})
	}), pol)
	if got.Code != "insufficient_data" {
		t.Fatalf("boundary data_quality 0.50: code = %q, want insufficient_data", got.Code)
	}

	// Exactly minBlock triggers risk_block (>=).
	got = Evaluate(entryReq(), okOutcome(func(a map[string]domain.Answer) {
		a["risk_check"] = choiceAnswer("block", map[string]float64{"pass": 0.55, "block": 0.5})
	}), pol)
	if got.Code != "risk_block" {
		t.Fatalf("boundary risk 0.50: code = %q, want risk_block", got.Code)
	}

	// Exactly minPass passes confidence (only < fails).
	got = Evaluate(entryReq(), okOutcome(func(a map[string]domain.Answer) {
		a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 0.55, "block": 0.45})
	}), pol)
	if got.Code != "allow" {
		t.Fatalf("boundary confidence 0.55: code = %q, want allow", got.Code)
	}

	// Just below minPass fails.
	got = Evaluate(entryReq(), okOutcome(func(a map[string]domain.Answer) {
		a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 0.5499, "block": 0.1, "x": 0.3501})
	}), pol)
	if got.Code != "low_confidence" {
		t.Fatalf("below confidence: code = %q, want low_confidence", got.Code)
	}

	// Directional boundary: both p_max exactly 0.50 -> block.
	got = Evaluate(entryReq(), okOutcome(func(a map[string]domain.Answer) {
		a["signal_alignment"] = choiceAnswer("conflict", map[string]float64{"aligned": 0.25, "neutral": 0.25, "conflict": 0.5})
		a["market_regime"] = choiceAnswer("adverse", map[string]float64{"favorable": 0.25, "neutral": 0.25, "adverse": 0.5})
		a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 0.9, "block": 0.1})
	}), pol)
	if got.Code != "directional_block" {
		t.Fatalf("directional boundary 0.50: code = %q, want directional_block", got.Code)
	}

	// Directional just below on one side -> allow.
	got = Evaluate(entryReq(), okOutcome(func(a map[string]domain.Answer) {
		a["signal_alignment"] = choiceAnswer("conflict", map[string]float64{"aligned": 0.25, "neutral": 0.26, "conflict": 0.49})
		a["market_regime"] = choiceAnswer("adverse", map[string]float64{"favorable": 0.2, "neutral": 0.2, "adverse": 0.6})
		a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 0.9, "block": 0.1})
	}), pol)
	if got.Code != "allow" {
		t.Fatalf("directional below: code = %q, want allow", got.Code)
	}
}

func TestEvaluateDeterminismAndModel(t *testing.T) {
	pol := DefaultPolicy()
	req := entryReq()
	out := okOutcome(nil)
	a := Evaluate(req, out, pol)
	b := Evaluate(req, out, pol)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("same input gave different verdicts:\n%#v\n%#v", a, b)
	}
	if a.Model != "test-model" {
		t.Fatalf("model = %q, want test-model", a.Model)
	}
	if a.PolicyHash != PolicyHash(pol) {
		t.Fatalf("policy hash mismatch")
	}
}

func TestEvaluateDirectionalIgnoresJunkKeys(t *testing.T) {
	pol := DefaultPolicy()
	// conflict + adverse chosen, but real option p_max below minBlock;
	// a junk key with 0.99 must not inflate p_max into a directional_block.
	got := Evaluate(entryReq(), okOutcome(func(a map[string]domain.Answer) {
		a["signal_alignment"] = choiceAnswer("conflict", map[string]float64{
			"aligned": 0.2, "neutral": 0.2, "conflict": 0.4, "junk": 0.99,
		})
		a["market_regime"] = choiceAnswer("adverse", map[string]float64{
			"favorable": 0.2, "neutral": 0.2, "adverse": 0.4, "junk": 0.99,
		})
		a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 0.9, "block": 0.1})
	}), pol)
	if got.Code != "allow" {
		t.Fatalf("junk key inflated p_max: code = %q, want allow (checks %v)", got.Code, got.Checks)
	}
}
