package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/report"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/shadow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// shadowStatusName is the status snapshot written by the shadow runner.
const shadowStatusName = "shadow-status.json"

// statusPolicyWidth is how many policy hash characters the human view shows.
const statusPolicyWidth = 12

// statusView is the JSON shape of `vend status --json`. It mirrors
// shadow.Status but keeps zero timestamps and the empty policy hash out of the
// output: before the first `vend serve` there is no snapshot, and scripts must
// not read "0001-01-01T00:00:00Z" as a real time.
type statusView struct {
	Version        string           `json:"version"`
	PolicyHash     string           `json:"policyHash,omitempty"`
	Events         int64            `json:"events"`
	Labeled        int              `json:"labeled"`
	LastLabelAt    *time.Time       `json:"lastLabelAt,omitempty"`
	LastCycleError string           `json:"lastCycleError,omitempty"`
	UpdatedAt      *time.Time       `json:"updatedAt,omitempty"`
	LastSeq        int64            `json:"lastSeq,omitempty"`
	G1             *domain.ReportG1 `json:"g1,omitempty"`
	Synthetic      bool             `json:"synthetic,omitempty"`
}

// newStatusView converts a runner snapshot and the journal-derived G1 block
// to the JSON view.
func newStatusView(st shadow.Status, g1 *domain.ReportG1) statusView {
	v := statusView{
		Version:        st.Version,
		PolicyHash:     st.PolicyHash,
		Events:         st.Events,
		Labeled:        st.Labeled,
		LastCycleError: st.LastCycleError,
		LastSeq:        st.LastSeq,
		G1:             g1,
	}
	if g1 != nil {
		v.Synthetic = g1.Synthetic
	}
	if !st.LastLabelAt.IsZero() {
		label := st.LastLabelAt.UTC()
		v.LastLabelAt = &label
	}
	if !st.UpdatedAt.IsZero() {
		updated := st.UpdatedAt.UTC()
		v.UpdatedAt = &updated
	}
	return v
}

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
	ctx := context.Background()
	events, labeled, err := countJournal(ctx, jr)
	if err != nil {
		_ = closer.Close()
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	// The G1 block is journal-derived, never a shadow-status claim.
	horizonBars, timeframe := r.g1LabelConfig(g)
	b, err := report.New(jr, ports.Deps{Clock: r.clock, NewID: r.newID}, report.Config{
		HorizonBars: horizonBars,
		Timeframe:   timeframe,
	})
	if err != nil {
		_ = closer.Close()
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the vend wiring and retry", false, nil)
	}
	rep, err := b.Build(ctx)
	_ = closer.Close()
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	st.Events = events
	st.Labeled = labeled
	g1 := rep.G1

	if jsonOut {
		r.printJSON(newStatusView(st, g1))
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
	printG1Human(r.stdout, g1)
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
	body := trimBOM(raw)
	if len(body) == 0 && len(raw) > 0 {
		return shadow.Status{}, fmt.Errorf("invalid shadow status at %s: file has a UTF-8 BOM but no JSON content", path)
	}
	if err := json.Unmarshal(body, &st); err != nil {
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
