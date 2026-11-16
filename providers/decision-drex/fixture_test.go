package decisiondrex

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// fixtureQuestions mirrors testdata/request.json.
func fixtureQuestions() []domain.Question {
	return []domain.Question{
		{
			ID:           "team",
			Type:         domain.QuestionChoice,
			Instructions: "Which team should handle this ticket?",
			Options: []domain.Option{
				{Key: "billing", Description: "Payments, charges, and refunds"},
				{Key: "technical", Description: "Bugs and outages"},
				{Key: "other", Description: "Anything else"},
			},
		},
		{
			ID:           "refund",
			Type:         domain.QuestionNoul,
			Instructions: "Does the customer explicitly ask for a refund?",
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

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return raw
}

func fixtureRequest(t *testing.T, model string) domain.DecisionRequest {
	t.Helper()
	return domain.DecisionRequest{
		Model: model,
		State: map[string]any{
			"ticket": "I was charged twice for the same order. Please refund the extra payment.",
		},
		Questions: fixtureQuestions(),
	}
}

func TestVendorFixtureConfidence(t *testing.T) {
	raw := loadFixture(t, "response.json")
	price, err := domain.ParseDecimal("60")
	if err != nil {
		t.Fatalf("parse price: %v", err)
	}
	// The local vendor fixture carries no model, so the full envelope
	// validation must reject it; the answers still go through the same
	// internal validation used by parseResponse.
	if _, err := parseResponse(raw, fixtureRequest(t, "drex-v1.5"), price); domain.CodeOf(err) != domain.CodeInvalidOutput {
		t.Fatalf("model-less fixture: expected invalid_output, got %v", err)
	}
	var wr wireResponse
	if err := json.Unmarshal(raw, &wr); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	questions := fixtureRequest(t, "drex-v1.5").Questions
	teamQ := questions[0]
	teamA, err := convertAnswer(teamQ, wr.Answers["team"])
	if err != nil {
		t.Fatalf("convertAnswer: %v", err)
	}
	if teamA.Choice != "billing" {
		t.Fatalf("choice = %q", teamA.Choice)
	}
	if got := recomputeConfidence(3, 0.9926); math.Abs(got-0.9889) > 0.0001 {
		t.Fatalf("recomputeConfidence(3, 0.9926) = %v, want 0.9889 ± 0.0001", got)
	}
	if teamA.Confidence == nil || math.Abs(*teamA.Confidence-0.9889) > 0.02 {
		t.Fatalf("stored confidence does not match recomputed value")
	}
	if wr.Usage.InputTokens != 87 || wr.Usage.OutputTokens != 166 {
		t.Fatalf("usage = %+v", wr.Usage)
	}
	latency := int64(math.Round(ptrValue(wr.LatencyMS)))
	if latency != 861 {
		t.Fatalf("latency = %d, want 861", latency)
	}
}

func ptrValue(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func TestVendorFixtureRequestShape(t *testing.T) {
	raw := loadFixture(t, "request.json")
	body := string(raw)
	if strings.Contains(body, `"options"`) {
		t.Fatalf("fixture request must not contain options")
	}
	order := []string{`"billing"`, `"technical"`, `"other"`}
	for i := 1; i < len(order); i++ {
		if strings.Index(body, order[i-1]) > strings.Index(body, order[i]) {
			t.Fatalf("criteria key order broken in fixture: %s", body)
		}
	}
	for _, key := range []string{`"Routine"`, `"Soon"`, `"Urgent"`} {
		if !strings.Contains(body, key) {
			t.Fatalf("fixture request misses %s", key)
		}
	}

	var fixture struct {
		State     map[string]any            `json:"state"`
		Questions map[string]map[string]any `json:"questions"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	built, err := buildRequestBody(fixtureRequest(t, "drex-v1.5"))
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	var got struct {
		State     map[string]any            `json:"state"`
		Questions map[string]map[string]any `json:"questions"`
	}
	if err := json.Unmarshal(built, &got); err != nil {
		t.Fatalf("unmarshal built body: %v", err)
	}
	if !reflect.DeepEqual(got.State, fixture.State) {
		t.Fatalf("state mismatch:\nbuilt: %v\nfixture: %v", got.State, fixture.State)
	}
	if !reflect.DeepEqual(got.Questions, fixture.Questions) {
		t.Fatalf("questions mismatch:\nbuilt: %s\nfixture: %s", got.Questions, fixture.Questions)
	}
}

func TestCloudFixtureEnvelope(t *testing.T) {
	raw := loadFixture(t, "response_cloud.json")
	price, err := domain.ParseDecimal("60")
	if err != nil {
		t.Fatalf("parse price: %v", err)
	}
	resp, err := parseResponse(raw, fixtureRequest(t, "drex-max-v1.1"), price)
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Model != "drex-max-v1.1" {
		t.Fatalf("model = %q", resp.Model)
	}
	if resp.RequestID != "req_9f2c7a1b4e" {
		t.Fatalf("request id = %q", resp.RequestID)
	}
	if resp.Usage.LatencyMS != 104 {
		t.Fatalf("latency = %d", resp.Usage.LatencyMS)
	}
	want := domain.MustParseDecimal("0.0000852")
	if resp.Usage.CostUSD.Cmp(want) != 0 {
		t.Fatalf("cost = %s, want 0.0000852", resp.Usage.CostUSD.String())
	}
}
