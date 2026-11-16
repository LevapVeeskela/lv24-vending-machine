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
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s: %v", kind, err)
	}
	if _, err := jr.Append(context.Background(), domain.Event{
		Kind:          kind,
		TS:            baseTime,
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       raw,
		CodeVersion:   "v-test",
	}); err != nil {
		t.Fatalf("seed %s: %v", kind, err)
	}
}

func testConfig(statusPath string) Config {
	return Config{LabelInterval: time.Minute, StatusPath: statusPath, CodeVersion: "v-test"}
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
	n := &fakeNotifier{err: errors.New("send boom")}
	r := newRunner(jr, fixedDeps(), testConfig(""), nil, nil, nil, nil, n)
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

	// Advance past the hour and add three more ABSTAINs.
	now = now.Add(2 * time.Hour)
	for i := 0; i < 3; i++ {
		seedAbstain(t, jr)
	}
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 2 || got[1].Kind != "gate.unavailable" {
		t.Fatalf("sends = %d, want 2 gate.unavailable", len(got))
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

func TestWeeklySendsCountsOnly(t *testing.T) {
	jr := &memJournal{}
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

func TestWeeklyFirstTimeRequiresLabeled(t *testing.T) {
	jr := &memJournal{}
	n := &fakeNotifier{}
	rep := &fakeReporter{rep: domain.Report{LabeledCount: 0, VerdictCounts: map[string]int{}}}
	r := newRunner(jr, fixedDeps(), testConfig(""), nil, nil, nil, rep, n)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sends = %v, want no weekly with zero labeled", got)
	}
}

func TestWeeklyAfterSevenDays(t *testing.T) {
	jr := &memJournal{}
	n := &fakeNotifier{}
	rep := &fakeReporter{rep: domain.Report{LabeledCount: 1, VerdictCounts: map[string]int{}}}
	now := baseTime
	r := newRunner(jr, mutableDeps(&now), testConfig(""), nil, nil, nil, rep, n)

	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("first Step() = %v", err)
	}
	if got := n.sent(); len(got) != 1 {
		t.Fatalf("sends = %d, want 1 (first weekly)", len(got))
	}

	// Six days later: still within the seven-day window.
	now = now.Add(6 * 24 * time.Hour)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 1 {
		t.Fatalf("sends = %d, want still 1 at six days", len(got))
	}

	// Seven days later: a second weekly is due.
	now = now.Add(24 * time.Hour)
	if err := r.Step(context.Background()); err != nil {
		t.Fatalf("Step() = %v", err)
	}
	if got := n.sent(); len(got) != 2 {
		t.Fatalf("sends = %d, want 2 at seven days", len(got))
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
	if got := r.debugSize(); got != 2 {
		t.Fatalf("debugSize = %d, want 2 (fixed keys initialized)", got)
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
