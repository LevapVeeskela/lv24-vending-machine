// Package report builds shadow-mode evaluation reports from the journal.
//
// The builder walks the journal once and derives verdict counts, decision
// costs and latency percentiles from every event, plus comparison
// statistics (group returns, bootstrap difference, calibration, per-model
// breakdown and the optional cross-model arena) once enough outcomes of the
// selected policy are labeled. It performs no I/O besides the journal walk
// and never touches the network.
package report

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"sort"
	"strconv"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

const (
	// DefaultMinLabeled is the labeled-outcome threshold applied when
	// Config.MinLabeled is not positive.
	DefaultMinLabeled = 100
	// DefaultBootstrapReps is the bootstrap draw count used when
	// Config.BootstrapReps is not positive.
	DefaultBootstrapReps = 2000
	// DefaultSeed is the deterministic bootstrap seed used when
	// Config.Seed is zero.
	DefaultSeed = 42
	// MinBootstrapDays is the smallest number of distinct UTC entry days
	// that yields a trustworthy 95% bootstrap interval.
	MinBootstrapDays = 5
	// MinCalibrationPairs is the smallest number of (predicted probability,
	// observed fact) pairs that makes Brier/ECE/AUC/BSS/reliability
	// trustworthy. Below it the report keeps the computed numbers but marks
	// them insufficient and prints n/a.
	MinCalibrationPairs = 30
	// MinGroupSize is the smallest number of observations in each comparison
	// group required for the bootstrap interval. One ALLOW among many REJECT
	// would otherwise produce a zero-width "significant" interval.
	MinGroupSize = 30
	// avgScale is the decimal scale of every averaged money value.
	avgScale = 12
	// dayLayout is the UTC key used to resample bootstrap days.
	dayLayout = "2006-01-02"
	// G1TargetLabels is the labeled-decision target of the first gate.
	G1TargetLabels = 100
	// G1TargetDays is the number of UTC days with gate answers the first
	// gate requires.
	G1TargetDays = 7
	// G1MaxGapHours is the longest pause between two gate verdicts that
	// still counts as continuous work. The gap is measured between
	// consecutive verdict timestamps, not between UTC dates: seven calendar
	// dates with a three-day hole would otherwise pass a date-only check.
	// 36 hours tolerates one missed daily cycle. ABSTAIN counts as an
	// answer: a gate that said "not enough data" was running, which is
	// continuity evidence even without a trade.
	G1MaxGapHours = 36.0
	// demoRunID marks the events written by `vend demo`. A journal holding
	// even one such event is synthetic and never G1 evidence.
	demoRunID = "demo"
)

// riskQuestion and passKey locate the calibration probability inside a
// decision response: answers["risk_check"].probabilities["pass"].
const (
	riskQuestion = "risk_check"
	passKey      = "pass"
)

// Config tunes the report builder.
type Config struct {
	// MinLabeled is the labeled-outcome threshold that unlocks
	// comparisons. Values <= 0 mean 100.
	MinLabeled int
	// PolicyHash restricts the labeled outcomes to one gate policy. An
	// empty value selects the policy of the highest-seq verdict.
	PolicyHash string
	// IncludeBackfill counts backfilled outcomes in the comparison
	// instead of only reporting them.
	IncludeBackfill bool
	// BootstrapReps is the number of bootstrap draws. Values <= 0 mean
	// 2000.
	BootstrapReps int
	// Seed is the deterministic bootstrap seed. Zero means 42.
	Seed int64
	// HorizonBars is the configured outcome horizon in bars. Values <= 0
	// make the builder derive it from the latest labeled outcome.
	HorizonBars int
	// Timeframe is the configured signal bar size. Zero makes the builder
	// derive it from the latest labeled outcome.
	Timeframe time.Duration
}

// Builder computes domain.Report values from a journal.
type Builder struct {
	jr              ports.Journal
	deps            ports.Deps
	min             int
	policyHash      string
	includeBackfill bool
	reps            int
	seed            int64
	days            int
	horizonBars     int
	timeframe       time.Duration
}

// New builds a Builder over jr. deps supplies the clock for GeneratedAt.
func New(jr ports.Journal, deps ports.Deps, cfg Config) (*Builder, error) {
	if jr == nil {
		return nil, fmt.Errorf("report: nil journal")
	}
	min := cfg.MinLabeled
	if min <= 0 {
		min = DefaultMinLabeled
	}
	reps := cfg.BootstrapReps
	if reps <= 0 {
		reps = DefaultBootstrapReps
	}
	seed := cfg.Seed
	if seed == 0 {
		seed = DefaultSeed
	}
	return &Builder{
		jr:              jr,
		deps:            deps,
		min:             min,
		policyHash:      cfg.PolicyHash,
		includeBackfill: cfg.IncludeBackfill,
		reps:            reps,
		seed:            seed,
		horizonBars:     cfg.HorizonBars,
		timeframe:       cfg.Timeframe,
	}, nil
}

// decisionInfo is the extracted per-decision data used by the report.
type decisionInfo struct {
	model   string
	pPass   float64
	hasP    bool
	cost    domain.Decimal
	latency int64
}

// verdictInfo is the extracted per-verdict data used by the report.
type verdictInfo struct {
	verdict    string
	policyHash string
	seq        int64
	// ts is when the gate answered: the event receipt time, falling back to
	// the payload decidedAt when an old journal has no envelope time. It is
	// the input of the G1 activity and continuity metrics.
	ts time.Time
	// decidedAt is the gate decision time from the payload. The labeler
	// anchors the labeling window on it, so labels-due mirrors that choice.
	decidedAt time.Time
}

// entrySignal is one entry gate.request: when the gate accepted it, its bar
// size and the decision it produced. It feeds the labels-due count.
type entrySignal struct {
	receivedAt time.Time
	timeframe  string
	decisionID string
}

