package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
)

// initNextSteps returns the copy-paste commands that continue after init: the
// config check, the environment probe, an offline gate replay and the server.
// Paths are spelled out so they work from any directory.
func initNextSteps(cfgPath, dataDir string) []string {
	return []string{
		fmt.Sprintf("vend config validate --config %s", cfgPath),
		fmt.Sprintf("vend doctor --config %s --data-dir %s", cfgPath, dataDir),
		fmt.Sprintf("vend gate test --request request.json --fixture response.json --config %s --data-dir %s",
			cfgPath, dataDir),
		fmt.Sprintf("vend serve --config %s --data-dir %s", cfgPath, dataDir),
	}
}

// initQuickNextSteps returns the commands after init --quick: validate the
// profile, run the strict offline preflight and start the server. Only
// signals estimate verifies the volume, over the network.
func initQuickNextSteps(cfgPath, dataDir string) []string {
	return []string{
		fmt.Sprintf("vend config validate --config %s", cfgPath),
		fmt.Sprintf("vend doctor --strict --config %s --data-dir %s", cfgPath, dataDir),
		fmt.Sprintf("vend serve --config %s --data-dir %s", cfgPath, dataDir),
	}
}

// initQuickExports returns the copy-paste secret exports for bash and
// PowerShell. Every value is the <token> placeholder: init prints command
// templates, never secret values.
func initQuickExports(cfg config.Config) (bash, powershell []string) {
	names := []string{localAPITokenEnv}
	if ref := strings.TrimSpace(cfg.Providers.Decision.SecretRef); ref != "" {
		if name := config.SecretEnvName(ref); name != localAPITokenEnv {
			names = append(names, name)
		}
	}
	for _, name := range names {
		bash = append(bash, fmt.Sprintf("export %s='<token>'", name))
		powershell = append(powershell, fmt.Sprintf("$env:%s = \"<token>\"", name))
	}
	return bash, powershell
}

func (r *runner) cmdInit(args []string) int {
	g, rest, gerr := splitGlobals(args)
	if gerr != nil {
		return r.fail(false, exitUsage, codeUsage, gerr.Error(),
			"run vend init [--quick] [--config FILE] [--data-dir DIR] [--force]", false, nil)
	}
	jsonOut := g.json
	fs := r.newFlagSet("init")
	force := fs.Bool("force", false, "overwrite existing config")
	quick := fs.Bool("quick", false, "write the G1 quick-start profile (10 USDT symbols, 15m, trend enabled)")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return r.fail(jsonOut, exitUsage, codeUsage,
			fmt.Sprintf("unexpected argument %q", fs.Arg(0)),
			"run vend init [--quick] [--config FILE] [--data-dir DIR] [--force]", false, nil)
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
	profile := ""
	if *quick {
		cfg = config.QuickProfile()
		profile = config.QuickProfileName
		if err := cfg.Validate(); err != nil {
			return r.fail(jsonOut, exitGeneral, codeGeneral,
				fmt.Sprintf("quick profile is invalid: %v", err),
				"report this profile bug; init --quick must write a valid config", false, nil)
		}
	}
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
	next := initNextSteps(cfgPath, dataDir)
	if *quick {
		next = initQuickNextSteps(cfgPath, dataDir)
	}
	if jsonOut {
		out := map[string]any{
			"config": cfgPath, "dataDir": dataDir, "database": dbPath, "mode": cfg.Mode,
			"nextSteps": next,
		}
		if *quick {
			out["profile"] = profile
		}
		r.printJSON(out)
		return exitOK
	}
	fmt.Fprintf(r.stdout, "config: %s\n", cfgPath)
	fmt.Fprintf(r.stdout, "data dir: %s\n", dataDir)
	fmt.Fprintf(r.stdout, "database: %s\n", dbPath)
	fmt.Fprintf(r.stdout, "mode: %s\n", cfg.Mode)
	if *quick {
		fmt.Fprintf(r.stdout, "profile: %s\n", profile)
	}
	fmt.Fprintln(r.stdout, "")
	if *quick {
		bash, powershell := initQuickExports(cfg)
		fmt.Fprintln(r.stdout, "set the secrets (replace <token>; values are never written to disk):")
		fmt.Fprintln(r.stdout, "  bash:")
		for _, line := range bash {
			fmt.Fprintf(r.stdout, "    %s\n", line)
		}
		fmt.Fprintln(r.stdout, "  PowerShell:")
		for _, line := range powershell {
			fmt.Fprintf(r.stdout, "    %s\n", line)
		}
		fmt.Fprintln(r.stdout, "")
	}
	fmt.Fprintln(r.stdout, "next steps:")
	for i, step := range next {
		fmt.Fprintf(r.stdout, "  %d. %s\n", i+1, step)
	}
	if *quick {
		fmt.Fprintln(r.stdout, "  (run the exports in one shell; doctor checks the profile, not signal volume)")
	} else {
		fmt.Fprintln(r.stdout, "  (replace request.json/response.json with your files; omit --fixture to call the provider)")
	}
	return exitOK
}
