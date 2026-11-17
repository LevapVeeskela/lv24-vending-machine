package shadow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/gateflow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/outcome"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

var baseTime = time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC)

func fixedDeps() ports.Deps {
	return ports.Deps{Clock: func() time.Time { return baseTime }}
}

func mutableDeps(now *time.Time) ports.Deps {
	return ports.Deps{Clock: func() time.Time { return *now }}
}

// memJournal is an in-memory ports.Journal for offline tests.
type memJournal struct {
	mu     sync.Mutex
	events []domain.Event
}

func (m *memJournal) Append(_ context.Context, ev domain.Event) (domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev.Seq = int64(len(m.events)) + 1
	if ev.EventID == "" {
		ev.EventID = "evt-test-" + itoa(len(m.events)+1)
	}
	m.events = append(m.events, ev)
	return ev, nil
}

func (m *memJournal) Query(_ context.Context, from int64, kind string) ([]domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Event
	for _, ev := range m.events {
		if ev.Seq > from && (kind == "" || ev.Kind == kind) {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (m *memJournal) Walk(_ context.Context, from int64, kind string, fn func(domain.Event) error) error {
	evs, err := m.Query(context.Background(), from, kind)
	if err != nil {
		return err
	}
	for _, ev := range evs {
		if err := fn(ev); err != nil {
			return err
		}
	}
	return nil
}

func (m *memJournal) Head(_ context.Context) (domain.Event, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return domain.Event{}, false, nil
	}
	return m.events[len(m.events)-1], true, nil
}

func (m *memJournal) Verify(_ context.Context) (domain.VerifyResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return domain.VerifyResult{OK: true, Count: int64(len(m.events))}, nil
}

func (m *memJournal) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

func (m *memJournal) kinds() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.events))
	for _, ev := range m.events {
		out = append(out, ev.Kind)
	}
	return out
}

func (m *memJournal) eventsOf(kind string) []domain.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Event
	for _, ev := range m.events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ---- fakes ----

type fakeLabeler struct {
	calls int
	order *[]string
	sum   outcome.Summary
	err   error
}

func (f *fakeLabeler) Run(context.Context) (outcome.Summary, error) {
	if f.order != nil {
		*f.order = append(*f.order, "labeler")
	}
	f.calls++
	return f.sum, f.err
}

type fakeSignals struct {
	reqs  []domain.GateRequest
	err   error
	order *[]string
}

func (f *fakeSignals) Scan(context.Context) ([]domain.GateRequest, error) {
	if f.order != nil {
		*f.order = append(*f.order, "signals")
	}
	return f.reqs, f.err
}

type fakeEvaluator struct {
	order *[]string
	calls int
	err   error
}

func (f *fakeEvaluator) Evaluate(context.Context, domain.GateRequest) (gateflow.Result, error) {
	if f.order != nil {
		*f.order = append(*f.order, "evaluator")
	}
	f.calls++
	return gateflow.Result{}, f.err
}

type fakeReporter struct {
	rep domain.Report
	err error
}

func (f *fakeReporter) Build(context.Context) (domain.Report, error) {
	return f.rep, f.err
}

type fakeNotifier struct {
	mu    sync.Mutex
	sends []ports.Notification
	err   error
}

func (f *fakeNotifier) Send(_ context.Context, n ports.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, n)
	return f.err
}

func (f *fakeNotifier) sent() []ports.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ports.Notification(nil), f.sends...)
}

func seedEvent(t *testing.T, jr *memJournal, kind string, payload any) {
	t.Helper()
	seedEventAt(t, jr, kind, baseTime, payload)
}

// seedEventAt appends a fixture event with an explicit timestamp.
func seedEventAt(t *testing.T, jr *memJournal, kind string, ts time.Time, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s: %v", kind, err)
	}
	if _, err := jr.Append(context.Background(), domain.Event{
		Kind:          kind,
		TS:            ts,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       raw,
		CodeVersion:   "v-test",
	}); err != nil {
		t.Fatalf("seed %s: %v", kind, err)
	}
}

// seedOldGateRequest seeds the first gate request that starts the weekly clock.
func seedOldGateRequest(t *testing.T, jr *memJournal, ts time.Time) {
	t.Helper()
	seedEventAt(t, jr, domain.EventGateRequest, ts, domain.GateRequest{SignalID: "sig-old"})
}

func testConfig(statusPath string) Config {
	return Config{LabelInterval: time.Minute, StatusPath: statusPath, CodeVersion: "v-test"}
}

// notifySentEvents decodes every notify.sent payload.
func notifySentEvents(t *testing.T, jr *memJournal) []notifySentPayload {
	t.Helper()
	var out []notifySentPayload
	for _, ev := range jr.eventsOf(domain.EventNotifySent) {
		var p notifySentPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode notify.sent seq %d: %v", ev.Seq, err)
		}
		out = append(out, p)
	}
	return out
}

func newRunner(jr *memJournal, deps ports.Deps, cfg Config, labeler Labeler, signals SignalSource, evaluator Evaluator, reporter Reporter, notifier ports.Notifier) *Runner {
	r, err := New(jr, labeler, signals, evaluator, reporter, notifier, deps, cfg)
	if err != nil {
		panic(err)
	}
	return r
}

