package cli

import "fmt"

func (r *runner) cmdVersion(args []string) int {
	g, rest, err := splitGlobals(args)
	if err != nil {
		return r.fail(false, exitUsage, codeUsage, err.Error(),
			"run vend version [--json]", false, nil)
	}
	fs := r.newFlagSet("version")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(g.json, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend version [--json]", false, nil)
	}
	if g.json {
		r.printJSON(map[string]string{"version": r.ver})
		return exitOK
	}
	fmt.Fprintf(r.stdout, "vend %s\n", r.ver)
	return exitOK
}
