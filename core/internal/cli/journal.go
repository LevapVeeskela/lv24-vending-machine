package cli

import (
	"context"
	"fmt"

	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
)

func (r *runner) cmdJournal(args []string) int {
	if len(args) < 1 {
		r.fail(false, exitUsage, codeUsage, "missing journal subcommand",
			"run vend journal verify", false, nil)
		return exitUsage
	}
	switch args[0] {
	case "verify":
		return r.cmdJournalVerify(args[1:])
	case "head":
		return r.cmdJournalHead(args[1:])
	default:
		r.fail(false, exitUsage, codeUsage,
			fmt.Sprintf("unknown journal subcommand %q", args[0]),
			"run vend journal verify", false, nil)
		return exitUsage
	}
}

func (r *runner) cmdJournalVerify(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend journal verify", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("journal verify")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend journal verify", false, nil)
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
	res, err := jr.Verify(ctx)
	_ = closer.Close()
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	if !res.OK {
		details := map[string]any{"seq": res.BrokenSeq, "reason": res.Reason}
		return r.fail(jsonOut, exitGeneral, codeGeneral,
			fmt.Sprintf("journal chain broken at seq %d: %s", res.BrokenSeq, res.Reason),
			"restore the database from backup; the journal is append-only", false, details)
	}
	if jsonOut {
		r.printJSON(map[string]any{"ok": true, "count": res.Count})
		return exitOK
	}
	fmt.Fprintf(r.stdout, "journal ok: %d events\n", res.Count)
	return exitOK
}

func (r *runner) cmdJournalHead(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend journal head", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("journal head")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend journal head", false, nil)
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
	jr, closer, err := r.openJournalAt(dbPath)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	head, ok, err := jr.Head(context.Background())
	_ = closer.Close()
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	if !ok {
		if jsonOut {
			r.printJSON(map[string]any{"empty": true})
			return exitOK
		}
		fmt.Fprintln(r.stdout, "journal empty")
		return exitOK
	}
	if jsonOut {
		r.printJSON(map[string]any{"seq": head.Seq, "hash": head.Hash})
		return exitOK
	}
	fmt.Fprintf(r.stdout, "seq: %d\n", head.Seq)
	fmt.Fprintf(r.stdout, "hash: %s\n", head.Hash)
	return exitOK
}
