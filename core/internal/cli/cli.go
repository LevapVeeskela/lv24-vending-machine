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
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
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
	// Help is resolved before dispatch so every command accepts --help/-h and
	// `vend help <command>` without a command-specific parser rejecting it.
	if args[0] == "help" {
		words, _ := scanHelpArgs(args[1:])
		return r.showHelp(words)
	}
	if words, ok := scanHelpArgs(args); ok {
		return r.showHelp(words)
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
	case "signals":
		return r.cmdSignals(args[1:])
	case "status":
		return r.cmdStatus(args[1:])
	default:
		r.fail(false, exitUsage, codeUsage,
			fmt.Sprintf("unknown command %q", args[0]),
			"run vend with one of: version, init, config, doctor, gate, journal, signals, serve, outcomes, status, help", false, nil)
		return exitUsage
	}
}

// usage prints the top-level help to stderr for a usage error (no command).
func (r *runner) usage() {
	fmt.Fprint(r.stderr, generalHelp)
}

// generalHelp is the top-level reference: every command and flag of the
// binary, kept in one place with the per-command pages below.
const generalHelp = `usage: vend <command> [options]

commands:
  version [--json]
  init [--force] [--config FILE] [--data-dir DIR] [--json]
  config path|get KEY|validate [--config FILE] [--data-dir DIR] [--json]
  doctor [--live] [--symbol SYMBOL] [--save-raw FILE] [--force] [--config FILE] [--data-dir DIR] [--json]
  gate test --request FILE [--fixture FILE] [--save-raw FILE] [--force] [--config FILE] [--data-dir DIR] [--json]
  gate report [--policy HASH] [--include-backfill] [--share [--out FILE] [--force]] [--json] [--config FILE] [--data-dir DIR]
  journal verify [--config FILE] [--data-dir DIR] [--json]
  journal head [--config FILE] [--data-dir DIR] [--json]
  journal tail [--kind KIND] [--limit N] [--config FILE] [--data-dir DIR] [--json]
  signals estimate [--days N] [--config FILE] [--data-dir DIR] [--json]
  outcomes label [--candles FILE] [--config FILE] [--data-dir DIR] [--json]
  status [--config FILE] [--data-dir DIR] [--json]
  serve [--addr 127.0.0.1:8420] [--config FILE] [--data-dir DIR] [--json]
  help [command]

global flags (accepted by every command, in any position):
  --config FILE   config file path
  --data-dir DIR  data directory
  --json          machine-readable output and JSON error envelopes

exit codes: 0 ok, 1 general, 2 usage, 3 not found, 4 conflict,
5 forbidden, 6 provider, 7 gate rejected, 8 timeout, 9 approval.

run "vend help <command>" or "vend <command> --help" for command details.
`

