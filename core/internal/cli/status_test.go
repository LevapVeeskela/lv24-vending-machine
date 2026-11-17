package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/app/shadow"
	"github.com/LevapVeeskela/lv24-vending-machine/core/internal/config"
)

// writeShadowStatusFile writes a runner status snapshot into dataDir.
func writeShadowStatusFile(t *testing.T, dataDir string, st shadow.Status) {
	t.Helper()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("encode status: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, shadowStatusName), raw, 0o600); err != nil {
		t.Fatalf("write status: %v", err)
	}
}

// seedLabeledOutcome appends one outcome.labeled event to the journal.
func seedLabeledOutcome(t *testing.T, env Env, dataDir string) {
	t.Helper()
	jr, closer, err := env.OpenJournal(config.DBPath(dataDir))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer func() { _ = closer.Close() }()
	if _, err := jr.Append(context.Background(), domain.Event{
		Kind:          domain.EventOutcomeLabeled,
		TS:            time.Date(2026, time.November, 10, 6, 0, 0, 0, time.UTC),
		SchemaVersion: domain.EventSchemaVersion,
		Payload:       []byte(`{"signalId":"sig-status-1"}`),
		CodeVersion:   "v-test",
	}); err != nil {
		t.Fatalf("append labeled outcome: %v", err)
	}
}

func TestStatusJSON(t *testing.T) {
	env, stdout, stderr, _, dataDir := initTestEnv(t)
	seedLabeledOutcome(t, env, dataDir)
	lastLabel := time.Date(2026, time.November, 10, 7, 0, 0, 0, time.UTC)
	writeShadowStatusFile(t, dataDir, shadow.Status{
		Version:        "0.1.0-m1",
		PolicyHash:     "abcdef0123456789",
		LastLabelAt:    lastLabel,
		LastCycleError: "unavailable",
		UpdatedAt:      lastLabel,
	})

	if code := runArgs(env, "status", "--json", "--data-dir", dataDir); code != 0 {
		t.Fatalf("status --json = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var got shadow.Status
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode status JSON %q: %v", stdout.String(), err)
	}
	if got.Version != "0.1.0-m1" || got.PolicyHash != "abcdef0123456789" {
		t.Fatalf("status header = %+v", got)
	}
	if got.Events != 1 || got.Labeled != 1 {
		t.Fatalf("status counters = events %d labeled %d, want 1/1", got.Events, got.Labeled)
	}
	if !got.LastLabelAt.Equal(lastLabel) || got.LastCycleError != "unavailable" {
		t.Fatalf("status tail = %+v", got)
	}
}

func TestStatusMissingStatusFile(t *testing.T) {
	env, stdout, stderr, _, dataDir := initTestEnv(t)

	if code := runArgs(env, "status", "--json", "--data-dir", dataDir); code != 0 {
		t.Fatalf("status --json without status file = %d, stdout=%s stderr=%s",
			code, stdout.String(), stderr.String())
	}
	var got shadow.Status
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode status JSON %q: %v", stdout.String(), err)
	}
	if got.Version != "0.1.0-m0-test" {
		t.Fatalf("version = %q, want the CLI version fallback", got.Version)
	}
	if got.Events != 0 || got.Labeled != 0 {
		t.Fatalf("counters = %d/%d, want 0/0", got.Events, got.Labeled)
	}
}

func TestStatusHuman(t *testing.T) {
	env, stdout, stderr, _, dataDir := initTestEnv(t)
	seedLabeledOutcome(t, env, dataDir)
	writeShadowStatusFile(t, dataDir, shadow.Status{
		Version:    "0.1.0-m1",
		PolicyHash: "abcdef0123456789",
	})

	if code := runArgs(env, "status", "--data-dir", dataDir); code != 0 {
		t.Fatalf("status = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"version: 0.1.0-m1",
		"events: 1",
		"labeled: 1",
		"last label: never",
		"policy: abcdef012345",
		"last error: none",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("human output misses %q: %q", want, got)
		}
	}
	if strings.Contains(got, "abcdef0123456") {
		t.Fatalf("human output is not truncating the policy: %q", got)
	}
}

func TestStatusMissingDB(t *testing.T) {
	env, _, stderr, _, dataDir := initTestEnv(t)
	if err := os.Remove(config.DBPath(dataDir)); err != nil {
		t.Fatalf("remove journal: %v", err)
	}
	if code := runArgs(env, "status", "--json", "--data-dir", dataDir); code != exitNotFound {
		t.Fatalf("status without database = %d, want %d, stderr=%s",
			code, exitNotFound, stderr.String())
	}
}

func TestStatusJSONBeforeServeOmitsZeroValues(t *testing.T) {
	env, stdout, stderr, _, dataDir := initTestEnv(t)
	if code := runArgs(env, "status", "--json", "--data-dir", dataDir); code != exitOK {
		t.Fatalf("status --json before serve = %d, stdout=%s stderr=%s",
			code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, forbidden := range []string{"0001-01-01", `"policyHash"`, `"updatedAt"`, `"lastLabelAt"`} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("status JSON before serve contains %s: %s", forbidden, out)
		}
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode status JSON %q: %v", out, err)
	}
	if got["version"] != "0.1.0-m0-test" || got["events"] != float64(0) || got["labeled"] != float64(0) {
		t.Fatalf("status JSON header = %v", got)
	}
}

func TestStatusJSONBOM(t *testing.T) {
	env, stdout, stderr, _, dataDir := initTestEnv(t)
	body := `{"version":"0.1.0-m1","policyHash":"abcdef0123456789",` +
		`"lastLabelAt":"2026-11-10T07:00:00Z","updatedAt":"2026-11-10T07:00:00Z"}`
	path := filepath.Join(dataDir, shadowStatusName)
	if err := os.WriteFile(path, append([]byte("\xef\xbb\xbf"), body...), 0o600); err != nil {
		t.Fatalf("write BOM status: %v", err)
	}
	if code := runArgs(env, "status", "--json", "--data-dir", dataDir); code != exitOK {
		t.Fatalf("status --json with BOM = %d, stdout=%s stderr=%s",
			code, stdout.String(), stderr.String())
	}
	var got shadow.Status
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode status JSON %q: %v", stdout.String(), err)
	}
	if got.PolicyHash != "abcdef0123456789" || got.UpdatedAt.IsZero() {
		t.Fatalf("BOM status = %+v, want policy and updatedAt", got)
	}
}
