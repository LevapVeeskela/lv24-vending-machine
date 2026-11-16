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
	"strconv"
	"strings"
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
	// TrendInterval is the trend bar duration. When positive, Run scans the
	// trend source two seconds after each bar close instead of on the label
	// ticker; zero means the trend source is disabled (or has no schedule).
	TrendInterval time.Duration
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
	notifyUnavailable    = "gate.unavailable"
	notifyProviderConfig = "gate.provider_config"
	notifyWeekly         = "gate.weekly"
	streakUnavailable    = "provider_unavailable"
	notifyChannel        = "telegram"
)

// providerUnavailable is the gate verdict code that feeds the outage streak.
const providerUnavailable = "provider_unavailable"

// providerAuth and providerMisconfigured are the gate verdict codes that mean
// the provider needs operator attention now, not after an outage streak.
const (
	providerAuth          = "provider_auth"
	providerMisconfigured = "provider_misconfigured"
)

// trendScanDelay is how long after a bar close the trend scan runs.
const trendScanDelay = 2 * time.Second

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
	// lastSent holds the in-memory send time per rate-limited notification
	// kind. It is a guard against hot loops only, never the source of truth:
	// the weekly schedule is derived from the journal.
	lastSent map[string]time.Time
	// trendOffset is the delay applied after a bar close; tests shorten it.
	trendOffset time.Duration

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

		trendOffset: trendScanDelay,
	}
	// Initialize the fixed persistent keys so debugSize reflects the steady
	// state from the very first step: the provider-unavailable streak counter
	// and the in-memory rate limits. A zero time means "never sent".
	r.streaks[streakUnavailable] = 0
	r.lastSent[notifyUnavailable] = time.Time{}
	r.lastSent[notifyProviderConfig] = time.Time{}
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

// Step runs one full cycle: labels, scans and evaluates, notifies, and writes
// the status. It returns nil: per-phase errors are recorded in the status and
// never stop the loop.
func (r *Runner) Step(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stepPhases(ctx, true, true)
	return nil
}