func TestStepOrderLabelerThenSignalsThenEvaluator(t *testing.T) {
	jr := &memJournal{}
	var order []string
	l := &fakeLabeler{order: &order}
	s := &fakeSignals{reqs: []domain.GateRequest{{SignalID: "sig-1"}}, order: &order}
	e := &fakeEvaluator{order: &order}
	r := newRunner(jr, fixedDeps(), testConfig(""), l, s, e, nil, nil)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	want := []string{"labeler", "signals", "evaluator"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestLabelerErrorDoesNotBlockSignals(t *testing.T) {
	jr := &memJournal{}
	l := &fakeLabeler{err: errors.New("labeler boom")}
	s := &fakeSignals{reqs: []domain.GateRequest{{SignalID: "sig-1"}}}
	e := &fakeEvaluator{}
	r := newRunner(jr, fixedDeps(), testConfig(""), l, s, e, nil, nil)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if l.calls != 1 {
		t.Fatalf("labeler calls = %d, want 1", l.calls)
	}
	if e.calls != 1 {
		t.Fatalf("evaluator calls = %d, want 1 despite labeler error", e.calls)
	}
	st := r.Status()
	if st.LastCycleError == "" {
		t.Fatalf("LastCycleError empty, want recorded labeler error")
	}
}

func TestModelChangedSendsOnceAndRecordsNotifySent(t *testing.T) {
	jr := &memJournal{}
	seedEvent(t, jr, domain.EventDecisionModelChanged, domain.ModelChanged{
		RequestedModel: "test-model",
		PreviousModel:  "model-v1",
		ActualModel:    "model-v2",
	})
	n := &fakeNotifier{}
	r := newRunner(jr, fixedDeps(), testConfig(""), nil, nil, nil, nil, n)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	// Run again: the same event must not be re-sent.
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	sends := n.sent()
	if len(sends) != 1 {
		t.Fatalf("sends = %d, want 1", len(sends))
	}
	if sends[0].Kind != domain.EventDecisionModelChanged {
		t.Fatalf("kind = %q, want %q", sends[0].Kind, domain.EventDecisionModelChanged)
	}
	if want := "decision model changed: model-v1 -> model-v2 (requested test-model)"; sends[0].Text != want {
		t.Fatalf("text = %q, want %q", sends[0].Text, want)
	}
	sent := jr.eventsOf(domain.EventNotifySent)
	if len(sent) != 1 {
		t.Fatalf("notify.sent = %d, want 1", len(sent))
	}
	var p struct {
		Kind    string `json:"kind"`
		Channel string `json:"channel"`
		OK      bool   `json:"ok"`
	}
	if err := json.Unmarshal(sent[0].Payload, &p); err != nil {
		t.Fatalf("decode notify.sent: %v", err)
	}
	if p.Kind != domain.EventDecisionModelChanged || p.Channel != "telegram" || !p.OK {
		t.Fatalf("notify.sent payload = %+v", p)
	}
}

func TestModelChangedSendErrorRecordsOKFalse(t *testing.T) {
	jr := &memJournal{}
	seedEvent(t, jr, domain.EventDecisionModelChanged, domain.ModelChanged{
		RequestedModel: "test-model",
		PreviousModel:  "model-v1",
		ActualModel:    "model-v2",
	})
	now := baseTime
	n := &fakeNotifier{err: errors.New("send boom")}
	r := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, nil, n)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	sent := jr.eventsOf(domain.EventNotifySent)
	if len(sent) != 1 {
		t.Fatalf("notify.sent = %d, want 1", len(sent))
	}
	var p struct {
		Kind    string `json:"kind"`
		Channel string `json:"channel"`
		OK      bool   `json:"ok"`
	}
	if err := json.Unmarshal(sent[0].Payload, &p); err != nil {
		t.Fatalf("decode notify.sent: %v", err)
	}
	if p.OK {
		t.Fatalf("notify.sent ok = true, want false")
	}
	if r.Status().LastCycleError == "" {
		t.Fatalf("LastCycleError empty, want recorded send error")
	}
	if got := r.Status().LastSeq; got != 0 {
		t.Fatalf("lastSeq = %d, want 0 (cursor must not pass a failed send)", got)
	}

	// The next cycle is inside the one-cycle backoff: no attempt, no record.
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("backoff Step() = %v", err)
	}
	if got := n.sent(); len(got) != 1 {
		t.Fatalf("sends during backoff = %d, want 1", len(got))
	}
	if got := jr.eventsOf(domain.EventNotifySent); len(got) != 1 {
		t.Fatalf("notify.sent during backoff = %d, want 1", len(got))
	}

	// A later retry with a healthy notifier delivers the same event.
	now = now.Add(time.Minute)
	n.err = nil
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("retry Step() = %v", err)
	}
	if got := n.sent(); len(got) != 2 {
		t.Fatalf("sends = %d, want 2 (retry after failure)", len(got))
	}
	if got := jr.eventsOf(domain.EventNotifySent); len(got) != 2 {
		t.Fatalf("notify.sent = %d, want 2", len(got))
	}
	if got := r.Status().LastSeq; got == 0 {
		t.Fatalf("lastSeq = 0, want advanced after a successful retry")
	}
}

func seedAbstain(t *testing.T, jr *memJournal) {
	t.Helper()
	seedEvent(t, jr, domain.EventGateVerdict, domain.GateVerdict{
		Verdict: domain.VerdictAbstain,
		Code:    "provider_unavailable",
	})
}

func TestThreeAbstainSendsOncePerHour(t *testing.T) {
	jr := &memJournal{}
	for i := 0; i < 3; i++ {
		seedAbstain(t, jr)
	}
	now := baseTime
	n := &fakeNotifier{}
	r := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, nil, n)

	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 1 || got[0].Kind != "gate.unavailable" {
		t.Fatalf("sends = %v, want one gate.unavailable", got)
	}
	// Within the hour: no further send.
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 1 {
		t.Fatalf("sends = %d, want still 1 (rate limited)", len(got))
	}

	// Two hours later without a new faulty verdict: no repeat.
	now = now.Add(2 * time.Hour)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("quiet Step() = %v", err)
	}
	if got := n.sent(); len(got) != 1 {
		t.Fatalf("sends = %d, want no repeat without new ABSTAINs", len(got))
	}

	// Three more ABSTAINs make the streak newer than the delivered alert, so
	// the hourly alert fires again.
	for i := 0; i < 3; i++ {
		seedAbstain(t, jr)
	}
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 2 || got[1].Kind != "gate.unavailable" {
		t.Fatalf("sends = %d, want 2 gate.unavailable", len(got))
	}
	events := notifySentEvents(t, jr)
	if len(events) != 2 || events[0].Code != providerUnavailable || events[0].UptoSeq == 0 {
		t.Fatalf("notify.sent = %+v, want two records with the outage code and seq", events)
	}
	if events[1].UptoSeq <= events[0].UptoSeq {
		t.Fatalf("uptoSeq = %d -> %d, want the second alert to cover newer verdicts",
			events[0].UptoSeq, events[1].UptoSeq)
	}
}