// labeledOutcome pairs an outcome with its gate verdict and decision ID.
// seq is the journal sequence of the surviving outcome.labeled event.
type labeledOutcome struct {
	outcome     domain.Outcome
	verdict     string
	decisionID  string
	seq         int64
	lagMS       int64
	lagRecorded bool
}

// outcomeEvent is one deduplicated outcome.labeled event with its seq. The
// optional entryLagMs field is decoded separately so an outcome written
// before the field existed is not mistaken for a zero lag.
type outcomeEvent struct {
	outcome     domain.Outcome
	lagMS       int64
	lagRecorded bool
	seq         int64
}

// labelPair is the (horizon, fee) pair of the labeling parameters.
type labelPair struct {
	horizon int
	feeBps  int
	ok      bool
}

// calPoint is one calibration pair: predicted pass probability and the
// binary fact (1 when NetReturn > 0, else 0).
type calPoint struct {
	p    float64
	fact float64
}

// modelAcc accumulates per-model statistics.
type modelAcc struct {
	count  int
	points []calPoint
	net    domain.Decimal
	cost   domain.Decimal
}

// groupAcc accumulates one comparison group.
type groupAcc struct {
	count int
	sum   domain.Decimal
}

// add folds one net return into the group.
func (g *groupAcc) add(nr domain.Decimal) {
	g.count++
	g.sum = g.sum.Add(nr)
}

// avg returns the scale-12 mean, or zero when the group is empty.
func (g *groupAcc) avg() domain.Decimal {
	if g.count == 0 {
		return domain.Decimal{}
	}
	n, err := domain.ParseDecimal(strconv.Itoa(g.count))
	if err != nil {
		return domain.Decimal{}
	}
	avg, err := g.sum.QuoRound(n, avgScale)
	if err != nil {
		return domain.Decimal{}
	}
	return avg
}

// dayAgg holds the ALLOW and REJECT outcomes of one UTC entry day.
type dayAgg struct {
	allowSum   domain.Decimal
	allowCount int
	rejectSum  domain.Decimal
	rejectN    int
}

// arenaPayload mirrors the arena.decision event payload. It is declared
// locally so the app layer keeps depending on domain and ports only.
type arenaPayload struct {
	DecisionID string                  `json:"decisionId"`
	Model      string                  `json:"model"`
	Response   domain.DecisionResponse `json:"response"`
	Verdict    domain.GateVerdict      `json:"verdict"`
}

// arenaAcc accumulates arena statistics for one model.
type arenaAcc struct {
	verdicts   int
	allowSum   domain.Decimal
	allowCount int
	cost       domain.Decimal
	points     []calPoint
}

// Days returns the number of distinct UTC entry days used by the last
// Build bootstrap (0 when no comparison ran).
func (b *Builder) Days() int { return b.days }

