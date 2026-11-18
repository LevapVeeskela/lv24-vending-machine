// Package e2e drives the real vend binary against the local smokestub
// rehearsal double over HTTP and the CLI. No real provider is called: the
// stub listens on 127.0.0.1 and answers decision, klines and Telegram roles.
//
// The test is opt-in because it builds binaries and waits for a real outcome
// labeling window:
//
//	VEND_E2E=1 go -C tools test -count=1 ./e2e/...
//
// VEND_E2E_BIN points at a prebuilt vend binary (absolute, or relative to the
// repository root). When it is set, the test runs that file instead of
// building vend; tools/smokestub is always built.
package e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// e2eTimeout bounds one HTTP request.
const e2eTimeout = 5 * time.Second

// secretEnv names the environment variables the child processes read. They
// are generated for the run and never echoed.
const (
	decisionSecretEnv = "VENDING_SECRET_CONN_DREX_APIKEY"
	telegramSecretEnv = "VENDING_SECRET_CONN_TELEGRAM_BOTTOKEN"
	localSecretEnv    = "VENDING_SECRET_CONN_LOCAL_APITOKEN"
)

// gateResponse mirrors the flat POST /v1/gate wrapper.
type gateResponse struct {
	Mode     string `json:"mode"`
	SignalID string `json:"signalId"`
	Verdict  struct {
		Verdict    string `json:"verdict"`
		Code       string `json:"code"`
		DecisionID string `json:"decisionId"`
	} `json:"verdict"`
}

// errorResponse is the machine-readable API error envelope.
type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// stats mirrors GET /ctl/stats of smokestub.
type stats struct {
	Decision int            `json:"decision"`
	Telegram int            `json:"telegram"`
	Klines   map[string]int `json:"klines"`
}

