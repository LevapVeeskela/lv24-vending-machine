package domain

import "time"

// ReliabilityBucket is one calibration bin of the reliability diagram.
type ReliabilityBucket struct {
	LowerBound    float64 `json:"lowerBound"`
	Count         int     `json:"count"`
	MeanPredicted float64 `json:"meanPredicted"`
	ObservedRate  float64 `json:"observedRate"`
}

// ModelBreakdown aggregates labeled outcomes for one decision model.
type ModelBreakdown struct {
	Model        string  `json:"model"`
	Count        int     `json:"count"`
	Brier        float64 `json:"brier"`
	ECE          float64 `json:"ece"`
	NetReturnSum Decimal `json:"netReturnSum"`
	CostUSD      Decimal `json:"costUsd"`
}

// ReportComparisons holds shadow-mode comparison statistics.
type ReportComparisons struct {
	AllEntriesReturn  Decimal             `json:"allEntriesReturn"`
	AllowReturn       Decimal             `json:"allowReturn"`
	RejectedAvgReturn Decimal             `json:"rejectedAvgReturn"`
	Brier             float64             `json:"brier"`
	ECE               float64             `json:"ece"`
	Reliability       []ReliabilityBucket `json:"reliability"`
	ByModel           []ModelBreakdown    `json:"byModel"`
}

// Report is a summary of labeled outcomes and gate performance.
type Report struct {
	GeneratedAt     time.Time          `json:"generatedAt"`
	LabeledCount    int                `json:"labeledCount"`
	Sufficient      bool               `json:"sufficient"`
	VerdictCounts   map[string]int     `json:"verdictCounts"`
	DecisionCostUSD Decimal            `json:"decisionCostUsd"`
	LatencyP50MS    int64              `json:"latencyP50Ms"`
	LatencyP95MS    int64              `json:"latencyP95Ms"`
	Comparisons     *ReportComparisons `json:"comparisons,omitempty"`
}
