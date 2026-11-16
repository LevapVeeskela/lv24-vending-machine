package cli

import "fmt"

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
	return r.fail(jsonOut, exitUsage, codeUsage,
		"gate report: not implemented yet",
		"the shadow report lands in wave 3; use vend gate test for now", false, nil)
}