// Build walks the journal once and computes the report. An empty journal
// yields a zero report without an error.
//
// Decision cost is summed over all unique recorded decisions (one entry
// per decisionId, last decision.response wins); unkeyed responses cannot
// be attributed and are ignored. By-model cost is summed over the included
// labeled decisions of that model only. Latency percentiles use the
// measured Usage.LatencyMS; vendor latency is stored in decision.response
// but not aggregated here.
func (b *Builder) Build(ctx context.Context) (domain.Report, error) {
	now := b.deps.Now().UTC()
	verdicts := map[string]verdictInfo{}
	decisions := map[string]decisionInfo{}
	signalToDecision := map[string]string{}
	var outcomes []outcomeEvent
	var arenas []arenaPayload
	// Labeling progress across the whole journal: entry signals still waiting
	// for an outcome, and signals recorded as unlabelable at their deadline.
	entrySignals := map[string]entrySignal{}
	labeledSignals := map[string]struct{}{}
	unlabelableSignals := map[string]struct{}{}
	missedDeadline := map[string]struct{}{}
	sourceRejected := map[string]struct{}{}
	// G1 provenance and the age of the latest label.
	synthetic := false
	var latestLabelAt time.Time

	if err := b.jr.Walk(ctx, 0, "", func(ev domain.Event) error {
		// One demo event taints the whole journal: the report may still be
		// useful, but it is not G1 evidence.
		if ev.RunID == demoRunID {
			synthetic = true
		}
		switch ev.Kind {
		case domain.EventGateRequest:
			var req struct {
				SignalID   string            `json:"signalId"`
				IntentKind domain.IntentKind `json:"intentKind"`
				Timeframe  string            `json:"timeframe"`
			}
			if err := json.Unmarshal(ev.Payload, &req); err != nil {
				return nil
			}
			if req.SignalID != "" && req.IntentKind == domain.IntentEntry {
				sig := entrySignals[req.SignalID]
				// Keep the earliest acceptance time: a replay of the same
				// signal must not hide a long-waiting request behind a fresh
				// timestamp.
				accepted := ev.TS.UTC()
				if sig.receivedAt.IsZero() || (!accepted.IsZero() && accepted.Before(sig.receivedAt)) {
					sig.receivedAt = accepted
				}
				if req.Timeframe != "" {
					sig.timeframe = req.Timeframe
				}
				if ev.DecisionID != "" {
					sig.decisionID = ev.DecisionID
				}
				entrySignals[req.SignalID] = sig
			}
			if req.SignalID != "" && ev.DecisionID != "" {
				signalToDecision[req.SignalID] = ev.DecisionID
			}
		case domain.EventDecisionResponse:
			var resp domain.DecisionResponse
			if err := json.Unmarshal(ev.Payload, &resp); err != nil {
				return nil
			}
			if ev.DecisionID == "" {
				return nil
			}
			info := decisionInfo{
				model:   resp.Model,
				cost:    resp.Usage.CostUSD,
				latency: resp.Usage.LatencyMS,
			}
			if p, ok := passProbability(resp); ok {
				info.pPass = p
				info.hasP = true
			}
			// Last response per decisionId wins; replays do not double count.
			decisions[ev.DecisionID] = info
		case domain.EventGateVerdict:
			var v domain.GateVerdict
			if err := json.Unmarshal(ev.Payload, &v); err != nil {
				return nil
			}
			id := ev.DecisionID
			if id == "" {
				id = v.DecisionID
			}
			if id == "" {
				return nil
			}
			policy := v.PolicyHash
			if policy == "" {
				policy = ev.PolicyHash
			}
			// Activity is measured on the answer timestamp: the receipt time
			// when the journal has one, otherwise the payload decidedAt.
			answeredAt := ev.TS.UTC()
			if answeredAt.IsZero() {
				answeredAt = v.DecidedAt.UTC()
			}
			// Last verdict per decisionId wins; replays do not double count.
			verdicts[id] = verdictInfo{
				verdict:    string(v.Verdict),
				policyHash: policy,
				seq:        ev.Seq,
				ts:         answeredAt,
				decidedAt:  v.DecidedAt.UTC(),
			}
		case domain.EventOutcomeLabeled:
			var o domain.Outcome
			if err := json.Unmarshal(ev.Payload, &o); err != nil {
				return nil
			}
			lagMS, lagRecorded := entryLagField(ev.Payload)
			outcomes = append(outcomes, outcomeEvent{
				outcome:     o,
				lagMS:       lagMS,
				lagRecorded: lagRecorded,
				seq:         ev.Seq,
			})
			if o.SignalID != "" {
				labeledSignals[o.SignalID] = struct{}{}
			}
			// The label age is operational evidence of a running labeler, so
			// it covers every labeled outcome, including backfills and
			// decisions of another policy.
			labeledAt := ev.TS.UTC()
			if labeledAt.IsZero() {
				labeledAt = o.LabeledAt.UTC()
			}
			if labeledAt.After(latestLabelAt) {
				latestLabelAt = labeledAt
			}
		case domain.EventOutcomeUnlabelable:
			var u domain.OutcomeUnlabelable
			if err := json.Unmarshal(ev.Payload, &u); err != nil {
				return nil
			}
			if u.SignalID == "" {
				return nil
			}
			unlabelableSignals[u.SignalID] = struct{}{}
			switch u.Reason {
			case "timeout":
				missedDeadline[u.SignalID] = struct{}{}
			case "source_rejected":
				sourceRejected[u.SignalID] = struct{}{}
			}
		case domain.EventArenaDecision:
			var a arenaPayload
			if err := json.Unmarshal(ev.Payload, &a); err != nil {
				return nil
			}
			if a.DecisionID == "" {
				a.DecisionID = ev.DecisionID
			}
			arenas = append(arenas, a)
		default:
			// Unknown kinds do not affect the report.
		}
		return nil
	}); err != nil {
		return domain.Report{}, fmt.Errorf("report: walk journal: %w", err)
	}

	// Verdict counts tally unique decisions, not verdict events.
	counts := map[string]int{
		string(domain.VerdictAllow):   0,
		string(domain.VerdictReject):  0,
		string(domain.VerdictAbstain): 0,
		string(domain.VerdictSkip):    0,
	}
	for _, v := range verdicts {
		counts[v.verdict]++
	}

	// Cost and latency aggregate unique decisions only.
	var latencies []int64
	costTotal := domain.Decimal{}
	for _, info := range decisions {
		latencies = append(latencies, info.latency)
		costTotal = costTotal.Add(info.cost)
	}

	selected := b.policyHash
	if selected == "" {
		selected = highestPolicy(verdicts)
	}
	otherPolicy := 0
	policyUsesSelected := false
	for _, v := range verdicts {
		if v.policyHash == selected {
			policyUsesSelected = true
		} else {
			otherPolicy++
		}
	}

	uniq := dedupOutcomes(outcomes)
	resolved := make(map[string]domain.Outcome, len(uniq))
	for _, oe := range uniq {
		if did := resolvedDecisionID(oe.outcome, signalToDecision); did != "" {
			resolved[did] = oe.outcome
		}
	}

	// Labeled outcomes: linked to a verdict of the selected policy, with
	// backfilled signals counted and (by default) excluded.
	var labeled []labeledOutcome
	backfillLabeled, backfillExcluded := 0, 0
	for _, oe := range uniq {
		o := oe.outcome
		did := resolvedDecisionID(o, signalToDecision)
		v, ok := verdicts[did]
		if !ok || v.policyHash != selected {
			continue
		}
		if o.Backfill {
			backfillLabeled++
			if !b.includeBackfill {
				backfillExcluded++
				continue
			}
		}
		labeled = append(labeled, labeledOutcome{
			outcome:     o,
			verdict:     v.verdict,
			decisionID:  did,
			seq:         oe.seq,
			lagMS:       oe.lagMS,
			lagRecorded: oe.lagRecorded,
		})
	}

	// Comparisons mix only one labeling parameter set: the latest
	// (horizon, fee) pair by journal seq. Older pairs stay counted in
	// OtherLabelParams so a silent change of labeling is visible.
	pair := latestLabelPair(labeled)
	compared := labeled
	otherLabelParams := 0
	if pair.ok {
		compared = make([]labeledOutcome, 0, len(labeled))
		for _, l := range labeled {
			if l.outcome.HorizonBars == pair.horizon && l.outcome.FeeBps == pair.feeBps {
				compared = append(compared, l)
			} else {
				otherLabelParams++
			}
		}
	}

	// A signal still waiting for labeling has neither a labeled outcome nor a
	// recorded unlabelable outcome yet.
	awaiting := 0
	for sig := range entrySignals {
		if _, ok := labeledSignals[sig]; ok {
			continue
		}
		if _, ok := unlabelableSignals[sig]; ok {
			continue
		}
		awaiting++
	}

	rep := domain.Report{
		GeneratedAt:          now,
		LabeledCount:         len(labeled),
		AwaitingLabeling:     awaiting,
		NoOutcomeByDeadline:  len(missedDeadline),
		SourceRejected:       len(sourceRejected),
		VerdictCounts:        counts,
		DecisionCostUSD:      costTotal,
		LatencyP50MS:         nearestRank(latencies, 0.5),
		LatencyP95MS:         nearestRank(latencies, 0.95),
		PolicyHash:           selected,
		OtherPolicyDecisions: otherPolicy,
		OtherLabelParams:     otherLabelParams,
		BackfillLabeled:      backfillLabeled,
		BackfillExcluded:     backfillExcluded,
		Assumptions:          assumptions(labeled, compared, pair),
		Arena:                arenaReport(arenas, resolved, verdicts, selected, b.includeBackfill),
	}
	// G1 is computed before the comparison early returns: a journal with few
	// labels still has an honest G1 progress block. Backfilled outcomes never
	// count towards G1, even with --include-backfill: an imported label is
	// not evidence that this installation labeled a live decision.
	g1Labels := 0
	for _, l := range labeled {
		if !l.outcome.Backfill {
			g1Labels++
		}
	}
	rep.G1 = b.g1Report(now, g1Labels, latestLabelAt, synthetic,
		verdicts, entrySignals, labeledSignals, unlabelableSignals, uniq)
	rep.Synthetic = rep.G1.Synthetic
	// An explicitly requested policy that no verdict used has no
	// comparable outcomes; this is not an error.
	if !policyUsesSelected {
		return rep, nil
	}
	if len(compared) < b.min {
		return rep, nil
	}
	rep.Sufficient = true
	rep.Comparisons = b.compare(compared, decisions)
	return rep, nil
}

