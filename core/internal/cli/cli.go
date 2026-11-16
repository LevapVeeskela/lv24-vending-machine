// Package cli implements the vend command tree on stdlib flag only.
//
// Exit codes: 0 success, 1 general error, 2 usage/validation,
// 3 not found, 4 state conflict, 5 forbidden (no secret, live),
// 6 provider error, 7 gate reject, 8 timeout, 9 approval.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/registry"
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
	// Clock returns the current time; nil means time.Now.
	Clock func() time.Time
	// NewID generates unique identifiers; nil means ULID.
	NewID func() string
}

// runner holds resolved I/O for one Run call.
type runner struct {
	stdout          io.Writer
	stderr          io.Writer
	getenv          func(string) string
	ver             string
	registryFactory func(cfg config.Config, logger *slog.Logger) (*registry.Registry, error)
	openJournal     func(path string) (ports.Journal, io.Closer, error)
	clock           func() time.Time
	newID           func() string
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
		clock:           env.Clock,
		newID:           env.NewID,
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
	if r.clock == nil {
		r.clock = time.Now
	}
	if r.newID == nil {
		r.newID = func() string { return ulid.Make().String() }
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
	case "serve":
		return r.cmdServe(args[1:])
	case "outcomes":
		return r.cmdOutcomes(args[1:])
	default:
		r.fail(false, exitUsage, codeUsage,
			fmt.Sprintf("unknown command %q", args[0]),
			"run vend with one of: version, init, config, doctor, gate, journal, serve, outcomes", false, nil)
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
	fmt.Fprintln(r.stderr, "  gate test --request FILE [--fixture FILE] [--save-raw FILE] [--config FILE] [--data-dir DIR] [--json]")
	fmt.Fprintln(r.stderr, "  gate report [--json]")
	fmt.Fprintln(r.stderr, "  journal verify [--config FILE] [--data-dir DIR] [--json]")
	fmt.Fprintln(r.stderr, "  journal head [--data-dir DIR] [--json]")
	fmt.Fprintln(r.stderr, "  serve [--addr 127.0.0.1:8420]")
	fmt.Fprintln(r.stderr, "  outcomes label [--candles FILE] [--json]")
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
