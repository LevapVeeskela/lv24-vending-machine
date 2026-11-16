package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/report"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// minModelLabeled is the labeled-outcome count a model needs before the
// human report prints its comparison metrics.
const minModelLabeled = 30

func (r *runner) cmdGateReport(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend gate report [--policy HASH] [--include-backfill] [--json]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("gate report")
	policy := fs.String("policy", "", "only report decisions made under this 64-hex policy hash")
	includeBackfill := fs.Bool("include-backfill", false, "count backfilled outcomes in the comparison")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend gate report [--policy HASH] [--include-backfill] [--json]", false, nil)
	}
	if *policy != "" && !isPolicyHash(*policy) {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("invalid --policy %q", *policy),
			"pass a 64-character lowercase hex policy hash", false, nil)
	}
	dataDir, err := r.dataDir(g.dataDir)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"set VENDING_DATA_DIR explicitly", false, nil)
	}
	dbPath := config.DBPath(dataDir)
	if err := statMissing(dbPath); err != nil {
		if isNotFound(err) {
			return r.fail(jsonOut, exitNotFound, codeNotFound,
				fmt.Sprintf("database not found at %s", dbPath),
				"run vend init to create it", false, nil)
		}
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the data directory is readable and retry", false, nil)
	}
	ctx := context.Background()
	jr, closer, err := r.openJournalAt(dbPath)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	defer func() { _ = closer.Close() }()
	b, err := report.New(jr, ports.Deps{Clock: r.clock, NewID: r.newID}, report.Config{
		PolicyHash:      *policy,
		IncludeBackfill: *includeBackfill,
	})
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the vend wiring and retry", false, nil)
	}
	rep, err := b.Build(ctx)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	if jsonOut {
		r.printJSON(rep)
		return exitOK
	}
	printReportHuman(r.stdout, rep)
	return exitOK
}

// isPolicyHash reports whether s is exactly 64 lowercase hex characters.
func isPolicyHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// printReportHuman prints the assumptions and base stats always, the
// comparison block only when the report holds enough labeled outcomes,
// and per-model metrics only at or above the model threshold.
func printReportHuman(w io.Writer, rep domain.Report) {
	if a := rep.Assumptions; a != nil {
		fmt.Fprintf(w, "assumptions: horizon=%d bars, fees=%d bps round-trip, "+
			"price source=%s, equal size, no slippage, no funding\n",
			a.HorizonBars, a.FeeBpsRoundTrip, a.PriceSource)
	}
	counts := rep.VerdictCounts
	fmt.Fprintf(w, "verdicts: ALLOW=%d REJECT=%d ABSTAIN=%d SKIP=%d\n",
		counts[string(domain.VerdictAllow)], counts[string(domain.VerdictReject)],
		counts[string(domain.VerdictAbstain)], counts[string(domain.VerdictSkip)])
	fmt.Fprintf(w, "labeled: %d\n", rep.LabeledCount)
	fmt.Fprintf(w, "policy: %s\n", shortPolicy(rep.PolicyHash))
	fmt.Fprintf(w, "other policies: %d\n", rep.OtherPolicyDecisions)
	fmt.Fprintf(w, "backfill: labeled=%d excluded=%d\n",
		rep.BackfillLabeled, rep.BackfillExcluded)
	fmt.Fprintf(w, "decision cost: %s USD\n", rep.DecisionCostUSD.String())
	fmt.Fprintf(w, "latency p50/p95: %dms/%dms\n", rep.LatencyP50MS, rep.LatencyP95MS)
	if a := rep.Arena; a != nil {
		fmt.Fprintf(w, "arena: decisions=%d agreement=%.4f\n", a.Decisions, a.AgreementShare)
		for _, m := range a.Models {
			fmt.Fprintf(w, "  %s: verdicts=%d avgAllow=%s auc=%.4f cost=%s USD\n",
				m.Model, m.Verdicts, m.AvgAllowReturn.String(), m.AUC, m.CostUSD.String())
		}
	}
	if !rep.Sufficient || rep.Comparisons == nil {
		fmt.Fprintf(w, "insufficient data: %d labeled, need %d\n", rep.LabeledCount, report.DefaultMinLabeled)
		return
	}
	c := rep.Comparisons
	fmt.Fprintf(w, "all entries return: %s\n", c.AllEntriesReturn.String())
	fmt.Fprintf(w, "allow return: %s\n", c.AllowReturn.String())
	fmt.Fprintf(w, "rejected avg return: %s\n", c.RejectedAvgReturn.String())
	fmt.Fprintf(w, "groups:\n")
	for _, name := range []string{"all", "ALLOW", "REJECT", "ABSTAIN"} {
		g := c.Groups[name]
		fmt.Fprintf(w, "  %s: n=%d sum=%s avg=%s\n",
			name, g.Count, g.SumNetReturn.String(), g.AvgNetReturn.String())
	}
	if d := c.Difference; d != nil {
		fmt.Fprintf(w, "difference ALLOW-REJECT: mean=%s ci95=[%s, %s] reps=%d days=%d\n",
			d.Mean.String(), d.Low.String(), d.High.String(), d.BootstrapReps, d.Days)
		if d.ContainsZero {
			fmt.Fprintf(w, "разница не отличима от шума\n")
		}
	} else {
		fmt.Fprintf(w, "difference ALLOW-REJECT: insufficient data\n")
	}
	fmt.Fprintf(w, "brier: %.6f\n", c.Brier)
	fmt.Fprintf(w, "base rate: %.6f\n", c.BaseRate)
	fmt.Fprintf(w, "constant brier: %.6f\n", c.ConstantBrier)
	fmt.Fprintf(w, "brier skill: %.6f\n", c.BrierSkillScore)
	fmt.Fprintf(w, "auc: %.6f\n", c.AUC)
	fmt.Fprintf(w, "ece: %.6f\n", c.ECE)
	fmt.Fprintf(w, "calibration pairs: %d\n", c.CalibrationCount)
	fmt.Fprintf(w, "reliability:\n")
	for _, b := range c.Reliability {
		fmt.Fprintf(w, "  [%.1f-%.1f): n=%d mean=%.4f observed=%.4f\n",
			b.LowerBound, b.LowerBound+0.1, b.Count, b.MeanPredicted, b.ObservedRate)
	}
	fmt.Fprintf(w, "by model:\n")
	for _, m := range c.ByModel {
		if m.Count < minModelLabeled {
			fmt.Fprintf(w, "  model %s: insufficient data (%d labeled)\n", m.Model, m.Count)
			continue
		}
		fmt.Fprintf(w, "  %s: n=%d brier=%.6f ece=%.6f net=%s avg=%s cost=%s USD\n",
			m.Model, m.Count, m.Brier, m.ECE, m.NetReturnSum.String(),
			m.AvgNetReturn.String(), m.CostUSD.String())
	}
}

// shortPolicy returns the first 12 characters of a policy hash.
func shortPolicy(hash string) string {
	if hash == "" {
		return "(none)"
	}
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