// g1Report derives the honest G1 progress block. Labels are the
// policy-scoped labeled outcomes, always excluding backfills: an imported
// label is not evidence that this installation labeled a live decision, so
// --include-backfill must not inflate G1.
//
// Activity is derived from gate.verdict events, not gate.request: a request
// without a verdict is an unfinished cycle, while every verdict (including
// ABSTAIN) is an answer from a running gate.
func (b *Builder) g1Report(now time.Time, labels int, latestLabelAt time.Time, synthetic bool,
	verdicts map[string]verdictInfo, entries map[string]entrySignal,
	labeled, unlabelable map[string]struct{}, outcomes []outcomeEvent,
) *domain.ReportG1 {
	horizon, timeframe := b.horizonBars, b.timeframe
	derivedHorizon, derivedTimeframe := deriveLabelWindow(outcomes)
	if horizon <= 0 {
		horizon = derivedHorizon
	}
	if timeframe <= 0 {
		timeframe = derivedTimeframe
	}

	g1 := &domain.ReportG1{
		Labels:            labels,
		TargetLabels:      G1TargetLabels,
		TargetDays:        G1TargetDays,
		Synthetic:         synthetic,
		LastLabelAgeHours: -1,
		LabelsDue:         -1,
	}
	if !latestLabelAt.IsZero() {
		age := now.Sub(latestLabelAt).Hours()
		if age < 0 {
			// A journal clock ahead of ours is not a negative age.
			age = 0
		}
		g1.LastLabelAgeHours = age
	}
	if horizon > 0 && timeframe > 0 {
		g1.ExpectedLabelIntervalHours = (time.Duration(horizon+1) * timeframe).Hours()
	}

	// One timestamp per decision: a replay of the same verdict must not look
	// like fresh activity.
	answers := make([]time.Time, 0, len(verdicts))
	dates := make(map[string]struct{}, len(verdicts))
	for _, v := range verdicts {
		if v.ts.IsZero() {
			continue
		}
		answers = append(answers, v.ts)
		dates[v.ts.Format(dayLayout)] = struct{}{}
	}
	g1.ActiveDays = len(dates)
	sort.Slice(answers, func(i, j int) bool { return answers[i].Before(answers[j]) })
	var maxGap time.Duration
	for i := 1; i < len(answers); i++ {
		if gap := answers[i].Sub(answers[i-1]); gap > maxGap {
			maxGap = gap
		}
	}
	g1.MaxGapHours = maxGap.Hours()

	if due, known := labelsDue(now, horizon, timeframe, entries, labeled, unlabelable, verdicts); known {
		g1.LabelsDue = due
	}

	if synthetic {
		// Demo events prove nothing about a real installation: the counts
		// that would earn G1 are zeroed, while the diagnostic ages stay so
		// the operator can see that the demo journal is fresh.
		g1.Labels = 0
		g1.ActiveDays = 0
		g1.MaxGapHours = 0
		g1.LabelsDue = 0
	}
	g1.Eligible = !synthetic &&
		g1.Labels >= G1TargetLabels &&
		g1.ActiveDays >= G1TargetDays &&
		g1.MaxGapHours <= G1MaxGapHours
	return g1
}

// deriveLabelWindow returns the horizon and bar size of the latest labeled
// outcome that recorded them. It lets the G1 block show the expected label
// interval even when the CLI was not given a config file.
func deriveLabelWindow(outcomes []outcomeEvent) (horizon int, timeframe time.Duration) {
	bestHorizonSeq, bestTimeframeSeq := int64(-1), int64(-1)
	for _, oe := range outcomes {
		if oe.outcome.HorizonBars > 0 && oe.seq > bestHorizonSeq {
			horizon = oe.outcome.HorizonBars
			bestHorizonSeq = oe.seq
		}
		if tf, err := domain.ParseTimeframe(oe.outcome.Timeframe); err == nil && oe.seq > bestTimeframeSeq {
			timeframe = tf
			bestTimeframeSeq = oe.seq
		}
	}
	return horizon, timeframe
}

