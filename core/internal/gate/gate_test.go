package gate

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// nan returns a quiet NaN for invalid-policy and invalid-output cases.
func nan() float64 { return math.NaN() }

// inf returns +Inf for invalid-policy cases.
func inf() float64 { return math.Inf(1) }

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
				a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 0.54, "block": 0.46})
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
		{
			name:        "invalid_policy_zero_thresholds",
			req:         entryReq(),
			outcome:     okOutcome(nil),
			policy:      domain.GatePolicy{Version: "gate-v1"},
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_policy",
			wantChecks:  []string{"policy"},
		},
		{
			name:        "invalid_policy_negative",
			req:         entryReq(),
			outcome:     okOutcome(nil),
			policy:      domain.GatePolicy{Version: "gate-v1", MinPass: -0.1, MinBlock: 0.5},
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_policy",
			wantChecks:  []string{"policy"},
		},
		{
			name:        "invalid_policy_above_one",
			req:         entryReq(),
			outcome:     okOutcome(nil),
			policy:      domain.GatePolicy{Version: "gate-v1", MinPass: 0.55, MinBlock: 1.5},
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_policy",
			wantChecks:  []string{"policy"},
		},
		{
			name:        "invalid_policy_nan",
			req:         entryReq(),
			outcome:     okOutcome(nil),
			policy:      domain.GatePolicy{Version: "gate-v1", MinPass: nan(), MinBlock: 0.5},
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_policy",
			wantChecks:  []string{"policy"},
		},
		{
			name:        "invalid_policy_inf",
			req:         entryReq(),
			outcome:     okOutcome(nil),
			policy:      domain.GatePolicy{Version: "gate-v1", MinPass: 0.55, MinBlock: inf()},
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_policy",
			wantChecks:  []string{"policy"},
		},
		{
			name:        "invalid_policy_beats_exit",
			req:         func() domain.GateRequest { r := entryReq(); r.IntentKind = domain.IntentExit; return r }(),
			outcome:     domain.DecisionOutcome{},
			policy:      domain.GatePolicy{MinPass: 0, MinBlock: 0},
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_policy",
			wantChecks:  []string{"policy"},
		},
		{
			name: "answers extra probability key",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 0.9, "block": 0.1, "other": 0.0})
			}),
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_output",
			wantChecks:  []string{"intent", "provider", "answers"},
		},
		{
			name: "answers missing probability key",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 1.0})
			}),
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_output",
			wantChecks:  []string{"intent", "provider", "answers"},
		},
		{
			name: "answers probability out of range",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 1.2, "block": -0.2})
			}),
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_output",
			wantChecks:  []string{"intent", "provider", "answers"},
		},
		{
			name: "answers probability nan",
			req:  entryReq(),
			outcome: okOutcome(func(a map[string]domain.Answer) {
				a["data_quality"] = choiceAnswer("ok", map[string]float64{"ok": nan(), "insufficient": 0.1})
			}),
			policy:      pol,
			wantVerdict: domain.VerdictAbstain,
			wantCode:    "invalid_output",
			wantChecks:  []string{"intent", "provider", "answers"},
		},
		{
			name:        "empty version with valid thresholds still allows",
			req:         entryReq(),
			outcome:     okOutcome(nil),
			policy:      domain.GatePolicy{MinPass: 0.55, MinBlock: 0.5},
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
			// Order must be a prefix of CheckOrder, except the policy
			// guard: it runs before the ordered checks and is not part
			// of the frozen order.
			if !(len(names) == 1 && names[0] == "policy") {
				for i, n := range names {
					if CheckOrder[i] != n {
						t.Fatalf("check %d = %q, want CheckOrder %q", i, n, CheckOrder[i])
					}
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

// TestEvaluateProviderErrorCodes pins the provider-error mapping: every
// provider failure is ABSTAIN, and only the code separates auth, request
// misconfiguration, invalid output and transient outage.
func TestEvaluateProviderErrorCodes(t *testing.T) {
	pol := DefaultPolicy()
	// probe is a fake credential/body marker that must never reach the
	// verdict for auth and misconfiguration failures.
	const probe = "token=sk-live-abc123 body={\"secret\":true}"

	cases := []struct {
		name        string
		err         error
		wantCode    string
		wantGeneric bool // reason and check detail must not carry err text
	}{
		{"auth 401", domain.NewError(domain.CodeAuth, "401 unauthorized "+probe), CodeProviderAuth, true},
		{"auth 403", domain.NewError(domain.CodeAuth, "403 forbidden "+probe), CodeProviderAuth, true},
		{"payment 402", domain.NewError(domain.CodePaymentRequired, "402 payment required "+probe), CodeProviderAuth, true},
		{"invalid_request 400", domain.NewError(domain.CodeInvalidRequest, "400 bad request "+probe), CodeProviderMisconfigured, true},
		{"invalid_request 404", domain.NewError(domain.CodeInvalidRequest, "404 not found "+probe), CodeProviderMisconfigured, true},
		{"invalid_request 413", domain.NewError(domain.CodeInvalidRequest, "413 too large "+probe), CodeProviderMisconfigured, true},
		{"invalid_request 422", domain.NewError(domain.CodeInvalidRequest, "422 unprocessable "+probe), CodeProviderMisconfigured, true},
		{"invalid_output", domain.NewError(domain.CodeInvalidOutput, "bad json"), CodeProviderInvalidOutput, false},
		{"unavailable 500", domain.NewError(domain.CodeUnavailable, "500 upstream"), CodeProviderUnavailable, false},
		{"timeout", domain.NewError(domain.CodeTimeout, "deadline exceeded"), CodeProviderUnavailable, false},
		{"rate_limit", domain.NewError(domain.CodeRateLimit, "429 too many"), CodeProviderUnavailable, false},
		{"unknown", errors.New("boom"), CodeProviderUnavailable, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(entryReq(), domain.DecisionOutcome{Err: tc.err}, pol)
			if got.Verdict != domain.VerdictAbstain {
				t.Fatalf("verdict = %q, want ABSTAIN", got.Verdict)
			}
			if got.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", got.Code, tc.wantCode)
			}
			if len(got.Checks) != 2 || got.Checks[1].Name != "provider" || got.Checks[1].Passed {
				t.Fatalf("checks = %+v, want failed provider check after intent", got.Checks)
			}
			if len(got.Reasons) == 0 {
				t.Fatalf("reasons empty")
			}
			if tc.wantGeneric {
				for _, r := range got.Reasons {
					if strings.Contains(r, probe) || strings.Contains(r, "sk-live") {
						t.Fatalf("reason leaks provider error text: %q", r)
					}
				}
				if d := got.Checks[1].Detail; strings.Contains(d, probe) || strings.Contains(d, "sk-live") {
					t.Fatalf("check detail leaks provider error text: %q", d)
				}
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
		a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 0.5499, "block": 0.4501})
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

func TestEvaluateInvalidPolicyThresholds(t *testing.T) {
	valid := DefaultPolicy()
	bads := map[string]float64{
		"nan":       math.NaN(),
		"pos_inf":   math.Inf(1),
		"neg_inf":   math.Inf(-1),
		"zero":      0,
		"negative":  -0.25,
		"above_one": 1.5,
	}
	for name, bad := range bads {
		for _, field := range []string{"minPass", "minBlock"} {
			pol := valid
			if field == "minPass" {
				pol.MinPass = bad
			} else {
				pol.MinBlock = bad
			}
			got := Evaluate(entryReq(), okOutcome(nil), pol)
			if got.Verdict != domain.VerdictAbstain || got.Code != "invalid_policy" {
				t.Fatalf("%s=%s: verdict=%q code=%q, want ABSTAIN invalid_policy",
					field, name, got.Verdict, got.Code)
			}
			if len(got.Checks) != 1 || got.Checks[0].Name != "policy" || got.Checks[0].Passed {
				t.Fatalf("%s=%s: checks=%+v, want single failed policy check", field, name, got.Checks)
			}
			if len(got.Reasons) == 0 {
				t.Fatalf("%s=%s: reasons empty", field, name)
			}
			if got.PolicyHash == "" {
				t.Fatalf("%s=%s: PolicyHash empty", field, name)
			}
		}
	}
	// Boundary 1 is valid for both thresholds.
	edge := domain.GatePolicy{Version: "gate-v1", MinPass: 1, MinBlock: 1}
	got := Evaluate(entryReq(), okOutcome(func(a map[string]domain.Answer) {
		a["data_quality"] = choiceAnswer("ok", map[string]float64{"ok": 0.99, "insufficient": 0.01})
		a["signal_alignment"] = choiceAnswer("aligned", map[string]float64{"aligned": 0.99, "neutral": 0.005, "conflict": 0.005})
		a["market_regime"] = choiceAnswer("favorable", map[string]float64{"favorable": 0.99, "neutral": 0.005, "adverse": 0.005})
		a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 1.0, "block": 0.0})
	}), edge)
	if got.Code != "allow" {
		t.Fatalf("thresholds at 1: code = %q, want allow", got.Code)
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

func TestEvaluateRejectsJunkKeys(t *testing.T) {
	pol := DefaultPolicy()
	// A junk key breaks the exact option-key match, so the gate must
	// abstain with invalid_output instead of trusting the extra data.
	got := Evaluate(entryReq(), okOutcome(func(a map[string]domain.Answer) {
		a["signal_alignment"] = choiceAnswer("aligned", map[string]float64{
			"aligned": 0.8, "neutral": 0.1, "conflict": 0.05, "junk": 0.05,
		})
		a["market_regime"] = choiceAnswer("favorable", map[string]float64{
			"favorable": 0.7, "neutral": 0.2, "adverse": 0.05, "junk": 0.05,
		})
		a["risk_check"] = choiceAnswer("pass", map[string]float64{"pass": 0.9, "block": 0.1})
	}), pol)
	if got.Code != "invalid_output" {
		t.Fatalf("junk keys: code = %q, want invalid_output (checks %v)", got.Code, got.Checks)
	}
	if got.Verdict != domain.VerdictAbstain {
		t.Fatalf("junk keys: verdict = %q, want ABSTAIN", got.Verdict)
	}
	names := make([]string, len(got.Checks))
	for i, c := range got.Checks {
		names[i] = c.Name
	}
	want := []string{"intent", "provider", "answers"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("junk keys checks = %v, want %v", names, want)
	}
}
