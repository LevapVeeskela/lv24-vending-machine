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

// alertInterval is the minimum time between two deliveries of the same
// journal-derived alert series.
const alertInterval = time.Hour

// retryCap bounds the exponential retry delay of an undelivered notification.
const retryCap = time.Hour

// notification is one pending message before it is rendered.
type notification struct {
	// seq is the journal seq the message covers; the cursor advances to it
	// only after a successful send.
	seq int64
	// kind is the notify.sent kind (and the Telegram message class).
	kind string
	// code is the gate verdict code of a provider alert, empty otherwise.
	code string
	// uptoSeq is the highest verdict seq covered by a journal-derived alert.
	uptoSeq int64
	// text is the rendered message.
	text string
	// weekly requests rendering by the reporter before the send.
	weekly bool
}

// retryState tracks the exponential retry of one undelivered notification.
type retryState struct {
	// failures is the number of consecutive failed sends.
	failures int
	// next is the earliest time of the next attempt.
	next time.Time
}

// deliveredAlert is the last successful delivery of one notification series,
// restored from its notify.sent event.
type deliveredAlert struct {
	at      time.Time
	uptoSeq int64
}

// journalNotifications is the notification schedule derived from the journal.
// It is rebuilt on every cycle, so a restart on the same journal cannot repeat
// a delivered alert.
type journalNotifications struct {
	firstRequest time.Time
	lastWeekly   time.Time
	latestSeq    int64
	// streak counts trailing provider_unavailable ABSTAINs; streakSeq is the
	// seq of the latest one.
	streak    int
	streakSeq int64
	// faults maps provider_auth/provider_misconfigured to the seq of the
	// latest verdict with that code.
	faults map[string]int64
	// delivered maps a notification key to its last successful delivery.
	delivered map[string]deliveredAlert
}

// notifyKey identifies one notification series for retries and delivery.
func notifyKey(kind, code string) string {
	if code == "" {
		return kind
	}
	return kind + "\x00" + code
}

// alertNeeded reports whether a journal-derived alert series is due: there is
// a verdict newer than the last delivered one and an hour passed since that
// delivery. A never-delivered series is due immediately.
func alertNeeded(now time.Time, last deliveredAlert, latestSeq int64) bool {
	if latestSeq <= last.uptoSeq {
		return false
	}
	return last.at.IsZero() || now.Sub(last.at) >= alertInterval
}

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
	// retry holds the exponential retry state of undelivered notifications,
	// keyed by notifyKey. It stays bounded by the fixed set of alert series.
	retry map[string]retryState
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
		retry:     make(map[string]retryState),

		trendOffset: trendScanDelay,
	}
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

// debugSize returns the total entries of the persistent maps: the retry
// state. It must stay bounded across cycles by the fixed set of notification
// series.
func (r *Runner) debugSize() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.retry)
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
// Every alert series is derived from the journal: a provider alert is due only
// when a verdict newer than the last delivered alert exists and an hour passed
// since that delivery, so a restart repeats nothing and a quiet journal stays
// quiet. The cursor (lastSeq) only moves past a model-changed event once its
// notification was delivered; an undelivered notification retries with a
// 1, 2, 4 ... cycle backoff capped at one hour. Only the first failure of a
// series is recorded as notify.sent with ok=false.
func (r *Runner) notify(ctx context.Context) error {
	now := r.deps.Now()
	var out []notification
	var notifyErr error

	state, stateErr := r.journalState(ctx)
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
		out = append(out, notification{
			seq:  ev.Seq,
			kind: domain.EventDecisionModelChanged,
			text: fmt.Sprintf("decision model changed: %s -> %s (requested %s)",
				mc.PreviousModel, mc.ActualModel, mc.RequestedModel),
		})
		return nil
	}); err != nil {
		notifyErr = err
	}
	head := state.latestSeq
	if walkSeq > head {
		head = walkSeq
	}

	// Outage notification: the journal must end in a provider_unavailable
	// ABSTAIN streak, and the latest verdict of that streak must be newer than
	// the delivered alert. A quiet journal never repeats it.
	if state.streak >= 3 &&
		alertNeeded(now, state.delivered[notifyKey(notifyUnavailable, providerUnavailable)], state.streakSeq) {
		out = append(out, notification{
			seq:     head,
			kind:    notifyUnavailable,
			code:    providerUnavailable,
			uptoSeq: state.streakSeq,
			text:    "gate unavailable: 3 consecutive ABSTAIN from the decision provider",
		})
	}

	// Provider configuration attention: auth and misconfiguration are separate
	// operator problems with separate delivery clocks, so an auth failure in
	// the hour after a configuration alert is still delivered.
	for _, code := range []string{providerAuth, providerMisconfigured} {
		seq := state.faults[code]
		if seq == 0 {
			continue
		}
		if !alertNeeded(now, state.delivered[notifyKey(notifyProviderConfig, code)], seq) {
			continue
		}
		out = append(out, notification{
			seq:     head,
			kind:    notifyProviderConfig,
			code:    code,
			uptoSeq: seq,
			text:    providerConfigText(code),
		})
	}

	// Weekly summary: first gate request at least seven days ago and no
	// successful weekly notification within the last seven days.
	if r.reporter != nil && weeklyDue(now, state.firstRequest, state.lastWeekly) {
		out = append(out, notification{seq: head, kind: notifyWeekly, weekly: true})
	}

	if len(out) == 0 {
		// Nothing to send: advance past every event that needed no notification.
		if walkSeq > r.lastSeq {
			r.lastSeq = walkSeq
		}
		return notifyErr
	}

	first := out[0]
	key := notifyKey(first.kind, first.code)
	if !r.sendDue(key, now) {
		// The first pending notification is inside its retry backoff. Keeping
		// it first preserves the journal order and the cursor.
		return notifyErr
	}
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
		if firstFailure := r.recordSendFailure(key, now); firstFailure {
			if _, appendErr := r.appendNotifySent(ctx, first, false); appendErr != nil {
				notifyErr = appendErr
			}
		}
		return notifyErr
	}
	if _, appendErr := r.appendNotifySent(ctx, first, true); appendErr != nil {
		notifyErr = appendErr
	}
	r.resetSendFailure(key)
	// Delivered: move the cursor past the source event only. Later events stay
	// pending for the following steps; the notify.sent record is skipped by the
	// walk and passed on the next clean sweep.
	if first.seq > r.lastSeq {
		r.lastSeq = first.seq
	}
	return notifyErr
}