// labelsDue counts entry signals whose labeling window has expired and that
// have neither outcome.labeled nor outcome.unlabelable. The window mirrors the
// labeler: (horizonBars+1) x timeframe after the anchor (the verdict time when
// recorded, otherwise the request receipt time).
//
// The second result is false when the horizon or a signal's timeframe is
// unknown: the honest answer is then "unknown", never a misleading zero.
func labelsDue(now time.Time, horizon int, fallbackFrame time.Duration,
	entries map[string]entrySignal, labeled, unlabelable map[string]struct{},
	verdicts map[string]verdictInfo,
) (int, bool) {
	if horizon < 1 {
		return 0, false
	}
	due := 0
	known := true
	for sig, e := range entries {
		if _, ok := labeled[sig]; ok {
			continue
		}
		if _, ok := unlabelable[sig]; ok {
			continue
		}
		frame := fallbackFrame
		if e.timeframe != "" {
			tf, err := domain.ParseTimeframe(e.timeframe)
			if err != nil {
				// An explicitly corrupt bar size cannot be replaced by a
				// guess: the window is unknowable for this signal.
				known = false
				continue
			}
			frame = tf
		}
		if frame <= 0 {
			known = false
			continue
		}
		anchor := e.receivedAt
		if v, ok := verdicts[e.decisionID]; ok {
			// Mirror the labeler: the payload decision time wins, then the
			// journal answer time, then the request receipt time.
			switch {
			case !v.decidedAt.IsZero():
				anchor = v.decidedAt
			case !v.ts.IsZero():
				anchor = v.ts
			}
		}
		if anchor.IsZero() {
			known = false
			continue
		}
		if !now.Before(anchor.Add(time.Duration(horizon+1) * frame)) {
			due++
		}
	}
	if !known {
		return 0, false
	}
	return due, true
}

// passProbability extracts the validated p(pass) from a decision response.
func passProbability(resp domain.DecisionResponse) (float64, bool) {
	ans, ok := resp.Answers[riskQuestion]
	if !ok {
		return 0, false
	}
	p, ok := ans.Probabilities[passKey]
	if !ok || p < 0 || p > 1 || math.IsNaN(p) || math.IsInf(p, 0) {
		return 0, false
	}
	return p, true
}

// highestPolicy returns the policy hash of the highest-seq verdict. An
// empty map yields an empty hash.
func highestPolicy(verdicts map[string]verdictInfo) string {
	bestSeq := int64(-1)
	bestID, policy := "", ""
	for id, v := range verdicts {
		if v.seq > bestSeq || (v.seq == bestSeq && id > bestID) {
			bestSeq = v.seq
			bestID = id
			policy = v.policyHash
		}
	}
	return policy
}

// resolvedDecisionID returns the decisionId of an outcome, falling back to
// the gate.request signalId mapping.
func resolvedDecisionID(o domain.Outcome, signalToDecision map[string]string) string {
	if o.DecisionID != "" {
		return o.DecisionID
	}
	return signalToDecision[o.SignalID]
}

// outcomeKey identifies an outcome for deduplication: signalId when
// present, otherwise the decisionId.
func outcomeKey(o domain.Outcome) string {
	if o.SignalID != "" {
		return "sig:" + o.SignalID
	}
	return "dec:" + o.DecisionID
}

// dedupOutcomes keeps the last outcome.labeled event per signalId,
// preserving first-to-last order of the surviving events.
func dedupOutcomes(outcomes []outcomeEvent) []outcomeEvent {
	lastIdx := make(map[string]int, len(outcomes))
	for i, oe := range outcomes {
		lastIdx[outcomeKey(oe.outcome)] = i
	}
	uniq := make([]outcomeEvent, 0, len(lastIdx))
	for i, oe := range outcomes {
		if lastIdx[outcomeKey(oe.outcome)] == i {
			uniq = append(uniq, oe)
		}
	}
	return uniq
}

// latestLabelPair returns the (horizon, fee) pair of the highest-seq
// included labeled outcome, so a labeling-parameter change starts a new
// comparison set instead of mixing both.
func latestLabelPair(labeled []labeledOutcome) labelPair {
	pair := labelPair{}
	bestSeq := int64(-1)
	for _, l := range labeled {
		if l.seq > bestSeq {
			bestSeq = l.seq
			pair = labelPair{horizon: l.outcome.HorizonBars, feeBps: l.outcome.FeeBps, ok: true}
		}
	}
	return pair
}

// compare derives shadow-mode comparison statistics. Money uses Decimal
// addition only; float64 is used solely for probabilities and calibration.
func (b *Builder) compare(labeled []labeledOutcome, decisions map[string]decisionInfo) *domain.ReportComparisons {
	comp := &domain.ReportComparisons{
		Reliability: makeBuckets(),
		Groups:      make(map[string]domain.GroupStats, 4),
	}
	groups := map[string]*groupAcc{
		"all":                         {},
		string(domain.VerdictAllow):   {},
		string(domain.VerdictReject):  {},
		string(domain.VerdictAbstain): {},
	}
	days := map[string]*dayAgg{}
	var global []calPoint
	byModel := map[string]*modelAcc{}
	for _, l := range labeled {
		nr := l.outcome.NetReturn
		comp.AllEntriesReturn = comp.AllEntriesReturn.Add(nr)
		// "all" covers every included labeled outcome, including SKIP,
		// so its count always equals LabeledCount.
		groups["all"].add(nr)
		switch l.verdict {
		case string(domain.VerdictAllow):
			comp.AllowReturn = comp.AllowReturn.Add(nr)
			groups[string(domain.VerdictAllow)].add(nr)
			day := dayAggFor(days, l.outcome)
			day.allowSum = day.allowSum.Add(nr)
			day.allowCount++
		case string(domain.VerdictReject):
			groups[string(domain.VerdictReject)].add(nr)
			day := dayAggFor(days, l.outcome)
			day.rejectSum = day.rejectSum.Add(nr)
			day.rejectN++
		case string(domain.VerdictAbstain):
			groups[string(domain.VerdictAbstain)].add(nr)
		}
		var fact float64
		if nr.Sign() > 0 {
			fact = 1
		}
		info, ok := decisions[l.decisionID]
		if !ok {
			continue
		}
		if info.hasP {
			global = append(global, calPoint{p: info.pPass, fact: fact})
		}
		// Outcomes without a linked response or actual model cannot be
		// attributed to a model and stay out of the breakdown.
		if info.model == "" {
			continue
		}
		acc := byModel[info.model]
		if acc == nil {
			acc = &modelAcc{}
			byModel[info.model] = acc
		}
		acc.count++
		if info.hasP {
			acc.points = append(acc.points, calPoint{p: info.pPass, fact: fact})
		}
		acc.net = acc.net.Add(nr)
		acc.cost = acc.cost.Add(info.cost)
	}
	for name, acc := range groups {
		comp.Groups[name] = domain.GroupStats{
			Count:        acc.count,
			SumNetReturn: acc.sum,
			AvgNetReturn: acc.avg(),
		}
	}
	comp.RejectedAvgReturn = groups[string(domain.VerdictReject)].avg()
	comp.BaseRate = meanFact(global)
	comp.Brier = brier(global)
	comp.ConstantBrier = comp.BaseRate * (1 - comp.BaseRate)
	if comp.ConstantBrier > 0 {
		comp.BrierSkillScore = 1 - comp.Brier/comp.ConstantBrier
	}
	comp.AUC = auc(global)
	comp.CalibrationCount = len(global)
	comp.CalibrationSufficient = comp.CalibrationCount >= MinCalibrationPairs
	comp.ECE = fillReliability(comp.Reliability, global)
	b.days = len(days)
	comp.Difference = b.difference(
		groups[string(domain.VerdictAllow)],
		groups[string(domain.VerdictReject)],
		days,
	)
	names := make([]string, 0, len(byModel))
	for name := range byModel {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		acc := byModel[name]
		comp.ByModel = append(comp.ByModel, domain.ModelBreakdown{
			Model:            name,
			Count:            acc.count,
			Brier:            brier(acc.points),
			ECE:              fillReliability(makeBuckets(), acc.points),
			CalibrationCount: len(acc.points),
			NetReturnSum:     acc.net,
			CostUSD:          acc.cost,
			AvgNetReturn:     average(acc.net, acc.count),
		})
	}
	return comp
}

