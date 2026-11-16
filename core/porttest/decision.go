// Package porttest holds provider conformance suites shared by core and providers.
package porttest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// DecisionOptions tunes the provider under conformance test.
type DecisionOptions struct {
	// BaseURL overrides the provider endpoint root (the test server URL).
	BaseURL string
	// PricePerBillion is the input-token price per 1B tokens, decimal string.
	PricePerBillion string
}

// DecisionSecretRef is the secret reference the test factory must configure.
// The conformance suite resolves it to a sentinel secret value.
const DecisionSecretRef = "conn/test/decision-drex"

// DecisionFactory builds the provider under test for the given server URL.
type DecisionFactory func(t *testing.T, opts DecisionOptions, deps ports.Deps) ports.Decision

// mapSecretStore is a static ports.SecretStore for tests.
type mapSecretStore map[string]string

// Get resolves ref or fails.
func (m mapSecretStore) Get(_ context.Context, ref string) (string, error) {
	v, ok := m[ref]
	if !ok || v == "" {
		return "", context.DeadlineExceeded
	}
	return v, nil
}

// decisionFixtureQuestions returns one question of each supported type.
func decisionFixtureQuestions() []domain.Question {
	return []domain.Question{
		{
			ID:           "team",
			Type:         domain.QuestionChoice,
			Instructions: "Which team should handle this ticket?",
			Options: []domain.Option{
				{Key: "alpha", Description: "Alpha team handles billing"},
				{Key: "beta", Description: "Beta team handles bugs"},
				{Key: "gamma", Description: "Gamma team handles the rest"},
			},
		},
		{
			ID:           "refund",
			Type:         domain.QuestionNoul,
			Instructions: "Does the customer ask for a refund?",
		},
		{
			ID:           "urgency",
			Type:         domain.QuestionScore,
			Instructions: "How urgent is this ticket?",
			Options: []domain.Option{
				{Key: "0", Description: "Routine"},
				{Key: "1", Description: "Soon"},
				{Key: "2", Description: "Urgent"},
			},
		},
	}
}