func TestOtherVerdictResetsStreak(t *testing.T) {
	jr := &memJournal{}
	seedAbstain(t, jr)
	seedAbstain(t, jr)
	// A REJECT verdict breaks the streak.
	seedEvent(t, jr, domain.EventGateVerdict, domain.GateVerdict{Verdict: domain.VerdictReject, Code: "ok"})
	seedAbstain(t, jr)
	seedAbstain(t, jr)
	n := &fakeNotifier{}
	r := newRunner(jr, fixedDeps(), testConfig(""), nil, nil, nil, nil, n)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sends = %v, want none after reset", got)
	}
}

// TestProviderConfigAuthAfterConfigAlert: auth and misconfiguration are
// separate alert series, so an auth failure in the hour after a configuration
// alert is still delivered.
func TestProviderConfigAuthAfterConfigAlert(t *testing.T) {
	jr := &memJournal{}
	seedEvent(t, jr, domain.EventGateVerdict, domain.GateVerdict{
		Verdict: domain.VerdictAbstain,
		Code:    providerMisconfigured,
	})
	now := baseTime
	n := &fakeNotifier{}
	r := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, nil, n)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("first Step() = %v", err)
	}
	if got := n.sent(); len(got) != 1 || !strings.Contains(got[0].Text, providerMisconfigured) {
		t.Fatalf("sends = %v, want one %s alert", got, providerMisconfigured)
	}

	now = now.Add(30 * time.Minute)
	seedEvent(t, jr, domain.EventGateVerdict, domain.GateVerdict{
		Verdict: domain.VerdictAbstain,
		Code:    providerAuth,
	})
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("second Step() = %v", err)
	}
	sends := n.sent()
	if len(sends) != 2 || !strings.Contains(sends[1].Text, providerAuth) {
		t.Fatalf("sends = %v, want an auth alert within the config hour", sends)
	}
	events := notifySentEvents(t, jr)
	if len(events) != 2 {
		t.Fatalf("notify.sent = %d, want 2", len(events))
	}
	if events[0].Code != providerMisconfigured || events[1].Code != providerAuth {
		t.Fatalf("notify.sent codes = %q/%q, want %q/%q",
			events[0].Code, events[1].Code, providerMisconfigured, providerAuth)
	}
	if events[0].UptoSeq == 0 || events[1].UptoSeq <= events[0].UptoSeq {
		t.Fatalf("uptoSeq = %d/%d, want ordered non-zero coverage",
			events[0].UptoSeq, events[1].UptoSeq)
	}
}

// TestProviderConfigSendsOncePerHour: the same fault code alerts at most once
// per hour and only when a newer faulty verdict exists.
func TestProviderConfigSendsOncePerHour(t *testing.T) {
	jr := &memJournal{}
	seedEvent(t, jr, domain.EventGateVerdict, domain.GateVerdict{
		Verdict: domain.VerdictAbstain,
		Code:    providerAuth,
	})
	now := baseTime
	n := &fakeNotifier{}
	r := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, nil, n)

	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	sends := n.sent()
	if len(sends) != 1 || sends[0].Kind != notifyProviderConfig {
		t.Fatalf("sends = %v, want one %s", sends, notifyProviderConfig)
	}
	if !strings.Contains(sends[0].Text, providerAuth) {
		t.Fatalf("text = %q, want the fault code", sends[0].Text)
	}

	// A new faulty verdict inside the hour waits.
	now = now.Add(30 * time.Minute)
	seedEvent(t, jr, domain.EventGateVerdict, domain.GateVerdict{
		Verdict: domain.VerdictAbstain,
		Code:    providerAuth,
	})
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 1 {
		t.Fatalf("sends = %d, want still 1 (hour not passed)", len(got))
	}

	// After the hour the new verdict is announced.
	now = now.Add(31 * time.Minute)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	sends = n.sent()
	if len(sends) != 2 || !strings.Contains(sends[1].Text, providerAuth) {
		t.Fatalf("sends = %v, want a second auth alert", sends)
	}
	events := notifySentEvents(t, jr)
	if len(events) != 2 || events[1].UptoSeq <= events[0].UptoSeq {
		t.Fatalf("notify.sent = %+v, want a newer uptoSeq", events)
	}

	// Two more hours without a new faulty verdict: no repeat.
	now = now.Add(2 * time.Hour)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("quiet Step() = %v", err)
	}
	if got := n.sent(); len(got) != 2 {
		t.Fatalf("sends = %d, want no repeat without a new verdict", len(got))
	}
}