// dayAggFor returns the UTC entry-day accumulator of an outcome.
func dayAggFor(days map[string]*dayAgg, o domain.Outcome) *dayAgg {
	key := o.EntryTS.UTC().Format(dayLayout)
	d := days[key]
	if d == nil {
		d = &dayAgg{}
		days[key] = d
	}
	return d
}

// difference bootstraps the ALLOW-REJECT mean difference by resampling
// whole UTC entry days with replacement. It returns nil when either group
// is empty or no draw carried both groups, and also when either group has
// fewer than MinGroupSize observations: an interval built from a single
// observation is not evidence of a significant difference.
func (b *Builder) difference(allow, reject *groupAcc, days map[string]*dayAgg) *domain.DifferenceStats {
	if allow.count < MinGroupSize || reject.count < MinGroupSize || len(days) == 0 {
		return nil
	}
	keys := make([]string, 0, len(days))
	for k := range days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Too few distinct entry days make the interval unreliable, so no
	// CI is emitted; the caller still has the point mean.
	if len(keys) < MinBootstrapDays {
		return nil
	}
	rng := rand.New(rand.NewSource(b.seed))
	draws := make([]domain.Decimal, 0, b.reps)
	for i := 0; i < b.reps; i++ {
		var allowSum, rejectSum domain.Decimal
		allowN, rejectN := 0, 0
		for j := 0; j < len(keys); j++ {
			d := days[keys[rng.Intn(len(keys))]]
			allowSum = allowSum.Add(d.allowSum)
			allowN += d.allowCount
			rejectSum = rejectSum.Add(d.rejectSum)
			rejectN += d.rejectN
		}
		if allowN == 0 || rejectN == 0 {
			continue
		}
		a, err := averageErr(allowSum, allowN)
		if err != nil {
			continue
		}
		r, err := averageErr(rejectSum, rejectN)
		if err != nil {
			continue
		}
		draws = append(draws, a.Sub(r))
	}
	if len(draws) == 0 {
		return nil
	}
	sort.Slice(draws, func(i, j int) bool { return draws[i].Cmp(draws[j]) < 0 })
	low := quantile(draws, 0.025)
	high := quantile(draws, 0.975)
	return &domain.DifferenceStats{
		Mean:          allow.avg().Sub(reject.avg()),
		Low:           low,
		High:          high,
		ContainsZero:  low.Sign() <= 0 && high.Sign() >= 0,
		BootstrapReps: len(draws),
		Days:          len(keys),
	}
}

// quantile linearly interpolates the p quantile of a sorted Decimal slice.
// float64 is used only for the position; the value stays rational.
func quantile(sorted []domain.Decimal, p float64) domain.Decimal {
	n := len(sorted)
	if n == 0 {
		return domain.Decimal{}
	}
	if n == 1 {
		return sorted[0]
	}
	pos := p * float64(n-1)
	lo := int(math.Floor(pos))
	if lo < 0 {
		lo = 0
	}
	hi := lo + 1
	if hi > n-1 {
		lo, hi = n-1, n-1
	}
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	if frac <= 0 {
		return sorted[lo]
	}
	return interpolate(sorted[lo], sorted[hi], frac)
}

// interpolate returns lo + (hi-lo)*frac as a scale-12 Decimal using exact
// rational arithmetic on the decimal strings.
func interpolate(lo, hi domain.Decimal, frac float64) domain.Decimal {
	loRat, ok := decimalRat(lo)
	if !ok {
		return lo
	}
	hiRat, ok := decimalRat(hi)
	if !ok {
		return lo
	}
	fr := new(big.Rat).SetFloat64(frac)
	if fr == nil {
		return lo
	}
	delta := new(big.Rat).Sub(hiRat, loRat)
	delta.Mul(delta, fr)
	sum := new(big.Rat).Add(loRat, delta)
	num, err := domain.ParseDecimal(sum.Num().String())
	if err != nil {
		return lo
	}
	den, err := domain.ParseDecimal(sum.Denom().String())
	if err != nil {
		return lo
	}
	q, err := num.QuoRound(den, avgScale)
	if err != nil {
		return lo
	}
	return q
}

