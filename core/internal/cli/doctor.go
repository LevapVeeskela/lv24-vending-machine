package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/gate"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/registry"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// Live probe bounds: one decision call, two candles and one Telegram
// message, each under its own deadline.
const (
	doctorLiveTimeout  = 20 * time.Second
	doctorLiveInterval = "1h"
	doctorLiveCandles  = 2
	doctorSymbol       = "BTCUSDT"
)

// docCheck is one doctor result. Live marks the opt-in network probes,
// whose detail is "ok" (plus cost for the decision call) or an error code.
type docCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Live   bool   `json:"live,omitempty"`
}

const doctorUsage = "run vend doctor [--live] [--symbol SYMBOL] [--save-raw FILE] [--config FILE] [--data-dir DIR]"

func (r *runner) cmdDoctor(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(), doctorUsage, false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("doctor")
	live := fs.Bool("live", false, "run opt-in network probes (decision, candles, telegram)")
	symbol := fs.String("symbol", doctorSymbol, "symbol for the live candles probe")
	saveRaw := fs.String("save-raw", "", "write the raw decision response to FILE (0600)")
	saveForce := fs.Bool("force", false, "overwrite an existing --save-raw file")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)), doctorUsage, false, nil)
	}
	if !*live && (strings.TrimSpace(*saveRaw) != "" || flagWasSet(fs, "symbol") || *saveForce) {
		return r.fail(jsonOut, exitUsage, codeUsage,
			"--symbol, --save-raw and --force require --live", doctorUsage, false, nil)
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
	cfgValid := false
	if cfgErr != nil {
		add("config", false, fmt.Sprintf("cannot load %s: %v", cfgPath, firstLine(cfgErr)))
	} else if verr := cfg.Validate(); verr != nil {
		add("config", false, verr.Error())
	} else {
		cfgValid = true
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

	var reg *registry.Registry
	if cfgErr != nil {
		add("providers", false, "config is unavailable")
	} else if built, err := r.newRegistry(cfg, nil); err != nil {
		add("providers", false, err.Error())
	} else {
		reg = built
		var ids, decisions []string
		for _, d := range reg.Descriptors() {
			if d.Kind == ports.KindDecision {
				decisions = append(decisions, d.ID)
			}
			ids = append(ids, d.ID)
		}
		// config validate cannot check the provider id: it does not see the
		// provider registry. doctor is the preflight that does.
		if _, err := reg.Decision(cfg.Providers.Decision.ID); err != nil {
			registered := strings.Join(decisions, ", ")
			if registered == "" {
				registered = "(none)"
			}
			add("providers", false, fmt.Sprintf(
				"unknown decision provider %q; registered: %s",
				cfg.Providers.Decision.ID, registered))
		} else {
			add("providers", true, strings.Join(ids, ", "))
		}
	}

	// The loopback gate API refuses to start without its bearer token, so a
	// missing token is a hard doctor failure, not a warning like the
	// provider secret: doctor is the preflight for vend serve.
	if r.getenv(localAPITokenEnv) == "" {
		add("secrets", false, fmt.Sprintf(
			"%s is not set; vend serve requires it", localAPITokenEnv))
	} else if cfgErr != nil {
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

	if *live {
		checks = append(checks, r.liveChecks(cfg, cfgErr, cfgValid, reg, *symbol, *saveRaw, dataDir, *saveForce)...)
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
			if c.Live {
				fmt.Fprintf(r.stdout, "%s: %s\n", c.Name, c.Detail)
				continue
			}
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

// liveChecks runs the opt-in network probes: one decision call with the
// frozen gate questions, two candles for symbol and, when enabled, one
// Telegram message. Each check reports only "ok" or a domain error code;
// tokens, URLs and response bodies are never printed.
func (r *runner) liveChecks(cfg config.Config, cfgErr error, cfgValid bool,
	reg *registry.Registry, symbol, saveRaw, dataDir string, saveForce bool,
) []docCheck {
	var out []docCheck
	add := func(name string, ok bool, detail string) {
		out = append(out, docCheck{Name: name, OK: ok, Detail: detail, Live: true})
	}
	if !cfgValid {
		code := string(domain.CodeInvalidRequest)
		if cfgErr != nil {
			code = liveCode(cfgErr)
		}
		add("decision", false, code)
		add("candles", false, code)
		return out
	}
	if reg == nil {
		code := string(domain.CodeUnavailable)
		add("decision", false, code)
		add("candles", false, code)
		if cfg.Notify.Telegram.Enabled {
			add("telegram", false, code)
		}
		return out
	}
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		symbol = doctorSymbol
	}

	var raw []byte
	if dec, err := reg.Decision(cfg.Providers.Decision.ID); err != nil {
		add("decision", false, string(domain.CodeUnavailable))
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), doctorLiveTimeout)
		resp, derr := dec.Decide(ctx, domain.DecisionRequest{
			Model:     cfg.Providers.Decision.Model,
			State:     doctorExampleState(symbol),
			Questions: gate.Questions(),
			Timeout:   doctorLiveTimeout,
		})
		cancel()
		if derr != nil {
			add("decision", false, liveDetail(derr))
		} else {
			raw = resp.Raw
			// The live answer goes through the same gate policy as a real
			// signal: a syntactically valid response that the gate classifies
			// as invalid output is a FAIL of the probe, not a success.
			verdict := gate.Evaluate(domain.GateRequest{
				Instrument: domain.Instrument{Venue: "binance", Symbol: symbol},
				Side:       domain.SideBuy,
				IntentKind: domain.IntentEntry,
				Timeframe:  doctorLiveInterval,
				TS:         r.clock(),
				Price:      domain.MustParseDecimal("100"),
			}, domain.DecisionOutcome{Response: &resp}, domain.GatePolicy{
				Version:  cfg.Gate.Version,
				MinPass:  cfg.Gate.MinPass,
				MinBlock: cfg.Gate.MinBlock,
			})
			ok := !(verdict.Verdict == domain.VerdictAbstain && verdict.Code == gate.CodeProviderInvalidOutput)
			status := "ok"
			if !ok {
				status = "FAIL"
			}
			add("decision", ok, fmt.Sprintf("%s verdict=%s code=%s cost=%s USD",
				status, verdict.Verdict, verdict.Code, resp.Usage.CostUSD.String()))
		}
	}
	if strings.TrimSpace(saveRaw) != "" {
		if len(raw) == 0 {
			add("save_raw", false, string(domain.CodeUnavailable))
		} else if werr := writeFileNew(saveRaw, raw, 0o600,
			newFileOptions{DataDir: dataDir, Force: saveForce}); werr != nil {
			add("save_raw", false, werr.Error())
		} else {
			add("save_raw", true, "ok")
		}
	}

	if cp, err := reg.Candle(cfg.Providers.MarketData.ID); err != nil {
		add("candles", false, string(domain.CodeUnavailable))
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), doctorLiveTimeout)
		got, cerr := cp.Candles(ctx, domain.CandleRequest{
			Symbol: symbol, Interval: doctorLiveInterval, Limit: doctorLiveCandles,
		})
		cancel()
		switch {
		case cerr != nil:
			add("candles", false, liveDetail(cerr))
		case len(got) < doctorLiveCandles:
			add("candles", false, string(domain.CodeInvalidOutput))
		default:
			add("candles", true, "ok")
		}
	}

	if cfg.Notify.Telegram.Enabled {
		nt, err := reg.Notifier("notify-telegram")
		if err != nil {
			add("telegram", false, string(domain.CodeUnavailable))
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), doctorLiveTimeout)
			serr := nt.Send(ctx, ports.Notification{
				Kind: "doctor", Text: "vend doctor --live: test message",
			})
			cancel()
			if serr != nil {
				add("telegram", false, liveDetail(serr))
			} else {
				add("telegram", true, "ok")
			}
		}
	}
	return out
}

