package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/shadow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// shadowStatusName is the status snapshot written by the shadow runner.
const shadowStatusName = "shadow-status.json"

// statusPolicyWidth is how many policy hash characters the human view shows.
const statusPolicyWidth = 12

// cmdStatus prints the shadow gate snapshot: the journal counters plus the
// last status file written by the runner.
func (r *runner) cmdStatus(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend status [--json]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("status")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend status [--json]", false, nil)
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
	st, err := readShadowStatus(filepath.Join(dataDir, shadowStatusName))
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"delete or fix the shadow status file and retry", false, nil)
	}
	if st.Version == "" {
		st.Version = r.ver
	}

	jr, closer, err := r.openJournalAt(dbPath)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	events, labeled, err := countJournal(context.Background(), jr)
	_ = closer.Close()
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	st.Events = events
	st.Labeled = labeled

	if jsonOut {
		r.printJSON(st)
		return exitOK
	}
	policy := st.PolicyHash
	if len(policy) > statusPolicyWidth {
		policy = policy[:statusPolicyWidth]
	}
	if policy == "" {
		policy = "none"
	}
	lastLabel := "never"
	if !st.LastLabelAt.IsZero() {
		lastLabel = st.LastLabelAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	lastError := st.LastCycleError
	if lastError == "" {
		lastError = "none"
	}
	fmt.Fprintf(r.stdout, "version: %s\n", st.Version)
	fmt.Fprintf(r.stdout, "events: %d\n", st.Events)
	fmt.Fprintf(r.stdout, "labeled: %d\n", st.Labeled)
	fmt.Fprintf(r.stdout, "last label: %s\n", lastLabel)
	fmt.Fprintf(r.stdout, "policy: %s\n", policy)
	fmt.Fprintf(r.stdout, "last error: %s\n", lastError)
	return exitOK
}

// readShadowStatus reads the runner status file. A missing file is not an
// error: it means the runner has not started yet.
func readShadowStatus(path string) (shadow.Status, error) {
	var st shadow.Status
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, fmt.Errorf("read shadow status at %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return shadow.Status{}, fmt.Errorf("invalid shadow status at %s: %v", path, firstLine(err))
	}
	return st, nil
}

// countJournal returns the total event count and the labeled-outcome count.
func countJournal(ctx context.Context, jr ports.Journal) (events int64, labeled int, err error) {
	err = jr.Walk(ctx, 0, "", func(ev domain.Event) error {
		events++
		if ev.Kind == domain.EventOutcomeLabeled {
			labeled++
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return events, labeled, nil
}
