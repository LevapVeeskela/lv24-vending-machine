package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/report"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// minModelLabeled is the labeled-outcome count a model needs before the
// human report prints its comparison metrics.
const minModelLabeled = 30

// shareDiscussionURL is the manual publication target of a --share summary.
const shareDiscussionURL = "https://github.com/LevapVeeskela/lv24-vending-machine/discussions"

// sharePolicyLen is the policy-hash prefix length printed by --share.
const sharePolicyLen = 8

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
	share := fs.Bool("share", false, "print a sanitized markdown summary instead of the full report")
	shareOut := fs.String("out", "", "write the --share markdown to FILE (0600)")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend gate report [--policy HASH] [--include-backfill] [--share [--out FILE]] [--json]", false, nil)
	}
	if *share && jsonOut {
		// A shareable summary is the opposite of the full JSON report.
		return r.fail(false, exitUsage, codeUsage,
			"--share cannot be combined with --json",
			"run vend gate report --share [--out FILE]", false, nil)
	}
	if *shareOut != "" && !*share {
		return r.fail(jsonOut, exitUsage, codeUsage,
			"--out requires --share",
			"run vend gate report --share --out FILE", false, nil)
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
	if *share {
		return r.printShare(ctx, jr, rep, *shareOut)
	}
	if jsonOut {
		r.printJSON(rep)
		return exitOK
	}
	printReportHuman(r.stdout, rep, b.Days())
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
func printReportHuman(w io.Writer, rep domain.Report, days int) {
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
	fmt.Fprintf(w, "ожидают разметки: %d\n", rep.AwaitingLabeling)
	fmt.Fprintf(w, "без исхода по сроку: %d\n", rep.NoOutcomeByDeadline)
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
	} else if allow, reject := c.Groups["ALLOW"], c.Groups["REJECT"]; allow.Count > 0 && reject.Count > 0 {
		mean := allow.AvgNetReturn.Sub(reject.AvgNetReturn)
		fmt.Fprintf(w, "difference ALLOW-REJECT: mean=%s\n", mean.String())
		fmt.Fprintf(w, "insufficient days for a 95%% interval (%d days, need %d)\n",
			days, report.MinBootstrapDays)
	} else {
		fmt.Fprintf(w, "difference ALLOW-REJECT: insufficient data\n")
	}
	fmt.Fprintf(w, "brier: %.6f\n", c.Brier)
	fmt.Fprintf(w, "base rate: %.6f\n", c.BaseRate)
	fmt.Fprintf(w, "constant brier: %.6f\n", c.ConstantBrier)
	// A degenerate base rate makes the constant forecast perfect, so the
	// skill score is undefined rather than zero.
	if c.BaseRate <= 0 || c.BaseRate >= 1 {
		fmt.Fprintf(w, "bss: n/a\n")
	} else {
		fmt.Fprintf(w, "bss: %.6f\n", c.BrierSkillScore)
	}
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

// printShare writes the sanitized markdown summary either to stdout or to
// path, and never echoes journal content beyond aggregate counts.
func (r *runner) printShare(ctx context.Context, jr ports.Journal, rep domain.Report, path string) int {
	decisions, days, err := shareWindow(ctx, jr)
	if err != nil {
		return r.fail(false, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	body := shareMarkdown(rep, r.ver, decisions, days)
	if path == "" {
		fmt.Fprint(r.stdout, body)
		return exitOK
	}
	if werr := writeFileAtomic(path, []byte(body), 0o600); werr != nil {
		return r.fail(false, exitGeneral, codeGeneral, werr.Error(),
			"check the output path is writable and retry", false, nil)
	}
	fmt.Fprintf(r.stdout, "written to %s\n", path)
	return exitOK
}

// shareWindow counts gate requests and the UTC-day span covered by their
// timestamps. Any request means at least one day.
func shareWindow(ctx context.Context, jr ports.Journal) (decisions, days int, err error) {
	var first, last time.Time
	if err := jr.Walk(ctx, 0, "", func(ev domain.Event) error {
		if ev.Kind != domain.EventGateRequest {
			return nil
		}
		decisions++
		var req struct {
			TS time.Time `json:"ts"`
		}
		if uerr := json.Unmarshal(ev.Payload, &req); uerr != nil || req.TS.IsZero() {
			return nil
		}
		ts := req.TS.UTC()
		if first.IsZero() || ts.Before(first) {
			first = ts
		}
		if last.IsZero() || ts.After(last) {
			last = ts
		}
		return nil
	}); err != nil {
		return 0, 0, fmt.Errorf("report: walk journal: %w", err)
	}
	if decisions == 0 {
		return 0, 0, nil
	}
	days = 1
	if !first.IsZero() && !last.IsZero() {
		span := int(last.Truncate(24*time.Hour).Sub(first.Truncate(24*time.Hour)).Hours()/24) + 1
		if span > days {
			days = span
		}
	}
	return decisions, days, nil
}

// shareMarkdown renders the anonymized summary: aggregate counts, model
// quality metrics and assumptions only. State, instruments, venues, prices,
// return sums, paths and identifiers never appear here.
func shareMarkdown(rep domain.Report, ver string, decisions, days int) string {
	var b strings.Builder
	total := 0
	for _, v := range rep.VerdictCounts {
		total += v
	}
	if decisions == 0 {
		decisions = total
	}
	policy := "n/a"
	if len(rep.PolicyHash) >= sharePolicyLen {
		policy = rep.PolicyHash[:sharePolicyLen]
	} else if rep.PolicyHash != "" {
		policy = rep.PolicyHash
	}
	fmt.Fprintf(&b, "# LV24 Vending — shadow gate summary\n\n")
	fmt.Fprintf(&b, "- version: %s\n", displayVersion(ver))
	fmt.Fprintf(&b, "- policy: %s\n", policy)
	fmt.Fprintf(&b, "- decisions: %d\n", decisions)
	fmt.Fprintf(&b, "- days: %d\n", days)
	fmt.Fprintf(&b, "- labeled outcomes: %d\n", rep.LabeledCount)
	fmt.Fprintf(&b, "- ожидают разметки: %d\n", rep.AwaitingLabeling)
	fmt.Fprintf(&b, "- без исхода по сроку: %d\n", rep.NoOutcomeByDeadline)
	fmt.Fprintf(&b, "- verdicts: ALLOW=%d REJECT=%d ABSTAIN=%d\n\n",
		rep.VerdictCounts[string(domain.VerdictAllow)],
		rep.VerdictCounts[string(domain.VerdictReject)],
		rep.VerdictCounts[string(domain.VerdictAbstain)])

	if rep.Sufficient && rep.Comparisons != nil {
		c := rep.Comparisons
		fmt.Fprintf(&b, "## Metrics\n\n")
		fmt.Fprintf(&b, "- Brier score: %.4f\n", c.Brier)
		fmt.Fprintf(&b, "- base rate: %.4f\n", c.BaseRate)
		fmt.Fprintf(&b, "- AUC: %.4f\n", c.AUC)
		fmt.Fprintf(&b, "- Brier skill score: %.4f\n", c.BrierSkillScore)
		if d := c.Difference; d != nil {
			fmt.Fprintf(&b, "- ALLOW vs REJECT mean difference: %s\n", d.Mean.String())
			fmt.Fprintf(&b, "- 95%% CI: [%s, %s] (%s)\n\n",
				d.Low.String(), d.High.String(), zeroWording(d.ContainsZero))
		} else {
			fmt.Fprintf(&b, "- ALLOW vs REJECT mean difference: insufficient data\n\n")
		}
	} else {
		fmt.Fprintf(&b, "## Metrics\n\n")
		fmt.Fprintf(&b, "- insufficient data: %d labeled outcomes, need %d\n\n",
			rep.LabeledCount, report.DefaultMinLabeled)
	}

	fmt.Fprintf(&b, "## Assumptions\n\n")
	if a := rep.Assumptions; a != nil {
		fmt.Fprintf(&b, "- horizon: %d bars\n", a.HorizonBars)
		fmt.Fprintf(&b, "- round-trip fee: %d bps\n", a.FeeBpsRoundTrip)
		fmt.Fprintf(&b, "- entry lag median: %.4f bars (%.3f s)\n",
			a.EntryLagMedian, float64(a.EntryLagMedianMS)/1000)
		fmt.Fprintf(&b, "- entry lag p95: %.4f bars (%.3f s)\n",
			a.EntryLagP95, float64(a.EntryLagP95MS)/1000)
		fmt.Fprintf(&b, "- entry shifted: %d\n", a.EntryShifted)
		fmt.Fprintf(&b, "- equal size, no slippage, no funding\n\n")
	} else {
		fmt.Fprintf(&b, "- no labeled outcomes yet\n\n")
	}

	fmt.Fprintf(&b, "## Publishing\n\n")
	fmt.Fprintf(&b, "Post this summary manually: %s\n", shareDiscussionURL)
	return b.String()
}

// displayVersion keeps the summary readable when the build has no version.
func displayVersion(ver string) string {
	if strings.TrimSpace(ver) == "" {
		return "dev"
	}
	return ver
}

// zeroWording describes whether a confidence interval covers zero.
func zeroWording(containsZero bool) string {
	if containsZero {
		return "contains zero"
	}
	return "does not contain zero"
}
