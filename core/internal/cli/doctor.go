package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/gate"
)

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
