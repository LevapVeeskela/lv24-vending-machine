package gate

import (
	"strings"
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

func TestDefaultPolicy(t *testing.T) {
	got := DefaultPolicy()
	want := domain.DefaultGatePolicy()
	if got != want {
		t.Fatalf("DefaultPolicy = %+v, want %+v", got, want)
	}
	if got.Version != "gate-v1" || got.MinPass != 0.55 || got.MinBlock != 0.50 {
		t.Fatalf("unexpected defaults: %+v", got)
	}
}

func TestNormalize(t *testing.T) {
	got := Normalize(domain.GatePolicy{})
	if got.Version != "gate-v1" || got.MinPass != 0.55 || got.MinBlock != 0.50 {
		t.Fatalf("Normalize zero = %+v", got)
	}
	keep := domain.GatePolicy{Version: "gate-v2", MinPass: 0.7, MinBlock: 0.3}
	if got := Normalize(keep); got != keep {
		t.Fatalf("Normalize overwrote non-zero: %+v", got)
	}
}

func TestCheckOrderFrozen(t *testing.T) {
	want := []string{"intent", "provider", "answers", "data_quality", "risk_check", "directional", "confidence"}
	if len(CheckOrder) != len(want) {
		t.Fatalf("CheckOrder = %v", CheckOrder)
	}
	for i := range want {
		if CheckOrder[i] != want[i] {
			t.Fatalf("CheckOrder = %v, want %v", CheckOrder, want)
		}
	}
}

func TestPolicyHashStableAndSensitive(t *testing.T) {
	base := DefaultPolicy()
	h1 := PolicyHash(base)
	h2 := PolicyHash(base)
	if h1 != h2 {
		t.Fatalf("hash unstable: %q vs %q", h1, h2)
	}
	if !strings.HasPrefix(h1, "sha256:") || len(h1) != len("sha256:")+64 {
		t.Fatalf("bad hash format: %q", h1)
	}
	// Zero policy normalizes to the same hash.
	if h := PolicyHash(domain.GatePolicy{}); h != h1 {
		t.Fatalf("zero policy hash %q != default %q", h, h1)
	}
	changed := base
	changed.MinPass = 0.6
	if h := PolicyHash(changed); h == h1 {
		t.Fatalf("hash did not change with MinPass")
	}
	changed = base
	changed.MinBlock = 0.4
	if h := PolicyHash(changed); h == h1 {
		t.Fatalf("hash did not change with MinBlock")
	}
	changed = base
	changed.Version = "gate-v2"
	if h := PolicyHash(changed); h == h1 {
		t.Fatalf("hash did not change with version")
	}
	// Sub-1e-6 differences must hash differently (exact round-trip format).
	a := base
	a.MinPass = 0.5500001
	b := base
	b.MinPass = 0.5500004
	if ha, hb := PolicyHash(a), PolicyHash(b); ha == hb {
		t.Fatalf("hash collision for 0.5500001 vs 0.5500004: %q", ha)
	}
}

func TestQuestionsFrozen(t *testing.T) {
	qs := Questions()
	if len(qs) != 4 {
		t.Fatalf("len(Questions) = %d, want 4", len(qs))
	}
	wantIDs := []string{"data_quality", "signal_alignment", "market_regime", "risk_check"}
	wantKeys := [][]string{
		{"ok", "insufficient"},
		{"aligned", "neutral", "conflict"},
		{"favorable", "neutral", "adverse"},
		{"pass", "block"},
	}
	for i, id := range wantIDs {
		q := qs[i]
		if q.ID != id {
			t.Fatalf("question %d id = %q, want %q", i, q.ID, id)
		}
		if q.Type != domain.QuestionChoice {
			t.Fatalf("question %q type = %q, want choice", id, q.Type)
		}
		if q.Instructions == "" {
			t.Fatalf("question %q has empty instructions", id)
		}
		if len(q.Options) != len(wantKeys[i]) {
			t.Fatalf("question %q options = %v", id, q.Options)
		}
		for j, k := range wantKeys[i] {
			if q.Options[j].Key != k {
				t.Fatalf("question %q option %d = %q, want %q", id, j, q.Options[j].Key, k)
			}
			if q.Options[j].Description == "" {
				t.Fatalf("question %q option %q has empty description", id, k)
			}
		}
	}
}