// shadowStatus mirrors the fields of `vend status --json` used here.
type shadowStatus struct {
	Labeled     int       `json:"labeled"`
	LastLabelAt time.Time `json:"lastLabelAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	LastError   string    `json:"lastCycleError"`
}

// gateRequest is the POST /v1/gate body used by the scenario.
type gateRequest struct {
	SignalID   string             `json:"signalId"`
	Instrument gateInstrument     `json:"instrument"`
	Side       string             `json:"side"`
	IntentKind string             `json:"intentKind"`
	Timeframe  string             `json:"timeframe"`
	TS         string             `json:"ts"`
	Price      string             `json:"price"`
	Source     string             `json:"source"`
	State      map[string]float64 `json:"state"`
}

// gateInstrument is the instrument block of a gate request.
type gateInstrument struct {
	Venue  string `json:"venue"`
	Symbol string `json:"symbol"`
}

// process is one child command with captured output and an exact-PID stop.
// stdout and stderr are kept apart for the failure dump; log is their merged
// view, used in test messages.
type process struct {
	name    string
	cmd     *exec.Cmd
	stdout  lockedBuffer
	stderr  lockedBuffer
	log     *lockedBuffer
	done    chan error
	mu      sync.Mutex
	stopped bool
}

// startProcess starts exe and begins capturing its stdout and stderr.
func startProcess(t *testing.T, name, exe string, args, env []string) *process {
	t.Helper()
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	p := &process{name: name, cmd: cmd, log: &lockedBuffer{}, done: make(chan error, 1)}
	cmd.Stdout = io.MultiWriter(&p.stdout, p.log)
	cmd.Stderr = io.MultiWriter(&p.stderr, p.log)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	go func() { p.done <- cmd.Wait() }()
	return p
}

// alive reports whether the process is still running.
func (p *process) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// stop kills exactly this PID and waits for it once. A nil process is a
// process that never started, which is not an error.
func (p *process) stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	p.mu.Unlock()
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
	}
}

// TestShadowGateE2E is the automated rehearsal: it builds vend and smokestub,
// starts both on free loopback ports, and checks the gate, conflict, auth,
// labeling, restart, report and secret behavior over HTTP and the CLI.
// secondAlertBound is how long after the first operator alert the second one
// may arrive: the 60-second local send limiter, two runner cycles and slack.
const secondAlertBound = 90 * time.Second

func TestShadowGateE2E(t *testing.T) {
	if os.Getenv("VEND_E2E") != "1" {
		t.Skip("set VEND_E2E=1 to run the end-to-end rehearsal")
	}
	started := time.Now()
	tmp := t.TempDir()
	h := newHarness(t, started)
	h.step("setup: work tree")
	root := repoRoot(t)
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}

	h.step("build: vend")
	vend := vendBinary(t, root, filepath.Join(binDir, exeName("vend")))
	h.vendPath = vend
	t.Logf("vend under test: %s", vend)
	h.step("build: smokestub")
	stub := buildBinary(t, filepath.Join(root, "tools"), "./smokestub", filepath.Join(binDir, exeName("smokestub")))

	decisionSecret := randomSecret(t)
	telegramSecret := randomSecret(t)
	localToken := randomSecret(t)
	h.addSecret(decisionSecret)
	h.addSecret(telegramSecret)
	h.addSecret(localToken)

	env := childEnv(map[string]string{
		decisionSecretEnv: decisionSecret,
		telegramSecretEnv: telegramSecret,
		localSecretEnv:    localToken,
	})

	h.step("start: smokestub")
	stubAddr := freeAddr(t)
	stubBase := "http://" + stubAddr
	stubProc := h.start("smokestub", stub, []string{"--addr", stubAddr}, env)
	h.stub = stubProc
	h.stubBase = stubBase
	waitHTTP(t, stubProc, stubBase+"/ctl/stats", 30*time.Second, h.redact)

	configPath := filepath.Join(tmp, "config.json")
	dataDir := filepath.Join(tmp, "data")
	writeConfig(t, configPath, stubBase)
	h.configPath = configPath
	h.dataDir = dataDir

	h.step("start: vend serve")
	vendAddr := freeAddr(t)
	vendBase := "http://" + vendAddr
	vendArgs := []string{"serve", "--addr", vendAddr, "--config", configPath, "--data-dir", dataDir}
	vendProc := h.start("vend", vend, vendArgs, env)
	h.vend = vendProc
	waitHTTP(t, vendProc, vendBase+"/healthz", 60*time.Second, h.redact)

	client := &http.Client{Timeout: e2eTimeout}
	post := func(token string, body []byte) (int, []byte) {
		return postGate(t, client, vendBase+"/v1/gate", token, body)
	}

	// 1. A plain buy signal is accepted and allowed.
	h.step("1: plain buy signal")
	sigA := newSignal(t, "e2e-allow-1", "BTC/USDT", "123456.789")
	status, raw := post(localToken, sigA)
	requireStatus(t, status, http.StatusOK, raw)
	var allow gateResponse
	decode(t, raw, &allow)
	if allow.Verdict.Verdict != "ALLOW" {
		t.Fatalf("first verdict = %q (%s), want ALLOW", allow.Verdict.Verdict, allow.Verdict.Code)
	}
	if allow.Verdict.DecisionID == "" {
		t.Fatal("first verdict has no decisionId")
	}
	if allow.Mode != "shadow" || allow.SignalID != "e2e-allow-1" {
		t.Fatalf("first response = %+v, want mode=shadow signalId=e2e-allow-1", allow)
	}

	// 2. A dash-separated spot symbol is accepted and canonicalized. Klines
	// are not requested before the labeling window closes, and the counter is
	// keyed by BTCUSDT.
	h.step("2: spot symbol format")
	sigB := newSignal(t, "e2e-allow-2", "BTC-USDT", "123457.001")
	status, raw = post(localToken, sigB)
	requireStatus(t, status, http.StatusOK, raw)
	var allowB gateResponse
	decode(t, raw, &allowB)
	if allowB.Verdict.Verdict != "ALLOW" {
		t.Fatalf("second verdict = %q (%s), want ALLOW", allowB.Verdict.Verdict, allowB.Verdict.Code)
	}
	if got := getStats(t, client, stubBase); len(got.Klines) != 0 {
		t.Fatalf("klines counters before the labeling window = %v, want none", got.Klines)
	}

	// 2b. A futures-style suffix is rejected before the provider is called:
	// the spot candle source could never label it.
	h.step("2b: futures symbol rejected")
	futures := newSignal(t, "e2e-futures", "BTC/USDT:USDT", "123457.002")
	status, raw = post(localToken, futures)
	if status != http.StatusBadRequest {
		t.Fatalf("futures symbol status = %d, want 400: %s", status, raw)
	}
	var futuresErr errorResponse
	decode(t, raw, &futuresErr)
	if futuresErr.Code != "bad_request" || !strings.Contains(futuresErr.Message, "futures") {
		t.Fatalf("futures error = %+v, want bad_request with a futures hint", futuresErr)
	}

	// 3. The identical body replays the recorded decision without a new call.
	h.step("3: identical replay")
	status, raw = post(localToken, sigA)
	requireStatus(t, status, http.StatusOK, raw)
	var replay gateResponse
	decode(t, raw, &replay)
	if replay.Verdict.DecisionID != allow.Verdict.DecisionID {
		t.Fatalf("replay decisionId = %q, want %q", replay.Verdict.DecisionID, allow.Verdict.DecisionID)
	}

	// 4. The same signalId with another body is a conflict.
	h.step("4: duplicate signal id")
	conflict := newSignal(t, "e2e-allow-1", "BTC/USDT", "123458.002")
	status, raw = post(localToken, conflict)
	if status != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409: %s", status, raw)
	}
	var conflictErr errorResponse
	decode(t, raw, &conflictErr)
	if conflictErr.Code != "conflict" {
		t.Fatalf("conflict code = %q, want conflict", conflictErr.Code)
	}

	// 5. A request without the bearer token is rejected.
	h.step("5: missing bearer token")
	status, raw = post("", sigA)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401: %s", status, raw)
	}

	// 6. A provider-rejected request is an ABSTAIN with provider_misconfigured
	// and produces one operator alert on the next cycle.
	h.step("6: misconfigured alert")
	setCtl(t, client, stubBase, "decision=404")
	sigC := newSignal(t, "e2e-misconfigured", "BTC/USDT", "123459.003")
	status, raw = post(localToken, sigC)
	requireStatus(t, status, http.StatusOK, raw)
	var misconfigured gateResponse
	decode(t, raw, &misconfigured)
	if misconfigured.Verdict.Verdict != "ABSTAIN" || misconfigured.Verdict.Code != "provider_misconfigured" {
		t.Fatalf("404 verdict = %s (%s), want ABSTAIN provider_misconfigured",
			misconfigured.Verdict.Verdict, misconfigured.Verdict.Code)
	}
	waitStats(t, client, stubBase, 45*time.Second, "the first Telegram alert", func(s stats) bool {
		return s.Telegram >= 1
	})
	firstAlertAt := time.Now()

	// 7. An auth failure is a separate alert. The local one-message-per-minute
	// limiter defers it by up to 60 seconds and the runner then needs up to
	// two 10-second cycles (a tick that lands just before the deferral ends
	// waits for the next one), so the alert is due within about 80 seconds of
	// the first one. The bound leaves room for a slow machine: a freshly built
	// binary can start and cycle slowly while it is being scanned.
	h.step("7: auth alert")
	setCtl(t, client, stubBase, "decision=401")
	sigD := newSignal(t, "e2e-auth", "BTC/USDT", "123460.004")
	status, raw = post(localToken, sigD)
	requireStatus(t, status, http.StatusOK, raw)
	var auth gateResponse
	decode(t, raw, &auth)
	if auth.Verdict.Verdict != "ABSTAIN" || auth.Verdict.Code != "provider_auth" {
		t.Fatalf("401 verdict = %s (%s), want ABSTAIN provider_auth",
			auth.Verdict.Verdict, auth.Verdict.Code)
	}
	setCtl(t, client, stubBase, "decision=200")
	waitStats(t, client, stubBase, secondAlertBound, "the second Telegram alert", func(s stats) bool {
		return s.Telegram >= 2
	})
	if elapsed := time.Since(firstAlertAt); elapsed > secondAlertBound {
		t.Fatalf("second alert arrived after %s, want <= %s", elapsed, secondAlertBound)
	}
	if got := getStats(t, client, stubBase); got.Telegram != 2 {
		t.Fatalf("telegram alerts = %d, want exactly 2", got.Telegram)
	}

	// 7b. The journal tail CLI must show exactly the two delivered alerts and
	// no failed send.
	h.step("7b: journal notify.sent")
	tailOut, tailErr, code := runVend(t, vend, configPath, dataDir,
		"journal", "tail", "--kind", "notify.sent", "--json")
	if code != 0 {
		t.Fatalf("journal tail exit = %d: %s", code, h.redact(tailOut+tailErr))
	}
	var sent []struct {
		Seq     int64  `json:"seq"`
		Kind    string `json:"kind"`
		Payload struct {
			OK bool `json:"ok"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(tailOut), &sent); err != nil {
		t.Fatalf("decode journal tail: %v (%s)", err, h.redact(tailOut))
	}
	if len(sent) != 2 {
		t.Fatalf("notify.sent in journal tail = %d, want exactly 2: %s", len(sent), h.redact(tailOut))
	}
	for i, ev := range sent {
		if ev.Kind != "notify.sent" {
			t.Fatalf("journal tail event %d kind = %q, want notify.sent", i, ev.Kind)
		}
		if !ev.Payload.OK {
			t.Fatalf("journal tail event %d has ok=false: %s", i, h.redact(tailOut))
		}
		if i > 0 && ev.Seq <= sent[i-1].Seq {
			t.Fatalf("journal tail seqs are not ascending: %d then %d", sent[i-1].Seq, ev.Seq)
		}
	}

	// Nothing may ask for candles while the forward windows are open.
	if got := getStats(t, client, stubBase); len(got.Klines) != 0 {
		t.Fatalf("klines requested before the windows closed = %v", got.Klines)
	}

	// 8. After (horizonBars+1) bars plus two cycles every entry signal is
	// labeled exactly once, and all klines requests use the canonical symbol.
	h.step("8: outcome labeling")
	before := waitStatus(t, vend, configPath, dataDir, 3*time.Minute, "all four outcomes labeled", func(st shadowStatus) bool {
		return st.Labeled >= 4
	})
	if before.LastLabelAt.IsZero() {
		t.Fatal("lastLabelAt is empty after labeling")
	}
	got := waitStats(t, client, stubBase, 20*time.Second, "klines counters to match labeled", func(s stats) bool {
		return totalKlines(s) >= before.Labeled
	})
	if total := totalKlines(got); total != before.Labeled {
		t.Fatalf("klines requests = %d, labeled = %d, want one request per label", total, before.Labeled)
	}
	if len(got.Klines) != 1 || got.Klines["BTCUSDT"] != before.Labeled {
		t.Fatalf("klines counters = %v, want only BTCUSDT=%d", got.Klines, before.Labeled)
	}

	// 9. A restart on the same journal must not relabel, repeat an alert, or
	// lose lastLabelAt.
	h.step("9: restart")
	vendProc.stop()
	vendProc = h.start("vend", vend, vendArgs, env)
	h.vend = vendProc
	waitHTTP(t, vendProc, vendBase+"/healthz", 60*time.Second, h.redact)
	waitStatus(t, vend, configPath, dataDir, 45*time.Second, "the restarted runner status", func(st shadowStatus) bool {
		return st.UpdatedAt.After(before.UpdatedAt)
	})
	// A second cycle proves the alert schedule stayed quiet after the restart.
	restartFirst := lastStatus(t, vend, configPath, dataDir)
	after := waitStatus(t, vend, configPath, dataDir, 30*time.Second, "a second post-restart cycle", func(st shadowStatus) bool {
		return st.UpdatedAt.After(restartFirst.UpdatedAt)
	})
	if after.Labeled != before.Labeled {
		t.Fatalf("labeled after restart = %d, want %d", after.Labeled, before.Labeled)
	}
	// lastLabelAt is the journal time of the latest label, so the running
	// cycle and the restarted runner must report exactly the same value.
	if !after.LastLabelAt.Equal(before.LastLabelAt) {
		t.Fatalf("lastLabelAt after restart = %s, want %s", after.LastLabelAt, before.LastLabelAt)
	}
	if after.LastLabelAt.IsZero() {
		t.Fatal("lastLabelAt is empty after restart")
	}
	if after.LastError != "" {
		t.Fatalf("lastCycleError after restart = %q, want empty", after.LastError)
	}
	if s := getStats(t, client, stubBase); s.Telegram != 2 || totalKlines(s) != before.Labeled {
		t.Fatalf("counters changed across restart: telegram=%d klines=%v", s.Telegram, s.Klines)
	}

	// 10. The report, the sanitized share and the journal check.
	h.step("10: report, share, verify")
	reportOut, reportErr, code := runVend(t, vend, configPath, dataDir, "gate", "report")
	if code != 0 {
		t.Fatalf("gate report exit = %d: %s", code, h.redact(reportOut+reportErr))
	}
	for _, line := range []string{"window:", "labeled:", "awaiting labeling:"} {
		if !strings.Contains(reportOut, line) {
			t.Fatalf("gate report misses %q:\n%s", line, reportOut)
		}
	}
	shareOut, shareErr, code := runVend(t, vend, configPath, dataDir, "gate", "report", "--share")
	if code != 0 {
		t.Fatalf("gate report --share exit = %d: %s", code, h.redact(shareOut+shareErr))
	}
	for _, forbidden := range []string{
		"BTCUSDT", "BTC/USDT", "binance", "123456.789", "123457.001",
		"123459.003", "123460.004", configPath, dataDir, tmp,
	} {
		if strings.Contains(shareOut, forbidden) || strings.Contains(shareErr, forbidden) {
			t.Fatalf("--share leaks %q:\n%s", forbidden, shareOut+shareErr)
		}
	}
	verifyOut, verifyErr, code := runVend(t, vend, configPath, dataDir, "journal", "verify")
	if code != 0 {
		t.Fatalf("journal verify exit = %d: %s", code, h.redact(verifyOut+verifyErr))
	}
	if !strings.Contains(verifyOut, "journal ok") {
		t.Fatalf("journal verify output = %q, want journal ok", verifyOut)
	}

	// 11. No generated secret reaches the output of vend or the stub.
	h.step("11: secret scan")
	vendProc.stop()
	stubProc.stop()
	cli := strings.Join([]string{reportOut, reportErr, shareOut, shareErr, verifyOut, verifyErr}, "\n")
	for _, secret := range h.secrets {
		if strings.Contains(vendProc.log.String(), secret) {
			t.Fatalf("vend output leaks a generated secret")
		}
		if strings.Contains(stubProc.log.String(), secret) {
			t.Fatalf("smokestub output leaks a generated secret")
		}
		if strings.Contains(cli, secret) {
			t.Fatalf("vend CLI output leaks a generated secret")
		}
	}

	elapsed := time.Since(started)
	if elapsed > 5*time.Minute {
		t.Fatalf("end-to-end rehearsal took %s, want <= 5m", elapsed)
	}
	t.Logf("end-to-end rehearsal passed in %s", elapsed.Round(time.Second))
}

