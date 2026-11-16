// Package cli implements the vend command tree on stdlib flag only.
//
// Exit codes: 0 success, 1 general error, 2 usage/validation,
// 3 not found, 4 state conflict, 5 forbidden (no secret, live),
// 6 provider error, 7 gate reject, 8 timeout, 9 approval.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/gate"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/registry"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/resilience"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// Exit codes.
const (
	exitOK        = 0
	exitGeneral   = 1
	exitUsage     = 2
	exitNotFound  = 3
	exitConflict  = 4
	exitForbidden = 5
	exitProvider  = 6
	exitReject    = 7
	exitTimeout   = 8
	exitApproval  = 9
)

// Machine-readable error codes for the {code, ...} envelope.
const (
	codeGeneral   = "general"
	codeUsage     = "usage"
	codeNotFound  = "not_found"
	codeConflict  = "conflict"
	codeForbidden = "forbidden"
	codeProvider  = "provider"
	codeReject    = "gate_rejected"
	codeTimeout   = "timeout"
)

// docsURL points at the CLI contract; it is included in every JSON
// error envelope (spec 03 §3) but never in human-readable output.
const docsURL = "https://github.com/LevapVeeskela/lv24-vending-machine/blob/main/docs/spec/03-core-cli.md"

// Env carries the process environment for Run. No os.Exit happens inside.
//
// NewRegistry and OpenJournal are composition-root factories filled by
// core/cmd/vend: the cli package never imports providers or the journal
// store itself.
type Env struct {
	Args        []string
	Stdout      io.Writer
	Stderr      io.Writer
	Getenv      func(string) string
	Version     string
	NewRegistry func(cfg config.Config, logger *slog.Logger) (*registry.Registry, error)
	OpenJournal func(path string) (ports.Journal, io.Closer, error)
}

// runner holds resolved I/O for one Run call.
type runner struct {
	stdout          io.Writer
	stderr          io.Writer
	getenv          func(string) string
	ver             string
	registryFactory func(cfg config.Config, logger *slog.Logger) (*registry.Registry, error)
	openJournal     func(path string) (ports.Journal, io.Closer, error)
}

// errBody is the structured error envelope.
type errBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Hint      string `json:"hint"`
	DocsURL   string `json:"docsUrl,omitempty"`
	Retryable bool   `json:"retryable"`
	Details   any    `json:"details,omitempty"`
}

// Run executes the CLI and returns the process exit code.
func Run(env Env) int {
	r := &runner{
		stdout:          env.Stdout,
		stderr:          env.Stderr,
		getenv:          env.Getenv,
		ver:             env.Version,
		registryFactory: env.NewRegistry,
		openJournal:     env.OpenJournal,
	}
	if r.stdout == nil {
		r.stdout = os.Stdout
	}
	if r.stderr == nil {
		r.stderr = os.Stderr
	}
	if r.getenv == nil {
		r.getenv = os.Getenv
	}
	args := env.Args
	if len(args) < 1 {
		r.usage()
		return exitUsage
	}
	switch args[0] {
	case "version":
		return r.cmdVersion(args[1:])
	case "init":
		return r.cmdInit(args[1:])
	case "config":
		return r.cmdConfig(args[1:])
	case "doctor":
		return r.cmdDoctor(args[1:])
	case "gate":
		return r.cmdGate(args[1:])
	case "journal":
		return r.cmdJournal(args[1:])
	default:
		r.fail(false, exitUsage, codeUsage,
			fmt.Sprintf("unknown command %q", args[0]),
			"run vend with one of: version, init, config, doctor, gate, journal", false, nil)
		return exitUsage
	}
}

func (r *runner) usage() {
	fmt.Fprintln(r.stderr, "usage: vend <command> [options]")
	fmt.Fprintln(r.stderr, "")
	fmt.Fprintln(r.stderr, "commands:")
	fmt.Fprintln(r.stderr, "  version [--json]")
	fmt.Fprintln(r.stderr, "  init [--config FILE] [--data-dir DIR] [--force] [--json]")
	fmt.Fprintln(r.stderr, "  config path|get KEY|validate [--config FILE] [--data-dir DIR] [--json]")
	fmt.Fprintln(r.stderr, "  doctor [--config FILE] [--data-dir DIR] [--json]")
	fmt.Fprintln(r.stderr, "  gate test --request FILE [--fixture FILE] [--config FILE] [--data-dir DIR] [--json]")
	fmt.Fprintln(r.stderr, "  journal verify [--config FILE] [--data-dir DIR] [--json]")
}

