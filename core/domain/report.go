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
	Model string  `json:"model"`
	Count int     `json:"count"`
	Brier float64 `json:"brier"`
	ECE   float64 `json:"ece"`
	// CalibrationCount is the number of labeled outcomes of this model with
	// a recorded pass probability. Brier and ECE are only meaningful when it
	// reaches the report calibration threshold.
	CalibrationCount int     `json:"calibrationCount"`
	NetReturnSum     Decimal `json:"netReturnSum"`
	CostUSD          Decimal `json:"costUsd"`
	AvgNetReturn     Decimal `json:"avgNetReturn,omitempty"`
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
	// CalibrationSufficient reports whether CalibrationCount reached the
	// minimum that makes Brier/ECE/AUC/BSS/reliability trustworthy. The
	// numeric fields stay populated for machine consumers; human and share
	// output print n/a while this is false.
	CalibrationSufficient bool `json:"calibrationSufficient"`
}

// ReportAssumptions records the labeling assumptions behind a report.
type ReportAssumptions struct {
	HorizonBars     int    `json:"horizonBars"`
	FeeBpsRoundTrip int    `json:"feeBpsRoundTrip"`
	PriceSource     string `json:"priceSource"`
	EqualSize       bool   `json:"equalSize"`
	NoSlippage      bool   `json:"noSlippage"`
	NoFunding       bool   `json:"noFunding"`
	// EntryLagMedian is the median entry lag as a fraction of one bar.
	EntryLagMedian float64 `json:"entryLagMedian,omitempty"`
	// EntryLagP95 is the 95th percentile entry lag as a fraction of one bar.
	EntryLagP95 float64 `json:"entryLagP95,omitempty"`
	// EntryLagMedianMS is the median entry lag in milliseconds, over the
	// outcomes that recorded entryLagMs.
	EntryLagMedianMS int64 `json:"entryLagMedianMs,omitempty"`
	// EntryLagP95MS is the 95th percentile entry lag in milliseconds.
	EntryLagP95MS int64 `json:"entryLagP95Ms,omitempty"`
	// EntryShifted counts labeled outcomes whose entry bar was shifted.
	EntryShifted int `json:"entryShifted,omitempty"`
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

// ReportG1 is the honest readiness block of the first gate: how many
// decisions were labeled, how many UTC days the gate actually answered, and
// how long the longest pause between two answers was. It is derived from the
// journal only; a status snapshot or a configuration claim cannot turn it
// green.
//
// Negative LastLabelAgeHours means the journal holds no labeled outcome yet.
// Negative LabelsDue means the journal does not reveal the labeling window
// (no horizon and/or no timeframe), so the count is unknown rather than zero.
type ReportG1 struct {
	Labels       int `json:"labels"`
	TargetLabels int `json:"targetLabels"`
	ActiveDays   int `json:"activeDays"`
	TargetDays   int `json:"targetDays"`
	// MaxGapHours is the longest pause between two consecutive gate verdicts
	// of the journal. It is measured between answer timestamps, not between
	// UTC dates, so seven calendar dates with a three-day hole cannot pass.
	MaxGapHours float64 `json:"maxGapHours"`
	Eligible    bool    `json:"eligible"`
	// Synthetic is true when the journal contains any event written by
	// `vend demo`. Such a journal is never G1 evidence.
	Synthetic                  bool    `json:"synthetic"`
	LastLabelAgeHours          float64 `json:"lastLabelAgeHours"`
	ExpectedLabelIntervalHours float64 `json:"expectedLabelIntervalHours"`
	LabelsDue                  int     `json:"labelsDue"`
}

// Report is a summary of labeled outcomes and gate performance.
type Report struct {
	GeneratedAt  time.Time `json:"generatedAt"`
	LabeledCount int       `json:"labeledCount"`
	// AwaitingLabeling is the number of entry signals with neither a labeled
	// outcome nor an outcome.unlabelable record yet.
	AwaitingLabeling int `json:"awaitingLabeling,omitempty"`
	// NoOutcomeByDeadline is the number of entry signals recorded as
	// unlabelable when their labeling deadline passed.
	NoOutcomeByDeadline int `json:"noOutcomeByDeadline,omitempty"`
	// SourceRejected is the number of entry signals recorded as unlabelable
	// because the candle source rejected the request as invalid.
	SourceRejected       int                `json:"sourceRejected,omitempty"`
	Sufficient           bool               `json:"sufficient"`
	VerdictCounts        map[string]int     `json:"verdictCounts"`
	DecisionCostUSD      Decimal            `json:"decisionCostUsd"`
	LatencyP50MS         int64              `json:"latencyP50Ms"`
	LatencyP95MS         int64              `json:"latencyP95Ms"`
	Comparisons          *ReportComparisons `json:"comparisons,omitempty"`
	PolicyHash           string             `json:"policyHash,omitempty"`
	OtherPolicyDecisions int                `json:"otherPolicyDecisions,omitempty"`
	// OtherLabelParams counts labeled outcomes produced with a different
	// horizon/fee pair than the report assumptions.
	OtherLabelParams int `json:"otherLabelParams,omitempty"`
	BackfillLabeled  int `json:"backfillLabeled,omitempty"`
	BackfillExcluded int `json:"backfillExcluded,omitempty"`
	// Days is the inclusive UTC-day span of accepted gate requests, the same
	// value the human report prints on its window line.
	Days int `json:"days,omitempty"`
	// ActiveDays is the number of distinct UTC dates with at least one
	// accepted gate request, the same value the human report prints.
	ActiveDays  int                `json:"activeDays,omitempty"`
	Assumptions *ReportAssumptions `json:"assumptions,omitempty"`
	Arena       *ArenaReport       `json:"arena,omitempty"`
	// G1 is the honest G1 progress block. It is always populated by the
	// report builder.
	G1 *ReportG1 `json:"g1,omitempty"`
	// Synthetic marks a report built from a journal that contains demo
	// events. The underlying numbers stay visible, but G1 is not credited.
	Synthetic bool `json:"synthetic,omitempty"`
}
