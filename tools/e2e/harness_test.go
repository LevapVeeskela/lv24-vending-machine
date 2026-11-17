// These tests run without VEND_E2E: they cover the failure dump itself, which
// is the part that must stay trustworthy when the rehearsal misbehaves.

package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testSecret is a stand-in for a generated provider secret.
const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef"

// TestFailureDumpRedactsSecrets: every artifact passes through redact, so a
// secret echoed by a child process never reaches the evidence directory.
func TestFailureDumpRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	h := &harness{t: t, dir: dir}
	h.addSecret(testSecret)
	h.addSecret("second-secret-value")

	h.writeFile("vend-serve.stderr.log", "provider rejected token "+testSecret+"\n")
	h.writeFile("config.json",
		`{"secretRef":"conn/drex/apiKey","leaked":"second-secret-value"}`)
	h.writeFile("smokestub-stats.json", `{"decision":1,"telegram":2}`)

	for _, name := range []string{"vend-serve.stderr.log", "config.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(raw), testSecret) {
			t.Fatalf("%s still contains the generated secret", name)
		}
		if strings.Contains(string(raw), "second-secret-value") {
			t.Fatalf("%s still contains the second generated secret", name)
		}
		if !strings.Contains(string(raw), redactedValue) {
			t.Fatalf("%s has no %q marker:\n%s", name, redactedValue, raw)
		}
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("stat %s: %v", name, err)
		} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			// Windows has no POSIX permission bits: Go reports 0666 for every
			// writable file there, so the check only means something elsewhere.
			t.Fatalf("%s permissions = %v, want 0600", name, info.Mode().Perm())
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "smokestub-stats.json"))
	if err != nil {
		t.Fatalf("read smokestub-stats.json: %v", err)
	}
	if strings.Contains(string(raw), redactedValue) {
		t.Fatalf("a clean artifact was rewritten:\n%s", raw)
	}
}

// TestFailureDumpSkipsEmptySecrets: an unset secret must not redact every
// character in the dump.
func TestFailureDumpSkipsEmptySecrets(t *testing.T) {
	h := &harness{t: t}
	h.addSecret("")

	if got := h.redact("plain text"); got != "plain text" {
		t.Fatalf("redact with an empty secret = %q, want the input", got)
	}
}

// TestFailureDumpStepRecordsNameAndTime: the step log is the timeline used to
// place a failure, so it must carry the name and the elapsed time.
func TestFailureDumpStepRecordsNameAndTime(t *testing.T) {
	h := &harness{t: t, started: time.Now()}
	h.step("7: auth alert")

	if len(h.steps) != 1 {
		t.Fatalf("steps = %v, want one entry", h.steps)
	}
	line := h.steps[0]
	if !strings.Contains(line, "7: auth alert") || !strings.Contains(line, "t+") {
		t.Fatalf("step line = %q, want the name and the elapsed time", line)
	}
}

// TestFailureDumpKeepsFailedRunAndDropsPassingRun: the directory survives a
// failure and is removed after a success.
func TestFailureDumpKeepsFailedRunAndDropsPassingRun(t *testing.T) {
	failedDir := t.TempDir()
	failed := &harness{t: t, dir: failedDir, started: time.Now()}
	failed.writeFile("vend-serve.stdout.log", "starting on http://127.0.0.1:1\n")
	failed.report(true)
	if _, err := os.Stat(filepath.Join(failedDir, "summary.txt")); err != nil {
		t.Fatalf("a failed run kept no summary: %v", err)
	}

	passedDir := t.TempDir()
	passed := &harness{t: t, dir: passedDir, started: time.Now()}
	passed.writeFile("vend-serve.stdout.log", "starting\n")
	passed.report(false)
	if _, err := os.Stat(passedDir); !os.IsNotExist(err) {
		t.Fatalf("a passing run left %s behind: %v", passedDir, err)
	}
}