// fail writes the error envelope to stderr and returns exit.
func (r *runner) fail(jsonMode bool, exit int, code, msg, hint string, retryable bool, details any) int {
	if jsonMode {
		raw, err := json.Marshal(errBody{
			Code: code, Message: msg, Hint: hint, DocsURL: docsURL, Retryable: retryable, Details: details,
		})
		if err == nil {
			fmt.Fprintln(r.stderr, string(raw))
			return exit
		}
	}
	fmt.Fprintf(r.stderr, "vend: %s\n", msg)
	fmt.Fprintf(r.stderr, "hint: %s\n", hint)
	return exit
}

// globals are the flags every command accepts, in any position.
type globals struct {
	config  string
	dataDir string
	json    bool
}

// splitGlobals extracts --config/--data-dir/--json (both --flag value and
// --flag=value forms, single or double dash) from anywhere in args.
// The rest is returned untouched; "--" stops extraction. Unknown flags
// stay in rest for the command flag set to reject.
func splitGlobals(args []string) (globals, []string, error) {
	var g globals
	var rest []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		name, val, hasVal := splitFlagArg(a)
		switch name {
		case "config", "data-dir":
			if !hasVal {
				if i+1 >= len(args) {
					return g, nil, fmt.Errorf("missing value for --%s", name)
				}
				val = args[i+1]
				i++
			}
			if name == "config" {
				g.config = val
			} else {
				g.dataDir = val
			}
		case "json":
			if hasVal {
				if val != "true" && val != "false" {
					return g, nil, fmt.Errorf("invalid value %q for --json", val)
				}
				g.json = val == "true"
			} else {
				g.json = true
			}
		default:
			rest = append(rest, a)
		}
		i++
	}
	return g, rest, nil
}

// splitFlagArg splits "-name", "--name" or "--name=value".
// It returns "", "", false for non-flag arguments.
func splitFlagArg(a string) (name, val string, hasVal bool) {
	if len(a) < 2 || a[0] != '-' {
		return "", "", false
	}
	s := strings.TrimLeft(a, "-")
	if s == "" {
		return "", "", false
	}
	if j := strings.IndexByte(s, '='); j >= 0 {
		return s[:j], s[j+1:], true
	}
	return s, "", false
}

// newFlagSet builds a flag set writing parse errors to stderr.
func (r *runner) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	return fs
}

// resolvePaths applies flag > env > OS-default priority.
func (r *runner) resolvePaths(flagConfig, flagDataDir string) (cfgPath, dataDir string, err error) {
	if cfgPath, err = r.configPath(flagConfig); err != nil {
		return "", "", err
	}
	if dataDir, err = r.dataDir(flagDataDir); err != nil {
		return "", "", err
	}
	return cfgPath, dataDir, nil
}

// configPath resolves only the config file path.
func (r *runner) configPath(flagConfig string) (string, error) {
	if strings.TrimSpace(flagConfig) != "" {
		return flagConfig, nil
	}
	return config.DefaultConfigPath(r.getenv)
}

// dataDir resolves only the data directory.
func (r *runner) dataDir(flagDataDir string) (string, error) {
	if strings.TrimSpace(flagDataDir) != "" {
		return flagDataDir, nil
	}
	return config.DefaultDataDir(r.getenv)
}

// notFoundError marks a missing file or directory. It exists because
// os.IsNotExist is unreliable for wrapped open errors on Windows
// (ERROR_PATH_NOT_FOUND), while os.Stat detects absence reliably.
type notFoundError struct{ path string }

func (e *notFoundError) Error() string { return "file not found: " + e.path }

// isNotFound reports a missing path via sentinel or os.IsNotExist.
func isNotFound(err error) bool {
	var nf *notFoundError
	if errors.As(err, &nf) {
		return true
	}
	return os.IsNotExist(err)
}

// statMissing stats path and returns a *notFoundError when absent.
func statMissing(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return &notFoundError{path: path}
		}
		return err
	}
	return nil
}

// newRegistry builds the provider registry via the composition-root
// factory. A nil factory is a wiring error of the vend binary.
func (r *runner) newRegistry(cfg config.Config, logger *slog.Logger) (*registry.Registry, error) {
	if r.registryFactory == nil {
		return nil, errors.New("registry factory is not configured")
	}
	return r.registryFactory(cfg, logger)
}

// openJournalAt opens the journal database via the composition-root factory.
// The database file itself is checked by the caller with os.Stat where a
// not-found exit code matters; OpenJournal only opens it.
func (r *runner) openJournalAt(path string) (ports.Journal, io.Closer, error) {
	if r.openJournal == nil {
		return nil, nil, errors.New("journal factory is not configured")
	}
	return r.openJournal(path)
}

