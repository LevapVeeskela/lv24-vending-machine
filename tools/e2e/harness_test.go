// Failure diagnostics for the end-to-end rehearsal.
//
// The rehearsal is slow and flaky evidence is expensive: one unexplained
// failure cost a full re-run with no message. The harness therefore records
// every step with its elapsed time and, when the test fails, copies the
// evidence that explains the state into a separate directory outside the
// temporary work tree. On success that directory is removed.
//
// Everything written there passes through redact first: the generated
// provider secrets of the run never reach disk, even if a child process
// echoes them.

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

const (
	// diagnosticsDirPrefix names the directory kept after a failed run.
	diagnosticsDirPrefix = "vend-e2e-failure-"
	// journalDumpLimit is how many journal events the failure dump keeps.
	journalDumpLimit = 200
	// redactedValue replaces every generated secret in a dumped file.
	redactedValue = "<redacted>"
)

// harness owns one rehearsal run: its processes, its generated secrets and
// the failure directory. It is the single place that stops child processes,
// so evidence is collected before anything is killed.
type harness struct {
	t       *testing.T
	started time.Time
	dir     string

	secrets []string
	steps   []string
	procs   []*process
	// notice receives the one-line pointer to the kept directory; nil means
	// os.Stderr. Tests of the dump itself set it, so a passing run of the
	// package never prints a failure notice.
	notice io.Writer

	vendPath   string
	vend       *process
	stub       *process
	stubBase   string
	configPath string
	dataDir    string
}

// newHarness creates the failure directory and registers the one cleanup that
// stops the children, writes the diagnostics and removes the directory on
// success. It must be called after t.TempDir(), so the work tree is still
// intact when the evidence is copied.
func newHarness(t *testing.T, started time.Time) *harness {
	t.Helper()
	dir, err := os.MkdirTemp("", diagnosticsDirPrefix)
	if err != nil {
		t.Fatalf("create the failure diagnostics directory: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restrict the failure diagnostics directory: %v", err)
	}
	h := &harness{t: t, started: started, dir: dir}
	t.Cleanup(h.finish)
	return h
}

// addSecret registers one generated secret for redaction.
func (h *harness) addSecret(secret string) {
	if secret != "" {
		h.secrets = append(h.secrets, secret)
	}
}

// redact replaces every registered secret with a fixed marker.
func (h *harness) redact(s string) string {
	for _, secret := range h.secrets {
		s = strings.ReplaceAll(s, secret, redactedValue)
	}
	return s
}

// step logs the name of a scenario step and its time since the run started.
func (h *harness) step(name string) {
	line := fmt.Sprintf("%-32s t+%7.2fs", name, time.Since(h.started).Seconds())
	h.steps = append(h.steps, line)
	h.t.Logf("step %s", line)
}

// start launches one child, tracks it for the final stop and returns it.
func (h *harness) start(name, exe string, args, env []string) *process {
	h.t.Helper()
	p := startProcess(h.t, name, exe, args, env)
	h.procs = append(h.procs, p)
	return p
}

// finish stops the children and reports the evidence of a failed run.
func (h *harness) finish() {
	h.report(h.t.Failed())
}

// report is the testable half of finish: failed runs keep a redacted snapshot,
// passing runs leave nothing behind.
func (h *harness) report(failed bool) {
	if failed {
		// The journal is read through the CLI: freeze vend serve first so the
		// database is not being written while it is read.
		h.vend.stop()
		h.collect()
	}
	for i := len(h.procs) - 1; i >= 0; i-- {
		h.procs[i].stop()
	}
	if !failed {
		if err := os.RemoveAll(h.dir); err != nil {
			h.t.Logf("remove the failure diagnostics directory %s: %v", h.dir, err)
		}
		return
	}
	h.t.Logf("failure diagnostics written to %s", h.dir)
	out := h.notice
	if out == nil {
		out = os.Stderr
	}
	fmt.Fprintf(out, "\nE2E failure diagnostics: %s\n", h.dir)
}

// collect copies the evidence of the failed run. Every write is redacted.
func (h *harness) collect() {
	h.writeFile("vend-serve.stdout.log", processStdout(h.vend))
	h.writeFile("vend-serve.stderr.log", processStderr(h.vend))
	h.writeFile("smokestub.stdout.log", processStdout(h.stub))
	h.writeFile("smokestub.stderr.log", processStderr(h.stub))
	h.collectEarlier()
	h.collectStats()
	h.collectCLI("vend-status", "status", "--json")
	h.collectCLI("vend-journal-tail", "journal", "tail", "--json",
		"--limit", fmt.Sprintf("%d", journalDumpLimit))
	h.collectConfig()
	h.writeFile("summary.txt", h.summary())
}