// repoRoot returns the repository root two levels above the package dir.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	root, err := filepath.Abs(filepath.Join(wd, "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.work")); err != nil {
		t.Fatalf("repository root %s has no go.work: %v", root, err)
	}
	return root
}

// exeName adds the Windows executable suffix.
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// buildBinary builds one package into out and returns out.
func buildBinary(t *testing.T, dir, pkg, out string) string {
	t.Helper()
	cmd := exec.Command("go", "-C", dir, "build", "-o", out, pkg)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build %s in %s: %v\n%s", pkg, dir, err, raw)
	}
	return out
}

// vendBinary returns the vend binary under test. With VEND_E2E_BIN set it
// uses that file and does not build vend; a relative value is resolved from
// the repository root. Without it vend is built from the checkout into
// fallback.
func vendBinary(t *testing.T, root, fallback string) string {
	t.Helper()
	configured := strings.TrimSpace(os.Getenv("VEND_E2E_BIN"))
	if configured == "" {
		return buildBinary(t, filepath.Join(root, "core"), "./cmd/vend", fallback)
	}
	path := configured
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("VEND_E2E_BIN %q: %v", configured, err)
	}
	if info.IsDir() {
		t.Fatalf("VEND_E2E_BIN %q is a directory, want the vend executable", configured)
	}
	return path
}