// loadConfig loads and validates the config file.
func (r *runner) loadConfig(path string) (config.Config, error) {
	if err := statMissing(path); err != nil {
		return config.Config{}, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, err
	}
	if verr := cfg.Validate(); verr != nil {
		return config.Config{}, verr
	}
	return cfg, nil
}

// loadFailureCode maps a load error to an exit code and envelope code.
func loadFailureCode(err error) (exit int, code string) {
	if isNotFound(err) {
		return exitNotFound, codeNotFound
	}
	if domain.CodeOf(err) == domain.CodeInvalidRequest {
		return exitUsage, codeUsage
	}
	return exitGeneral, codeGeneral
}

// printJSON writes v as JSON to stdout.
func (r *runner) printJSON(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(r.stderr, "vend: encode output: %v\n", err)
		return
	}
	fmt.Fprintln(r.stdout, string(raw))
}

// --- version ---

func (r *runner) cmdVersion(args []string) int {
	g, rest, err := splitGlobals(args)
	if err != nil {
		return r.fail(false, exitUsage, codeUsage, err.Error(),
			"run vend version [--json]", false, nil)
	}
	fs := r.newFlagSet("version")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(g.json, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend version [--json]", false, nil)
	}
	if g.json {
		r.printJSON(map[string]string{"version": r.ver})
		return exitOK
	}
	fmt.Fprintf(r.stdout, "vend %s\n", r.ver)
	return exitOK
}

// --- init ---

func (r *runner) cmdInit(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend init [--config FILE] [--data-dir DIR] [--force]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("init")
	force := fs.Bool("force", false, "overwrite existing config")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend init [--config FILE] [--data-dir DIR] [--force]", false, nil)
	}
	cfgPath, dataDir, err := r.resolvePaths(g.config, g.dataDir)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"set VENDING_CONFIG_FILE and VENDING_DATA_DIR explicitly", false, nil)
	}
	if _, err := os.Stat(cfgPath); err == nil && !*force {
		return r.fail(jsonOut, exitConflict, codeConflict,
			fmt.Sprintf("config already exists at %s", cfgPath),
			"run vend init --force to overwrite, or pass --config for another path", false, nil)
	}
	cfg := config.Default()
	if err := config.Save(cfgPath, cfg); err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the directory is writable and retry", false, nil)
	}
	dbPath := config.DBPath(dataDir)
	_, closer, err := r.openJournalAt(dbPath)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the data directory is writable and retry", false, nil)
	}
	if err := closer.Close(); err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"retry vend init", false, nil)
	}
	if jsonOut {
		r.printJSON(map[string]string{
			"config": cfgPath, "dataDir": dataDir, "database": dbPath, "mode": cfg.Mode,
		})
		return exitOK
	}
	fmt.Fprintf(r.stdout, "config: %s\n", cfgPath)
	fmt.Fprintf(r.stdout, "data dir: %s\n", dataDir)
	fmt.Fprintf(r.stdout, "database: %s\n", dbPath)
	fmt.Fprintf(r.stdout, "mode: %s\n", cfg.Mode)
	return exitOK
}

// --- config ---

