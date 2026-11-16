package cli

import "fmt"

func (r *runner) cmdServe(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend serve [--addr 127.0.0.1:8420]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("serve")
	addr := fs.String("addr", "127.0.0.1:8420", "listen address")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend serve [--addr 127.0.0.1:8420]", false, nil)
	}
	_ = *addr
	return r.fail(jsonOut, exitUsage, codeUsage,
		"serve: not implemented yet",
		"the HTTP server lands in wave 3; use vend gate test for now", false, nil)
}