// decimalRat converts a Decimal to an exact rational from its string form.
func decimalRat(d domain.Decimal) (*big.Rat, bool) {
	r := new(big.Rat)
	if _, ok := r.SetString(d.String()); !ok {
		return nil, false
	}
	return r, true
}

// average returns sum/count at scale 12, or zero for an empty group.
func average(sum domain.Decimal, count int) domain.Decimal {
	avg, err := averageErr(sum, count)
	if err != nil {
		return domain.Decimal{}
	}
	return avg
}

// averageErr divides sum by count at scale 12.
func averageErr(sum domain.Decimal, count int) (domain.Decimal, error) {
	if count == 0 {
		return domain.Decimal{}, nil
	}
	n, err := domain.ParseDecimal(strconv.Itoa(count))
	if err != nil {
		return domain.Decimal{}, err
	}
	return sum.QuoRound(n, avgScale)
}

// assumptions records the labeling model of the included outcomes. The
// horizon/fee pair and price source come from the compared pair, while the
// entry-lag statistics cover every included labeled outcome. It is nil when
// nothing is included.
func assumptions(labeled, compared []labeledOutcome, pair labelPair) *domain.ReportAssumptions {
	if len(labeled) == 0 {
		return nil
	}
	sources := map[string]struct{}{}
	for _, l := range compared {
		if l.outcome.PriceSource != "" {
			sources[l.outcome.PriceSource] = struct{}{}
		}
	}
	priceSource := ""
	switch len(sources) {
	case 0:
	case 1:
		for s := range sources {
			priceSource = s
		}
	default:
		priceSource = "mixed"
	}
	median, p95, medianMS, p95MS, shifted := entryLagStats(labeled)
	return &domain.ReportAssumptions{
		HorizonBars:      pair.horizon,
		FeeBpsRoundTrip:  2 * pair.feeBps,
		PriceSource:      priceSource,
		EqualSize:        true,
		NoSlippage:       true,
		NoFunding:        true,
		EntryLagMedian:   median,
		EntryLagP95:      p95,
		EntryLagMedianMS: medianMS,
		EntryLagP95MS:    p95MS,
		EntryShifted:     shifted,
	}
}

// entryLagField reports the optional entryLagMs value and whether the field
// was present at all. An absent field must not be read as a zero lag: outcomes
// written before the field existed are not evidence of a perfect entry.
func entryLagField(payload []byte) (int64, bool) {
	var aux struct {
		domain.Outcome
		EntryLagMS *int64 `json:"entryLagMs"`
	}
	if err := json.Unmarshal(payload, &aux); err != nil || aux.EntryLagMS == nil {
		return 0, false
	}
	return *aux.EntryLagMS, true
}

// entryLagStats returns the median and nearest-rank p95 entry lag as a
// fraction of one bar and in milliseconds, plus the count of shifted entries.
// Only outcomes that recorded entryLagMs contribute to the lag statistics;
// every included outcome still counts as shifted. Fractions are clamped to
// [0, 1] because the contract bounds them.
func entryLagStats(labeled []labeledOutcome) (median, p95 float64, medianMS, p95MS int64, shifted int) {
	fracs := make([]float64, 0, len(labeled))
	lags := make([]int64, 0, len(labeled))
	for _, l := range labeled {
		if l.outcome.EntryShifted {
			shifted++
		}
		if !l.lagRecorded || l.lagMS < 0 {
			continue
		}
		lags = append(lags, l.lagMS)
		interval, err := domain.ParseTimeframe(l.outcome.Timeframe)
		if err != nil {
			continue
		}
		frac := float64(l.lagMS) / float64(interval/time.Millisecond)
		if math.IsNaN(frac) || math.IsInf(frac, 0) {
			continue
		}
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		fracs = append(fracs, frac)
	}
	medianMS = medianInt64(lags)
	p95MS = nearestRankInt64(lags, 0.95)
	if len(fracs) == 0 {
		return 0, 0, medianMS, p95MS, shifted
	}
	sort.Float64s(fracs)
	return medianFloat(fracs), nearestRankFloat(fracs, 0.95), medianMS, p95MS, shifted
}

