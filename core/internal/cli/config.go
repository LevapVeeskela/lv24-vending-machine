package cli

import (
	"encoding/json"
	"fmt"

	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
)

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