// helpPages holds the command pages printed by `vend help <topic>` and
// `vend <topic> --help`. Every command and flag in generalHelp has a page.
var helpPages = map[string]string{
	"version": `usage: vend version [--json]

Print the vend version.
`,
	"init": `usage: vend init [--force] [--config FILE] [--data-dir DIR] [--json]

Create the default config file and an empty journal, then print the resolved
paths and the next commands to run.

flags:
  --force   overwrite an existing config file
`,
	"config": `usage: vend config path|get KEY|validate [--config FILE] [--data-dir DIR] [--json]

subcommands:
  path      print the resolved config file path
  get KEY   print one dotted config value, e.g. gate.minPass
  validate  parse and validate the config file
`,
	"doctor": `usage: vend doctor [--live] [--symbol SYMBOL] [--save-raw FILE] [--force] [--config FILE] [--data-dir DIR] [--json]

Check the config, data directory, journal chain, provider registry, secrets and
gate policy. With --live, also run the opt-in network probes.

flags:
  --live           run the network probes (decision, candles, telegram)
  --symbol SYMBOL  symbol for the live candles probe (default BTCUSDT)
  --save-raw FILE  write the raw decision response to FILE (0600); needs --live
  --force          overwrite an existing --save-raw file
`,
	"gate": `usage: vend gate test --request FILE [options]
       vend gate report [options]

subcommands:
  test    evaluate one gate request against the provider or an offline fixture
  report  build the shadow-gate report from the journal
`,
	"gate test": `usage: vend gate test --request FILE [--fixture FILE] [--save-raw FILE] [--force]
                   [--config FILE] [--data-dir DIR] [--json]

Evaluate one gate request and record the verdict in the journal. With
--fixture the decision comes from a file and the provider is never called;
without it entry intents call the configured decision provider.

flags:
  --request FILE   gate request JSON file (required)
  --fixture FILE   decision response fixture for an offline replay
  --save-raw FILE  write the raw decision response to FILE (0600)
  --force          overwrite an existing --save-raw file
`,
	"gate report": `usage: vend gate report [--policy HASH] [--include-backfill] [--share [--out FILE] [--force]]
                       [--json] [--config FILE] [--data-dir DIR]

Build the shadow-gate report. --share prints a sanitized markdown summary and
cannot be combined with --json; --out requires --share.

flags:
  --policy HASH        only report decisions made under this 64-hex policy hash
  --include-backfill   count backfilled outcomes in the comparison
  --share              print the sanitized markdown summary instead of the report
  --out FILE           write the --share summary to FILE (0600); refuses an existing file
  --force              overwrite the --out file if it already exists
  --allow-broken-chain report even when the journal hash chain is broken
`,
	"journal": `usage: vend journal verify|head|tail [--config FILE] [--data-dir DIR] [--json]

subcommands:
  verify  verify the hash chain
  head    print the last seq and hash
  tail    print the last events, oldest first
`,
	"journal verify": `usage: vend journal verify [--config FILE] [--data-dir DIR] [--json]

Verify the journal hash chain and print the event count.
`,
	"journal head": `usage: vend journal head [--config FILE] [--data-dir DIR] [--json]

Print the last journal seq and hash, or "journal empty".
`,
	"journal tail": `usage: vend journal tail [--kind KIND] [--limit N] [--config FILE] [--data-dir DIR] [--json]

Print the last events, oldest first. The human output omits payloads.

flags:
  --kind KIND  event kind to filter, e.g. gate.request
  --limit N    number of events to print (1..1000, default 20)
`,
	"signals": `usage: vend signals estimate [--days N] [--config FILE] [--data-dir DIR] [--json]

subcommands:
  estimate  count the trend signals the config would have produced
`,
	"signals estimate": `usage: vend signals estimate [--days N] [--config FILE] [--data-dir DIR] [--json]

Count the signals the configured trend source would have produced over the last
N days. Reads candles only: no journal, no decision provider, no secrets.

flags:
  --days N  history window in days (1..30, default 7)
`,
	"outcomes": `usage: vend outcomes label [--candles FILE] [--config FILE] [--data-dir DIR] [--json]

subcommands:
  label  label gate requests from a candles file or the marketdata provider
`,
	"outcomes label": `usage: vend outcomes label [--candles FILE] [--config FILE] [--data-dir DIR] [--json]

Label outcomes for recorded gate requests. With --candles the prices come from
a JSON file; without it the configured marketdata provider is used.

flags:
  --candles FILE  candles JSON file (UTF-8, BOM tolerated)
`,
	"status": `usage: vend status [--config FILE] [--data-dir DIR] [--json]

Print the shadow-gate status: the journal counters and the last snapshot written
by the runner. Before the first serve, policyHash and updatedAt are omitted from
--json instead of showing zero values.
`,
	"serve": `usage: vend serve [--addr 127.0.0.1:8420] [--config FILE] [--data-dir DIR] [--json]

Start the loopback gate API and the background shadow runner. The local API
token must be set in VENDING_SECRET_CONN_LOCAL_APITOKEN.

flags:
  --addr ADDR  listen address (loopback, port 1..65535)
`,
}

// scanHelpArgs reports whether a help flag is present and returns the command
// words that precede it. Values of --config/--data-dir are skipped so they are
// never mistaken for a help topic; "--" ends option parsing.
func scanHelpArgs(args []string) (words []string, help bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return words, help
		}
		if isHelpFlag(a) {
			help = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			name, _, hasVal := splitFlagArg(a)
			if !hasVal && (name == "config" || name == "data-dir") {
				i++
			}
			continue
		}
		words = append(words, a)
	}
	return words, help
}

// isHelpFlag reports whether arg is --help, -help, --h or -h.
func isHelpFlag(arg string) bool {
	name, _, hasVal := splitFlagArg(arg)
	return !hasVal && (name == "help" || name == "h")
}

// showHelp prints the page of the longest known command prefix. It returns 0
// for any known topic and 2 for an unknown one.
func (r *runner) showHelp(words []string) int {
	if len(words) == 0 {
		fmt.Fprint(r.stdout, generalHelp)
		return exitOK
	}
	for i := len(words); i > 0; i-- {
		if page, ok := helpPages[strings.Join(words[:i], " ")]; ok {
			fmt.Fprint(r.stdout, page)
			return exitOK
		}
	}
	return r.fail(false, exitUsage, codeUsage,
		fmt.Sprintf("unknown help topic %q", strings.Join(words, " ")),
		"run vend help for the command list, e.g. vend help gate test", false, nil)
}