// Run runs a label cycle immediately and then on every LabelInterval until ctx
// is cancelled. When a trend interval is configured, the trend scan runs on its
// own timer two seconds after each bar close instead of on the label ticker.
// The mutex prevents overlapping cycles; on cancel it waits for an in-flight
// cycle to finish before returning.
func (r *Runner) Run(ctx context.Context) {
	// With no trend schedule the trend source stays on the label ticker, which
	// is the pre-existing behaviour for signal sources without a timeframe.
	trendOnLabel := r.cfg.TrendInterval <= 0
	r.mu.Lock()
	r.stepPhases(ctx, true, trendOnLabel)
	r.mu.Unlock()

	interval := r.cfg.LabelInterval
	if interval <= 0 {
		interval = defaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var trendC <-chan time.Time
	var trendTimer *time.Timer
	if !trendOnLabel {
		trendTimer = time.NewTimer(r.NextTrendDelay(r.deps.Now()))
		defer trendTimer.Stop()
		trendC = trendTimer.C
	}

	for {
		select {
		case <-ctx.Done():
			// Wait for any in-flight cycle (a cycle holds the mutex).
			r.mu.Lock()
			r.mu.Unlock()
			return
		case <-ticker.C:
			r.mu.Lock()
			r.stepPhases(ctx, true, false)
			r.mu.Unlock()
		case <-trendC:
			r.mu.Lock()
			r.stepPhases(ctx, false, true)
			r.mu.Unlock()
			trendTimer.Reset(r.NextTrendDelay(r.deps.Now()))
		}
	}
}

// NextTrendDelay returns the wait until the next trend scan: two seconds after
// the next bar close of the configured trend interval. It returns zero when no
// trend interval is configured.
func (r *Runner) NextTrendDelay(now time.Time) time.Duration {
	return nextTrendDelay(now, r.cfg.TrendInterval, r.trendOffset)
}

// nextTrendDelay aligns now to the bar grid and adds the post-close offset.
func nextTrendDelay(now time.Time, interval, offset time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	fire := now.Truncate(interval).Add(offset)
	if !fire.After(now) {
		fire = fire.Add(interval)
	}
	return fire.Sub(now)
}

// TimeframeInterval parses a candle timeframe (an amount followed by m, h, d
// or w) into the bar duration used to schedule trend scans.
func TimeframeInterval(raw string) (time.Duration, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if len(s) < 2 {
		return 0, fmt.Errorf("timeframe %q must be an amount followed by m, h, d or w", raw)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("timeframe %q must start with a positive integer", raw)
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	case 'w':
		unit = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("timeframe %q unit must be m, h, d or w", raw)
	}
	return time.Duration(n) * unit, nil
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

// stepPhases runs one cycle under the caller-held mutex. doLabel runs the
// outcome labeler; doTrend scans and evaluates the signal source. The label
// ticker and the trend timer each run only their own phase.
func (r *Runner) stepPhases(ctx context.Context, doLabel, doTrend bool) {
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
	if doLabel && r.labeler != nil {
		sum, err := r.labeler.Run(ctx)
		if err != nil {
			record(err)
		} else if sum.Labeled > 0 {
			r.lastLabelAt = r.deps.Now()
		}
	}

	// b) Scan signals and evaluate each one.
	if doTrend && r.signals != nil {
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

// notify walks new journal events, builds the notifications that are due,
// sends at most one of them, and appends a notify.sent event for it.
//
// The cursor (lastSeq) only moves past an event once its notification was
// delivered. A send error therefore leaves the cursor in front of the event so
// the notification is retried later; events that need no notification are
// skipped by advancing the cursor to the first pending event.
func (r *Runner) notify(ctx context.Context) error {
	type pending struct {
		seq     int64
		kind    string
		text    string
		weekly  bool
		rateKey string
	}
	now := r.deps.Now()
	var out []pending
	var notifyErr error

	// The weekly schedule, the outage streak and the trailing provider fault
	// are derived from the journal, so a restarted Runner on the same journal
	// cannot repeat a weekly summary.
	firstRequest, lastWeekly, providerFault, stateErr := r.journalState(ctx)
	if stateErr != nil {
		notifyErr = stateErr
	}

	walkSeq := r.lastSeq
	if err := r.jr.Walk(ctx, r.lastSeq, "", func(ev domain.Event) error {
		if ev.Seq > walkSeq {
			walkSeq = ev.Seq
		}
		if ev.Kind != domain.EventDecisionModelChanged {
			return nil
		}
		var mc domain.ModelChanged
		if err := json.Unmarshal(ev.Payload, &mc); err != nil {
			return nil
		}
		out = append(out, pending{
			seq:  ev.Seq,
			kind: domain.EventDecisionModelChanged,
			text: fmt.Sprintf("decision model changed: %s -> %s (requested %s)",
				mc.PreviousModel, mc.ActualModel, mc.RequestedModel),
		})
		return nil
	}); err != nil {
		notifyErr = err
	}

	// Outage notification: only while the journal ends in a provider outage
	// streak, and at most once per hour (in-memory rate limit).
	if r.streaks[streakUnavailable] >= 3 {
		last := r.lastSent[notifyUnavailable]
		if last.IsZero() || now.Sub(last) >= time.Hour {
			out = append(out, pending{
				seq:     walkSeq,
				kind:    notifyUnavailable,
				text:    "gate unavailable: 3 consecutive ABSTAIN from the decision provider",
				rateKey: notifyUnavailable,
			})
		}
	}

	// Provider configuration attention: an auth or misconfiguration verdict is
	// an operator problem, not an outage streak. At most once per hour across
	// both codes.
	if providerFault != "" {
		last := r.lastSent[notifyProviderConfig]
		if last.IsZero() || now.Sub(last) >= time.Hour {
			out = append(out, pending{
				seq:     walkSeq,
				kind:    notifyProviderConfig,
				text:    providerConfigText(providerFault),
				rateKey: notifyProviderConfig,
			})
		}
	}

	// Weekly summary: first gate request at least seven days ago and no
	// successful weekly notification within the last seven days.
	if r.reporter != nil && weeklyDue(now, firstRequest, lastWeekly) {
		out = append(out, pending{seq: walkSeq, kind: notifyWeekly, weekly: true, rateKey: notifyWeekly})
	}

	if len(out) == 0 {
		// Nothing to send: advance past every event that needed no notification.
		if walkSeq > r.lastSeq {
			r.lastSeq = walkSeq
		}
		return notifyErr
	}

	first := out[0]
	if first.weekly {
		rep, err := r.reporter.Build(ctx)
		if err != nil {
			return err
		}
		first.text = weeklyText(rep)
	}

	sendErr := r.notifier.Send(ctx, ports.Notification{Kind: first.kind, Text: first.text})
	if sendErr != nil {
		notifyErr = sendErr
	}
	if _, appendErr := r.appendNotifySent(ctx, first.kind, sendErr == nil); appendErr != nil {
		notifyErr = appendErr
	}
	if sendErr == nil {
		// Delivered: move the cursor past the source event only. Later events
		// stay pending for the following steps; the notify.sent record is
		// skipped by the walk and passed on the next clean sweep.
		if first.seq > r.lastSeq {
			r.lastSeq = first.seq
		}
		if first.rateKey != "" {
			r.lastSent[first.rateKey] = now
		}
	}
	return notifyErr
}

// journalState derives the notification schedule inputs from the whole journal:
// the trailing provider-unavailable streak, the first gate.request timestamp,
// the timestamp of the latest successful weekly notification, and the trailing
// provider auth/misconfiguration code.
func (r *Runner) journalState(ctx context.Context) (firstRequest, lastWeekly time.Time, providerFault string, err error) {
	streak := 0
	walkErr := r.jr.Walk(ctx, 0, "", func(ev domain.Event) error {
		switch ev.Kind {
		case domain.EventGateRequest:
			if firstRequest.IsZero() {
				firstRequest = ev.TS
			}
		case domain.EventGateVerdict:
			var v domain.GateVerdict
			if json.Unmarshal(ev.Payload, &v) != nil {
				return nil
			}
			switch {
			case v.Verdict == domain.VerdictAbstain && v.Code == providerUnavailable:
				streak++
				providerFault = ""
			case v.Verdict == domain.VerdictAbstain && (v.Code == providerAuth || v.Code == providerMisconfigured):
				streak = 0
				providerFault = v.Code
			default:
				streak = 0
				providerFault = ""
			}
		case domain.EventNotifySent:
			var p notifySentPayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				return nil
			}
			if p.Kind == notifyWeekly && p.OK {
				lastWeekly = ev.TS
			}
		}
		return nil
	})
	if walkErr != nil {
		return time.Time{}, time.Time{}, "", walkErr
	}
	r.streaks[streakUnavailable] = streak
	return firstRequest, lastWeekly, providerFault, nil
}

// providerConfigText renders the operator alert for a provider fault code
// without echoing provider output.
func providerConfigText(code string) string {
	return fmt.Sprintf("gate provider config: %s (check provider credentials and settings)", code)
}

// weeklyDue reports whether a weekly summary is due as of now. The first
// request must be at least seven days old, and any previous weekly must be at
// least seven days old too.
func weeklyDue(now, firstRequest, lastWeekly time.Time) bool {
	if firstRequest.IsZero() {
		return false
	}
	if now.Sub(firstRequest) < 7*24*time.Hour {
		return false
	}
	if lastWeekly.IsZero() {
		return true
	}
	return now.Sub(lastWeekly) >= 7*24*time.Hour
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

// notifySentPayload is the payload of a notify.sent event. It is the journal
// record that drives the weekly schedule after a restart.
type notifySentPayload struct {
	Kind    string `json:"kind"`
	Channel string `json:"channel"`
	OK      bool   `json:"ok"`
}

// appendNotifySent appends one notify.sent event and returns its seq. The
// caller decides whether to advance the notification cursor.
func (r *Runner) appendNotifySent(ctx context.Context, kind string, ok bool) (int64, error) {
	payload, err := json.Marshal(notifySentPayload{Kind: kind, Channel: notifyChannel, OK: ok})
	if err != nil {
		return 0, err
	}
	stored, err := r.jr.Append(ctx, domain.Event{
		Kind:          domain.EventNotifySent,
		TS:            r.deps.Now(),
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       payload,
		CodeVersion:   r.cfg.CodeVersion,
	})
	if err != nil {
		return 0, err
	}
	return stored.Seq, nil
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