// TestProviderConfigNoRepeatAfterSuccessAndRestart: a delivered fault alert is
// never re-sent after a later successful verdict and a restart, because the
// notify.sent record in the journal is the source of truth.
func TestProviderConfigNoRepeatAfterSuccessAndRestart(t *testing.T) {
	jr := &memJournal{}
	seedEvent(t, jr, domain.EventGateVerdict, domain.GateVerdict{
		Verdict: domain.VerdictAbstain,
		Code:    providerAuth,
	})
	now := baseTime
	n1 := &fakeNotifier{}
	r1 := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, nil, n1)
	if err := r1.Step(context.Background()); err != nil {
		t.Fatalf("first Step() = %v", err)
	}
	if got := n1.sent(); len(got) != 1 {
		t.Fatalf("first sends = %d, want 1", len(got))
	}

	// A successful verdict follows the fault, and the process restarts.
	seedEvent(t, jr, domain.EventGateVerdict, domain.GateVerdict{
		Verdict: domain.VerdictAllow,
		Code:    "allow",
	})
	now = now.Add(2 * time.Hour)
	n2 := &fakeNotifier{}
	r2 := newRunner(jr, mutableDeps(&now), testConfig(filepath.Join(t.TempDir(), "status.json")),
		nil, nil, nil, nil, n2)
	if err := r2.Step(context.Background()); err != nil {
		t.Fatalf("restarted Step() = %v", err)
	}
	if got := n2.sent(); len(got) != 0 {
		t.Fatalf("restart sends = %v, want no repeat of the delivered fault", got)
	}
	if got := jr.eventsOf(domain.EventNotifySent); len(got) != 1 {
		t.Fatalf("notify.sent = %d, want 1", len(got))
	}
}

// TestFailedSendRecordsOneOKFalse: a failing Telegram channel is retried with
// 1, 2, 4 ... cycle backoff, but the journal records only the first failure of
// the series.
func TestFailedSendRecordsOneOKFalse(t *testing.T) {
	jr := &memJournal{}
	for i := 0; i < 3; i++ {
		seedAbstain(t, jr)
	}
	now := baseTime
	n := &fakeNotifier{err: domain.NewError(domain.CodeAuth, "notify-telegram: bot token rejected")}
	r := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, nil, n)
	for i := 0; i < 50; i++ {
		now = baseTime.Add(time.Duration(i) * time.Minute)
		if err := r.Step(context.Background()); err != nil {
			t.Fatalf("Step() cycle %d = %v", i, err)
		}
	}
	if attempts := len(n.sent()); attempts <= 1 {
		t.Fatalf("send attempts = %d, want the failure to be retried", attempts)
	}
	events := notifySentEvents(t, jr)
	failed := 0
	for _, ev := range events {
		if !ev.OK {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("notify.sent ok=false = %d, want exactly 1: %+v", failed, events)
	}
	if len(events) != 1 {
		t.Fatalf("notify.sent = %d, want only the first failure", len(events))
	}
}

func TestStepPhasesHonorsPhaseFlags(t *testing.T) {
	jr := &memJournal{}
	var order []string
	l := &fakeLabeler{order: &order}
	s := &fakeSignals{reqs: []domain.GateRequest{{SignalID: "sig-1"}}, order: &order}
	e := &fakeEvaluator{order: &order}
	r := newRunner(jr, fixedDeps(), testConfig(""), l, s, e, nil, nil)

	r.stepPhases(context.Background(), false, true)
	if got := strings.Join(order, ","); got != "signals,evaluator" {
		t.Fatalf("trend-only order = %q, want signals,evaluator", got)
	}

	order = nil
	r.stepPhases(context.Background(), true, false)
	if got := strings.Join(order, ","); got != "labeler" {
		t.Fatalf("label-only order = %q, want labeler", got)
	}
}

func TestWeeklySendsCountsOnly(t *testing.T) {
	jr := &memJournal{}
	seedOldGateRequest(t, jr, baseTime.Add(-8*24*time.Hour))
	n := &fakeNotifier{}
	rep := &fakeReporter{rep: domain.Report{
		LabeledCount: 5,
		VerdictCounts: map[string]int{
			string(domain.VerdictAllow):   2,
			string(domain.VerdictReject):  1,
			string(domain.VerdictAbstain): 0,
			string(domain.VerdictSkip):    7,
		},
	}}
	r := newRunner(jr, fixedDeps(), testConfig(""), nil, nil, nil, rep, n)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	sends := n.sent()
	if len(sends) != 1 || sends[0].Kind != "gate.weekly" {
		t.Fatalf("sends = %v, want one gate.weekly", sends)
	}
	want := "weekly gate summary: decisions=3, labeled=5, ALLOW=2, REJECT=1, ABSTAIN=0"
	if sends[0].Text != want {
		t.Fatalf("text = %q, want %q", sends[0].Text, want)
	}
	if strings.Contains(sends[0].Text, "$") || strings.Contains(sends[0].Text, "sig-") {
		t.Fatalf("weekly text leaks sums/ids: %q", sends[0].Text)
	}
}

func TestWeeklyRequiresSevenDaysSinceFirstRequest(t *testing.T) {
	jr := &memJournal{}
	n := &fakeNotifier{}
	rep := &fakeReporter{rep: domain.Report{LabeledCount: 5, VerdictCounts: map[string]int{}}}
	now := baseTime
	r := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, rep, n)

	// No gate request at all: the weekly clock never starts.
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sends = %v, want none without a gate request", got)
	}

	// A recent first request keeps the weekly summary closed.
	seedOldGateRequest(t, jr, now.Add(-24*time.Hour))
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sends = %v, want none at one day", got)
	}

	// Seven days after the first request it becomes due.
	now = now.Add(7 * 24 * time.Hour)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 1 || got[0].Kind != "gate.weekly" {
		t.Fatalf("sends = %v, want one gate.weekly", got)
	}
}