func (r *runner) cmdConfig(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend config path|get KEY|validate", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("config")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	pos := fs.Args()
	if len(pos) < 1 {
		return r.fail(jsonOut, exitUsage, codeUsage, "missing config subcommand",
			"run vend config path|get KEY|validate", false, nil)
	}
	cfgPath, err := r.configPath(g.config)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"set VENDING_CONFIG_FILE explicitly", false, nil)
	}
	switch pos[0] {
	case "path":
		if len(pos) > 1 {
			return r.fail(jsonOut, exitUsage, codeUsage,
				fmt.Sprintf("unexpected argument %q", pos[1]),
				"run vend config path", false, nil)
		}
		if jsonOut {
			r.printJSON(map[string]string{"path": cfgPath})
			return exitOK
		}
		fmt.Fprintln(r.stdout, cfgPath)
		return exitOK
	case "get":
		if len(pos) < 2 {
			return r.fail(jsonOut, exitUsage, codeUsage, "missing key",
				"run vend config get <dotted-key>, e.g. vend config get gate.minPass", false, nil)
		}
		if len(pos) > 2 {
			return r.fail(jsonOut, exitUsage, codeUsage,
				fmt.Sprintf("unexpected argument %q", pos[2]),
				"run vend config get <dotted-key>", false, nil)
		}
		cfg, err := r.loadConfig(cfgPath)
		if err != nil {
			code, errCode := loadFailureCode(err)
			hint := "run vend init to create the config"
			if code == exitUsage {
				hint = "fix the config file or run vend init --force to recreate it"
			}
			return r.fail(jsonOut, code, errCode,
				fmt.Sprintf("cannot load config at %s: %v", cfgPath, firstLine(err)), hint, false, nil)
		}
		v, err := cfg.Get(pos[1])
		if err != nil {
			return r.fail(jsonOut, exitNotFound, codeNotFound, err.Error(),
				"list available keys via the config file schema", false, nil)
		}
		if s, ok := v.(string); ok && !jsonOut {
			fmt.Fprintln(r.stdout, s)
			return exitOK
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
				"retry the command", false, nil)
		}
		if jsonOut {
			r.printJSON(map[string]any{"key": pos[1], "value": v})
			return exitOK
		}
		fmt.Fprintln(r.stdout, string(raw))
		return exitOK
	case "validate":
		if len(pos) > 1 {
			return r.fail(jsonOut, exitUsage, codeUsage,
				fmt.Sprintf("unexpected argument %q", pos[1]),
				"run vend config validate", false, nil)
		}
		raw, err := config.Load(cfgPath)
		if err != nil {
			if isNotFound(err) {
				return r.fail(jsonOut, exitNotFound, codeNotFound,
					fmt.Sprintf("config file not found at %s", cfgPath),
					"run vend init to create the config", false, nil)
			}
			return r.fail(jsonOut, exitUsage, codeUsage, err.Error(),
				"fix the JSON syntax or run vend init --force to recreate it", false, nil)
		}
		if verr := raw.Validate(); verr != nil {
			return r.fail(jsonOut, exitUsage, codeUsage, verr.Error(),
				"fix the config value or run vend init --force to recreate it", false, nil)
		}
		if jsonOut {
			r.printJSON(map[string]any{"ok": true, "path": cfgPath})
			return exitOK
		}
		fmt.Fprintln(r.stdout, "config ok")
		return exitOK
	default:
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unknown config subcommand %q", pos[0]),
			"run vend config path|get KEY|validate", false, nil)
	}
}

// --- doctor ---

type docCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func (r *runner) cmdDoctor(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend doctor [--config FILE] [--data-dir DIR]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("doctor")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend doctor [--config FILE] [--data-dir DIR]", false, nil)
	}
	cfgPath, dataDir, err := r.resolvePaths(g.config, g.dataDir)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"set VENDING_CONFIG_FILE and VENDING_DATA_DIR explicitly", false, nil)
	}
	var checks []docCheck
	add := func(name string, ok bool, detail string) {
		checks = append(checks, docCheck{Name: name, OK: ok, Detail: detail})
	}

	cfg, cfgErr := config.Load(cfgPath)
	if cfgErr != nil {
		add("config", false, fmt.Sprintf("cannot load %s: %v", cfgPath, firstLine(cfgErr)))
	} else if verr := cfg.Validate(); verr != nil {
		add("config", false, verr.Error())
	} else {
		add("config", true, cfgPath)
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		add("data_dir", false, err.Error())
	} else if err := probeWritable(dataDir); err != nil {
		add("data_dir", false, err.Error())
	} else {
		add("data_dir", true, dataDir)
	}

	dbPath := config.DBPath(dataDir)
	dbOK := false
	if _, err := os.Stat(dbPath); err != nil {
		add("database", false, fmt.Sprintf("database file not found at %s: run vend init", dbPath))
	} else if _, closer, err := r.openJournalAt(dbPath); err != nil {
		add("database", false, err.Error())
	} else {
		_ = closer.Close()
		dbOK = true
		add("database", true, dbPath)
	}

	if !dbOK {
		add("journal", false, "database is unavailable")
	} else {
		jr, closer, err := r.openJournalAt(dbPath)
		if err != nil {
			add("journal", false, err.Error())
		} else {
			res, err := jr.Verify(context.Background())
			_ = closer.Close()
			if err != nil {
				add("journal", false, err.Error())
			} else if !res.OK {
				add("journal", false, fmt.Sprintf("chain broken at seq %d: %s", res.BrokenSeq, res.Reason))
			} else {
				add("journal", true, fmt.Sprintf("%d events", res.Count))
			}
		}
	}

	if cfgErr != nil {
		add("providers", false, "config is unavailable")
	} else if reg, err := r.newRegistry(cfg, nil); err != nil {
		add("providers", false, err.Error())
	} else {
		var ids []string
		for _, d := range reg.Descriptors() {
			ids = append(ids, d.ID)
		}
		add("providers", true, strings.Join(ids, ", "))
	}

	if cfgErr != nil {
		add("secrets", false, "config is unavailable")
	} else {
		name := config.SecretEnvName(cfg.Providers.Decision.SecretRef)
		if v := r.getenv(name); v != "" {
			add("secrets", true, fmt.Sprintf("env mode: %s is set", name))
		} else {
			add("secrets", true, fmt.Sprintf("env mode: %s is not set (warning: live provider calls need it)", name))
		}
	}

	if cfgErr != nil {
		add("gate_policy", false, "config is unavailable")
	} else {
		pol := domain.GatePolicy{
			Version: cfg.Gate.Version, MinPass: cfg.Gate.MinPass, MinBlock: cfg.Gate.MinBlock,
		}
		add("gate_policy", true, gate.PolicyHash(pol))
	}

	ok := true
	for _, c := range checks {
		if !c.OK {
			ok = false
			break
		}
	}
	if jsonOut {
		r.printJSON(map[string]any{"ok": ok, "checks": checks})
	} else {
		for _, c := range checks {
			status := "ok"
			if !c.OK {
				status = "FAIL"
			}
			fmt.Fprintf(r.stdout, "%s %s: %s\n", status, c.Name, c.Detail)
		}
	}
	if !ok {
		return exitGeneral
	}
	return exitOK
}

