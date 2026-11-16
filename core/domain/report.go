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
	AvgNetReturn Decimal `json:"avgNetReturn,omitempty"`
}

// GroupStats aggregates labeled outcomes for one comparison group.
type GroupStats struct {
	Count        int     `json:"count"`
	SumNetReturn Decimal `json:"sumNetReturn"`
	AvgNetReturn Decimal `json:"avgNetReturn"`
}

// DifferenceStats is the bootstrap comparison of two groups.
type DifferenceStats struct {
	Mean          Decimal `json:"mean"`
	Low           Decimal `json:"low"`
	High          Decimal `json:"high"`
	ContainsZero  bool    `json:"containsZero"`
	BootstrapReps int     `json:"bootstrapReps"`
	Days          int     `json:"days"`
}

// ReportComparisons holds shadow-mode comparison statistics.
type ReportComparisons struct {
	AllEntriesReturn  Decimal               `json:"allEntriesReturn"`
	AllowReturn       Decimal               `json:"allowReturn"`
	RejectedAvgReturn Decimal               `json:"rejectedAvgReturn"`
	Brier             float64               `json:"brier"`
	ECE               float64               `json:"ece"`
	Reliability       []ReliabilityBucket   `json:"reliability"`
	ByModel           []ModelBreakdown      `json:"byModel"`
	Groups            map[string]GroupStats `json:"groups,omitempty"`
	Difference        *DifferenceStats      `json:"difference,omitempty"`
	BaseRate          float64               `json:"baseRate"`
	ConstantBrier     float64               `json:"constantBrier"`
	BrierSkillScore   float64               `json:"brierSkillScore"`
	AUC               float64               `json:"auc"`
	CalibrationCount  int                   `json:"calibrationCount"`
}

// ReportAssumptions records the labeling assumptions behind a report.
type ReportAssumptions struct {
	HorizonBars     int    `json:"horizonBars"`
	FeeBpsRoundTrip int    `json:"feeBpsRoundTrip"`
	PriceSource     string `json:"priceSource"`
	EqualSize       bool   `json:"equalSize"`
	NoSlippage      bool   `json:"noSlippage"`
	NoFunding       bool   `json:"noFunding"`
}

// ArenaModelStats aggregates arena verdicts for one model.
type ArenaModelStats struct {
	Model          string  `json:"model"`
	Verdicts       int     `json:"verdicts"`
	AvgAllowReturn Decimal `json:"avgAllowReturn"`
	AUC            float64 `json:"auc"`
	CostUSD        Decimal `json:"costUsd"`
}

// ArenaReport summarizes a cross-model arena comparison.
type ArenaReport struct {
	Decisions      int               `json:"decisions"`
	AgreementShare float64           `json:"agreementShare"`
	Models         []ArenaModelStats `json:"models"`
}

// Report is a summary of labeled outcomes and gate performance.
type Report struct {
	GeneratedAt          time.Time          `json:"generatedAt"`
	LabeledCount         int                `json:"labeledCount"`
	Sufficient           bool               `json:"sufficient"`
	VerdictCounts        map[string]int     `json:"verdictCounts"`
	DecisionCostUSD      Decimal            `json:"decisionCostUsd"`
	LatencyP50MS         int64              `json:"latencyP50Ms"`
	LatencyP95MS         int64              `json:"latencyP95Ms"`
	Comparisons          *ReportComparisons `json:"comparisons,omitempty"`
	PolicyHash           string             `json:"policyHash,omitempty"`
	OtherPolicyDecisions int                `json:"otherPolicyDecisions,omitempty"`
	BackfillLabeled      int                `json:"backfillLabeled,omitempty"`
	BackfillExcluded     int                `json:"backfillExcluded,omitempty"`
	Assumptions          *ReportAssumptions `json:"assumptions,omitempty"`
	Arena                *ArenaReport       `json:"arena,omitempty"`
}
