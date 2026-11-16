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
		{"internal providers banned", "core/internal", p + "/providers/decision-drex", false, false},
		{"internal providers sub banned", "core/internal", p + "/providers/marketdata-binance", false, false},
		{"internal providers test banned", "core/internal", p + "/providers/decision-drex", true, false},
		{"internal cmd allowed", "core/internal", p + "/core/cmd/vend", false, true},
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
		{"core/internal/gate/gate.go", "core/internal"},
		{"core/internal/cli/cli.go", "core/internal"},
		{"core/internal/cli/cli_test.go", "core/internal"},
		{"core/cmd/vend/main.go", "other"},
	}
	for _, tc := range tests {
		if got := layerOf(tc.rel); got != tc.want {
			t.Errorf("layerOf(%q) = %q, want %q", tc.rel, got, tc.want)
		}
	}
}