// probeWritable creates and removes a probe file inside dir.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".vend-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

func firstLine(err error) error {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return errMsg(msg)
}

type errMsg string

func (e errMsg) Error() string { return string(e) }

// appendVerdict records the verdict in the journal database.
func (r *runner) appendVerdict(dbPath string, verdict domain.GateVerdict) error {
	payload, err := json.Marshal(verdict)
	if err != nil {
		return err
	}
	jr, closer, err := r.openJournalAt(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()
	_, err = jr.Append(context.Background(), domain.Event{
		Kind:        "gate.verdict",
		CodeVersion: r.ver,
		DecisionID:  verdict.DecisionID,
		PolicyHash:  verdict.PolicyHash,
		Payload:     payload,
	})
	return err
}

// --- gate ---

func (r *runner) cmdGate(args []string) int {
	if len(args) < 1 {
		r.fail(false, exitUsage, codeUsage, "missing gate subcommand",
			"run vend gate test --request FILE", false, nil)
		return exitUsage
	}
	switch args[0] {
	case "test":
		return r.cmdGateTest(args[1:])
	default:
		r.fail(false, exitUsage, codeUsage,
			fmt.Sprintf("unknown gate subcommand %q", args[0]),
			"run vend gate test --request FILE", false, nil)
		return exitUsage
	}
}

func (r *runner) cmdGateTest(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend gate test --request FILE [--fixture FILE]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("gate test")
	reqFlag := fs.String("request", "", "gate request JSON file")
	fixFlag := fs.String("fixture", "", "decision response fixture file (offline)")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend gate test --request FILE [--fixture FILE]", false, nil)
	}
	if strings.TrimSpace(*reqFlag) == "" {
		return r.fail(jsonOut, exitUsage, codeUsage, "missing --request",
			"run vend gate test --request FILE [--fixture FILE]", false, nil)
	}
	cfgPath, dataDir, err := r.resolvePaths(g.config, g.dataDir)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"set VENDING_CONFIG_FILE and VENDING_DATA_DIR explicitly", false, nil)
	}
	cfg, err := r.loadConfig(cfgPath)
	if err != nil {
		code, errCode := loadFailureCode(err)
		hint := "run vend init to create the config"
		if code == exitUsage {
			hint = "fix the config file or run vend init --force to recreate it"
		}
		return r.fail(jsonOut, code, errCode,
			fmt.Sprintf("cannot load config at %s: %v", cfgPath, firstLine(err)), hint, false, nil)
	}

	gateReq, failCode, failErr := readGateRequest(*reqFlag)
	if failErr != nil {
		hint := "fix the request file"
		errCode := codeUsage
		if failCode == exitNotFound {
			errCode = codeNotFound
		}
		return r.fail(jsonOut, failCode, errCode, failErr.Error(), hint, false, nil)
	}

	model := cfg.Providers.Decision.Model
	decReq := domain.DecisionRequest{
		Model:     model,
		Questions: gate.Questions(),
		Timeout:   15 * time.Second,
		State:     gateTestState(gateReq),
	}

	var outcome domain.DecisionOutcome
	if strings.TrimSpace(*fixFlag) != "" {
		resp, failCode, failErr := readDecisionFixture(*fixFlag, model)
		if failErr != nil {
			hint := "fix the fixture file"
			errCode := codeUsage
			if failCode == exitNotFound {
				errCode = codeNotFound
			}
			return r.fail(jsonOut, failCode, errCode, failErr.Error(), hint, false, nil)
		}
		outcome = domain.DecisionOutcome{Response: &resp}
		if verr := validateFixture(resp, gate.Questions()); verr != nil {
			return r.fail(jsonOut, exitUsage, codeUsage, verr.Error(), "fix the fixture file", false, nil)
		}
	} else if gateReq.IntentKind != domain.IntentExit {
		reg, err := r.newRegistry(cfg,
			slog.New(slog.NewTextHandler(r.stderr, nil)))
		if err != nil {
			return r.fail(jsonOut, exitProvider, codeProvider, err.Error(),
				"fix the provider settings via vend config validate", domain.IsRetryable(err), nil)
		}
		p, err := reg.Decision(cfg.Providers.Decision.ID)
		if err != nil {
			return r.fail(jsonOut, exitProvider, codeProvider, err.Error(),
				"align providers.decision.id with a registered provider", false, nil)
		}
		var resp domain.DecisionResponse
		var callErr error
		if rerr := resilience.Do(context.Background(), resilience.DefaultPolicy(),
			func(ctx context.Context) error {
				rr, derr := p.Decide(ctx, decReq)
				if derr != nil {
					callErr = derr
					return derr
				}
				resp = rr
				callErr = nil
				return nil
			}); rerr != nil && callErr == nil {
			callErr = rerr
		}
		if callErr != nil {
			outcome = domain.DecisionOutcome{Err: callErr}
		} else {
			outcome = domain.DecisionOutcome{Response: &resp}
		}
	}
	// IntentExit never calls the provider: Evaluate short-circuits to SKIP.

	policy := domain.GatePolicy{
		Version: cfg.Gate.Version, MinPass: cfg.Gate.MinPass, MinBlock: cfg.Gate.MinBlock,
	}
	verdict := gate.Evaluate(gateReq, outcome, policy)
	verdict.DecisionID = ulid.Make().String()
	verdict.DecidedAt = time.Now().UTC()

	if err := r.appendVerdict(config.DBPath(dataDir), verdict); err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the data directory is writable and retry", false, nil)
	}

	if jsonOut {
		raw, err := json.Marshal(verdict)
		if err != nil {
			return r.fail(true, exitGeneral, codeGeneral, err.Error(),
				"retry the command", false, nil)
		}
		fmt.Fprintln(r.stdout, string(raw))
	} else {
		printVerdictHuman(r.stdout, verdict)
	}

	if outcome.Err != nil {
		switch domain.CodeOf(outcome.Err) {
		case domain.CodeAuth:
			return r.fail(jsonOut, exitForbidden, codeForbidden, outcome.Err.Error(),
				fmt.Sprintf("set %s and retry", config.SecretEnvName(cfg.Providers.Decision.SecretRef)),
				domain.IsRetryable(outcome.Err), nil)
		case domain.CodeTimeout:
			return r.fail(jsonOut, exitTimeout, codeTimeout, outcome.Err.Error(),
				"retry later; the request timed out", true, nil)
		default:
			return r.fail(jsonOut, exitProvider, codeProvider, outcome.Err.Error(),
				"retry later or check provider status", domain.IsRetryable(outcome.Err), nil)
		}
	}
	if verdict.Verdict == domain.VerdictReject {
		return r.fail(jsonOut, exitReject, codeReject,
			fmt.Sprintf("gate rejected: %s", verdict.Code),
			"inspect the verdict reasons and adjust the intent", false, nil)
	}
	return exitOK
}