// sendDue reports whether the notification series may be attempted now.
func (r *Runner) sendDue(key string, now time.Time) bool {
	st, ok := r.retry[key]
	return !ok || !now.Before(st.next)
}

// recordSendFailure schedules the next retry of a failed series: one label
// cycle after the first failure, doubling per consecutive failure, capped at
// one hour so a retry happens at least hourly. It reports whether this was the
// first failure of the series.
func (r *Runner) recordSendFailure(key string, now time.Time) bool {
	st := r.retry[key]
	first := st.failures == 0
	st.failures++
	interval := r.cfg.LabelInterval
	if interval <= 0 {
		interval = defaultInterval
	}
	delay := interval
	for i := 1; i < st.failures; i++ {
		if delay >= retryCap {
			break
		}
		delay *= 2
	}
	if delay > retryCap {
		delay = retryCap
	}
	st.next = now.Add(delay)
	r.retry[key] = st
	return first
}

// resetSendFailure forgets the retry state of a delivered series.
func (r *Runner) resetSendFailure(key string) {
	delete(r.retry, key)
}

// journalState derives the notification schedule inputs from the whole journal:
// the trailing provider-unavailable streak, the first gate.request timestamp,
// the timestamp of the latest successful weekly notification, the latest
// verdict seq per provider fault code, and the delivery record of every alert
// series.
func (r *Runner) journalState(ctx context.Context) (journalNotifications, error) {
	st := journalNotifications{
		faults:    make(map[string]int64),
		delivered: make(map[string]deliveredAlert),
	}
	err := r.jr.Walk(ctx, 0, "", func(ev domain.Event) error {
		if ev.Seq > st.latestSeq {
			st.latestSeq = ev.Seq
		}
		switch ev.Kind {
		case domain.EventGateRequest:
			if st.firstRequest.IsZero() {
				st.firstRequest = ev.TS
			}
		case domain.EventGateVerdict:
			var v domain.GateVerdict
			if json.Unmarshal(ev.Payload, &v) != nil {
				return nil
			}
			switch {
			case v.Verdict == domain.VerdictAbstain && v.Code == providerUnavailable:
				st.streak++
				st.streakSeq = ev.Seq
			case v.Verdict == domain.VerdictAbstain && (v.Code == providerAuth || v.Code == providerMisconfigured):
				st.streak = 0
				if ev.Seq > st.faults[v.Code] {
					st.faults[v.Code] = ev.Seq
				}
			default:
				st.streak = 0
			}
		case domain.EventNotifySent:
			var p notifySentPayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				return nil
			}
			if !p.OK {
				return nil
			}
			if p.Kind == notifyWeekly {
				st.lastWeekly = ev.TS
			}
			st.delivered[notifyKey(p.Kind, p.Code)] = deliveredAlert{at: ev.TS, uptoSeq: p.UptoSeq}
		}
		return nil
	})
	if err != nil {
		return journalNotifications{}, err
	}
	return st, nil
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
// record that drives the alert and weekly schedules after a restart.
type notifySentPayload struct {
	Kind    string `json:"kind"`
	Channel string `json:"channel"`
	OK      bool   `json:"ok"`
	// Code is the gate verdict code covered by a provider alert.
	Code string `json:"code,omitempty"`
	// UptoSeq is the highest journal seq covered by a delivered alert.
	UptoSeq int64 `json:"uptoSeq,omitempty"`
}

// appendNotifySent appends one notify.sent event and returns its seq. The
// caller decides whether to advance the notification cursor. A failed attempt
// carries no uptoSeq: it covers nothing.
func (r *Runner) appendNotifySent(ctx context.Context, n notification, ok bool) (int64, error) {
	p := notifySentPayload{Kind: n.kind, Channel: notifyChannel, OK: ok, Code: n.code}
	if ok {
		p.UptoSeq = n.uptoSeq
	}
	payload, err := json.Marshal(p)
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