// fail writes the error envelope to stderr and returns exit.
func (r *runner) fail(jsonMode bool, exit int, code, msg, hint string, retryable bool, details any) int {
	// Typed errors render as "<code>: <message>"; keep the envelope code from
	// turning that into "vend: conflict: conflict: ..." on stderr.
	msg = trimCodePrefix(msg, code)
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

// trimCodePrefix drops a leading "<code>: " that a typed domain error already
// carries, so the human line is not double-prefixed ("vend: conflict: ...").
func trimCodePrefix(msg, code string) string {
	if code != "" && strings.HasPrefix(msg, code+": ") {
		return msg[len(code)+2:]
	}
	return msg
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

// writeFileAtomic writes data to path through a temp file in the same
// directory followed by a rename, so readers never observe a partial file
// and an existing file is replaced with perm (0600 for secrets and raw
// vendor bodies).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	discard := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}
	if err := f.Chmod(perm); err != nil {
		discard()
		return err
	}
	if _, err := f.Write(data); err != nil {
		discard()
		return err
	}
	if err := f.Sync(); err != nil {
		discard()
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Output-path guards returned by writeFileNew; test them with errors.Is.
var (
	errOutputExists    = errors.New("output file already exists")
	errOutputInDataDir = errors.New("refusing to write inside the data directory")
)

// newFileOptions guards one CLI output file.
type newFileOptions struct {
	// DataDir is the resolved data directory. The journal and the config live
	// there (or next to it), so a path inside it is refused: an output file
	// must never overwrite vending.db or shadow-status.json.
	DataDir string
	// Force is the parsed --force flag: when set, an existing file is
	// replaced atomically instead of refused.
	Force bool
}

// writeFileNew writes data to path, refusing to replace an existing file
// unless opts.Force is set, and refusing paths inside opts.DataDir.
//
// The no-overwrite path reserves the name with O_EXCL before the atomic
// rename, so two concurrent writers cannot both pass the existence check.
// The returned error wraps errOutputExists or errOutputInDataDir for errors.Is.
func writeFileNew(path string, data []byte, perm os.FileMode, opts newFileOptions) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("output path is empty")
	}
	inside, err := pathInside(opts.DataDir, path)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("%w: %s (the journal and config are protected; choose another output path)",
			errOutputInDataDir, path)
	}
	if opts.Force {
		return writeFileAtomic(path, data, perm)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s (pass --force to overwrite)", errOutputExists, path)
		}
		return err
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(path)
		return cerr
	}
	if werr := writeFileAtomic(path, data, perm); werr != nil {
		_ = os.Remove(path)
		return werr
	}
	return nil
}

// failOutput maps writeFileNew's guard errors to a usage failure with a hint.
func (r *runner) failOutput(jsonMode bool, err error) int {
	switch {
	case errors.Is(err, errOutputExists):
		return r.fail(jsonMode, exitUsage, codeUsage, err.Error(),
			"pass --force to overwrite the file, or choose another path", false, nil)
	case errors.Is(err, errOutputInDataDir):
		return r.fail(jsonMode, exitUsage, codeUsage, err.Error(),
			"choose an output path outside the data directory", false, nil)
	default:
		return r.fail(jsonMode, exitGeneral, codeGeneral, err.Error(),
			"check the output path is writable and retry", false, nil)
	}
}

// pathInside reports whether path is dataDir itself or lies below it. Both
// paths are made absolute and cleaned; on Windows the comparison is
// case-insensitive, matching the file system.
func pathInside(dataDir, path string) (bool, error) {
	if strings.TrimSpace(dataDir) == "" || strings.TrimSpace(path) == "" {
		return false, nil
	}
	dirAbs, err := filepath.Abs(dataDir)
	if err != nil {
		return false, err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	dirAbs, pathAbs = filepath.Clean(dirAbs), filepath.Clean(pathAbs)
	if runtime.GOOS == "windows" {
		dirAbs, pathAbs = strings.ToLower(dirAbs), strings.ToLower(pathAbs)
	}
	rel, err := filepath.Rel(dirAbs, pathAbs)
	if err != nil {
		// Different volumes: the path cannot be inside the data directory.
		return false, nil
	}
	if rel == "." {
		return true, nil
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel), nil
}

// utf8BOM is the byte order mark prepended by some Windows editors. Clients
// accept it in JSON input files but never write it.
const utf8BOM = "\xef\xbb\xbf"

// trimBOM removes one leading UTF-8 BOM so JSON files saved with it parse.
func trimBOM(raw []byte) []byte {
	if len(raw) >= len(utf8BOM) && string(raw[:len(utf8BOM)]) == utf8BOM {
		return raw[len(utf8BOM):]
	}
	return raw
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
	switch domain.CodeOf(err) {
	case domain.CodeInvalidRequest:
		return exitUsage, codeUsage
	case domain.CodeConflict:
		return exitConflict, codeConflict
	default:
		return exitGeneral, codeGeneral
	}
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