// gateTestState copies the numeric request state and adds routing context.
func gateTestState(req domain.GateRequest) map[string]any {
	state := make(map[string]any, len(req.State)+7)
	for k, v := range req.State {
		state[k] = v
	}
	state["symbol"] = req.Instrument.Symbol
	state["venue"] = req.Instrument.Venue
	state["timeframe"] = req.Timeframe
	state["side"] = string(req.Side)
	state["intent_kind"] = string(req.IntentKind)
	state["close"] = req.Price
	state["timestamp"] = req.TS.UTC().Format(time.RFC3339Nano)
	return state
}

// readGateRequest parses the request file strictly and validates fields.
func readGateRequest(path string) (domain.GateRequest, int, error) {
	var req domain.GateRequest
	if err := statMissing(path); err != nil {
		if isNotFound(err) {
			return req, exitNotFound, fmt.Errorf("request file not found at %s", path)
		}
		return req, exitGeneral, err
	}
	f, err := os.Open(path)
	if err != nil {
		if isNotFound(err) {
			return req, exitNotFound, fmt.Errorf("request file not found at %s", path)
		}
		return req, exitGeneral, err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, exitUsage, fmt.Errorf("invalid request JSON: %v", firstLine(err))
	}
	if strings.TrimSpace(req.SignalID) == "" {
		return req, exitUsage, fmt.Errorf("invalid request: signalId must not be empty")
	}
	if req.Side != domain.SideBuy && req.Side != domain.SideSell {
		return req, exitUsage, fmt.Errorf("invalid request: side must be buy or sell")
	}
	if req.IntentKind != domain.IntentEntry && req.IntentKind != domain.IntentExit {
		return req, exitUsage, fmt.Errorf("invalid request: intentKind must be entry or exit")
	}
	if strings.TrimSpace(req.Timeframe) == "" {
		return req, exitUsage, fmt.Errorf("invalid request: timeframe must not be empty")
	}
	if req.TS.IsZero() {
		return req, exitUsage, fmt.Errorf("invalid request: ts must be set")
	}
	if req.Price.Sign() <= 0 {
		return req, exitUsage, fmt.Errorf("invalid request: price must be > 0")
	}
	if strings.TrimSpace(req.Instrument.Symbol) == "" || strings.TrimSpace(req.Instrument.Venue) == "" {
		return req, exitUsage, fmt.Errorf("invalid request: instrument symbol and venue must not be empty")
	}
	return req, exitOK, nil
}

