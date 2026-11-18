package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
)

func TestDemoDeterministicAndSynthetic(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	dirs := []string{t.TempDir(), t.TempDir()}
	var reports []domain.Report
	for _, dir := range dirs {
		stdout.Reset()
		stderr.Reset()
		if code := runArgs(env, "demo", "--data-dir", dir, "--json"); code != exitOK {
			t.Fatalf("demo --json = %d, stderr=%s", code, stderr.String())
		}
		var rep domain.Report
		if err := json.Unmarshal([]byte(stdout.String()), &rep); err != nil {
			t.Fatalf("decode demo JSON: %v\n%s", err, stdout.String())
		}
		reports = append(reports, rep)
	}
	a, b := reports[0], reports[1]
	if a.LabeledCount != 120 || a.AwaitingLabeling != 20 {
		t.Fatalf("demo counts = labeled %d awaiting %d, want 120/20", a.LabeledCount, a.AwaitingLabeling)
	}
	if a.Comparisons == nil || a.Comparisons.CalibrationCount != 120 {
		t.Fatalf("demo comparisons = %+v, want 120 calibration pairs", a.Comparisons)
	}
	if a.Comparisons.Brier != b.Comparisons.Brier || a.Comparisons.ECE != b.Comparisons.ECE || a.Comparisons.AUC != b.Comparisons.AUC {
		t.Fatalf("demo is not deterministic: %+v vs %+v", a.Comparisons, b.Comparisons)
	}
	if a.G1 == nil || !a.G1.Synthetic || a.G1.Labels != 0 || a.G1.ActiveDays != 0 || a.G1.Eligible {
		t.Fatalf("demo G1 = %+v, want synthetic and zeroed", a.G1)
	}
	if !a.Synthetic {
		t.Fatal("demo report is not marked synthetic")
	}
}

func TestDemoStatusAndShare(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	dir := t.TempDir()
	if code := runArgs(env, "demo", "--data-dir", dir, "--json"); code != exitOK {
		t.Fatalf("demo = %d, stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "status", "--data-dir", dir); code != exitOK {
		t.Fatalf("status = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"SYNTHETIC", "g1: labels 0/100", "active days 0/7"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status misses %q:\n%s", want, out)
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "demo", "--share"); code != exitUsage {
		t.Fatalf("demo --share = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "synthetic journal is not G1 evidence") {
		t.Fatalf("share stderr misses the refusal: %q", stderr.String())
	}
}

func TestDemoKeepAndExistingJournal(t *testing.T) {
	env, stdout, stderr := testEnv(t)
	dir := t.TempDir()
	if code := runArgs(env, "demo", "--data-dir", dir, "--keep"); code != exitOK {
		t.Fatalf("demo --keep = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "demo data dir:") {
		t.Fatalf("keep output misses the path: %q", stdout.String())
	}
	if _, err := os.Stat(config.DBPath(dir)); err != nil {
		t.Fatalf("demo journal was not kept: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "journal", "verify", "--data-dir", dir); code != exitOK {
		t.Fatalf("journal verify = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "journal ok:") {
		t.Fatalf("journal verify output = %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runArgs(env, "demo", "--data-dir", dir); code != exitConflict {
		t.Fatalf("demo into an existing journal = %d, want %d", code, exitConflict)
	}
	if !strings.Contains(stderr.String(), "existing journal") {
		t.Fatalf("existing-journal stderr = %q", stderr.String())
	}
}

func TestDemoExplicitDirIsNotDeletedWithoutKeep(t *testing.T) {
	env, _, stderr := testEnv(t)
	dir := filepath.Join(t.TempDir(), "demo-data")
	if code := runArgs(env, "demo", "--data-dir", dir); code != exitOK {
		t.Fatalf("demo = %d, stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(config.DBPath(dir)); err != nil {
		t.Fatalf("an explicit --data-dir must be kept: %v", err)
	}
}
