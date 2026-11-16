// Package report builds shadow-mode evaluation reports from the journal.
//
// The builder walks the journal once and derives verdict counts, decision
// costs and latency percentiles from every event, plus comparison
// statistics (returns, Brier score, ECE, reliability curve and per-model
// breakdown) once enough outcomes are labeled. It performs no I/O besides
// the journal walk and never touches the network.
package report

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// defaultMinLabeled is the labeled-outcome threshold applied when
// Config.MinLabeled is not positive.
const defaultMinLabeled = 100

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
}

// Builder computes domain.Report values from a journal.
type Builder struct {
	jr   ports.Journal
	deps ports.Deps
	min  int
}

// New builds a Builder over jr. deps supplies the clock for GeneratedAt.
func New(jr ports.Journal, deps ports.Deps, cfg Config) (*Builder, error) {
	if jr == nil {
		return nil, fmt.Errorf("report: nil journal")
	}
	min := cfg.MinLabeled
	if min <= 0 {
		min = defaultMinLabeled
	}
	return &Builder{jr: jr, deps: deps, min: min}, nil
}

// decisionInfo is the extracted per-decision data used by the report.
type decisionInfo struct {
	model   string
	pPass   float64
	hasP    bool
	cost    domain.Decimal
	latency int64
}

// labeledOutcome pairs an outcome with its gate verdict and decision ID.
type labeledOutcome struct {
	outcome    domain.Outcome
	verdict    string
	decisionID string
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

// Build walks the journal once and computes the report. An empty journal
// yields a zero report without an error.
func (b *Builder) Build(ctx context.Context) (domain.Report, error) {
	counts := map[string]int{
		string(domain.VerdictAllow):   0,
		string(domain.VerdictReject):  0,
		string(domain.VerdictAbstain): 0,
		string(domain.VerdictSkip):    0,
	}
	verdicts := map[string]string{}
	decisions := map[string]decisionInfo{}
	signalToDecision := map[string]string{}
	var outcomes []domain.Outcome
	var latencies []int64
	costTotal := domain.Decimal{}

	if err := b.jr.Walk(ctx, 0, "", func(ev domain.Event) error {
		switch ev.Kind {
		case domain.EventGateRequest:
			var req struct {
				SignalID string `json:"signalId"`
			}
			if err := json.Unmarshal(ev.Payload, &req); err != nil {
				return nil
			}
			if req.SignalID != "" && ev.DecisionID != "" {
				signalToDecision[req.SignalID] = ev.DecisionID
			}
		case domain.EventDecisionResponse:
			var resp domain.DecisionResponse
			if err := json.Unmarshal(ev.Payload, &resp); err != nil {
				return nil
			}
			info := decisionInfo{
				model:   resp.Model,
				cost:    resp.Usage.CostUSD,
				latency: resp.Usage.LatencyMS,
			}
			if ans, ok := resp.Answers[riskQuestion]; ok {
				if p, ok := ans.Probabilities[passKey]; ok && p >= 0 && p <= 1 &&
					!math.IsNaN(p) && !math.IsInf(p, 0) {
					info.pPass = p
					info.hasP = true
				}
			}
			latencies = append(latencies, resp.Usage.LatencyMS)
			costTotal = costTotal.Add(resp.Usage.CostUSD)
			if ev.DecisionID != "" {
				decisions[ev.DecisionID] = info
			}
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
			verdicts[id] = string(v.Verdict)
			counts[string(v.Verdict)]++
		case domain.EventOutcomeLabeled:
			var o domain.Outcome
			if err := json.Unmarshal(ev.Payload, &o); err != nil {
				return nil
			}
			outcomes = append(outcomes, o)
		default:
			// Unknown kinds do not affect the report.
		}
		return nil
	}); err != nil {
		return domain.Report{}, fmt.Errorf("report: walk journal: %w", err)
	}

	var labeled []labeledOutcome
	for _, o := range outcomes {
		did := o.DecisionID
		if did == "" {
			did = signalToDecision[o.SignalID]
		}
		v, ok := verdicts[did]
		if !ok {
			continue
		}
		labeled = append(labeled, labeledOutcome{outcome: o, verdict: v, decisionID: did})
	}

	rep := domain.Report{
		GeneratedAt:     b.deps.Now().UTC(),
		LabeledCount:    len(labeled),
		VerdictCounts:   counts,
		DecisionCostUSD: costTotal,
		LatencyP50MS:    nearestRank(latencies, 0.5),
		LatencyP95MS:    nearestRank(latencies, 0.95),
	}
	if len(labeled) < b.min {
		return rep, nil
	}
	rep.Sufficient = true
	rep.Comparisons = b.compare(labeled, decisions)
	return rep, nil
}

// compare derives shadow-mode comparison statistics. Money uses Decimal
// addition only; float64 is used solely for probabilities and calibration.
func (b *Builder) compare(labeled []labeledOutcome, decisions map[string]decisionInfo) *domain.ReportComparisons {
	comp := &domain.ReportComparisons{
		Reliability: make([]domain.ReliabilityBucket, 10),
	}
	for i := range comp.Reliability {
		comp.Reliability[i].LowerBound = float64(i) / 10
	}
	var global []calPoint
	rejectSum := domain.Decimal{}
	rejectCount := 0
	byModel := map[string]*modelAcc{}
	for _, l := range labeled {
		nr := l.outcome.NetReturn
		comp.AllEntriesReturn = comp.AllEntriesReturn.Add(nr)
		switch l.verdict {
		case string(domain.VerdictAllow):
			comp.AllowReturn = comp.AllowReturn.Add(nr)
		case string(domain.VerdictReject):
			rejectSum = rejectSum.Add(nr)
			rejectCount++
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
	if rejectCount > 0 {
		n, err := domain.ParseDecimal(strconv.Itoa(rejectCount))
		if err == nil {
			if avg, err := rejectSum.QuoRound(n, 12); err == nil {
				comp.RejectedAvgReturn = avg
			}
		}
	}
	comp.Brier = brier(global)
	comp.ECE = fillReliability(comp.Reliability, global)
	names := make([]string, 0, len(byModel))
	for name := range byModel {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		acc := byModel[name]
		comp.ByModel = append(comp.ByModel, domain.ModelBreakdown{
			Model:        name,
			Count:        acc.count,
			Brier:        brier(acc.points),
			ECE:          fillReliability(makeBuckets(), acc.points),
			NetReturnSum: acc.net,
			CostUSD:      acc.cost,
		})
	}
	return comp
}

// makeBuckets returns 10 empty reliability buckets.
func makeBuckets() []domain.ReliabilityBucket {
	buckets := make([]domain.ReliabilityBucket, 10)
	for i := range buckets {
		buckets[i].LowerBound = float64(i) / 10
	}
	return buckets
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