// readDecisionFixture parses an offline decision response.
func readDecisionFixture(path, model string) (domain.DecisionResponse, int, error) {
	var resp domain.DecisionResponse
	if err := statMissing(path); err != nil {
		if isNotFound(err) {
			return resp, exitNotFound, fmt.Errorf("fixture file not found at %s", path)
		}
		return resp, exitGeneral, err
	}
	f, err := os.Open(path)
	if err != nil {
		if isNotFound(err) {
			return resp, exitNotFound, fmt.Errorf("fixture file not found at %s", path)
		}
		return resp, exitGeneral, err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&resp); err != nil {
		return resp, exitUsage, fmt.Errorf("invalid fixture JSON: %v", firstLine(err))
	}
	resp.RequestedModel = model
	return resp, exitOK, nil
}

// validateFixture checks an offline decision response against the gate
// question set with the same strictness as the provider adapter, so a
// replayed verdict is as trustworthy as a live one.
func validateFixture(resp domain.DecisionResponse, questions []domain.Question) error {
	if len(resp.Answers) != len(questions) {
		return fmt.Errorf("invalid fixture: answer count %d does not match question count %d",
			len(resp.Answers), len(questions))
	}
	for _, q := range questions {
		a, ok := resp.Answers[q.ID]
		if !ok {
			return fmt.Errorf("invalid fixture: missing answer for question %q", q.ID)
		}
		if a.Type != q.Type {
			return fmt.Errorf("invalid fixture: question %q: expected type %q, got %q",
				q.ID, string(q.Type), string(a.Type))
		}
		if err := validateFixtureAnswer(q, a); err != nil {
			return err
		}
	}
	return nil
}

// validateFixtureAnswer checks one replayed answer by question type.
func validateFixtureAnswer(q domain.Question, a domain.Answer) error {
	switch q.Type {
	case domain.QuestionChoice:
		return validateFixtureChoice(q, a)
	case domain.QuestionNoul:
		if a.Noul == nil || math.IsNaN(*a.Noul) || math.IsInf(*a.Noul, 0) ||
			*a.Noul < 0 || *a.Noul > 1 {
			return fmt.Errorf("invalid fixture: question %q: noul out of range [0, 1]", q.ID)
		}
		return nil
	case domain.QuestionScore:
		if a.Score == nil || math.IsNaN(*a.Score) || math.IsInf(*a.Score, 0) {
			return fmt.Errorf("invalid fixture: question %q: score is missing or not finite", q.ID)
		}
		if len(a.Legend) == 0 {
			return fmt.Errorf("invalid fixture: question %q: score legend is empty", q.ID)
		}
		if len(a.Probabilities) == 0 {
			return fmt.Errorf("invalid fixture: question %q: score probabilities are empty", q.ID)
		}
		return nil
	default:
		return fmt.Errorf("invalid fixture: question %q: unknown type %q", q.ID, string(q.Type))
	}
}

