package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv(t *testing.T) (Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	return Env{
		Stdout:  &stdout,
		Stderr:  &stderr,
		Getenv:  func(string) string { return "" },
		Version: "0.1.0-m0-test",
	}, &stdout, &stderr
}

func runArgs(env Env, args ...string) int {
	env.Args = args
	return Run(env)
}

func TestCLIEndToEnd(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	dataDir := filepath.Join(dir, "data")
	req := filepath.Join("..", "..", "cmd", "vend", "testdata", "gate-request.json")
	allowFix := filepath.Join("..", "..", "cmd", "vend", "testdata", "decision-response.json")
	rejectFix := filepath.Join("..", "..", "cmd", "vend", "testdata", "decision-response-reject.json")

	if code := runArgs(env, "init", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("init = %d, stderr=%s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "gate", "test",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", req, "--fixture", allowFix); code != 0 {
		t.Fatalf("gate test allow = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "ALLOW") {
		t.Fatalf("verdict stdout misses ALLOW: %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "verify", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal verify = %d, stderr=%s", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "journal ok: 1 events") {
		t.Fatalf("journal stdout = %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "config", "get", "gate.minPass", "--config", cfgPath); code != 0 {
		t.Fatalf("config get = %d, stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "0.55" {
		t.Fatalf("config get gate.minPass = %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "gate", "test",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", req, "--fixture", rejectFix); code != 7 {
		t.Fatalf("gate test reject = %d, want 7, stderr=%s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "doctor", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("doctor = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "init", "--config", cfgPath, "--data-dir", dataDir); code != 4 {
		t.Fatalf("second init = %d, want 4, stderr=%s", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "--force") {
		t.Fatalf("conflict stderr misses hint: %q", got)
	}
}

func TestErrorFormat(t *testing.T) {
	env, _, stderr := testEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	if code := runArgs(env, "journal", "verify", "--json",
		"--config", cfgPath, "--data-dir", filepath.Join(dir, "data")); code != 3 {
		t.Fatalf("journal verify missing db = %d", code)
	}
	got := stderr.String()
	for _, want := range []string{`"code"`, `"message"`, `"hint"`, `"retryable"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("json error misses %s: %q", want, got)
		}
	}

	stderr.Reset()
	if code := runArgs(env, "config", "get", "gate.minPass", "--config", cfgPath); code != 3 {
		t.Fatalf("config get missing = %d", code)
	}
	if got := stderr.String(); !strings.HasPrefix(got, "vend: ") || !strings.Contains(got, "hint: ") {
		t.Fatalf("human error format = %q", got)
	}
}

func TestVersion(t *testing.T) {
	env, stdout, _ := testEnv(t)
	if code := runArgs(env, "version"); code != 0 {
		t.Fatalf("version = %d", code)
	}
	if got := stdout.String(); !strings.Contains(got, "vend 0.1.0-m0-test") {
		t.Fatalf("version stdout = %q", got)
	}
}