func TestWeeklyAfterSevenDays(t *testing.T) {
	jr := &memJournal{}
	seedOldGateRequest(t, jr, baseTime)
	n := &fakeNotifier{}
	rep := &fakeReporter{rep: domain.Report{LabeledCount: 1, VerdictCounts: map[string]int{}}}
	now := baseTime
	r := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, rep, n)

	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("first Step() = %v", err)
	}
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sends = %d, want 0 before seven days", len(got))
	}

	// Six days later: still within the seven-day window.
	now = now.Add(6 * 24 * time.Hour)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sends = %d, want still 0 at six days", len(got))
	}

	// Seven days later: the first weekly is due.
	now = now.Add(24 * time.Hour)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 1 {
		t.Fatalf("sends = %d, want 1 at seven days", len(got))
	}

	// A restart on the same journal must not repeat it.
	n2 := &fakeNotifier{}
	r2 := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, rep, n2)
	if err := r2.Step(context.Background()); err != nil {
		t.Fatalf("restarted Step() = %v", err)
	}
	if got := n2.sent(); len(got) != 0 {
		t.Fatalf("restart sends = %d, want 0", len(got))
	}
	if got := jr.eventsOf(domain.EventNotifySent); len(got) != 1 {
		t.Fatalf("notify.sent = %d, want 1 weekly record", len(got))
	}
}

func TestWeeklyNotDuplicatedAcrossRestart(t *testing.T) {
	jr := &memJournal{}
	seedOldGateRequest(t, jr, baseTime.Add(-8*24*time.Hour))
	rep := &fakeReporter{rep: domain.Report{LabeledCount: 3, VerdictCounts: map[string]int{}}}
	path := filepath.Join(t.TempDir(), "status.json")

	n1 := &fakeNotifier{}
	r1 := newRunner(jr, fixedDeps(), testConfig(path), nil, nil, nil, rep, n1)
	if err := r1.Step(context.Background()); err != nil {
		t.Fatalf("first Step() = %v", err)
	}
	if got := n1.sent(); len(got) != 1 || got[0].Kind != "gate.weekly" {
		t.Fatalf("first sends = %v, want one gate.weekly", got)
	}

	// Same journal, same status path: the journal weekly record suppresses it.
	n2 := &fakeNotifier{}
	r2 := newRunner(jr, fixedDeps(), testConfig(path), nil, nil, nil, rep, n2)
	if err := r2.Step(context.Background()); err != nil {
		t.Fatalf("second Step() = %v", err)
	}
	if got := n2.sent(); len(got) != 0 {
		t.Fatalf("second runner sends = %d, want 0", len(got))
	}

	// Even with a fresh status file the journal remains the source of truth.
	n3 := &fakeNotifier{}
	r3 := newRunner(jr, fixedDeps(), testConfig(filepath.Join(t.TempDir(), "status.json")), nil, nil, nil, rep, n3)
	if err := r3.Step(context.Background()); err != nil {
		t.Fatalf("third Step() = %v", err)
	}
	if got := n3.sent(); len(got) != 0 {
		t.Fatalf("fresh-status runner sends = %d, want 0", len(got))
	}
	if got := jr.eventsOf(domain.EventNotifySent); len(got) != 1 {
		t.Fatalf("notify.sent = %d, want 1 weekly record", len(got))
	}
}

func TestOneNotificationPerStep(t *testing.T) {
	jr := &memJournal{}
	seedEvent(t, jr, domain.EventDecisionModelChanged, domain.ModelChanged{
		RequestedModel: "test-model",
		PreviousModel:  "model-v1",
		ActualModel:    "model-v2",
	})
	seedEvent(t, jr, domain.EventDecisionModelChanged, domain.ModelChanged{
		RequestedModel: "test-model",
		PreviousModel:  "model-v2",
		ActualModel:    "model-v3",
	})
	n := &fakeNotifier{}
	r := newRunner(jr, fixedDeps(), testConfig(""), nil, nil, nil, nil, n)

	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("first Step() = %v", err)
	}
	if got := n.sent(); len(got) != 1 {
		t.Fatalf("first Step sends = %d, want 1", len(got))
	}
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("second Step() = %v", err)
	}
	sends := n.sent()
	if len(sends) != 2 {
		t.Fatalf("sends after two steps = %d, want 2", len(sends))
	}
	if !strings.Contains(sends[1].Text, "model-v2 -> model-v3") {
		t.Fatalf("second send text = %q, want the second event", sends[1].Text)
	}
	if got := jr.eventsOf(domain.EventNotifySent); len(got) != 2 {
		t.Fatalf("notify.sent = %d, want 2", len(got))
	}

	// A third step must not repeat anything.
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("third Step() = %v", err)
	}
	if got := n.sent(); len(got) != 2 {
		t.Fatalf("sends after third step = %d, want still 2", len(got))
	}
}

func TestStatusFileJSON(t *testing.T) {
	jr := &memJournal{}
	seedEvent(t, jr, domain.EventGateVerdict, domain.GateVerdict{
		Verdict:    domain.VerdictAllow,
		Code:       "ok",
		PolicyHash: "policy-abc",
	})
	seedEvent(t, jr, domain.EventOutcomeLabeled, domain.Outcome{SignalID: "sig-1"})
	seedEvent(t, jr, domain.EventOutcomeLabeled, domain.Outcome{SignalID: "sig-2"})

	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	l := &fakeLabeler{sum: outcome.Summary{Labeled: 2}}
	r := newRunner(jr, fixedDeps(), testConfig(path), l, nil, nil, nil, nil)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read status file: %v", err)
	}
	var st Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if st.Version != "v-test" {
		t.Fatalf("version = %q", st.Version)
	}
	if st.PolicyHash != "policy-abc" {
		t.Fatalf("policyHash = %q", st.PolicyHash)
	}
	if st.Events != 3 {
		t.Fatalf("events = %d, want 3", st.Events)
	}
	if st.Labeled != 2 {
		t.Fatalf("labeled = %d, want 2", st.Labeled)
	}
	if st.LastLabelAt.IsZero() {
		t.Fatalf("lastLabelAt zero, want set")
	}
	if !st.UpdatedAt.Equal(baseTime) {
		t.Fatalf("updatedAt = %v, want %v", st.UpdatedAt, baseTime)
	}
	if st.LastCycleError != "" {
		t.Fatalf("lastCycleError = %q, want empty", st.LastCycleError)
	}
}

