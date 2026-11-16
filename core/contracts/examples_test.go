package contracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// examplesDir is fixed relative to this test file: the test binary runs with
// cwd=core/contracts, so ../../contracts/examples points at the repo examples.
func TestContractExamples(t *testing.T) {
	examplesDir := filepath.Join("..", "..", "contracts", "examples")

	newOf := map[string]func() any{
		"decision-request.json":         func() any { return &DecisionRequest{} },
		"decision-response-choice.json": func() any { return &DecisionResponse{} },
		"decision-response-noul.json":   func() any { return &DecisionResponse{} },
		"decision-response-score.json":  func() any { return &DecisionResponse{} },
		"gate-request.json":             func() any { return &GateRequest{} },
		"gate-verdict.json":             func() any { return &GateVerdict{} },
		"event.json":                    func() any { return &Event{} },
	}

	invalid := map[string]func() any{
		// invalid-decision-request.json: missing required "model".
		"invalid-decision-request.json": func() any { return &DecisionRequest{} },
		// invalid-decision-response.json: missing required "model".
		"invalid-decision-response.json": func() any { return &DecisionResponse{} },
		// invalid-gate-request.json: "side" outside buy|sell enum.
		"invalid-gate-request.json": func() any { return &GateRequest{} },
		// invalid-gate-verdict.json: "verdict" outside enum.
		"invalid-gate-verdict.json": func() any { return &GateVerdict{} },
		// invalid-event.json: "hash" is not 64 hex chars.
		"invalid-event.json": func() any { return &Event{} },
	}

	files, err := filepath.Glob(filepath.Join(examplesDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no examples found in %s", examplesDir)
	}

	for _, f := range files {
		name := filepath.Base(f)
		mkValid, ok := newOf[name]
		mkInvalid, okInv := invalid[name]
		if !ok && !okInv {
			t.Errorf("example %s is not covered by the decode table; add it", name)
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if ok {
			v := mkValid()
			if err := json.Unmarshal(data, v); err != nil {
				t.Errorf("valid example %s failed to decode: %v", name, err)
			}
		}
		if okInv {
			v := mkInvalid()
			if err := json.Unmarshal(data, v); err == nil {
				t.Errorf("invalid example %s decoded without error, want error", name)
			}
		}
	}

	for name := range newOf {
		if _, err := os.Stat(filepath.Join(examplesDir, name)); err != nil {
			t.Errorf("table entry %s has no file in %s", name, examplesDir)
		}
	}
	for name := range invalid {
		if _, err := os.Stat(filepath.Join(examplesDir, name)); err != nil {
			t.Errorf("table entry %s has no file in %s", name, examplesDir)
		}
	}
}