// validateFixtureChoice mirrors the provider choice validation: exact
// option keys, finite [0, 1] values, unit sum, argmax choice and a
// recomputed confidence within 0.02.
func validateFixtureChoice(q domain.Question, a domain.Answer) error {
	n := len(q.Options)
	if n < 2 {
		return fmt.Errorf("invalid fixture: question %q: choice needs at least two options", q.ID)
	}
	if len(a.Probabilities) != n {
		return fmt.Errorf("invalid fixture: question %q: probability count does not match options", q.ID)
	}
	max := math.Inf(-1)
	sum := 0.0
	for _, o := range q.Options {
		p, ok := a.Probabilities[o.Key]
		if !ok {
			return fmt.Errorf("invalid fixture: question %q: probabilities miss option %q", q.ID, o.Key)
		}
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return fmt.Errorf("invalid fixture: question %q: probability out of range [0, 1]", q.ID)
		}
		sum += p
		if p > max {
			max = p
		}
	}
	if math.Abs(sum-1) > 0.001 {
		return fmt.Errorf("invalid fixture: question %q: probabilities do not sum to 1", q.ID)
	}
	got, ok := a.Probabilities[a.Choice]
	if !ok || got != max {
		return fmt.Errorf("invalid fixture: question %q: choice is not the argmax", q.ID)
	}
	if a.Confidence == nil {
		return fmt.Errorf("invalid fixture: question %q: choice confidence is missing", q.ID)
	}
	if math.IsNaN(*a.Confidence) || math.IsInf(*a.Confidence, 0) {
		return fmt.Errorf("invalid fixture: question %q: choice confidence is not finite", q.ID)
	}
	want := (max - 1/float64(n)) / (1 - 1/float64(n))
	if math.Abs(*a.Confidence-want) > 0.02 {
		return fmt.Errorf("invalid fixture: question %q: choice confidence does not match recomputed value", q.ID)
	}
	return nil
}

// printVerdictHuman prints the verdict in human-readable form.
func printVerdictHuman(w io.Writer, v domain.GateVerdict) {
	fmt.Fprintf(w, "verdict: %s\n", string(v.Verdict))
	fmt.Fprintf(w, "code: %s\n", v.Code)
	if v.Model != "" {
		fmt.Fprintf(w, "model: %s\n", v.Model)
	}
	fmt.Fprintf(w, "policy: %s\n", v.PolicyHash)
	for _, reason := range v.Reasons {
		fmt.Fprintf(w, "reason: %s\n", reason)
	}
}

// --- journal ---

func (r *runner) cmdJournal(args []string) int {
	if len(args) < 1 {
		r.fail(false, exitUsage, codeUsage, "missing journal subcommand",
			"run vend journal verify", false, nil)
		return exitUsage
	}
	switch args[0] {
	case "verify":
		return r.cmdJournalVerify(args[1:])
	default:
		r.fail(false, exitUsage, codeUsage,
			fmt.Sprintf("unknown journal subcommand %q", args[0]),
			"run vend journal verify", false, nil)
		return exitUsage
	}
}

func (r *runner) cmdJournalVerify(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend journal verify", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("journal verify")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend journal verify", false, nil)
	}
	dataDir, err := r.dataDir(g.dataDir)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"set VENDING_DATA_DIR explicitly", false, nil)
	}
	dbPath := config.DBPath(dataDir)
	if _, err := os.Stat(dbPath); err != nil {
		if isNotFound(err) {
			return r.fail(jsonOut, exitNotFound, codeNotFound,
				fmt.Sprintf("database not found at %s", dbPath),
				"run vend init to create it", false, nil)
		}
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the data directory is readable and retry", false, nil)
	}
	ctx := context.Background()
	jr, closer, err := r.openJournalAt(dbPath)
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	res, err := jr.Verify(ctx)
	_ = closer.Close()
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the database file is a valid sqlite database", false, nil)
	}
	if !res.OK {
		details := map[string]any{"seq": res.BrokenSeq, "reason": res.Reason}
		return r.fail(jsonOut, exitGeneral, codeGeneral,
			fmt.Sprintf("journal chain broken at seq %d: %s", res.BrokenSeq, res.Reason),
			"restore the database from backup; the journal is append-only", false, details)
	}
	if jsonOut {
		r.printJSON(map[string]any{"ok": true, "count": res.Count})
		return exitOK
	}
	fmt.Fprintf(r.stdout, "journal ok: %d events\n", res.Count)
	return exitOK
}