// freeAddr reserves and releases a loopback port.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a loopback port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("release %s: %v", addr, err)
	}
	return addr
}

// childEnv copies the process environment without any VENDING_* variable and
// adds the generated secrets for this run.
func childEnv(extra map[string]string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(kv), "VENDING_") {
			continue
		}
		env = append(env, kv)
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

// randomSecret generates one run-only secret.
func randomSecret(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("generate a secret: %v", err)
	}
	return hex.EncodeToString(buf)
}

// writeConfig writes a shadow config that points every provider at the stub.
func writeConfig(t *testing.T, path, stubBase string) {
	t.Helper()
	cfg := map[string]any{
		"apiVersion": "vending.lv24/v1",
		"mode":       "shadow",
		"gate":       map[string]any{"version": "gate-v1", "minPass": 0.55, "minBlock": 0.50},
		"outcome":    map[string]any{"horizonBars": 1, "feeBps": 0, "labelIntervalSec": 10},
		"providers": map[string]any{
			"decision": map[string]any{
				"id":              "decision-drex",
				"model":           "drex-e2e-v1",
				"profile":         "drex",
				"baseUrl":         stubBase,
				"secretRef":       "conn/drex/apiKey",
				"pricePerBillion": "60",
			},
			"marketdata": map[string]any{
				"id":      "marketdata-binance",
				"baseUrl": stubBase,
			},
		},
		"notify": map[string]any{
			"telegram": map[string]any{
				"enabled":   true,
				"chatId":    "1000001",
				"secretRef": "conn/telegram/botToken",
				"baseUrl":   stubBase,
			},
		},
		"signals": map[string]any{
			"trend": map[string]any{"enabled": false},
		},
		"arena": map[string]any{"enabled": false},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// newSignal builds one gate request body with a fresh UTC timestamp.
func newSignal(t *testing.T, id, symbol, price string) []byte {
	t.Helper()
	req := gateRequest{
		SignalID:   id,
		Instrument: gateInstrument{Venue: "binance", Symbol: symbol},
		Side:       "buy",
		IntentKind: "entry",
		Timeframe:  "1m",
		TS:         time.Now().UTC().Format(time.RFC3339Nano),
		Price:      price,
		Source:     "e2e",
		State:      map[string]float64{"rsi": 55.5},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("encode signal %s: %v", id, err)
	}
	return raw
}

// waitHTTP polls url until it answers 200, failing if the process exits.
func waitHTTP(t *testing.T, p *process, url string, timeout time.Duration, clean func(string) string) {
	t.Helper()
	client := &http.Client{Timeout: e2eTimeout}
	deadline := time.Now().Add(timeout)
	for {
		if !p.alive() {
			t.Fatalf("%s exited before %s answered\n%s", p.name, url, clean(p.log.String()))
		}
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not answer %s within %s\n%s", p.name, url, timeout, clean(p.log.String()))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// postGate sends one gate request with an optional bearer token.
func postGate(t *testing.T, client *http.Client, url, token string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build gate request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/gate: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read gate response: %v", err)
	}
	return resp.StatusCode, raw
}

// requireStatus fails unless got equals want.
func requireStatus(t *testing.T, got, want int, body []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("status = %d, want %d: %s", got, want, body)
	}
}

// decode unmarshals raw into v.
func decode(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %T: %v (%s)", v, err, raw)
	}
}

// setCtl switches the stub role statuses.
func setCtl(t *testing.T, client *http.Client, base, query string) {
	t.Helper()
	resp, err := client.Get(base + "/ctl?" + query)
	if err != nil {
		t.Fatalf("ctl %s: %v", query, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ctl %s status = %d: %s", query, resp.StatusCode, raw)
	}
}

// getStats reads the stub request counters.
func getStats(t *testing.T, client *http.Client, base string) stats {
	t.Helper()
	resp, err := client.Get(base + "/ctl/stats")
	if err != nil {
		t.Fatalf("read stats: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stats body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d: %s", resp.StatusCode, raw)
	}
	var s stats
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode stats: %v (%s)", err, raw)
	}
	return s
}

// waitStats polls the stub counters until pred is true.
func waitStats(t *testing.T, client *http.Client, base string, timeout time.Duration, what string, pred func(stats) bool) stats {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		s := getStats(t, client, base)
		if pred(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s (stats=%+v)", timeout, what, s)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// totalKlines sums the klines counters.
func totalKlines(s stats) int {
	total := 0
	for _, n := range s.Klines {
		total += n
	}
	return total
}

// runVend runs one CLI command and returns stdout, stderr and the exit code.
func runVend(t *testing.T, bin, configPath, dataDir string, args ...string) (string, string, int) {
	t.Helper()
	stdout, stderr, code, err := execVend(bin, configPath, dataDir, args...)
	if err != nil {
		t.Fatalf("vend %s: %v", strings.Join(args, " "), err)
	}
	return stdout, stderr, code
}

// lastStatus reads the current shadow status once.
func lastStatus(t *testing.T, bin, configPath, dataDir string) shadowStatus {
	t.Helper()
	out, errOut, code := runVend(t, bin, configPath, dataDir, "status", "--json")
	if code != 0 {
		t.Fatalf("vend status --json exit = %d: %s", code, errOut)
	}
	var st shadowStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("decode status: %v (%s)", err, out)
	}
	return st
}

// waitStatus polls `vend status --json` until pred is true.
func waitStatus(t *testing.T, bin, configPath, dataDir string, timeout time.Duration, what string, pred func(shadowStatus) bool) shadowStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st := lastStatus(t, bin, configPath, dataDir)
		if pred(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s (status=%+v)", timeout, what, st)
		}
		time.Sleep(time.Second)
	}
}