// medianFloat returns the middle value of a sorted slice, averaging the two
// middle values for an even length. Empty input yields 0.
func medianFloat(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// nearestRankFloat returns the nearest-rank percentile of a sorted slice:
// rank = ceil(p*n), 1-indexed. Empty input yields 0.
func nearestRankFloat(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := int(math.Ceil(p * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// medianInt64 returns the middle value of a sorted copy, averaging the two
// middle values for an even length. Empty input yields 0.
func medianInt64(vals []int64) int64 {
	n := len(vals)
	if n == 0 {
		return 0
	}
	cp := append([]int64(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

// nearestRankInt64 returns the nearest-rank percentile of a sorted copy:
// rank = ceil(p*n), 1-indexed. Empty input yields 0.
func nearestRankInt64(vals []int64, p float64) int64 {
	n := len(vals)
	if n == 0 {
		return 0
	}
	cp := append([]int64(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	rank := int(math.Ceil(p * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return cp[rank-1]
}

// arenaReport aggregates arena.decision events of the selected policy. It
// returns nil when no arena event was recorded. Events whose decision has
// no verdict of the selected policy, or whose primary outcome is an
// excluded backfill, are ignored.
func arenaReport(events []arenaPayload, outcomes map[string]domain.Outcome, verdicts map[string]verdictInfo, selected string, includeBackfill bool) *domain.ArenaReport {
	if len(events) == 0 {
		return nil
	}
	events = dedupArena(events)
	accs := map[string]*arenaAcc{}
	decisionIDs := map[string]struct{}{}
	agreed, considered := 0, 0
	for _, e := range events {
		pv, ok := verdicts[e.DecisionID]
		if !ok || pv.policyHash != selected {
			continue
		}
		o, hasOutcome := outcomes[e.DecisionID]
		if hasOutcome && o.Backfill && !includeBackfill {
			continue
		}
		considered++
		decisionIDs[e.DecisionID] = struct{}{}
		if pv.verdict == string(e.Verdict.Verdict) {
			agreed++
		}
		if e.Model == "" {
			continue
		}
		acc := accs[e.Model]
		if acc == nil {
			acc = &arenaAcc{}
			accs[e.Model] = acc
		}
		acc.verdicts++
		acc.cost = acc.cost.Add(e.Response.Usage.CostUSD)
		if e.Verdict.Verdict == domain.VerdictAllow && hasOutcome {
			acc.allowSum = acc.allowSum.Add(o.NetReturn)
			acc.allowCount++
		}
		if p, ok := passProbability(e.Response); ok && hasOutcome {
			var fact float64
			if o.NetReturn.Sign() > 0 {
				fact = 1
			}
			acc.points = append(acc.points, calPoint{p: p, fact: fact})
		}
	}
	models := make([]string, 0, len(accs))
	for name := range accs {
		models = append(models, name)
	}
	sort.Strings(models)
	rep := &domain.ArenaReport{
		Decisions: len(decisionIDs),
		Models:    make([]domain.ArenaModelStats, 0, len(models)),
	}
	// The denominator is the number of considered arena events (selected
	// policy, non-excluded backfill), so ignored events cannot deflate the
	// share below 0 or inflate it above 1.
	if considered > 0 {
		rep.AgreementShare = float64(agreed) / float64(considered)
	}
	for _, name := range models {
		acc := accs[name]
		rep.Models = append(rep.Models, domain.ArenaModelStats{
			Model:          name,
			Verdicts:       acc.verdicts,
			AvgAllowReturn: average(acc.allowSum, acc.allowCount),
			AUC:            auc(acc.points),
			CostUSD:        acc.cost,
		})
	}
	return rep
}

// dedupArena keeps the last arena.decision per (decisionId, model), so a
// replay does not double count while every model keeps one verdict.
func dedupArena(events []arenaPayload) []arenaPayload {
	key := func(e arenaPayload) string { return e.DecisionID + "\x00" + e.Model }
	last := make(map[string]int, len(events))
	for i, e := range events {
		last[key(e)] = i
	}
	uniq := make([]arenaPayload, 0, len(last))
	for i, e := range events {
		if last[key(e)] == i {
			uniq = append(uniq, e)
		}
	}
	return uniq
}

// makeBuckets returns 10 empty reliability buckets.
func makeBuckets() []domain.ReliabilityBucket {
	buckets := make([]domain.ReliabilityBucket, 10)
	for i := range buckets {
		buckets[i].LowerBound = float64(i) / 10
	}
	return buckets
}

// meanFact returns the observed positive rate of the calibration pairs.
func meanFact(points []calPoint) float64 {
	if len(points) == 0 {
		return 0
	}
	sum := 0.0
	for _, pt := range points {
		sum += pt.fact
	}
	return sum / float64(len(points))
}

// auc returns the rank-based area under the ROC curve with average ranks
// for tied predictions. It is 0.5 when either class is empty.
func auc(points []calPoint) float64 {
	n := len(points)
	if n == 0 {
		return 0.5
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return points[idx[a]].p < points[idx[b]].p
	})
	rankSum := 0.0
	pos, neg := 0, 0
	for i := 0; i < n; {
		j := i
		for j+1 < n && points[idx[j+1]].p == points[idx[i]].p {
			j++
		}
		avgRank := (float64(i+1) + float64(j+1)) / 2
		for k := i; k <= j; k++ {
			if points[idx[k]].fact > 0.5 {
				rankSum += avgRank
				pos++
			} else {
				neg++
			}
		}
		i = j + 1
	}
	if pos == 0 || neg == 0 {
		return 0.5
	}
	return (rankSum - float64(pos*(pos+1))/2) / float64(pos*neg)
}

// brier returns the mean squared error between predicted probabilities
// and binary facts. No pairs means 0.
func brier(points []calPoint) float64 {
	if len(points) == 0 {
		return 0
	}
	sum := 0.0
	for _, pt := range points {
		d := pt.p - pt.fact
		sum += d * d
	}
	return sum / float64(len(points))
}

// fillReliability fills bucket counts, mean predictions and observed
// rates, and returns the expected calibration error: the count-weighted
// mean absolute gap between predicted and observed rates over non-empty
// buckets. Bucket index is min(9, int(p*10)).
func fillReliability(buckets []domain.ReliabilityBucket, points []calPoint) float64 {
	if len(points) == 0 {
		return 0
	}
	means := make([]float64, len(buckets))
	rates := make([]float64, len(buckets))
	for _, pt := range points {
		idx := int(pt.p * 10)
		if idx < 0 {
			idx = 0
		}
		if idx > len(buckets)-1 {
			idx = len(buckets) - 1
		}
		buckets[idx].Count++
		means[idx] += pt.p
		rates[idx] += pt.fact
	}
	ece := 0.0
	for i := range buckets {
		if buckets[i].Count == 0 {
			continue
		}
		mean := means[i] / float64(buckets[i].Count)
		rate := rates[i] / float64(buckets[i].Count)
		buckets[i].MeanPredicted = mean
		buckets[i].ObservedRate = rate
		ece += float64(buckets[i].Count) / float64(len(points)) * math.Abs(mean-rate)
	}
	return ece
}

// nearestRank returns the nearest-rank percentile over a copy of vals:
// rank = ceil(p*n), 1-indexed. Empty input yields 0.
func nearestRank(vals []int64, p float64) int64 {
	if len(vals) == 0 {
		return 0
	}
	cp := make([]int64, len(vals))
	copy(cp, vals)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	rank := int(math.Ceil(p * float64(len(cp))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(cp) {
		rank = len(cp)
	}
	return cp[rank-1]
}