// decisionOKBody renders a valid vendor response for decisionFixtureQuestions.
func decisionOKBody(model string, inputTokens int, latency float64) string {
	return `{"model":` + quoteJSON(model) + `,"answers":{` +
		`"team":{"type":"choice","choice":"alpha","confidence":0.55,` +
		`"probabilities":{"alpha":0.7,"beta":0.2,"gamma":0.1}},` +
		`"refund":{"type":"noul","noul":0.93},` +
		`"urgency":{"type":"score","score":1.3362,` +
		`"legend":["Routine","Soon","Urgent"],` +
		`"probabilities":{"0":0.2489,"1":0.166,"2":0.5851},"confidence":0.6681}},` +
		`"usage":{"input_tokens":` + itoa(inputTokens) + `,"output_tokens":12},` +
		`"request_id":"req-test-001","evaluation_time_ms":` + ftoa(latency) + `}`
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func ftoa(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// DecisionConformance runs the provider conformance suite for decision adapters.
func DecisionConformance(t *testing.T, newProvider DecisionFactory) {
	t.Helper()

	const sentinel = "test-secret-conformance-001"
	var handler atomic.Value
	handler.Store(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler.Load().(func(http.ResponseWriter, *http.Request))(w, r)
	}))
	t.Cleanup(srv.Close)

	deps := ports.Deps{Secrets: mapSecretStore{DecisionSecretRef: sentinel}}
	p := newProvider(t, DecisionOptions{BaseURL: srv.URL, PricePerBillion: "60"}, deps)

	setHandler := func(fn func(w http.ResponseWriter, r *http.Request)) {
		t.Helper()
		handler.Store(fn)
	}

	baseReq := func() domain.DecisionRequest {
		return domain.DecisionRequest{
			Model:     "drex-max-v1.1",
			State:     map[string]any{"ticket": "charged twice"},
			Questions: decisionFixtureQuestions(),
		}
	}

	decide := func(req domain.DecisionRequest) (domain.DecisionResponse, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return p.Decide(ctx, req)
	}

	t.Run("three question types", func(t *testing.T) {
		setHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(decisionOKBody("drex-max-v1.1", 1000, 104)))
		})
		resp, err := decide(baseReq())
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if resp.Model != "drex-max-v1.1" {
			t.Fatalf("model = %q", resp.Model)
		}
		if resp.RequestedModel != "drex-max-v1.1" {
			t.Fatalf("requested model = %q", resp.RequestedModel)
		}
		if resp.RequestID != "req-test-001" {
			t.Fatalf("request id = %q", resp.RequestID)
		}
		team := resp.Answers["team"]
		if team.Choice != "alpha" || team.Confidence == nil {
			t.Fatalf("team answer = %+v", team)
		}
		if p := team.Probabilities["alpha"]; p != 0.7 {
			t.Fatalf("team alpha prob = %v", p)
		}
		refund := resp.Answers["refund"]
		if refund.Noul == nil || *refund.Noul != 0.93 {
			t.Fatalf("refund answer = %+v", refund)
		}
		urg := resp.Answers["urgency"]
		if urg.Score == nil || *urg.Score != 1.3362 {
			t.Fatalf("urgency answer = %+v", urg)
		}
		if len(urg.Legend) != 3 || urg.Legend[2] != "Urgent" {
			t.Fatalf("urgency legend = %v", urg.Legend)
		}
		if resp.Usage.InputTokens != 1000 || resp.Usage.OutputTokens != 12 {
			t.Fatalf("usage = %+v", resp.Usage)
		}
		if resp.Usage.VendorLatencyMS != 104 {
			t.Fatalf("vendor latency = %d, want 104", resp.Usage.VendorLatencyMS)
		}
		if resp.Usage.LatencyMS < 0 {
			t.Fatalf("measured latency = %d, want >= 0", resp.Usage.LatencyMS)
		}
		if resp.Raw == nil || !strings.Contains(string(resp.Raw), "req-test-001") {
			t.Fatalf("raw response is not preserved")
		}
	})

	t.Run("option order preserved", func(t *testing.T) {
		var captured []byte
		setHandler(func(w http.ResponseWriter, r *http.Request) {
			captured = readAll(t, r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(decisionOKBody("drex-max-v1.1", 10, 1)))
		})
		if _, err := decide(baseReq()); err != nil {
			t.Fatalf("Decide: %v", err)
		}
		body := string(captured)
		if strings.Contains(body, `"options"`) {
			t.Fatalf("wire body must not contain options: %s", body)
		}
		ia := strings.Index(body, `"alpha"`)
		ib := strings.Index(body, `"beta"`)
		ig := strings.Index(body, `"gamma"`)
		if ia < 0 || ib < 0 || ig < 0 || !(ia < ib && ib < ig) {
			t.Fatalf("choice criteria order not preserved: %s", body)
		}
		ir := strings.Index(body, `"Routine"`)
		is := strings.Index(body, `"Soon"`)
		iu := strings.Index(body, `"Urgent"`)
		if ir < 0 || is < 0 || iu < 0 || !(ir < is && is < iu) {
			t.Fatalf("score criteria order not preserved: %s", body)
		}
	})

	t.Run("choice validation", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(answers map[string]any, probs map[string]any)
			model  string
		}{
			{"extra key", func(_ map[string]any, probs map[string]any) {
				probs["delta"] = 0.0
				probs["alpha"] = 0.7
			}, "m"},
			{"missing key", func(_ map[string]any, probs map[string]any) {
				delete(probs, "gamma")
			}, "m"},
			{"probability below zero", func(_ map[string]any, probs map[string]any) {
				probs["alpha"] = 0.9
				probs["beta"] = 0.2
				probs["gamma"] = -0.1
			}, "m"},
			{"probability above one", func(_ map[string]any, probs map[string]any) {
				probs["alpha"] = 1.2
				probs["beta"] = -0.1
				probs["gamma"] = -0.1
			}, "m"},
			{"sum not one", func(_ map[string]any, probs map[string]any) {
				probs["alpha"] = 0.7
				probs["beta"] = 0.2
				probs["gamma"] = 0.0
			}, "m"},
			{"wrong argmax", func(answers map[string]any, _ map[string]any) {
				answers["team"].(map[string]any)["choice"] = "beta"
			}, "m"},
			{"confidence diverged", func(answers map[string]any, _ map[string]any) {
				answers["team"].(map[string]any)["confidence"] = 0.9
			}, "m"},
			{"missing confidence", func(answers map[string]any, _ map[string]any) {
				delete(answers["team"].(map[string]any), "confidence")
			}, "m"},
			{"type mismatch", func(answers map[string]any, _ map[string]any) {
				answers["team"].(map[string]any)["type"] = "score"
			}, "m"},
			{"empty model", func(_ map[string]any, _ map[string]any) {}, ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				answers := map[string]any{
					"team": map[string]any{
						"type": "choice", "choice": "alpha", "confidence": 0.55,
						"probabilities": map[string]any{"alpha": 0.7, "beta": 0.2, "gamma": 0.1},
					},
					"refund": map[string]any{"type": "noul", "noul": 0.93},
					"urgency": map[string]any{"type": "score", "score": 1.0,
						"legend":        []any{"Routine", "Soon", "Urgent"},
						"probabilities": map[string]any{"0": 0.3, "1": 0.3, "2": 0.4},
						"confidence":    0.2},
				}
				probs := answers["team"].(map[string]any)["probabilities"].(map[string]any)
				tc.mutate(answers, probs)
				payload := map[string]any{
					"model": tc.model, "answers": answers,
					"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
				}
				raw, _ := json.Marshal(payload)
				setHandler(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(raw)
				})
				_, err := decide(baseReq())
				if domain.CodeOf(err) != domain.CodeInvalidOutput {
					t.Fatalf("expected invalid_output, got %v", err)
				}
			})
		}
	})

	t.Run("choice tie accepted", func(t *testing.T) {
		payload := `{"model":"m","answers":{` +
			`"team":{"type":"choice","choice":"beta","confidence":0.25,` +
			`"probabilities":{"alpha":0.5,"beta":0.5,"gamma":0.0}},` +
			`"refund":{"type":"noul","noul":0.5},` +
			`"urgency":{"type":"score","score":1.0,` +
			`"legend":["Routine","Soon","Urgent"],` +
			`"probabilities":{"0":0.3,"1":0.3,"2":0.4},"confidence":0.2}},` +
			`"usage":{"input_tokens":10,"output_tokens":5}}`
		setHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(payload))
		})
		resp, err := decide(baseReq())
		if err != nil {
			t.Fatalf("tied argmax must be accepted, got %v", err)
		}
		if resp.Answers["team"].Choice != "beta" {
			t.Fatalf("choice = %q", resp.Answers["team"].Choice)
		}
	})

	t.Run("choice confidence boundary", func(t *testing.T) {
		for _, tc := range []struct {
			sent float64
			ok   bool
		}{
			{0.56, true},
			{0.58, false},
		} {
			answers := map[string]any{
				"team": map[string]any{
					"type": "choice", "choice": "alpha", "confidence": tc.sent,
					"probabilities": map[string]any{"alpha": 0.7, "beta": 0.2, "gamma": 0.1},
				},
				"refund": map[string]any{"type": "noul", "noul": 0.93},
				"urgency": map[string]any{"type": "score", "score": 1.0,
					"legend":        []any{"Routine", "Soon", "Urgent"},
					"probabilities": map[string]any{"0": 0.3, "1": 0.3, "2": 0.4},
					"confidence":    0.2},
			}
			raw, _ := json.Marshal(map[string]any{
				"model": "m", "answers": answers,
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
			})
			setHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(raw)
			})
			_, err := decide(baseReq())
			if tc.ok && err != nil {
				t.Fatalf("confidence %v: expected success, got %v", tc.sent, err)
			}
			if !tc.ok && domain.CodeOf(err) != domain.CodeInvalidOutput {
				t.Fatalf("confidence %v: expected invalid_output, got %v", tc.sent, err)
			}
		}
	})

	t.Run("answer validation", func(t *testing.T) {
		cases := map[string]func(answers map[string]any){
			"noul out of range": func(answers map[string]any) {
				answers["refund"].(map[string]any)["noul"] = 1.5
			},
			"score without probabilities": func(answers map[string]any) {
				delete(answers["urgency"].(map[string]any), "probabilities")
			},
			"extra answer": func(answers map[string]any) {
				answers["extra"] = map[string]any{"type": "noul", "noul": 0.5}
			},
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				answers := map[string]any{
					"team": map[string]any{
						"type": "choice", "choice": "alpha", "confidence": 0.55,
						"probabilities": map[string]any{"alpha": 0.7, "beta": 0.2, "gamma": 0.1},
					},
					"refund": map[string]any{"type": "noul", "noul": 0.93},
					"urgency": map[string]any{"type": "score", "score": 1.0,
						"legend":        []any{"Routine", "Soon", "Urgent"},
						"probabilities": map[string]any{"0": 0.3, "1": 0.3, "2": 0.4},
						"confidence":    0.2},
				}
				mutate(answers)
				raw, _ := json.Marshal(map[string]any{
					"model": "m", "answers": answers,
					"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
				})
				setHandler(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(raw)
				})
				_, err := decide(baseReq())
				if domain.CodeOf(err) != domain.CodeInvalidOutput {
					t.Fatalf("%s: expected invalid_output, got %v", name, err)
				}
			})
		}
	})

	t.Run("redirect not followed", func(t *testing.T) {
		var secondHits atomic.Int64
		second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			secondHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(decisionOKBody("m", 1, 1)))
		}))
		t.Cleanup(second.Close)
		setHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", second.URL+"/systemone")
			w.WriteHeader(http.StatusFound)
		})
		_, err := decide(baseReq())
		if domain.CodeOf(err) != domain.CodeInvalidOutput {
			t.Fatalf("expected invalid_output for redirect, got %v", err)
		}
		var derr *domain.Error
		if !errors.As(err, &derr) {
			t.Fatalf("error is not *domain.Error: %v", err)
		}
		if derr.Retryable {
			t.Fatalf("redirect error must not be retryable")
		}
		if secondHits.Load() != 0 {
			t.Fatalf("provider followed the redirect")
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		setHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(decisionOKBody("m", 10, 1) + strings.Repeat(" ", 1<<20)))
		})
		_, err := decide(baseReq())
		if domain.CodeOf(err) != domain.CodeInvalidOutput {
			t.Fatalf("expected invalid_output for oversized body, got %v", err)
		}
	})

	t.Run("retry after", func(t *testing.T) {
		respond429 := func(header string) {
			setHandler(func(w http.ResponseWriter, _ *http.Request) {
				if header != "" {
					w.Header().Set("Retry-After", header)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_error"}}`))
			})
		}
		respond429("5")
		_, err := decide(baseReq())
		if domain.CodeOf(err) != domain.CodeRateLimit {
			t.Fatalf("expected rate_limit, got %v", err)
		}
		if d := domain.RetryAfterOf(err); d != 5*time.Second {
			t.Fatalf("retry after = %v, want 5s", d)
		}

		future := deps.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
		respond429(future)
		_, err = decide(baseReq())
		if domain.CodeOf(err) != domain.CodeRateLimit {
			t.Fatalf("expected rate_limit, got %v", err)
		}
		if d := domain.RetryAfterOf(err); d < 29*time.Second || d > 31*time.Second {
			t.Fatalf("retry after = %v, want ~30s", d)
		}

		respond429("")
		_, err = decide(baseReq())
		if domain.CodeOf(err) != domain.CodeRateLimit {
			t.Fatalf("expected rate_limit, got %v", err)
		}
		if d := domain.RetryAfterOf(err); d != 0 {
			t.Fatalf("retry after = %v, want 0", d)
		}
	})

	t.Run("error mapping", func(t *testing.T) {
		cases := []struct {
			status    int
			code      domain.ErrorCode
			retryable bool
		}{
			{401, domain.CodeAuth, false},
			{403, domain.CodeAuth, false},
			{402, domain.CodePaymentRequired, false},
			{400, domain.CodeInvalidRequest, false},
			{422, domain.CodeInvalidRequest, false},
			{413, domain.CodeInvalidRequest, false},
			{429, domain.CodeRateLimit, true},
			{500, domain.CodeUnavailable, true},
			{503, domain.CodeUnavailable, true},
			{529, domain.CodeUnavailable, true},
		}
		for _, tc := range cases {
			t.Run(http.StatusText(tc.status)+itoa(tc.status), func(t *testing.T) {
				setHandler(func(w http.ResponseWriter, _ *http.Request) {
					if tc.status == 429 {
						w.Header().Set("Retry-After", "5")
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"error":{"code":"vendor_code_for_` + itoa(tc.status) + `"}}`))
				})
				_, err := decide(baseReq())
				if err == nil {
					t.Fatalf("expected error for status %d", tc.status)
				}
				if domain.CodeOf(err) != tc.code {
					t.Fatalf("status %d: code = %q, want %q (%v)", tc.status, domain.CodeOf(err), tc.code, err)
				}
				var derr *domain.Error
				if !errors.As(err, &derr) {
					t.Fatalf("status %d: error is not *domain.Error", tc.status)
				}
				if derr.Retryable != tc.retryable {
					t.Fatalf("status %d: retryable = %v, want %v", tc.status, derr.Retryable, tc.retryable)
				}
				if strings.Contains(err.Error(), sentinel) {
					t.Fatalf("status %d: error leaks secret", tc.status)
				}
			})
		}
	})

	t.Run("timeout", func(t *testing.T) {
		setHandler(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(300 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(decisionOKBody("m", 1, 1)))
		})
		req := baseReq()
		req.Timeout = 50 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := p.Decide(ctx, req)
		if domain.CodeOf(err) != domain.CodeTimeout {
			t.Fatalf("expected timeout, got %v", err)
		}

		canceled, stop := context.WithCancel(context.Background())
		stop()
		_, err = p.Decide(canceled, baseReq())
		if domain.CodeOf(err) != domain.CodeTimeout {
			t.Fatalf("expected timeout for canceled context, got %v", err)
		}
	})

	t.Run("cost", func(t *testing.T) {
		setHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(decisionOKBody("drex-max-v1.1", 1000, 7)))
		})
		resp, err := decide(baseReq())
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		want := domain.MustParseDecimal("0.00006")
		if resp.Usage.CostUSD.Cmp(want) != 0 {
			t.Fatalf("cost = %s, want 0.00006", resp.Usage.CostUSD.String())
		}
	})

	t.Run("secret never leaks", func(t *testing.T) {
		setHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"boom"},"detail":` + quoteJSON(sentinel) + `}`))
		})
		_, err := decide(baseReq())
		if err == nil {
			t.Fatalf("expected error")
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Fatalf("error leaks secret: %v", err)
		}

		setHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`not json {` + sentinel + `}`))
		})
		_, err = decide(baseReq())
		if domain.CodeOf(err) != domain.CodeInvalidOutput {
			t.Fatalf("expected invalid_output, got %v", err)
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Fatalf("error leaks secret: %v", err)
		}
	})

	t.Run("request validation local", func(t *testing.T) {
		noul := domain.Question{ID: "q", Type: domain.QuestionNoul, Instructions: "?"}
		cases := map[string]domain.DecisionRequest{
			"model alias latest": {
				Model: "drex-max-latest", Questions: []domain.Question{noul},
			},
			"model alias latest upper": {
				Model: "DREX-MAX-LATEST", Questions: []domain.Question{noul},
			},
			"model alias preview": {
				Model: "jev-preview", Questions: []domain.Question{noul},
			},
			"model alias preview mixed": {
				Model: "Drex-Max-Preview", Questions: []domain.Question{noul},
			},
			"empty question id": {
				Model: "m",
				Questions: []domain.Question{
					{ID: "", Type: domain.QuestionNoul, Instructions: "?"},
				},
			},
			"whitespace question id": {
				Model: "m",
				Questions: []domain.Question{
					{ID: "   ", Type: domain.QuestionNoul, Instructions: "?"},
				},
			},
			"duplicate question ids": {
				Model:     "m",
				Questions: []domain.Question{noul, noul},
			},
			"empty option key": {
				Model: "m",
				Questions: []domain.Question{{
					ID: "c", Type: domain.QuestionChoice, Instructions: "pick",
					Options: []domain.Option{{Key: "", Description: "x"}, {Key: "b", Description: "y"}},
				}},
			},
			"whitespace option key": {
				Model: "m",
				Questions: []domain.Question{{
					ID: "c", Type: domain.QuestionChoice, Instructions: "pick",
					Options: []domain.Option{{Key: "  ", Description: "x"}, {Key: "b", Description: "y"}},
				}},
			},
			"duplicate option keys": {
				Model: "m",
				Questions: []domain.Question{{
					ID: "c", Type: domain.QuestionChoice, Instructions: "pick",
					Options: []domain.Option{{Key: "a", Description: "x"}, {Key: "a", Description: "y"}},
				}},
			},
			"score without options": {
				Model: "m",
				Questions: []domain.Question{
					{ID: "s", Type: domain.QuestionScore, Instructions: "rate"},
				},
			},
		}
		for name, req := range cases {
			t.Run(name, func(t *testing.T) {
				before := hits.Load()
				_, err := decide(req)
				if domain.CodeOf(err) != domain.CodeInvalidRequest {
					t.Fatalf("expected invalid_request, got %v", err)
				}
				if hits.Load() != before {
					t.Fatalf("validation must happen before any HTTP for %s", name)
				}
			})
		}
	})

	t.Run("score validation", func(t *testing.T) {
		validAnswers := func() map[string]any {
			return map[string]any{
				"team": map[string]any{
					"type": "choice", "choice": "alpha", "confidence": 0.55,
					"probabilities": map[string]any{"alpha": 0.7, "beta": 0.2, "gamma": 0.1},
				},
				"refund": map[string]any{"type": "noul", "noul": 0.93},
				"urgency": map[string]any{"type": "score", "score": 1.0,
					"legend":        []any{"Routine", "Soon", "Urgent"},
					"probabilities": map[string]any{"0": 0.3, "1": 0.3, "2": 0.4},
					"confidence":    0.2},
			}
		}
		cases := map[string]func(u map[string]any){
			"empty legend": func(u map[string]any) {
				u["legend"] = []any{}
			},
			"legend short": func(u map[string]any) {
				u["legend"] = []any{"Routine", "Soon"}
			},
			"legend long": func(u map[string]any) {
				u["legend"] = []any{"Routine", "Soon", "Urgent", "Critical"}
			},
			"probabilities empty": func(u map[string]any) {
				u["probabilities"] = map[string]any{}
			},
			"probability count mismatch": func(u map[string]any) {
				u["probabilities"] = map[string]any{"0": 0.5, "1": 0.5}
			},
			"extra key": func(u map[string]any) {
				u["probabilities"].(map[string]any)["3"] = 0.0
			},
			"missing index": func(u map[string]any) {
				delete(u["probabilities"].(map[string]any), "2")
			},
			"wrong key": func(u map[string]any) {
				probs := u["probabilities"].(map[string]any)
				delete(probs, "2")
				probs["x"] = 0.4
			},
			"probability below zero": func(u map[string]any) {
				u["probabilities"].(map[string]any)["0"] = -0.1
			},
			"probability above one": func(u map[string]any) {
				u["probabilities"].(map[string]any)["0"] = 1.2
			},
			"sum not one": func(u map[string]any) {
				u["probabilities"] = map[string]any{"0": 0.3, "1": 0.3, "2": 0.2}
			},
			"score missing": func(u map[string]any) {
				delete(u, "score")
			},
			"score string": func(u map[string]any) {
				u["score"] = "high"
			},
			"confidence string": func(u map[string]any) {
				u["confidence"] = "high"
			},
			"confidence below zero": func(u map[string]any) {
				u["confidence"] = -0.1
			},
			"confidence above one": func(u map[string]any) {
				u["confidence"] = 1.1
			},
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				answers := validAnswers()
				mutate(answers["urgency"].(map[string]any))
				raw, _ := json.Marshal(map[string]any{
					"model": "m", "answers": answers,
					"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
				})
				setHandler(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(raw)
				})
				_, err := decide(baseReq())
				if domain.CodeOf(err) != domain.CodeInvalidOutput {
					t.Fatalf("%s: expected invalid_output, got %v", name, err)
				}
			})
		}
	})

	t.Run("score confidence passthrough", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			body string
		}{
			{"arbitrary confidence kept", `{"model":"m","answers":{
				"team":{"type":"choice","choice":"alpha","confidence":0.55,
				"probabilities":{"alpha":0.7,"beta":0.2,"gamma":0.1}},
				"refund":{"type":"noul","noul":0.93},
				"urgency":{"type":"score","score":1.0,
				"legend":["Routine","Soon","Urgent"],
				"probabilities":{"0":0.3,"1":0.3,"2":0.4},"confidence":0.99}},
				"usage":{"input_tokens":10,"output_tokens":5}}`},
			{"missing confidence accepted", `{"model":"m","answers":{
				"team":{"type":"choice","choice":"alpha","confidence":0.55,
				"probabilities":{"alpha":0.7,"beta":0.2,"gamma":0.1}},
				"refund":{"type":"noul","noul":0.93},
				"urgency":{"type":"score","score":1.0,
				"legend":["Routine","Soon","Urgent"],
				"probabilities":{"0":0.3,"1":0.3,"2":0.4}}},
				"usage":{"input_tokens":10,"output_tokens":5}}`},
			{"confidence zero accepted", `{"model":"m","answers":{
				"team":{"type":"choice","choice":"alpha","confidence":0.55,
				"probabilities":{"alpha":0.7,"beta":0.2,"gamma":0.1}},
				"refund":{"type":"noul","noul":0.93},
				"urgency":{"type":"score","score":1.0,
				"legend":["Routine","Soon","Urgent"],
				"probabilities":{"0":0.3,"1":0.3,"2":0.4},"confidence":0.0}},
				"usage":{"input_tokens":10,"output_tokens":5}}`},
			{"confidence one accepted", `{"model":"m","answers":{
				"team":{"type":"choice","choice":"alpha","confidence":0.55,
				"probabilities":{"alpha":0.7,"beta":0.2,"gamma":0.1}},
				"refund":{"type":"noul","noul":0.93},
				"urgency":{"type":"score","score":1.0,
				"legend":["Routine","Soon","Urgent"],
				"probabilities":{"0":0.3,"1":0.3,"2":0.4},"confidence":1.0}},
				"usage":{"input_tokens":10,"output_tokens":5}}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				setHandler(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.body))
				})
				resp, err := decide(baseReq())
				if err != nil {
					t.Fatalf("Decide: %v", err)
				}
				if tc.name == "arbitrary confidence kept" {
					got := resp.Answers["urgency"].Confidence
					if got == nil || *got != 0.99 {
						t.Fatalf("confidence = %v, want 0.99 as sent", got)
					}
				}
			})
		}
	})

	t.Run("negative usage", func(t *testing.T) {
		bodies := map[string]string{
			"negative input tokens": `{"model":"m","answers":{
				"team":{"type":"choice","choice":"alpha","confidence":0.55,
				"probabilities":{"alpha":0.7,"beta":0.2,"gamma":0.1}},
				"refund":{"type":"noul","noul":0.93},
				"urgency":{"type":"score","score":1.0,
				"legend":["Routine","Soon","Urgent"],
				"probabilities":{"0":0.3,"1":0.3,"2":0.4},"confidence":0.2}},
				"usage":{"input_tokens":-1,"output_tokens":5}}`,
			"negative output tokens": `{"model":"m","answers":{
				"team":{"type":"choice","choice":"alpha","confidence":0.55,
				"probabilities":{"alpha":0.7,"beta":0.2,"gamma":0.1}},
				"refund":{"type":"noul","noul":0.93},
				"urgency":{"type":"score","score":1.0,
				"legend":["Routine","Soon","Urgent"],
				"probabilities":{"0":0.3,"1":0.3,"2":0.4},"confidence":0.2}},
				"usage":{"input_tokens":10,"output_tokens":-5}}`,
		}
		for name, body := range bodies {
			t.Run(name, func(t *testing.T) {
				setHandler(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(body))
				})
				_, err := decide(baseReq())
				if domain.CodeOf(err) != domain.CodeInvalidOutput {
					t.Fatalf("%s: expected invalid_output, got %v", name, err)
				}
			})
		}
	})

	t.Run("latency split", func(t *testing.T) {
		setHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(decisionOKBody("drex-max-v1.1", 1000, 104)))
		})
		resp, err := decide(baseReq())
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if resp.Usage.VendorLatencyMS != 104 {
			t.Fatalf("vendor latency = %d, want 104", resp.Usage.VendorLatencyMS)
		}
		if resp.Usage.LatencyMS < 0 {
			t.Fatalf("measured latency = %d, want >= 0", resp.Usage.LatencyMS)
		}
		if resp.Raw == nil || !strings.Contains(string(resp.Raw), "req-test-001") {
			t.Fatalf("raw response is not preserved")
		}

		fallback := `{"model":"m","answers":{
			"team":{"type":"choice","choice":"alpha","confidence":0.55,
			"probabilities":{"alpha":0.7,"beta":0.2,"gamma":0.1}},
			"refund":{"type":"noul","noul":0.93},
			"urgency":{"type":"score","score":1.0,
			"legend":["Routine","Soon","Urgent"],
			"probabilities":{"0":0.3,"1":0.3,"2":0.4},"confidence":0.2}},
			"usage":{"input_tokens":10,"output_tokens":5},
			"request_id":"req-fallback","latency_ms":77}`
		setHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fallback))
		})
		resp, err = decide(baseReq())
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if resp.Usage.VendorLatencyMS != 77 {
			t.Fatalf("vendor latency = %d, want 77 from latency_ms", resp.Usage.VendorLatencyMS)
		}
		if resp.Usage.LatencyMS < 0 {
			t.Fatalf("measured latency = %d, want >= 0", resp.Usage.LatencyMS)
		}

		absent := `{"model":"m","answers":{
			"team":{"type":"choice","choice":"alpha","confidence":0.55,
			"probabilities":{"alpha":0.7,"beta":0.2,"gamma":0.1}},
			"refund":{"type":"noul","noul":0.93},
			"urgency":{"type":"score","score":1.0,
			"legend":["Routine","Soon","Urgent"],
			"probabilities":{"0":0.3,"1":0.3,"2":0.4},"confidence":0.2}},
			"usage":{"input_tokens":10,"output_tokens":5},
			"request_id":"req-absent"}`
		setHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(absent))
		})
		resp, err = decide(baseReq())
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if resp.Usage.VendorLatencyMS != 0 {
			t.Fatalf("vendor latency = %d, want 0 when absent", resp.Usage.VendorLatencyMS)
		}
		if resp.Usage.LatencyMS < 0 {
			t.Fatalf("measured latency = %d, want >= 0", resp.Usage.LatencyMS)
		}
	})
}

// DecisionConstructor builds the provider under test for base URL policy
// checks, reporting construction errors instead of failing the test.
type DecisionConstructor func(opts DecisionOptions, deps ports.Deps) (ports.Decision, error)

// DecisionBaseURLConformance checks the New() base URL policy shared by
// decision adapters: absolute http(s) URLs without userinfo, query or
// fragment; https is always allowed, http only for the 127.0.0.1 and ::1
// loopback hosts.
func DecisionBaseURLConformance(t *testing.T, build DecisionConstructor) {
	t.Helper()

	deps := ports.Deps{Secrets: mapSecretStore{DecisionSecretRef: "s"}}

	allowed := []string{
		"https://example.com",
		"https://example.com/base/",
		"http://127.0.0.1:11434/v1",
		"http://127.0.0.1",
		"http://[::1]:8080",
		"http://[::1]",
	}
	for _, base := range allowed {
		t.Run("allowed "+base, func(t *testing.T) {
			if _, err := build(DecisionOptions{BaseURL: base}, deps); err != nil {
				t.Fatalf("New(%q): expected success, got %v", base, err)
			}
		})
	}

	rejected := []string{
		"http://localhost:11434/v1",
		"http://localhost",
		"http://0.0.0.0:8080",
		"http://0.0.0.0",
		"http://example.com",
		"http://127.0.0.1.evil.com",
		"http://[::2]",
		"http://[::ffff:127.0.0.1]",
		"https://example.com?x=1",
		"https://example.com#frag",
		"https://user@example.com",
		"https://user:pass@example.com",
		"ftp://example.com/v1",
		"ws://example.com",
		"example.com/v1",
		"/relative/path",
		"?x=1",
	}
	for _, base := range rejected {
		t.Run("rejected "+base, func(t *testing.T) {
			_, err := build(DecisionOptions{BaseURL: base}, deps)
			if err == nil {
				t.Fatalf("New(%q): expected invalid_request, got nil", base)
			}
			if domain.CodeOf(err) != domain.CodeInvalidRequest {
				t.Fatalf("New(%q): expected invalid_request, got %v", base, err)
			}
		})
	}
}

func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return []byte(sb.String())
}
