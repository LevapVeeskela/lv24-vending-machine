package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
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
	case "tail":
		return r.cmdJournalTail(args[1:])
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

// journalTailLimitMin and journalTailLimitMax bound --limit.
const (
	journalTailLimitMin = 1
	journalTailLimitMax = 1000
)

// journalTailEntry is one row of `vend journal tail --json`.
type journalTailEntry struct {
	Seq        int64           `json:"seq"`
	TS         time.Time       `json:"ts"`
	Kind       string          `json:"kind"`
	DecisionID string          `json:"decisionId"`
	Payload    json.RawMessage `json:"payload"`
}

// cmdJournalTail prints the last events of the journal, oldest first. It is
// read-only: it does not verify the chain and appends nothing.
func (r *runner) cmdJournalTail(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend journal tail", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("journal tail")
	kind := fs.String("kind", "", "event kind to filter (exact match)")
	limit := fs.Int("limit", 20, "number of events to print")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend journal tail", false, nil)
	}
	if *limit < journalTailLimitMin || *limit > journalTailLimitMax {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("invalid --limit %d: must be between %d and %d",
				*limit, journalTailLimitMin, journalTailLimitMax),
			"pass --limit between 1 and 1000", false, nil)
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
	events := make([]domain.Event, 0, *limit)
	walkErr := jr.Walk(context.Background(), 0, *kind, func(ev domain.Event) error {
		if len(events) == *limit {
			copy(events, events[1:])
			events[len(events)-1] = ev
			return nil
		}
		events = append(events, ev)
		return nil
	})
	_ = closer.Close()
	if walkErr != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, walkErr.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	if jsonOut {
		entries := make([]journalTailEntry, 0, len(events))
		for _, ev := range events {
			entries = append(entries, journalTailEntry{
				Seq:        ev.Seq,
				TS:         ev.TS.UTC(),
				Kind:       ev.Kind,
				DecisionID: ev.DecisionID,
				Payload:    ev.Payload,
			})
		}
		r.printJSON(entries)
		return exitOK
	}
	for _, ev := range events {
		decisionID := ev.DecisionID
		if decisionID == "" {
			decisionID = "-"
		}
		fmt.Fprintf(r.stdout, "%d %s %s %s\n",
			ev.Seq, ev.TS.UTC().Format(time.RFC3339Nano), ev.Kind, decisionID)
	}
	return exitOK
}
