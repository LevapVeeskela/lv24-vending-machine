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

// reportNeedLabeled mirrors the report default (Config{} means MinLabeled
// 100) for the human "insufficient data" line.
const reportNeedLabeled = 100

func (r *runner) cmdGateReport(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend gate report [--json]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("gate report")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend gate report [--json]", false, nil)
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
	b, err := report.New(jr, ports.Deps{Clock: r.clock, NewID: r.newID}, report.Config{})
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

// printReportHuman prints the base stats always and the comparison block
// only when the report holds enough labeled outcomes.
func printReportHuman(w io.Writer, rep domain.Report) {
	counts := rep.VerdictCounts
	fmt.Fprintf(w, "verdicts: ALLOW=%d REJECT=%d ABSTAIN=%d SKIP=%d\n",
		counts[string(domain.VerdictAllow)], counts[string(domain.VerdictReject)],
		counts[string(domain.VerdictAbstain)], counts[string(domain.VerdictSkip)])
	fmt.Fprintf(w, "labeled: %d\n", rep.LabeledCount)
	fmt.Fprintf(w, "decision cost: %s USD\n", rep.DecisionCostUSD.String())
	fmt.Fprintf(w, "latency p50/p95: %dms/%dms\n", rep.LatencyP50MS, rep.LatencyP95MS)
	if !rep.Sufficient || rep.Comparisons == nil {
		fmt.Fprintf(w, "insufficient data: %d labeled, need %d\n", rep.LabeledCount, reportNeedLabeled)
		return
	}
	c := rep.Comparisons
	fmt.Fprintf(w, "all entries return: %s\n", c.AllEntriesReturn.String())
	fmt.Fprintf(w, "allow return: %s\n", c.AllowReturn.String())
	fmt.Fprintf(w, "rejected avg return: %s\n", c.RejectedAvgReturn.String())
	fmt.Fprintf(w, "brier: %.6f\n", c.Brier)
	fmt.Fprintf(w, "ece: %.6f\n", c.ECE)
	fmt.Fprintf(w, "reliability:\n")
	for _, b := range c.Reliability {
		fmt.Fprintf(w, "  [%.1f-%.1f): n=%d mean=%.4f observed=%.4f\n",
			b.LowerBound, b.LowerBound+0.1, b.Count, b.MeanPredicted, b.ObservedRate)
	}
	fmt.Fprintf(w, "by model:\n")
	for _, m := range c.ByModel {
		fmt.Fprintf(w, "  %s: n=%d brier=%.6f ece=%.6f net=%s cost=%s USD\n",
			m.Model, m.Count, m.Brier, m.ECE, m.NetReturnSum.String(), m.CostUSD.String())
	}
}
