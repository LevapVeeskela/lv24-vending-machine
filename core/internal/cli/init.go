package cli

import (
	"fmt"
	"os"

	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
)

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