func TestNoNotifyWhenNotifierNil(t *testing.T) {
	jr := &memJournal{}
	seedEvent(t, jr, domain.EventDecisionModelChanged, domain.ModelChanged{
		RequestedModel: "test-model",
		PreviousModel:  "model-v1",
		ActualModel:    "model-v2",
	})
	r := newRunner(jr, fixedDeps(), testConfig(""), nil, nil, nil, nil, nil)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := jr.eventsOf(domain.EventNotifySent); len(got) != 0 {
		t.Fatalf("notify.sent = %d, want 0 with nil notifier", len(got))
	}
}

// TestNextTrendDelay pins the bar-grid alignment: a scan runs offset after
// every close, never exactly on the close itself.
func TestNextTrendDelay(t *testing.T) {
	const hour = time.Hour
	closeAt := time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC) // bar close
	offset := 2 * time.Second

	cases := []struct {
		name     string
		now      time.Time
		interval time.Duration
		offset   time.Duration
		want     time.Duration
	}{
		{"exactly at close", closeAt, hour, offset, offset},
		{"one second after close", closeAt.Add(time.Second), hour, offset, time.Second},
		{"mid bar", closeAt.Add(30 * time.Minute), hour, offset, 30*time.Minute + offset},
		{"exactly at fire", closeAt.Add(offset), hour, offset, hour},
		{"no interval", closeAt.Add(30 * time.Minute), 0, offset, 0},
		{"negative interval", closeAt, -hour, offset, 0},
		{"mid bar without offset", closeAt.Add(30 * time.Minute), hour, 0, 30 * time.Minute},
		{"close without offset waits a full bar", closeAt, hour, 0, hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextTrendDelay(tc.now, tc.interval, tc.offset); got != tc.want {
				t.Fatalf("nextTrendDelay(%s, %s, %s) = %s, want %s",
					tc.now, tc.interval, tc.offset, got, tc.want)
			}
		})
	}
}

// countingLabeler and countingSignals are race-free counters for the Run
// scheduler test: Run's goroutine writes while the test reads.
type countingLabeler struct{ n atomic.Int32 }

func (l *countingLabeler) Run(context.Context) (outcome.Summary, error) {
	l.n.Add(1)
	return outcome.Summary{}, nil
}

type countingSignals struct {
	n     atomic.Int32
	first chan time.Time
}

func (s *countingSignals) Scan(context.Context) ([]domain.GateRequest, error) {
	s.n.Add(1)
	select {
	case s.first <- time.Now():
	default:
	}
	return nil, nil
}

