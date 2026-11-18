package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
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

// shareDayLayout is the UTC date key used by the report window.
const shareDayLayout = "2006-01-02"

// reportWindow describes when the journal accepted gate requests: the
// inclusive UTC-day span, the number of distinct UTC dates and the request
// count. Payload timestamps are ignored so a late or replayed signal cannot
// stretch the window.
type reportWindow struct {
	decisions int
	days      int
	active    int
}

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
	shareForce := fs.Bool("force", false, "overwrite an existing --out file")
	allowBrokenChain := fs.Bool("allow-broken-chain", false,
		"report even when the journal hash chain is broken")
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
	g1Horizon, g1Frame := r.g1LabelConfig(g)
	jr, closer, err := r.openJournalAt(dbPath)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	defer func() { _ = closer.Close() }()
	// A report is only evidence when the journal behind it is intact. The
	// opt-in flag keeps the pre-A1 behavior for an operator who needs the
	// numbers from a damaged database.
	if !*allowBrokenChain {
		res, verr := jr.Verify(ctx)
		if verr != nil {
			return r.fail(jsonOut, exitGeneral, codeGeneral, verr.Error(),
				"check the database file is a valid sqlite database", false, nil)
		}
		if !res.OK {
			return r.fail(jsonOut, exitGeneral, codeGeneral,
				fmt.Sprintf("journal chain broken at seq %d: %s", res.BrokenSeq, res.Reason),
				"restore the database from backup; pass --allow-broken-chain to report anyway",
				false, map[string]any{"seq": res.BrokenSeq, "reason": res.Reason})
		}
	}
	b, err := report.New(jr, ports.Deps{Clock: r.clock, NewID: r.newID}, report.Config{
		PolicyHash:      *policy,
		IncludeBackfill: *includeBackfill,
		HorizonBars:     g1Horizon,
		Timeframe:       g1Frame,
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
	win, err := journalWindow(ctx, jr)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	// The window belongs to the report output: the human line and the JSON
	// fields days/activeDays always carry the same numbers.
	rep.Days = win.days
	rep.ActiveDays = win.active
	if *share {
		// A synthetic journal is useful for a demo, but publishing it as a
		// shadow result would misrepresent real G1 progress.
		if rep.G1 != nil && rep.G1.Synthetic {
			return r.fail(false, exitUsage, codeUsage,
				"synthetic journal is not G1 evidence",
				"run vend demo for a synthetic walkthrough, or report a real data directory",
				false, nil)
		}
		return r.printShare(rep, *shareOut, win, dataDir, *shareForce)
	}
	if jsonOut {
		r.printJSON(rep)
		return exitOK
	}
	printReportHuman(r.stdout, rep, b.Days(), win)
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
func printReportHuman(w io.Writer, rep domain.Report, days int, win reportWindow) {
	if a := rep.Assumptions; a != nil {
		fmt.Fprintf(w, "assumptions: horizon=%d bars, fees=%d bps round-trip, "+
			"price source=%s, equal size, no slippage, no funding\n",
			a.HorizonBars, a.FeeBpsRoundTrip, a.PriceSource)
	}
	counts := rep.VerdictCounts
	fmt.Fprintf(w, "verdicts: ALLOW=%d REJECT=%d ABSTAIN=%d SKIP=%d\n",
		counts[string(domain.VerdictAllow)], counts[string(domain.VerdictReject)],
		counts[string(domain.VerdictAbstain)], counts[string(domain.VerdictSkip)])
	fmt.Fprintf(w, "window: days=%d active=%d\n", win.days, win.active)
	printG1Human(w, rep.G1)
	fmt.Fprintf(w, "labeled: %d\n", rep.LabeledCount)
	fmt.Fprintf(w, "awaiting labeling: %d\n", rep.AwaitingLabeling)
	fmt.Fprintf(w, "no outcome by deadline: %d\n", rep.NoOutcomeByDeadline)
	fmt.Fprintf(w, "source rejected: %d\n", rep.SourceRejected)
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
		switch {
		case allow.Count < report.MinGroupSize || reject.Count < report.MinGroupSize:
			fmt.Fprintf(w, "insufficient group size (ALLOW=%d, REJECT=%d, need %d per group)\n",
				allow.Count, reject.Count, report.MinGroupSize)
		case days < report.MinBootstrapDays:
			fmt.Fprintf(w, "insufficient days for a 95%% interval (%d days, need %d)\n",
				days, report.MinBootstrapDays)
		default:
			fmt.Fprintf(w, "insufficient data for a 95%% interval\n")
		}
	} else {
		fmt.Fprintf(w, "difference ALLOW-REJECT: insufficient data\n")
	}
	// Calibration metrics describe a probability sample; without enough
	// pairs they are n/a, never zero. AUC additionally needs both observed
	// classes and BSS a non-degenerate base rate.
	if !c.CalibrationSufficient {
		fmt.Fprintf(w, "calibration pairs: %d (need %d)\n", c.CalibrationCount, report.MinCalibrationPairs)
		fmt.Fprintf(w, "brier: n/a\n")
		fmt.Fprintf(w, "base rate: n/a\n")
		fmt.Fprintf(w, "constant brier: n/a\n")
		fmt.Fprintf(w, "bss: n/a\n")
		fmt.Fprintf(w, "auc: n/a\n")
		fmt.Fprintf(w, "ece: n/a\n")
		fmt.Fprintf(w, "reliability: n/a\n")
	} else {
		fmt.Fprintf(w, "calibration pairs: %d\n", c.CalibrationCount)
		fmt.Fprintf(w, "brier: %.6f\n", c.Brier)
		fmt.Fprintf(w, "base rate: %.6f\n", c.BaseRate)
		fmt.Fprintf(w, "constant brier: %.6f\n", c.ConstantBrier)
		// A degenerate base rate makes the constant forecast perfect, so the
		// skill score and the AUC are undefined rather than zero.
		if c.BaseRate <= 0 || c.BaseRate >= 1 {
			fmt.Fprintf(w, "bss: n/a\n")
			fmt.Fprintf(w, "auc: n/a\n")
		} else {
			fmt.Fprintf(w, "bss: %.6f\n", c.BrierSkillScore)
			fmt.Fprintf(w, "auc: %.6f\n", c.AUC)
		}
		fmt.Fprintf(w, "ece: %.6f\n", c.ECE)
		fmt.Fprintf(w, "reliability:\n")
		for _, b := range c.Reliability {
			fmt.Fprintf(w, "  [%.1f-%.1f): n=%d mean=%.4f observed=%.4f\n",
				b.LowerBound, b.LowerBound+0.1, b.Count, b.MeanPredicted, b.ObservedRate)
		}
	}
	fmt.Fprintf(w, "by model:\n")
	for _, m := range c.ByModel {
		switch {
		case m.Count < minModelLabeled:
			fmt.Fprintf(w, "  model %s: insufficient data (%d labeled)\n", m.Model, m.Count)
		case m.CalibrationCount < report.MinCalibrationPairs:
			fmt.Fprintf(w, "  model %s: n=%d insufficient calibration (%d pairs, need %d)\n",
				m.Model, m.Count, m.CalibrationCount, report.MinCalibrationPairs)
		default:
			fmt.Fprintf(w, "  %s: n=%d brier=%.6f ece=%.6f net=%s avg=%s cost=%s USD\n",
				m.Model, m.Count, m.Brier, m.ECE, m.NetReturnSum.String(),
				m.AvgNetReturn.String(), m.CostUSD.String())
		}
	}
}

// printG1Human prints the readiness block shared by gate report and status.
// It never prints a bare zero for an unknown value: an unknown label window is
// "n/a" and an unknown due count is "unknown".
func printG1Human(w io.Writer, g1 *domain.ReportG1) {
	if g1 == nil {
		return
	}
	if g1.Synthetic {
		fmt.Fprintln(w, "SYNTHETIC — not G1 evidence")
	}
	fmt.Fprintf(w, "g1: labels %d/%d, active days %d/%d, max gap %sh, eligible=%s\n",
		g1.Labels, g1.TargetLabels, g1.ActiveDays, g1.TargetDays,
		formatHours(g1.MaxGapHours), yesNo(g1.Eligible))
	fmt.Fprintf(w, "last label age: %s\n", formatAge(g1.LastLabelAgeHours))
	fmt.Fprintf(w, "expected label interval: %s\n", formatInterval(g1.ExpectedLabelIntervalHours))
	fmt.Fprintf(w, "labels due: %s\n", formatDue(g1.LabelsDue))
}

// formatHours renders a fractional hour count without a trailing zero.
func formatHours(h float64) string {
	s := strconv.FormatFloat(h, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// formatAge renders the age of the latest label; -1 means there is none.
func formatAge(h float64) string {
	if h < 0 {
		return "never"
	}
	return formatHours(h) + "h"
}

// formatInterval renders the expected label interval; <= 0 means unknown.
func formatInterval(h float64) string {
	if h <= 0 {
		return "n/a"
	}
	return formatHours(h) + "h"
}

// formatDue renders the labels-due count; -1 means unknown.
func formatDue(n int) string {
	if n < 0 {
		return "unknown"
	}
	return strconv.Itoa(n)
}

// yesNo renders a boolean as the human report does.
func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// g1LabelConfig loads the outcome horizon and signal timeframe for the G1
// block. It is best-effort: a report or status must keep working from the
// journal alone, so a missing or unreadable config only leaves the values
// zero and the G1 block then derives what it can from recorded outcomes.
func (r *runner) g1LabelConfig(g globals) (horizonBars int, timeframe time.Duration) {
	path, err := r.configPath(g.config)
	if err != nil {
		return 0, 0
	}
	cfg, err := config.Load(path)
	if err != nil {
		return 0, 0
	}
	if tf, perr := domain.ParseTimeframe(cfg.Signals.Trend.Timeframe); perr == nil {
		timeframe = tf
	}
	return cfg.Outcome.HorizonBars, timeframe
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
func (r *runner) printShare(rep domain.Report, path string, win reportWindow, dataDir string, force bool) int {
	body := shareMarkdown(rep, r.ver, win)
	if path == "" {
		fmt.Fprint(r.stdout, body)
		return exitOK
	}
	if werr := writeFileNew(path, []byte(body), 0o600, newFileOptions{DataDir: dataDir, Force: force}); werr != nil {
		switch {
		case errors.Is(werr, errOutputExists):
			return r.fail(false, exitUsage, codeUsage, werr.Error(),
				"pass --force to overwrite the file, or choose another path", false, nil)
		case errors.Is(werr, errOutputInDataDir):
			return r.fail(false, exitUsage, codeUsage, werr.Error(),
				"choose an output path outside the data directory", false, nil)
		default:
			return r.fail(false, exitGeneral, codeGeneral, werr.Error(),
				"check the output path is writable and retry", false, nil)
		}
	}
	fmt.Fprintf(r.stdout, "written to %s\n", path)
	return exitOK
}

// journalWindow counts gate requests and the UTC-day span covered by the
// journal acceptance time (event ts), never by the signal payload ts. Any
// request means at least one day; active counts distinct UTC dates.
func journalWindow(ctx context.Context, jr ports.Journal) (reportWindow, error) {
	win := reportWindow{}
	var first, last time.Time
	dates := map[string]struct{}{}
	if err := jr.Walk(ctx, 0, "", func(ev domain.Event) error {
		if ev.Kind != domain.EventGateRequest {
			return nil
		}
		win.decisions++
		ts := ev.TS.UTC()
		if ts.IsZero() {
			return nil
		}
		if first.IsZero() || ts.Before(first) {
			first = ts
		}
		if last.IsZero() || ts.After(last) {
			last = ts
		}
		dates[ts.Format(shareDayLayout)] = struct{}{}
		return nil
	}); err != nil {
		return reportWindow{}, fmt.Errorf("report: walk journal: %w", err)
	}
	if win.decisions == 0 {
		return win, nil
	}
	win.days = 1
	if !first.IsZero() && !last.IsZero() {
		span := int(last.Truncate(24*time.Hour).Sub(first.Truncate(24*time.Hour)).Hours()/24) + 1
		if span > win.days {
			win.days = span
		}
	}
	win.active = len(dates)
	return win, nil
}

// shareMarkdown renders the anonymized summary: aggregate counts, model
// quality metrics and assumptions only. State, instruments, venues, prices,
// return sums, paths and identifiers never appear here.
func shareMarkdown(rep domain.Report, ver string, win reportWindow) string {
	var b strings.Builder
	total := 0
	for _, v := range rep.VerdictCounts {
		total += v
	}
	decisions := win.decisions
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
	fmt.Fprintf(&b, "- days: %d\n", win.days)
	fmt.Fprintf(&b, "- active days: %d\n", win.active)
	fmt.Fprintf(&b, "- labeled outcomes: %d\n", rep.LabeledCount)
	fmt.Fprintf(&b, "- awaiting labeling: %d\n", rep.AwaitingLabeling)
	fmt.Fprintf(&b, "- no outcome by deadline: %d\n", rep.NoOutcomeByDeadline)
	fmt.Fprintf(&b, "- source rejected: %d\n", rep.SourceRejected)
	if g1 := rep.G1; g1 != nil {
		// The share summary carries the same G1 numbers without paths,
		// symbols, prices or identifiers.
		fmt.Fprintf(&b, "- g1 labels: %d/%d\n", g1.Labels, g1.TargetLabels)
		fmt.Fprintf(&b, "- g1 active days: %d/%d\n", g1.ActiveDays, g1.TargetDays)
		fmt.Fprintf(&b, "- g1 max gap hours: %s\n", formatHours(g1.MaxGapHours))
		fmt.Fprintf(&b, "- g1 eligible: %s\n", yesNo(g1.Eligible))
		fmt.Fprintf(&b, "- g1 labels due: %s\n", formatDue(g1.LabelsDue))
		fmt.Fprintf(&b, "- g1 last label age hours: %s\n", formatAge(g1.LastLabelAgeHours))
		fmt.Fprintf(&b, "- g1 expected label interval hours: %s\n", formatInterval(g1.ExpectedLabelIntervalHours))
		if g1.Synthetic {
			fmt.Fprintf(&b, "- g1 synthetic: true (not G1 evidence)\n")
		}
	}
	fmt.Fprintf(&b, "- verdicts: ALLOW=%d REJECT=%d ABSTAIN=%d\n\n",
		rep.VerdictCounts[string(domain.VerdictAllow)],
		rep.VerdictCounts[string(domain.VerdictReject)],
		rep.VerdictCounts[string(domain.VerdictAbstain)])

	if rep.Sufficient && rep.Comparisons != nil {
		c := rep.Comparisons
		fmt.Fprintf(&b, "## Metrics\n\n")
		fmt.Fprintf(&b, "- calibration pairs: %d\n", c.CalibrationCount)
		if !c.CalibrationSufficient {
			fmt.Fprintf(&b, "- metrics: insufficient (%d pairs, need %d)\n",
				c.CalibrationCount, report.MinCalibrationPairs)
		} else {
			fmt.Fprintf(&b, "- Brier score: %.4f\n", c.Brier)
			fmt.Fprintf(&b, "- base rate: %.4f\n", c.BaseRate)
			// One observed class leaves the AUC undefined; a degenerate base
			// rate makes the constant forecast perfect and the skill score
			// undefined.
			if c.BaseRate <= 0 || c.BaseRate >= 1 {
				fmt.Fprintf(&b, "- AUC: n/a\n")
				fmt.Fprintf(&b, "- Brier skill score: n/a\n")
			} else {
				fmt.Fprintf(&b, "- AUC: %.4f\n", c.AUC)
				fmt.Fprintf(&b, "- Brier skill score: %.4f\n", c.BrierSkillScore)
			}
		}
		if d := c.Difference; d != nil {
			fmt.Fprintf(&b, "- ALLOW vs REJECT mean difference: %s\n", d.Mean.String())
			fmt.Fprintf(&b, "- 95%% CI: [%s, %s] (%s)\n",
				d.Low.String(), d.High.String(), zeroWording(d.ContainsZero))
		} else if allow, reject := c.Groups["ALLOW"], c.Groups["REJECT"]; allow.Count > 0 && reject.Count > 0 {
			if allow.Count < report.MinGroupSize || reject.Count < report.MinGroupSize {
				fmt.Fprintf(&b, "- ALLOW vs REJECT mean difference: insufficient group size "+
					"(ALLOW=%d, REJECT=%d, need %d per group)\n",
					allow.Count, reject.Count, report.MinGroupSize)
			} else {
				fmt.Fprintf(&b, "- ALLOW vs REJECT mean difference: insufficient days "+
					"for a 95%% interval (need %d)\n", report.MinBootstrapDays)
			}
		} else {
			fmt.Fprintf(&b, "- ALLOW vs REJECT mean difference: insufficient data\n")
		}
		fmt.Fprintf(&b, "\n")
	} else {
		fmt.Fprintf(&b, "## Metrics\n\n")
		fmt.Fprintf(&b, "- calibration pairs: 0\n")
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
