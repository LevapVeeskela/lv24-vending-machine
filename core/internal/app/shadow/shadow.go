// Package shadow runs the unattended shadow gate loop: it labels outcomes,
// scans for new signals, evaluates them, and pushes sanitized notifications
// for operator-visible events. It never touches the network itself and never
// stops on a per-cycle error: every failure is recorded in the status file.
package shadow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/gateflow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/outcome"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// Labeler labels gate entry signals with forward returns.
type Labeler interface {
	Run(context.Context) (outcome.Summary, error)
}

// SignalSource produces gate requests to evaluate.
type SignalSource interface {
	Scan(context.Context) ([]domain.GateRequest, error)
}

// Evaluator runs one gate request through the decision flow.
type Evaluator interface {
	Evaluate(context.Context, domain.GateRequest) (gateflow.Result, error)
}

// Reporter builds a shadow-mode report from the journal.
type Reporter interface {
	Build(context.Context) (domain.Report, error)
}

// Config tunes one shadow Runner.
type Config struct {
	// LabelInterval is the period between cycles; <=0 means 5m.
	LabelInterval time.Duration
	// StatusPath is the file the status JSON is atomically written to.
	StatusPath string
	// CodeVersion is stamped on the status and every notify.sent event.
	CodeVersion string
}

// Status is the operator-facing snapshot of the shadow gate.
type Status struct {
	Version        string    `json:"version"`
	PolicyHash     string    `json:"policyHash"`
	Events         int64     `json:"events"`
	Labeled        int       `json:"labeled"`
	LastLabelAt    time.Time `json:"lastLabelAt,omitzero"`
	LastCycleError string    `json:"lastCycleError,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
	// LastSeq is the highest journal seq whose notifications were processed;
	// it is restored on restart so already-notified events are not re-sent.
	LastSeq int64 `json:"lastSeq,omitzero"`
}

// defaultInterval is the cycle period applied when Config.LabelInterval is
// not positive.
const defaultInterval = 5 * time.Minute

// Notification kinds and streak keys.
const (
	notifyUnavailable = "gate.unavailable"
	notifyWeekly      = "gate.weekly"
	streakUnavailable = "provider_unavailable"
	notifyChannel     = "telegram"
)

// providerUnavailable is the gate verdict code that feeds the outage streak.
const providerUnavailable = "provider_unavailable"

// Runner drives the shadow gate loop.
type Runner struct {
	jr        ports.Journal
	labeler   Labeler
	signals   SignalSource
	evaluator Evaluator
	reporter  Reporter
	notifier  ports.Notifier
	deps      ports.Deps
	cfg       Config

	mu sync.Mutex
	// lastSeq is the highest journal seq already processed for notifications.
	lastSeq int64
	// streaks holds bounded consecutive-event counters keyed by metric.
	streaks map[string]int
	// lastSent holds the last send time per rate-limited notification kind.
	lastSent map[string]time.Time

	// Status snapshot fields, assembled under mu.
	events         int64
	labeled        int
	lastLabelAt    time.Time
	lastCycleError string
	policyHash     string
}

// New builds a Runner. A nil notifier disables notifications; a nil reporter
// disables the weekly summary. The remaining collaborators may be nil and are
// then skipped in Step.
func New(jr ports.Journal, labeler Labeler, signals SignalSource, evaluator Evaluator, reporter Reporter, notifier ports.Notifier, deps ports.Deps, cfg Config) (*Runner, error) {
	if jr == nil {
		return nil, fmt.Errorf("shadow: nil journal")
	}
	r := &Runner{
		jr:        jr,
		labeler:   labeler,
		signals:   signals,
		evaluator: evaluator,
		reporter:  reporter,
		notifier:  notifier,
		deps:      deps,
		cfg:       cfg,
		streaks:   make(map[string]int),
		lastSent:  make(map[string]time.Time),
	}
	// Initialize the fixed persistent keys so debugSize reflects the steady
	// state from the very first step: the provider-unavailable streak counter
	// and the weekly rate-limit timestamp. A zero time still means "never
	// sent", so the weekly check is unchanged.
	r.streaks[streakUnavailable] = 0
	r.lastSent[notifyWeekly] = time.Time{}
	// Restore the notification cursor from a previous run so notifications
	// only process events appended after the last persisted seq. A missing or
	// corrupt status file leaves the cursor at zero (reprocess from scratch).
	if cfg.StatusPath != "" {
		if raw, err := os.ReadFile(cfg.StatusPath); err == nil {
			var st Status
			if json.Unmarshal(raw, &st) == nil {
				r.lastSeq = st.LastSeq
			}
		}
	}
	return r, nil
}

// Step runs one full cycle. It returns nil: per-phase errors are recorded in
// the status and never stop the loop.
func (r *Runner) Step(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.step(ctx)
	return nil
}

// Run steps immediately and then on every LabelInterval until ctx is
// cancelled. The mutex prevents overlapping steps; on cancel it waits for an
// in-flight step to finish before returning.
func (r *Runner) Run(ctx context.Context) {
	r.Step(ctx)
	interval := r.cfg.LabelInterval
	if interval <= 0 {
		interval = defaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Wait for any in-flight step (Step holds the mutex).
			r.mu.Lock()
			r.mu.Unlock()
			return
		case <-ticker.C:
			r.Step(ctx)
		}
	}
}

// Status returns a snapshot of the last written status.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Status{
		Version:        r.cfg.CodeVersion,
		PolicyHash:     r.policyHash,
		Events:         r.events,
		Labeled:        r.labeled,
		LastLabelAt:    r.lastLabelAt,
		LastCycleError: r.lastCycleError,
		UpdatedAt:      r.lastUpdated(),
		LastSeq:        r.lastSeq,
	}
}

// debugSize returns the total entries of the persistent maps: the streak
// counters plus the rate-limit state. It must stay bounded across cycles.
func (r *Runner) debugSize() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.streaks) + len(r.lastSent)
}

// step runs one cycle under the caller-held mutex.
func (r *Runner) step(ctx context.Context) {
	var cycleErr string
	record := func(err error) {
		if err == nil {
			return
		}
		s := sanitizeErr(err)
		cycleErr = s
		if r.deps.Logger != nil {
			r.deps.Logger.Warn("shadow: step error", "err", s)
		}
	}

	// a) Label outcomes.
	if r.labeler != nil {
		sum, err := r.labeler.Run(ctx)
		if err != nil {
			record(err)
		} else if sum.Labeled > 0 {
			r.lastLabelAt = r.deps.Now()
		}
	}

	// b) Scan signals and evaluate each one.
	if r.signals != nil {
		reqs, err := r.signals.Scan(ctx)
		if err != nil {
			record(err)
		} else if r.evaluator != nil {
			for _, req := range reqs {
				// On shutdown, stop starting new (up to 20s) evaluations; the
				// in-flight Evaluate still finishes.
				if ctx.Err() != nil {
					break
				}
				if _, err := r.evaluator.Evaluate(ctx, req); err != nil {
					record(err)
				}
			}
		}
	}

	// c) Notifications.
	if r.notifier != nil {
		if err := r.notify(ctx); err != nil {
			record(err)
		}
	}

	// d) Status file.
	if err := r.writeStatus(ctx); err != nil {
		record(err)
	}

	r.lastCycleError = cycleErr
}

// notify walks new journal events, collects sanitized notifications, sends
// them, and appends one notify.sent event per notification.
func (r *Runner) notify(ctx context.Context) error {
	type pending struct {
		kind string
		text string
	}
	var out []pending
	var notifyErr error
	now := r.deps.Now()

	err := r.jr.Walk(ctx, r.lastSeq, "", func(ev domain.Event) error {
		if ev.Seq > r.lastSeq {
			r.lastSeq = ev.Seq
		}
		switch ev.Kind {
		case domain.EventDecisionModelChanged:
			var mc domain.ModelChanged
			if err := json.Unmarshal(ev.Payload, &mc); err != nil {
				return nil
			}
			out = append(out, pending{
				kind: domain.EventDecisionModelChanged,
				text: fmt.Sprintf("decision model changed: %s -> %s (requested %s)",
					mc.PreviousModel, mc.ActualModel, mc.RequestedModel),
			})
		case domain.EventGateVerdict:
			var v domain.GateVerdict
			if err := json.Unmarshal(ev.Payload, &v); err != nil {
				return nil
			}
			if v.Verdict == domain.VerdictAbstain && v.Code == providerUnavailable {
				r.streaks[streakUnavailable]++
				if r.streaks[streakUnavailable] >= 3 {
					last := r.lastSent[notifyUnavailable]
					if last.IsZero() || now.Sub(last) >= time.Hour {
						out = append(out, pending{
							kind: notifyUnavailable,
							text: "gate unavailable: 3 consecutive ABSTAIN from the decision provider",
						})
						r.lastSent[notifyUnavailable] = now
					}
				}
			} else {
				r.streaks[streakUnavailable] = 0
			}
		}
		return nil
	})
	if err != nil {
		notifyErr = err
	}

	// Weekly summary.
	if r.reporter != nil && r.weeklyDue(now) {
		rep, err := r.reporter.Build(ctx)
		if err != nil {
			notifyErr = err
		} else {
			last := r.lastSent[notifyWeekly]
			if !last.IsZero() || rep.LabeledCount > 0 {
				out = append(out, pending{kind: notifyWeekly, text: weeklyText(rep)})
				r.lastSent[notifyWeekly] = now
			}
		}
	}

	// Send outside the walk, then append one notify.sent per notification.
	for _, p := range out {
		ok := true
		if err := r.notifier.Send(ctx, ports.Notification{Kind: p.kind, Text: p.text}); err != nil {
			ok = false
			notifyErr = err
		}
		if err := r.appendNotifySent(ctx, p.kind, ok); err != nil {
			notifyErr = err
		}
	}
	return notifyErr
}

// weeklyDue reports whether a weekly summary is due as of now.
func (r *Runner) weeklyDue(now time.Time) bool {
	last := r.lastSent[notifyWeekly]
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= 7*24*time.Hour
}

// weeklyText renders a weekly summary with counts only: no sums, symbols,
// decision ids or prices. decisions is the number of gated decisions
// (ALLOW+REJECT+ABSTAIN); SKIP bypasses are not decisions.
func weeklyText(rep domain.Report) string {
	allow := rep.VerdictCounts[string(domain.VerdictAllow)]
	reject := rep.VerdictCounts[string(domain.VerdictReject)]
	abstain := rep.VerdictCounts[string(domain.VerdictAbstain)]
	return fmt.Sprintf("weekly gate summary: decisions=%d, labeled=%d, ALLOW=%d, REJECT=%d, ABSTAIN=%d",
		allow+reject+abstain,
		rep.LabeledCount,
		allow,
		reject,
		abstain)
}

// appendNotifySent appends one notify.sent event.
func (r *Runner) appendNotifySent(ctx context.Context, kind string, ok bool) error {
	payload, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Channel string `json:"channel"`
		OK      bool   `json:"ok"`
	}{Kind: kind, Channel: notifyChannel, OK: ok})
	if err != nil {
		return err
	}
	stored, err := r.jr.Append(ctx, domain.Event{
		Kind:          domain.EventNotifySent,
		TS:            r.deps.Now(),
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       payload,
		CodeVersion:   r.cfg.CodeVersion,
	})
	if err != nil {
		return err
	}
	if stored.Seq > r.lastSeq {
		r.lastSeq = stored.Seq
	}
	return nil
}

// writeStatus derives the status from the journal and writes it atomically.
func (r *Runner) writeStatus(ctx context.Context) error {
	now := r.deps.Now()
	var labeled int
	var policyHash string
	var bestSeq int64 = -1
	if err := r.jr.Walk(ctx, 0, "", func(ev domain.Event) error {
		switch ev.Kind {
		case domain.EventOutcomeLabeled:
			labeled++
		case domain.EventGateVerdict:
			if ev.Seq > bestSeq {
				var v domain.GateVerdict
				// The highest-seq verdict wins even when its policy hash is
				// empty or its payload is corrupt: the result is then empty.
				if err := json.Unmarshal(ev.Payload, &v); err == nil {
					policyHash = v.PolicyHash
				} else {
					policyHash = ""
				}
				bestSeq = ev.Seq
			}
		}
		return nil
	}); err != nil {
		return err
	}

	head, ok, err := r.jr.Head(ctx)
	if err != nil {
		return err
	}
	var events int64
	if ok {
		events = head.Seq
	}

	st := Status{
		Version:        r.cfg.CodeVersion,
		PolicyHash:     policyHash,
		Events:         events,
		Labeled:        labeled,
		LastLabelAt:    r.lastLabelAt,
		LastCycleError: r.lastCycleError,
		UpdatedAt:      now,
		LastSeq:        r.lastSeq,
	}

	// Persist the snapshot fields even if the file write fails.
	r.events = st.Events
	r.labeled = st.Labeled
	r.policyHash = st.PolicyHash

	if r.cfg.StatusPath == "" {
		return nil
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeAtomic(r.cfg.StatusPath, raw)
}

// lastUpdated returns the current deps clock for Status(), without holding a
// separate lock beyond the caller's.
func (r *Runner) lastUpdated() time.Time {
	return r.deps.Now()
}

// writeAtomic writes data to path via a temp file and rename with 0600 mode.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".status-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// sanitizeErr renders err without ever leaking a wrapped cause. A *domain.Error
// already drops its cause; any other error is reduced to its code.
func sanitizeErr(err error) string {
	if err == nil {
		return ""
	}
	var de *domain.Error
	if errors.As(err, &de) && de != nil {
		return de.Error()
	}
	return string(domain.CodeOf(err))
}