// TestRunTrendScanAfterBarClose runs the real scheduler with a short bar and
// offset: the scan must wait for the bar close plus the offset, and the label
// ticker must keep running without triggering it. Cancelling ctx must return
// from Run.
func TestRunTrendScanAfterBarClose(t *testing.T) {
	jr := &memJournal{}
	labeler := &countingLabeler{}
	signals := &countingSignals{first: make(chan time.Time, 1)}

	// A fixed clock makes the delay deterministic: now is 200ms into a 1s
	// bar, the offset is 100ms, so the next fire is 900ms away.
	now := baseTime.Add(200 * time.Millisecond)
	deps := ports.Deps{Clock: func() time.Time { return now }}
	r := newRunner(jr, deps, Config{
		LabelInterval: 10 * time.Millisecond,
		TrendInterval: time.Second,
		StatusPath:    filepath.Join(t.TempDir(), "status.json"),
		CodeVersion:   "v-test",
	}, labeler, signals, nil, nil, nil)
	r.trendOffset = 100 * time.Millisecond
	want := 900 * time.Millisecond
	if got := r.NextTrendDelay(now); got != want {
		t.Fatalf("NextTrendDelay() = %s, want %s", got, want)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	start := time.Now()
	go func() {
		r.Run(ctx)
		close(done)
	}()

	var first time.Time
	select {
	case first = <-signals.first:
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("trend scan did not run within 3s")
	}
	if elapsed := first.Sub(start); elapsed < want-100*time.Millisecond {
		t.Fatalf("trend scan after %s, want not before %s (bar close + offset)", elapsed, want)
	}
	if got := labeler.n.Load(); got < 2 {
		t.Fatalf("label cycles before the trend scan = %d, want >= 2", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestRunCancelDoesNotOverlapStep(t *testing.T) {
	jr := &memJournal{}
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var concurrent, maxConc int32

	bl := &blockingLabeler{
		entered:    entered,
		release:    release,
		concurrent: &concurrent,
		maxConc:    &maxConc,
	}
	r := newRunner(jr, fixedDeps(), Config{LabelInterval: time.Millisecond, CodeVersion: "v-test"}, bl, nil, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	// Wait for the first step to be inside the labeler, then cancel.
	<-entered
	cancel()

	// Run must wait for the in-flight step: done must stay open while the
	// labeler is still blocked.
	select {
	case <-done:
		t.Fatal("Run returned before the in-flight step finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(release) // let the step finish
	<-done         // Run returns only now

	if got := atomic.LoadInt32(&maxConc); got > 1 {
		t.Fatalf("max concurrent steps = %d, want 1", got)
	}
}

type blockingLabeler struct {
	entered    chan struct{}
	release    chan struct{}
	concurrent *int32
	maxConc    *int32
}

func (b *blockingLabeler) Run(context.Context) (outcome.Summary, error) {
	cur := atomic.AddInt32(b.concurrent, 1)
	for {
		old := atomic.LoadInt32(b.maxConc)
		if cur <= old || atomic.CompareAndSwapInt32(b.maxConc, old, cur) {
			break
		}
	}
	b.entered <- struct{}{}
	<-b.release
	atomic.AddInt32(b.concurrent, -1)
	return outcome.Summary{}, nil
}

func TestDebugSizeStable(t *testing.T) {
	jr := &memJournal{}
	seedEvent(t, jr, domain.EventDecisionModelChanged, domain.ModelChanged{
		RequestedModel: "test-model",
		PreviousModel:  "model-v1",
		ActualModel:    "model-v2",
	})
	for i := 0; i < 3; i++ {
		seedAbstain(t, jr)
	}
	n := &fakeNotifier{}
	rep := &fakeReporter{rep: domain.Report{LabeledCount: 1, VerdictCounts: map[string]int{}}}
	r := newRunner(jr, fixedDeps(), testConfig(""), nil, nil, nil, rep, n)
	if got := r.debugSize(); got != 0 {
		t.Fatalf("debugSize = %d, want 0 before any failure", got)
	}
	for i := 0; i < 5; i++ {
		if err := r.Step(context.Background()); err != nil {
			t.Fatalf("Step() = %v", err)
		}
	}
	if got := r.debugSize(); got > 3 {
		t.Fatalf("debugSize = %d, want <= 3 (bounded)", got)
	}
}

func TestNotificationCursorPersistsAcrossRestart(t *testing.T) {
	jr := &memJournal{}
	seedEvent(t, jr, domain.EventDecisionModelChanged, domain.ModelChanged{
		RequestedModel: "test-model",
		PreviousModel:  "model-v1",
		ActualModel:    "model-v2",
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")

	n1 := &fakeNotifier{}
	r1 := newRunner(jr, fixedDeps(), testConfig(path), nil, nil, nil, nil, n1)
	if err := r1.Step(context.Background()); err != nil {
		t.Fatalf("first Step() = %v", err)
	}
	if got := n1.sent(); len(got) != 1 {
		t.Fatalf("first runner sends = %d, want 1", len(got))
	}

	// A new runner on the same journal and status path must not re-send.
	n2 := &fakeNotifier{}
	r2 := newRunner(jr, fixedDeps(), testConfig(path), nil, nil, nil, nil, n2)
	if err := r2.Step(context.Background()); err != nil {
		t.Fatalf("second Step() = %v", err)
	}
	if got := n2.sent(); len(got) != 0 {
		t.Fatalf("second runner sends = %d, want 0 (cursor persisted)", len(got))
	}
	if got := jr.eventsOf(domain.EventNotifySent); len(got) != 1 {
		t.Fatalf("notify.sent = %d, want 1 across restart", len(got))
	}
}

func TestStatusOmitsZeroTimes(t *testing.T) {
	jr := &memJournal{}
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	r := newRunner(jr, fixedDeps(), testConfig(path), nil, nil, nil, nil, nil)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read status file: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, "0001-01-01") {
		t.Fatalf("status serializes a zero time: %s", s)
	}
	if strings.Contains(s, "lastLabelAt") {
		t.Fatalf("status serializes zero lastLabelAt: %s", s)
	}
	if strings.Contains(s, "lastSeq") {
		t.Fatalf("status serializes zero lastSeq: %s", s)
	}
}

func TestLastLabelAtOnlyWhenLabeledPositive(t *testing.T) {
	jr := &memJournal{}
	l := &fakeLabeler{sum: outcome.Summary{Labeled: 0}}
	r := newRunner(jr, fixedDeps(), testConfig(""), l, nil, nil, nil, nil)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if !r.Status().LastLabelAt.IsZero() {
		t.Fatalf("lastLabelAt = %v, want zero when nothing labeled", r.Status().LastLabelAt)
	}
	l.sum = outcome.Summary{Labeled: 3}
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if r.Status().LastLabelAt.IsZero() {
		t.Fatalf("lastLabelAt zero, want set after labeling")
	}
}

type cancelingEvaluator struct {
	cancel context.CancelFunc
	calls  int
}

func (c *cancelingEvaluator) Evaluate(context.Context, domain.GateRequest) (gateflow.Result, error) {
	c.calls++
	c.cancel()
	return gateflow.Result{}, nil
}

func TestStepStopsEvaluatingWhenCancelled(t *testing.T) {
	jr := &memJournal{}
	s := &fakeSignals{reqs: []domain.GateRequest{{SignalID: "a"}, {SignalID: "b"}, {SignalID: "c"}}}
	ctx, cancel := context.WithCancel(context.Background())
	e := &cancelingEvaluator{cancel: cancel}
	r := newRunner(jr, fixedDeps(), testConfig(""), nil, s, e, nil, nil)
	if err := r.Step(ctx); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if e.calls != 1 {
		t.Fatalf("evaluator calls = %d, want 1 (stop starting new calls after cancel)", e.calls)
	}
}

// localRateNotifier mimics the Telegram provider's local one-message-per-
// interval limiter: a rejected send returns CodeRateLimit with the remaining
// delay and never touches the network.
type localRateNotifier struct {
	mu          sync.Mutex
	minInterval time.Duration
	now         func() time.Time
	last        time.Time
	sends       []ports.Notification
	times       []time.Time
}

func (n *localRateNotifier) Send(_ context.Context, msg ports.Notification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now().UTC()
	if !n.last.IsZero() {
		if elapsed := now.Sub(n.last); elapsed < n.minInterval {
			e := domain.NewError(domain.CodeRateLimit,
				"notify-telegram: local rate limit (one message per interval)")
			e.Retryable = true
			e.RetryAfter = n.minInterval - elapsed
			return e
		}
	}
	n.last = now
	n.sends = append(n.sends, msg)
	n.times = append(n.times, now)
	return nil
}

func (n *localRateNotifier) delivered() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.sends)
}

// TestLocalRateLimitIsNotDeliveryFailure: a local limiter rejection writes no
// notify.sent, keeps the series healthy and defers the next attempt to the
// remaining delay, so the second alert still arrives within the interval.
func TestLocalRateLimitIsNotDeliveryFailure(t *testing.T) {
	jr := &memJournal{}
	now := baseTime
	seedEvent(t, jr, domain.EventDecisionModelChanged, domain.ModelChanged{
		RequestedModel: "test-model",
		PreviousModel:  "model-v1",
		ActualModel:    "model-v2",
	})
	n := &localRateNotifier{minInterval: time.Minute, now: func() time.Time { return now }}
	r := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, nil, n)

	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("first Step() = %v", err)
	}
	if got := n.delivered(); got != 1 {
		t.Fatalf("delivered = %d, want 1", got)
	}

	// A second alert 30 seconds later is rejected locally, not delivered.
	now = baseTime.Add(30 * time.Second)
	seedEventAt(t, jr, domain.EventDecisionModelChanged, now, domain.ModelChanged{
		RequestedModel: "test-model",
		PreviousModel:  "model-v2",
		ActualModel:    "model-v3",
	})
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("rate-limited Step() = %v", err)
	}
	if got := n.delivered(); got != 1 {
		t.Fatalf("delivered = %d, want still 1 after the local limit", got)
	}
	events := notifySentEvents(t, jr)
	if len(events) != 1 || !events[0].OK {
		t.Fatalf("notify.sent after the local limit = %+v, want only the first ok=true", events)
	}
	if got := r.Status().LastCycleError; got != "" {
		t.Fatalf("LastCycleError = %q, want empty (local limit is not a failure)", got)
	}

	// The remaining delay is 30 seconds: the next cycle after 61 seconds
	// delivers the pending notification.
	now = baseTime.Add(61 * time.Second)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("retry Step() = %v", err)
	}
	if got := n.delivered(); got != 2 {
		t.Fatalf("delivered = %d, want 2 after the local limit", got)
	}
	for _, ev := range notifySentEvents(t, jr) {
		if !ev.OK {
			t.Fatalf("notify.sent carries ok=false after a local limit: %+v", ev)
		}
	}
	if got := n.times[1].Sub(n.times[0]); got > 70*time.Second {
		t.Fatalf("second delivery took %v after the first, want <= 70s", got)
	}
}

// TestLastLabelAtRestoredFromJournal: a restarted runner must not lose the
// timestamp of the last outcome.labeled event.
func TestLastLabelAtRestoredFromJournal(t *testing.T) {
	jr := &memJournal{}
	labelAt := baseTime.Add(-2 * time.Hour)
	seedEventAt(t, jr, domain.EventOutcomeLabeled, labelAt, domain.Outcome{SignalID: "sig-old"})
	r := newRunner(jr, fixedDeps(), testConfig(""), nil, nil, nil, nil, nil)
	got := r.Status().LastLabelAt
	if got.IsZero() {
		t.Fatalf("lastLabelAt zero, want restored %v", labelAt)
	}
	if !got.Equal(labelAt) {
		t.Fatalf("lastLabelAt = %v, want %v", got, labelAt)
	}
}

// appendingLabeler appends one outcome.labeled event per run with a journal
// time that differs from the cycle clock, like the real labeler does.
type appendingLabeler struct {
	jr *memJournal
	at time.Time
}

func (l *appendingLabeler) Run(ctx context.Context) (outcome.Summary, error) {
	raw, err := json.Marshal(domain.Outcome{SignalID: "sig-new"})
	if err != nil {
		return outcome.Summary{}, err
	}
	if _, err := l.jr.Append(ctx, domain.Event{
		Kind:          domain.EventOutcomeLabeled,
		TS:            l.at,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       raw,
		CodeVersion:   "v-test",
	}); err != nil {
		return outcome.Summary{}, err
	}
	return outcome.Summary{Labeled: 1}, nil
}

// TestLastLabelAtSameAcrossRestart: the running cycle and a restarted runner
// must report the same lastLabelAt, the journal time of the latest label, not
// the cycle clock.
func TestLastLabelAtSameAcrossRestart(t *testing.T) {
	jr := &memJournal{}
	labelAt := baseTime.Add(-3 * time.Millisecond)
	r := newRunner(jr, fixedDeps(), testConfig(""), &appendingLabeler{jr: jr, at: labelAt}, nil, nil, nil, nil)
	r.Step(context.Background())
	running := r.Status().LastLabelAt
	if !running.Equal(labelAt) {
		t.Fatalf("running lastLabelAt = %v, want the journal time %v", running, labelAt)
	}
	restarted := newRunner(jr, fixedDeps(), testConfig(""), nil, nil, nil, nil, nil)
	if got := restarted.Status().LastLabelAt; !got.Equal(running) {
		t.Fatalf("restarted lastLabelAt = %v, want %v", got, running)
	}
}

// TestProviderFaultAlertOnlyWhenFresh: a provider fault verdict older than a
// day must not page the operator after a restart; a fresh one still does.
func TestProviderFaultAlertOnlyWhenFresh(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   time.Time
		want int
	}{
		{name: "three days old", at: baseTime.Add(-72 * time.Hour), want: 0},
		{name: "one hour old", at: baseTime.Add(-time.Hour), want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jr := &memJournal{}
			seedEventAt(t, jr, domain.EventGateVerdict, tc.at, domain.GateVerdict{
				Verdict: domain.VerdictAbstain,
				Code:    providerAuth,
			})
			now := baseTime
			n := &fakeNotifier{}
			r := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, nil, n)
			if err := r.Step(context.Background()); err != nil {
				t.Fatalf("Step() = %v", err)
			}
			if got := len(n.sent()); got != tc.want {
				t.Fatalf("sends = %d, want %d", got, tc.want)
			}
			if got := len(jr.eventsOf(domain.EventNotifySent)); got != tc.want {
				t.Fatalf("notify.sent = %d, want %d", got, tc.want)
			}
		})
	}
}
