package main

import "testing"

func TestClassify(t *testing.T) {
	p := modulePrefix
	tests := []struct {
		name       string
		layer      string
		importPath string
		isTest     bool
		want       bool
	}{
		{"domain stdlib", "core/domain", "context", false, true},
		{"domain stdlib slash", "core/domain", "encoding/json", false, true},
		{"domain self", "core/domain", p + "/core/domain", false, true},
		{"domain rejects ports", "core/domain", p + "/core/ports", false, false},
		{"domain rejects internal", "core/domain", p + "/core/internal/gate", false, false},
		{"domain rejects external", "core/domain", "github.com/foo/bar", false, false},

		{"ports stdlib", "core/ports", "io", false, true},
		{"ports domain", "core/ports", p + "/core/domain", false, true},
		{"ports domain sub", "core/ports", p + "/core/domain/money", false, true},
		{"ports self", "core/ports", p + "/core/ports", false, true},
		{"ports rejects internal", "core/ports", p + "/core/internal/gate", false, false},
		{"ports rejects porttest", "core/ports", p + "/core/porttest", true, false},

		{"provider stdlib", "providers/decision-drex", "net/http", false, true},
		{"provider domain", "providers/decision-drex", p + "/core/domain", false, true},
		{"provider ports", "providers/decision-drex", p + "/core/ports", false, true},
		{"provider porttest non-test", "providers/decision-drex", p + "/core/porttest", false, false},
		{"provider porttest test", "providers/decision-drex", p + "/core/porttest", true, true},
		{"provider own module", "providers/decision-drex", p + "/providers/decision-drex/internal/foo", false, true},
		{"provider other provider", "providers/decision-drex", p + "/providers/marketdata-binance", false, false},
		{"provider internal", "providers/decision-drex", p + "/core/internal/gate", false, false},
		{"provider contracts", "providers/decision-drex", p + "/core/contracts", false, false},

		{"other unrestricted", "other", "github.com/foo/bar", false, true},
		{"other internal ok", "other", p + "/core/internal/gate", false, true},

		{"internal stdlib", "core/internal", "encoding/json", false, true},
		{"internal cross ok", "core/internal", p + "/core/internal/config", false, true},
		{"internal registry ok", "core/internal", p + "/core/internal/registry", false, true},
		{"internal store ok", "core/internal", p + "/core/internal/store/sqlite", false, true},
		{"internal store test ok", "core/internal", p + "/core/internal/store/sqlite", true, true},
		{"internal gate ok", "core/internal", p + "/core/internal/gate", false, true},
		{"internal app ok", "core/internal", p + "/core/internal/app/gateflow", false, true},
		{"internal providers banned", "core/internal", p + "/providers/decision-drex", false, false},
		{"internal providers sub banned", "core/internal", p + "/providers/marketdata-binance", false, false},
		{"internal providers test banned", "core/internal", p + "/providers/decision-drex", true, false},
		{"internal cmd allowed", "core/internal", p + "/core/cmd/vend", false, true},

		{"gate stdlib", "core/internal/gate", "encoding/json", false, true},
		{"gate domain", "core/internal/gate", p + "/core/domain", false, true},
		{"gate domain sub", "core/internal/gate", p + "/core/domain/money", false, true},
		{"gate test stdlib", "core/internal/gate", "testing", true, true},
		{"gate rejects ports", "core/internal/gate", p + "/core/ports", false, false},
		{"gate rejects ports test", "core/internal/gate", p + "/core/ports", true, false},
		{"gate rejects resilience", "core/internal/gate", p + "/core/internal/resilience", false, false},
		{"gate rejects app", "core/internal/gate", p + "/core/internal/app/gateflow", false, false},
		{"gate rejects journal", "core/internal/gate", p + "/core/internal/journal", false, false},
		{"gate rejects config", "core/internal/gate", p + "/core/internal/config", false, false},
		{"gate rejects providers", "core/internal/gate", p + "/providers/decision-drex", false, false},
		{"gate rejects providers test", "core/internal/gate", p + "/providers/decision-drex", true, false},

		{"app stdlib", "core/internal/app", "context", false, true},
		{"app domain", "core/internal/app", p + "/core/domain", false, true},
		{"app ports", "core/internal/app", p + "/core/ports", false, true},
		{"app gate", "core/internal/app", p + "/core/internal/gate", false, true},
		{"app resilience", "core/internal/app", p + "/core/internal/resilience", false, true},
		{"app test ports", "core/internal/app", p + "/core/ports", true, true},
		{"app rejects store", "core/internal/app", p + "/core/internal/store/sqlite", false, false},
		{"app rejects store test", "core/internal/app", p + "/core/internal/store/sqlite", true, false},
		{"app rejects cli", "core/internal/app", p + "/core/internal/cli", false, false},
		{"app rejects journal", "core/internal/app", p + "/core/internal/journal", false, false},
		{"app rejects config", "core/internal/app", p + "/core/internal/config", true, false},
		{"app rejects contracts", "core/internal/app", p + "/core/contracts", false, false},
		{"app rejects providers", "core/internal/app", p + "/providers/decision-drex", false, false},
		{"app rejects providers test", "core/internal/app", p + "/providers/decision-drex", true, false},
		{"app rejects registry", "core/internal/app", p + "/core/internal/registry", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.layer, tc.importPath, tc.isTest); got != tc.want {
				t.Errorf("classify(%q, %q, isTest=%v) = %v, want %v",
					tc.layer, tc.importPath, tc.isTest, got, tc.want)
			}
		})
	}
}

func TestLayerOf(t *testing.T) {
	tests := []struct {
		rel  string
		want string
	}{
		{"core/domain/decimal.go", "core/domain"},
		{"core/ports/decision.go", "core/ports"},
		{"providers/decision-drex/provider.go", "providers/decision-drex"},
		{"providers/marketdata-binance/x_test.go", "providers/marketdata-binance"},
		{"tools/lintimports/main.go", "other"},
		{"core/internal/gate/gate.go", "core/internal/gate"},
		{"core/internal/gate/gate_test.go", "core/internal/gate"},
		{"core/internal/app/gateflow/flow.go", "core/internal/app"},
		{"core/internal/app/gateflow/flow_test.go", "core/internal/app"},
		{"core/internal/cli/cli.go", "core/internal"},
		{"core/internal/cli/gate.go", "core/internal"},
		{"core/internal/cli/cli_test.go", "core/internal"},
		{"core/internal/config/config.go", "core/internal"},
		{"core/cmd/vend/main.go", "other"},
	}
	for _, tc := range tests {
		if got := layerOf(tc.rel); got != tc.want {
			t.Errorf("layerOf(%q) = %q, want %q", tc.rel, got, tc.want)
		}
	}
}
