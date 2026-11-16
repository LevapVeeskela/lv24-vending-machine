package contracts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGeneratedContractsMatch regenerates contracts.gen.go into a temp dir and
// requires a byte-identical match with the committed file. Skipped with -short.
func TestGeneratedContractsMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skip codegen drift check in -short mode")
	}

	// Test cwd is core/contracts; repo root is two levels up.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	toolsDir := filepath.Join(root, "tools")
	committed := filepath.Join(root, "core", "contracts", "contracts.gen.go")

	out := filepath.Join(t.TempDir(), "contracts.gen.go")
	args := []string{
		"tool", "go-jsonschema",
		"--tags", "json",
		"--capitalization", "ID",
		"--capitalization", "USD",
		"-p", "contracts",
		"-t",
		"-o", out,
		"../contracts/jsonschema/common/v1.json",
		"../contracts/jsonschema/decision/v1.json",
		"../contracts/jsonschema/gate/v1.json",
		"../contracts/jsonschema/events/v1.json",
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = toolsDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("codegen failed: %v\n%s", err, out)
	}

	want, err := os.ReadFile(committed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got, want) {
		return
	}

	t.Errorf("contracts.gen.go drift: regenerated file differs from committed %s", committed)
	wantLines := bytes.Split(want, []byte("\n"))
	gotLines := bytes.Split(got, []byte("\n"))
	n := len(wantLines)
	if len(gotLines) < n {
		n = len(gotLines)
	}
	shown := 0
	for i := 0; i < n && shown < 20; i++ {
		if !bytes.Equal(wantLines[i], gotLines[i]) {
			t.Errorf("line %d:\n committed: %s\n regenerated: %s", i+1, wantLines[i], gotLines[i])
			shown++
		}
	}
	if len(wantLines) != len(gotLines) {
		t.Errorf("line counts differ: committed=%d regenerated=%d", len(wantLines), len(gotLines))
	}
}
