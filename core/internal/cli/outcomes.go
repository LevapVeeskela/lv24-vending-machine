package cli

import "fmt"

func (r *runner) cmdOutcomes(args []string) int {
	if len(args) < 1 {
		r.fail(false, exitUsage, codeUsage, "missing outcomes subcommand",
			"run vend outcomes label [--candles FILE]", false, nil)
		return exitUsage
	}
	switch args[0] {
	case "label":
		return r.cmdOutcomesLabel(args[1:])
	default:
		r.fail(false, exitUsage, codeUsage,
			fmt.Sprintf("unknown outcomes subcommand %q", args[0]),
			"run vend outcomes label [--candles FILE]", false, nil)
		return exitUsage
	}
}

func (r *runner) cmdOutcomesLabel(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend outcomes label [--candles FILE]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("outcomes label")
	candles := fs.String("candles", "", "candles JSON file")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend outcomes label [--candles FILE]", false, nil)
	}
	_ = *candles
	return r.fail(jsonOut, exitUsage, codeUsage,
		"outcomes label: not implemented yet",
		"outcome labeling lands in wave 3; use vend gate test for now", false, nil)
}