// collectEarlier saves the output of every process that was replaced during
// the run, such as the vend serve instance stopped by the restart step. A
// failure after the restart is often explained by what ran before it.
func (h *harness) collectEarlier() {
	for i, p := range h.procs {
		if p == h.vend || p == h.stub {
			continue
		}
		prefix := fmt.Sprintf("earlier-%02d-%s", i, p.name)
		h.writeFile(prefix+".stdout.log", processStdout(p))
		h.writeFile(prefix+".stderr.log", processStderr(p))
	}
}

// collectStats saves the raw /ctl/stats answer of the stub.
func (h *harness) collectStats() {
	if h.stubBase == "" {
		h.writeFile("smokestub-stats.json", "not collected: the stub never started\n")
		return
	}
	client := &http.Client{Timeout: e2eTimeout}
	resp, err := client.Get(h.stubBase + "/ctl/stats")
	if err != nil {
		h.writeFile("smokestub-stats.json", "GET /ctl/stats failed: "+err.Error()+"\n")
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.writeFile("smokestub-stats.json", "read /ctl/stats failed: "+err.Error()+"\n")
		return
	}
	if resp.StatusCode != http.StatusOK {
		h.writeFile("smokestub-stats.json",
			fmt.Sprintf("GET /ctl/stats returned %d: %s\n", resp.StatusCode, raw))
		return
	}
	h.writeFile("smokestub-stats.json", string(raw))
}

// collectCLI saves the output of one vend command. The stdout file keeps the
// JSON untouched; the exit code and stderr go to a sibling file.
func (h *harness) collectCLI(prefix string, args ...string) {
	if h.vendPath == "" || h.configPath == "" || h.dataDir == "" {
		h.writeFile(prefix+".json",
			"not collected: the binary, config or data directory is unknown\n")
		return
	}
	stdout, stderr, code, err := execVend(h.vendPath, h.configPath, h.dataDir, args...)
	if err != nil {
		h.writeFile(prefix+".json",
			fmt.Sprintf("vend %s failed: %v\n", strings.Join(args, " "), err))
	} else {
		h.writeFile(prefix+".json", stdout)
	}
	h.writeFile(prefix+".stderr.log", fmt.Sprintf("exit: %d\n%s", code, stderr))
}

// collectConfig saves the config the server ran with. It holds secret
// references only, and the redaction pass guarantees nothing else leaks.
func (h *harness) collectConfig() {
	if h.configPath == "" {
		h.writeFile("config.json", "not collected: the config path is unknown\n")
		return
	}
	raw, err := os.ReadFile(h.configPath)
	if err != nil {
		h.writeFile("config.json", "read the config failed: "+err.Error()+"\n")
		return
	}
	h.writeFile("config.json", string(raw))
}

// summary lists the run facts and the steps that were reached.
func (h *harness) summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "test: %s\n", h.t.Name())
	fmt.Fprintf(&b, "started: %s\n", h.started.Format(time.RFC3339Nano))
	fmt.Fprintf(&b, "elapsed: %.2fs\n", time.Since(h.started).Seconds())
	fmt.Fprintf(&b, "platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "vend: %s\n", h.vendPath)
	fmt.Fprintf(&b, "stub: %s\n", h.stubBase)
	fmt.Fprintf(&b, "config: %s\n", h.configPath)
	fmt.Fprintf(&b, "data dir: %s\n", h.dataDir)
	fmt.Fprintf(&b, "steps:\n")
	for _, s := range h.steps {
		fmt.Fprintf(&b, "  %s\n", s)
	}
	return b.String()
}

// writeFile writes one redacted artifact with owner-only permissions.
func (h *harness) writeFile(name, content string) {
	path := filepath.Join(h.dir, name)
	if err := os.WriteFile(path, []byte(h.redact(content)), 0o600); err != nil {
		h.t.Logf("write %s: %v", path, err)
	}
}

// processStdout returns the captured stdout of p, or empty for a process that
// never started.
func processStdout(p *process) string {
	if p == nil {
		return ""
	}
	return p.stdout.String()
}

// processStderr returns the captured stderr of p, or empty for a process that
// never started.
func processStderr(p *process) string {
	if p == nil {
		return ""
	}
	return p.stderr.String()
}

// execVend runs one CLI command and returns stdout, stderr, the exit code and
// a start error. It never calls t.Fatalf, so the failure dump can use it.
func execVend(bin, configPath, dataDir string, args ...string) (string, string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	full := append(append([]string{}, args...), "--config", configPath, "--data-dir", dataDir)
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Env = childEnv(nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return stdout.String(), stderr.String(), 0, err
		}
		return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
	}
	return stdout.String(), stderr.String(), 0, nil
}

// lockedBuffer is an io.Writer safe for concurrent child output.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