// doctorExampleState is the fixed synthetic state sent by the live
// decision probe; it carries no account data and no secrets.
func doctorExampleState(symbol string) map[string]any {
	return map[string]any{
		"source":    "vend doctor --live",
		"mode":      "probe",
		"symbol":    symbol,
		"timeframe": doctorLiveInterval,
		"side":      "buy",
		"price":     "100",
	}
}

// liveCode maps an error to the short code a live check prints.
func liveCode(err error) string {
	if c := domain.CodeOf(err); c != domain.CodeUnknown {
		return string(c)
	}
	return string(domain.CodeUnavailable)
}

// liveDetail adds a short operator action to a failed live-probe code. The
// provider message itself is never printed: it may carry a token or a body.
func liveDetail(err error) string {
	code := liveCode(err)
	switch code {
	case string(domain.CodeAuth):
		return code + " (check the provider secret and quota)"
	case string(domain.CodePaymentRequired):
		return code + " (top up or enable the provider quota)"
	case string(domain.CodeRateLimit):
		return code + " (wait and retry)"
	case string(domain.CodeInvalidRequest):
		return code + " (check the provider URL, model and profile)"
	case string(domain.CodeInvalidOutput):
		return code + " (retry; if it repeats, save the raw answer)"
	case string(domain.CodeTimeout):
		return code + " (check the network/provider and retry)"
	case string(domain.CodeIPBanned):
		return code + " (do not retry; change the network/provider)"
	case string(domain.CodeRegionUnavailable):
		return code + " (use an available region/provider)"
	default:
		return code + " (check the provider URL and network, then retry)"
	}
}

// flagWasSet reports whether name was given explicitly on the command line.
func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
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
