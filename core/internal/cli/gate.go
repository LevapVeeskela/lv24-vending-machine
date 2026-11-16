package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/gateflow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/gate"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

func (r *runner) cmdGate(args []string) int {
	if len(args) < 1 {
		r.fail(false, exitUsage, codeUsage, "missing gate subcommand",
			"run vend gate test --request FILE", false, nil)
		return exitUsage
	}
	switch args[0] {
	case "test":
		return r.cmdGateTest(args[1:])
	case "report":
		return r.cmdGateReport(args[1:])
	default:
		r.fail(false, exitUsage, codeUsage,
			fmt.Sprintf("unknown gate subcommand %q", args[0]),
			"run vend gate test --request FILE", false, nil)
		return exitUsage
	}
}

// fixtureProvider replays a decoded fixture as the decision response.
type fixtureProvider struct {
	resp domain.DecisionResponse
}

func (p fixtureProvider) Descriptor() ports.Descriptor {
	return ports.Descriptor{ID: "fixture", Name: "fixture", Kind: ports.KindDecision, Version: "v0"}
}

func (p fixtureProvider) Decide(_ context.Context, _ domain.DecisionRequest) (domain.DecisionResponse, error) {
	return p.resp, nil
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
	saveRaw := fs.String("save-raw", "", "write the raw provider response to FILE (0600)")
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

	var provider ports.Decision
	if strings.TrimSpace(*fixFlag) != "" {
		resp, raw, failCode, failErr := readDecisionFixture(*fixFlag, cfg.Providers.Decision.Model)
		if failErr != nil {
			hint := "fix the fixture file"
			errCode := codeUsage
			if failCode == exitNotFound {
				errCode = codeNotFound
			}
			return r.fail(jsonOut, failCode, errCode, failErr.Error(), hint, false, nil)
		}
		if verr := validateFixture(resp, gate.Questions()); verr != nil {
			return r.fail(jsonOut, exitUsage, codeUsage, verr.Error(), "fix the fixture file", false, nil)
		}
		resp.Raw = json.RawMessage(raw)
		provider = fixtureProvider{resp: resp}
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
		provider = p
	}
	// IntentExit never builds the registry or calls the provider.

	jr, closer, err := r.openJournalAt(config.DBPath(dataDir))
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the data directory is writable and retry", false, nil)
	}
	defer func() { _ = closer.Close() }()

	flow, err := gateflow.New(jr, provider, ports.Deps{
		Clock:  r.clock,
		NewID:  r.newID,
		Logger: slog.New(slog.NewTextHandler(r.stderr, nil)),
	}, gateflow.Config{
		Model:       cfg.Providers.Decision.Model,
		Policy:      domain.GatePolicy{Version: cfg.Gate.Version, MinPass: cfg.Gate.MinPass, MinBlock: cfg.Gate.MinBlock},
		CodeVersion: r.ver,
	})
	if err != nil {
		return r.fail(jsonOut, exitGeneral, codeGeneral, err.Error(),
			"check the vend wiring and retry", false, nil)
	}
	res, err := flow.Evaluate(context.Background(), gateReq)
	if err != nil {
		code, errCode := loadFailureCode(err)
		if domain.CodeOf(err) == domain.CodeInvalidRequest {
			code, errCode = exitUsage, codeUsage
		}
		return r.fail(jsonOut, code, errCode, err.Error(),
			"fix the request file or check the data directory", false, nil)
	}

	if strings.TrimSpace(*saveRaw) != "" {
		if res.Response == nil || len(res.Response.Raw) == 0 {
			return r.fail(jsonOut, exitGeneral, codeGeneral,
				"raw response unavailable, e.g. idempotent replay",
				"omit --save-raw on replay or rerun with a new signalId", false, nil)
		}
		if werr := os.WriteFile(*saveRaw, res.Response.Raw, 0o600); werr != nil {
			return r.fail(jsonOut, exitGeneral, codeGeneral, werr.Error(),
				"check the output path is writable and retry", false, nil)
		}
		// Best effort: an existing file must not keep wide permissions.
		_ = os.Chmod(*saveRaw, 0o600)
	}

	if jsonOut {
		raw, err := json.Marshal(res.Verdict)
		if err != nil {
			return r.fail(true, exitGeneral, codeGeneral, err.Error(),
				"retry the command", false, nil)
		}
		fmt.Fprintln(r.stdout, string(raw))
	} else {
		printVerdictHuman(r.stdout, res.Verdict)
	}

	if res.ProviderErr != nil {
		switch domain.CodeOf(res.ProviderErr) {
		case domain.CodeAuth:
			return r.fail(jsonOut, exitForbidden, codeForbidden, res.ProviderErr.Error(),
				fmt.Sprintf("set %s and retry", config.SecretEnvName(cfg.Providers.Decision.SecretRef)),
				domain.IsRetryable(res.ProviderErr), nil)
		case domain.CodeTimeout:
			return r.fail(jsonOut, exitTimeout, codeTimeout, res.ProviderErr.Error(),
				"retry later; the request timed out", true, nil)
		default:
			return r.fail(jsonOut, exitProvider, codeProvider, res.ProviderErr.Error(),
				"retry later or check provider status", domain.IsRetryable(res.ProviderErr), nil)
		}
	}
	if res.Verdict.Verdict == domain.VerdictReject {
		return r.fail(jsonOut, exitReject, codeReject,
			fmt.Sprintf("gate rejected: %s", res.Verdict.Code),
			"inspect the verdict reasons and adjust the intent", false, nil)
	}
	return exitOK
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

// readDecisionFixture parses an offline decision response and returns it
// with the raw file bytes for --save-raw.
func readDecisionFixture(path, model string) (domain.DecisionResponse, []byte, int, error) {
	var resp domain.DecisionResponse
	raw, err := os.ReadFile(path)
	if err != nil {
		if isNotFound(err) || os.IsNotExist(err) {
			return resp, nil, exitNotFound, fmt.Errorf("fixture file not found at %s", path)
		}
		return resp, nil, exitGeneral, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&resp); err != nil {
		return resp, nil, exitUsage, fmt.Errorf("invalid fixture JSON: %v", firstLine(err))
	}
	resp.RequestedModel = model
	return resp, raw, exitOK, nil
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
		n := len(q.Options)
		if a.Score == nil || math.IsNaN(*a.Score) || math.IsInf(*a.Score, 0) {
			return fmt.Errorf("invalid fixture: question %q: score is missing or not finite", q.ID)
		}
		if len(a.Legend) == 0 {
			return fmt.Errorf("invalid fixture: question %q: score legend is empty", q.ID)
		}
		if len(a.Legend) != n {
			return fmt.Errorf("invalid fixture: question %q: score legend length does not match options", q.ID)
		}
		if len(a.Probabilities) == 0 {
			return fmt.Errorf("invalid fixture: question %q: score probabilities are empty", q.ID)
		}
		if len(a.Probabilities) != len(a.Legend) {
			return fmt.Errorf("invalid fixture: question %q: score probability count does not match legend", q.ID)
		}
		sum := 0.0
		for i := range a.Legend {
			p, ok := a.Probabilities[strconv.Itoa(i)]
			if !ok {
				return fmt.Errorf("invalid fixture: question %q: score probabilities miss an index", q.ID)
			}
			if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
				return fmt.Errorf("invalid fixture: question %q: score probability out of range [0, 1]", q.ID)
			}
			sum += p
		}
		if math.Abs(sum-1) > 0.001 {
			return fmt.Errorf("invalid fixture: question %q: score probabilities do not sum to 1", q.ID)
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
