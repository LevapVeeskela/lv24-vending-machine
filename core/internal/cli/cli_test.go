package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/registry"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/store/sqlite"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

func testEnv(t *testing.T) (Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	return Env{
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: func(name string) string {
			// doctor requires the loopback API token because serve cannot
			// start without it; a missing-token test overrides Getenv.
			if name == localAPITokenEnv {
				return "test-token"
			}
			return ""
		},
		Version: "0.1.0-m0-test",
		NewRegistry: func(cfg config.Config, logger *slog.Logger) (*registry.Registry, error) {
			return registry.New(), nil
		},
		OpenJournal: func(path string) (ports.Journal, io.Closer, error) {
			st, err := sqlite.Open(context.Background(), path)
			if err != nil {
				return nil, nil, err
			}
			return st, st, nil
		},
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
	if got := stdout.String(); !strings.Contains(got, "journal ok: 3 events") {
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
	rejectReq := writeSignalRequest(t, dir, req, "sig-test-002")
	if code := runArgs(env, "gate", "test",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", rejectReq, "--fixture", rejectFix); code != 7 {
		t.Fatalf("gate test reject = %d, want 7, stderr=%s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "verify",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal verify = %d, stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "journal ok: 6 events" {
		t.Fatalf("journal stdout = %q, want 6 events after allow plus reject", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "head",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal head = %d, stderr=%s", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "seq: 6") {
		t.Fatalf("journal head stdout = %q, want seq 6", got)
	}

	stdout.Reset()
	stderr.Reset()
	env.NewRegistry = liveRegistry(t, okDecision(), okCandles(), nil)
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
	for _, want := range []string{`"code"`, `"message"`, `"hint"`, `"docsUrl"`, `"retryable"`} {
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

func TestNilFactories(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	env.NewRegistry = nil
	env.OpenJournal = nil
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	dataDir := filepath.Join(dir, "data")

	if code := runArgs(env, "init",
		"--config", cfgPath, "--data-dir", dataDir); code != 1 {
		t.Fatalf("init without journal factory = %d, want 1", code)
	}
	if got := stderr.String(); !strings.Contains(got, "journal factory is not configured") {
		t.Fatalf("init stderr misses factory error: %q", got)
	}

	// The config file was still written; doctor must fail on the registry.
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "doctor",
		"--config", cfgPath, "--data-dir", dataDir); code != 1 {
		t.Fatalf("doctor without registry factory = %d, want 1", code)
	}
	if got := stdout.String(); !strings.Contains(got, "registry factory is not configured") {
		t.Fatalf("doctor stdout misses factory error: %q", got)
	}
}

func initTestEnv(t *testing.T) (Env, *bytes.Buffer, *bytes.Buffer, string, string) {
	t.Helper()
	env, stdout, stderr := testEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	dataDir := filepath.Join(dir, "data")
	if code := runArgs(env, "init", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("init = %d, stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	return env, stdout, stderr, cfgPath, dataDir
}

func testdataRel(names ...string) string {
	parts := append([]string{"..", "..", "cmd", "vend", "testdata"}, names...)
	return filepath.Join(parts...)
}

// writeSignalRequest copies a gate request fixture with a new signalId so
// idempotency treats it as a distinct intent.
func writeSignalRequest(t *testing.T, dir, src, signalID string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read request fixture: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode request fixture: %v", err)
	}
	v["signalId"] = signalID
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	path := filepath.Join(dir, signalID+".json")
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return path
}

// writeRequestPrice copies a gate request fixture with a new price, keeping
// the signalId so the journal sees a conflicting body.
func writeRequestPrice(t *testing.T, dir, src, price string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read request fixture: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode request fixture: %v", err)
	}
	v["price"] = price
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	path := filepath.Join(dir, "changed-request.json")
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return path
}

func TestWriteFileNew(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	out := filepath.Join(dir, "out.json")

	if err := writeFileNew(out, []byte("one"), 0o600, newFileOptions{DataDir: dataDir}); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	if raw, err := os.ReadFile(out); err != nil || string(raw) != "one" {
		t.Fatalf("new file = %q, err=%v", raw, err)
	}

	err := writeFileNew(out, []byte("two"), 0o600, newFileOptions{DataDir: dataDir})
	if !errors.Is(err, errOutputExists) {
		t.Fatalf("existing file error = %v, want errOutputExists", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("existing file error misses the --force hint: %v", err)
	}
	if raw, _ := os.ReadFile(out); string(raw) != "one" {
		t.Fatalf("refused write changed the file: %q", raw)
	}

	if err := writeFileNew(out, []byte("two"), 0o600, newFileOptions{DataDir: dataDir, Force: true}); err != nil {
		t.Fatalf("forced write: %v", err)
	}
	if raw, _ := os.ReadFile(out); string(raw) != "two" {
		t.Fatalf("forced write = %q, want two", raw)
	}

	inside := filepath.Join(dataDir, "vending.db")
	err = writeFileNew(inside, []byte("x"), 0o600, newFileOptions{DataDir: dataDir, Force: true})
	if !errors.Is(err, errOutputInDataDir) {
		t.Fatalf("data-dir path error = %v, want errOutputInDataDir", err)
	}
	if _, statErr := os.Stat(inside); !os.IsNotExist(statErr) {
		t.Fatalf("data-dir path was written: %v", statErr)
	}

	// A sibling directory whose name starts with the data dir name is not
	// inside it; the check must compare path elements, not string prefixes.
	siblingDir := filepath.Join(dir, "data-out")
	if err := os.MkdirAll(siblingDir, 0o755); err != nil {
		t.Fatalf("create sibling dir: %v", err)
	}
	if err := writeFileNew(filepath.Join(siblingDir, "out.json"), []byte("ok"), 0o600,
		newFileOptions{DataDir: dataDir}); err != nil {
		t.Fatalf("sibling path was refused: %v", err)
	}

	// Escaping the data dir with ".." resolves outside and is allowed.
	if err := writeFileNew(filepath.Join(dataDir, "..", "escaped.json"), []byte("ok"), 0o600,
		newFileOptions{DataDir: dataDir}); err != nil {
		t.Fatalf("escaped path was refused: %v", err)
	}

	if err := writeFileNew("", []byte("x"), 0o600, newFileOptions{}); err == nil {
		t.Fatal("empty output path was accepted")
	}
}

func TestVersionJSON(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	if code := runArgs(env, "version", "--json"); code != 0 {
		t.Fatalf("version --json = %d, stderr=%s", code, stderr.String())
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &v); err != nil {
		t.Fatalf("decode version JSON: %v", err)
	}
	if v.Version != "0.1.0-m0-test" {
		t.Fatalf("version = %q", v.Version)
	}
}

func TestConfigPathValidate(t *testing.T) {
	env, stdout, stderr, cfgPath, _ := initTestEnv(t)

	if code := runArgs(env, "config", "path", "--config", cfgPath); code != 0 {
		t.Fatalf("config path = %d, stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != cfgPath {
		t.Fatalf("config path = %q, want %q", got, cfgPath)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "config", "validate", "--config", cfgPath); code != 0 {
		t.Fatalf("config validate = %d, stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "config ok" {
		t.Fatalf("config validate stdout = %q", got)
	}
}

func TestConfigValidateMissingFileHint(t *testing.T) {
	env, _, stderr := testEnv(t)
	path := filepath.Join(t.TempDir(), "missing.json")

	if code := runArgs(env, "config", "validate", "--config", path); code != exitUsage {
		t.Fatalf("config validate missing = %d, want %d", code, exitUsage)
	}
	got := stderr.String()
	if !strings.Contains(got, "run vend init to create the config") {
		t.Fatalf("stderr misses the missing-file hint: %q", got)
	}
	if strings.Contains(got, "fix the JSON syntax") {
		t.Fatalf("stderr still suggests fixing JSON syntax: %q", got)
	}
}

func TestDoctorJSON(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	env.NewRegistry = liveRegistry(t, okDecision(), okCandles(), nil)

	if code := runArgs(env, "doctor", "--json", "--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("doctor --json = %d, stderr=%s", code, stderr.String())
	}
	var res struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode doctor JSON: %v", err)
	}
	if !res.OK {
		t.Fatalf("doctor ok = false: %+v", res.Checks)
	}
	if len(res.Checks) != 7 {
		t.Fatalf("doctor checks = %d, want 7", len(res.Checks))
	}
}

func TestGateTestJSON(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)

	args := []string{"gate", "test", "--json",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", testdataRel("gate-request.json"),
		"--fixture", testdataRel("decision-response.json")}
	if code := runArgs(env, args...); code != 0 {
		t.Fatalf("gate test --json = %d, stderr=%s", code, stderr.String())
	}
	var verdict struct {
		Verdict string `json:"verdict"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &verdict); err != nil {
		t.Fatalf("decode verdict JSON: %v", err)
	}
	if verdict.Verdict != "ALLOW" {
		t.Fatalf("verdict = %+v", verdict)
	}
}

func TestExitIntentNoProvider(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	env.NewRegistry = func(config.Config, *slog.Logger) (*registry.Registry, error) {
		t.Fatal("provider registry must not be built for exit intents")
		return nil, nil
	}

	args := []string{"gate", "test",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", testdataRel("gate-request-exit.json")}
	if code := runArgs(env, args...); code != 0 {
		t.Fatalf("gate test exit = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "SKIP") {
		t.Fatalf("exit verdict stdout misses SKIP: %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "verify",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal verify = %d, stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "journal ok: 2 events" {
		t.Fatalf("journal stdout = %q, want 2 events for exit", got)
	}
}

func TestJournalHead(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)

	if code := runArgs(env, "journal", "head",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal head empty = %d, stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "journal empty" {
		t.Fatalf("journal head empty stdout = %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "head", "--json",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal head --json empty = %d, stderr=%s", code, stderr.String())
	}
	var empty struct {
		Empty bool `json:"empty"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &empty); err != nil || !empty.Empty {
		t.Fatalf("journal head JSON empty = %q, err=%v", stdout.String(), err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "gate", "test",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", testdataRel("gate-request.json"),
		"--fixture", testdataRel("decision-response.json")); code != 0 {
		t.Fatalf("gate test = %d, stderr=%s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "head",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal head = %d, stderr=%s", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "seq: 3") || !strings.Contains(got, "hash: ") {
		t.Fatalf("journal head stdout = %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "head", "--json",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal head --json = %d, stderr=%s", code, stderr.String())
	}
	var head struct {
		Seq  int64  `json:"seq"`
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &head); err != nil {
		t.Fatalf("decode head JSON: %v", err)
	}
	if head.Seq != 3 || head.Hash == "" {
		t.Fatalf("head = %+v, want seq 3 with hash", head)
	}

	if code := runArgs(env, "journal", "head",
		"--data-dir", filepath.Join(dataDir, "missing")); code != 3 {
		t.Fatalf("journal head missing db = %d, want 3", code)
	}
}

func TestGateTestSaveRaw(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	rawPath := filepath.Join(t.TempDir(), "raw.json")

	args := []string{"gate", "test",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", testdataRel("gate-request.json"),
		"--fixture", testdataRel("decision-response.json"),
		"--save-raw", rawPath}
	if code := runArgs(env, args...); code != 0 {
		t.Fatalf("gate test --save-raw = %d, stderr=%s", code, stderr.String())
	}
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read saved raw: %v", err)
	}
	var v struct {
		Answers map[string]any `json:"answers"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || len(v.Answers) != 4 {
		t.Fatalf("saved raw = %q, err=%v", raw, err)
	}
	entries, err := os.ReadDir(filepath.Dir(rawPath))
	if err != nil {
		t.Fatalf("read raw dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "raw.json" {
		t.Fatalf("raw dir entries = %v, want only raw.json (no temp leftovers)", entries)
	}

	// Idempotent replay has no response to save.
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, args...); code != 1 {
		t.Fatalf("gate test --save-raw replay = %d, want 1, stderr=%s", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "raw response unavailable") {
		t.Fatalf("replay stderr misses hint: %q", got)
	}
	_ = stdout
}

func TestInvalidFixtureNoJournal(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)

	args := []string{"gate", "test",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", testdataRel("gate-request.json"),
		"--fixture", testdataRel("decision-response-invalid.json")}
	if code := runArgs(env, args...); code != 2 {
		t.Fatalf("gate test invalid fixture = %d, want 2, stderr=%s", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "invalid fixture") {
		t.Fatalf("stderr misses 'invalid fixture': %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "verify",
		"--config", cfgPath, "--data-dir", dataDir); code != 0 {
		t.Fatalf("journal verify = %d, stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "journal ok: 0 events" {
		t.Fatalf("journal stdout = %q, want empty chain", got)
	}
}

func TestHelp(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		wants []string
	}{
		{
			name:  "long flag",
			args:  []string{"--help"},
			wants: []string{"usage: vend <command>", "gate test", "--data-dir", "exit codes"},
		},
		{
			name:  "short flag",
			args:  []string{"-h"},
			wants: []string{"usage: vend <command>", "journal tail"},
		},
		{
			name:  "help command",
			args:  []string{"help"},
			wants: []string{"usage: vend <command>", "outcomes label", "signals estimate"},
		},
		{
			name:  "help topic",
			args:  []string{"help", "gate"},
			wants: []string{"gate test", "gate report"},
		},
		{
			name:  "command flag",
			args:  []string{"gate", "--help"},
			wants: []string{"subcommands:", "offline fixture"},
		},
		{
			name:  "subcommand flag",
			args:  []string{"gate", "test", "--help"},
			wants: []string{"--request FILE", "--fixture FILE", "--save-raw FILE"},
		},
		{
			name:  "short subcommand flag with global",
			args:  []string{"journal", "tail", "--json", "-h"},
			wants: []string{"--kind KIND", "--limit N"},
		},
		{
			name:  "config subcommand falls back to the parent page",
			args:  []string{"config", "validate", "--help"},
			wants: []string{"config path|get KEY|validate", "validate  parse and validate"},
		},
		{
			name:  "status page",
			args:  []string{"status", "--help"},
			wants: []string{"policyHash", "updatedAt", "--data-dir"},
		},
		{
			name:  "init topic",
			args:  []string{"help", "init"},
			wants: []string{"--force", "next commands"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stdout, stderr := testEnv(t)
			if code := runArgs(env, tc.args...); code != exitOK {
				t.Fatalf("%v = %d, want 0, stderr=%s", tc.args, code, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("%v wrote to stderr: %s", tc.args, stderr.String())
			}
			got := stdout.String()
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Fatalf("%v output misses %q:\n%s", tc.args, want, got)
				}
			}
			if strings.Contains(got, "unknown") {
				t.Fatalf("%v output reports an unknown command:\n%s", tc.args, got)
			}
		})
	}
}

func TestHelpListsEveryCommandAndFlag(t *testing.T) {
	env, stdout, _ := testEnv(t)
	if code := runArgs(env, "--help"); code != exitOK {
		t.Fatalf("--help = %d", code)
	}
	got := stdout.String()
	for _, want := range []string{
		"version", "init", "config path|get KEY|validate", "doctor",
		"gate test", "gate report", "journal verify", "journal head", "journal tail",
		"signals estimate", "outcomes label", "status", "serve", "help",
		"--force", "--live", "--symbol", "--save-raw", "--request", "--fixture",
		"--policy", "--include-backfill", "--share", "--out", "--kind", "--limit",
		"--days", "--candles", "--addr", "--config", "--data-dir", "--json",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("root help misses %q:\n%s", want, got)
		}
	}
}

func TestHelpUnknownTopic(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	if code := runArgs(env, "help", "nope"); code != exitUsage {
		t.Fatalf("help nope = %d, want %d", code, exitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("help nope stdout = %q, want empty", stdout.String())
	}
	got := stderr.String()
	for _, want := range []string{"unknown help topic", "run vend help"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help nope stderr misses %q: %q", want, got)
		}
	}
}

func TestInitNextSteps(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	dataDir := filepath.Join(dir, "data")
	if code := runArgs(env, "init", "--config", cfgPath, "--data-dir", dataDir); code != exitOK {
		t.Fatalf("init = %d, stderr=%s", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"config: " + cfgPath,
		"data dir: " + dataDir,
		"database: " + config.DBPath(dataDir),
		"next steps:",
		"vend config validate --config " + cfgPath,
		"vend doctor --config " + cfgPath + " --data-dir " + dataDir,
		"vend gate test",
		"vend serve --config " + cfgPath + " --data-dir " + dataDir,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("init output misses %q:\n%s", want, got)
		}
	}

	stdout.Reset()
	stderr.Reset()
	other := filepath.Join(dir, "other.json")
	if code := runArgs(env, "init", "--json", "--config", other,
		"--data-dir", filepath.Join(dir, "other-data")); code != exitOK {
		t.Fatalf("init --json = %d, stderr=%s", code, stderr.String())
	}
	var res struct {
		NextSteps []string `json:"nextSteps"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode init JSON %q: %v", stdout.String(), err)
	}
	if len(res.NextSteps) != 4 || !strings.Contains(res.NextSteps[3], "serve") {
		t.Fatalf("init JSON nextSteps = %v, want four steps ending with serve", res.NextSteps)
	}
}

func TestGateConflictExitCode(t *testing.T) {
	env, stdout, stderr, cfgPath, dataDir := initTestEnv(t)
	req := testdataRel("gate-request.json")
	fixture := testdataRel("decision-response.json")
	first := []string{"gate", "test",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", req, "--fixture", fixture}
	if code := runArgs(env, first...); code != exitOK {
		t.Fatalf("gate test = %d, stderr=%s", code, stderr.String())
	}

	// Same signalId with a changed body is a state conflict, not a general error.
	changed := writeRequestPrice(t, t.TempDir(), req, "67432.51")
	jsonArgs := []string{"gate", "test", "--json",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", changed, "--fixture", fixture}
	if code := runArgs(env, jsonArgs...); code != exitConflict {
		t.Fatalf("gate test conflict = %d, want %d, stderr=%s",
			code, exitConflict, stderr.String())
	}
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatalf("decode conflict envelope %q: %v", stderr.String(), err)
	}
	if envelope.Code != codeConflict {
		t.Fatalf("conflict code = %q, want %q", envelope.Code, codeConflict)
	}
	if strings.HasPrefix(envelope.Message, "conflict: ") || strings.Contains(envelope.Message, "conflict: conflict") {
		t.Fatalf("conflict message is double-prefixed: %q", envelope.Message)
	}
	if !strings.Contains(envelope.Message, "signalId already used") {
		t.Fatalf("conflict message = %q, want the flow reason", envelope.Message)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "gate", "test",
		"--config", cfgPath, "--data-dir", dataDir,
		"--request", changed, "--fixture", fixture); code != exitConflict {
		t.Fatalf("gate test conflict human = %d, want %d, stderr=%s",
			code, exitConflict, stderr.String())
	}
	human := stderr.String()
	if strings.Contains(human, "vend: conflict:") {
		t.Fatalf("human conflict error is double-prefixed: %q", human)
	}
	if !strings.Contains(human, "signalId already used") {
		t.Fatalf("human conflict error misses the reason: %q", human)
	}
}
