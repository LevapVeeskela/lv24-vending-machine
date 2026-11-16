package gate

import (
	"regexp"
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
	// Only an empty version is defaulted; thresholds are never filled:
	// an out-of-range threshold (including zero) is invalid, not missing.
	got := Normalize(domain.GatePolicy{})
	if got.Version != "gate-v1" {
		t.Fatalf("Normalize zero version = %q, want gate-v1", got.Version)
	}
	if got.MinPass != 0 || got.MinBlock != 0 {
		t.Fatalf("Normalize must not default thresholds: %+v", got)
	}
	keep := domain.GatePolicy{Version: "gate-v2", MinPass: 0.7, MinBlock: 0.3}
	if got := Normalize(keep); got != keep {
		t.Fatalf("Normalize overwrote non-zero: %+v", got)
	}
	// Empty version with valid thresholds normalizes to the default version.
	noversion := domain.GatePolicy{MinPass: 0.55, MinBlock: 0.5}
	if got := Normalize(noversion); got.Version != "gate-v1" || got.MinPass != 0.55 || got.MinBlock != 0.5 {
		t.Fatalf("Normalize version = %+v", got)
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

var hashFormat = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestPolicyHashFormat(t *testing.T) {
	h := PolicyHash(DefaultPolicy())
	if !hashFormat.MatchString(h) {
		t.Fatalf("bad hash format: %q (want 64 lowercase hex, no prefix)", h)
	}
	if strings.HasPrefix(h, "sha256:") {
		t.Fatalf("hash must not carry a sha256: prefix: %q", h)
	}
}

// TestPolicyHashGolden pins the default gate-v1 hash. Any intentional
// policy, check-order or question-set change must update this constant
// deliberately, with reviewer approval.
func TestPolicyHashGolden(t *testing.T) {
	const want = "333fe42d74a35d19bf0e23033da26bd14ff39fb3d35ed0acf8fea2ee634c2dba"
	if got := PolicyHash(DefaultPolicy()); got != want {
		t.Fatalf("golden hash mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestPolicyHashStableAndSensitive(t *testing.T) {
	base := DefaultPolicy()
	h1 := PolicyHash(base)
	if h2 := PolicyHash(base); h1 != h2 {
		t.Fatalf("hash unstable: %q vs %q", h1, h2)
	}
	// Empty version normalizes to the same hash.
	if h := PolicyHash(domain.GatePolicy{MinPass: 0.55, MinBlock: 0.5}); h != h1 {
		t.Fatalf("empty-version hash %q != default %q", h, h1)
	}
	// Zero thresholds are invalid and hash differently from the defaults.
	if h := PolicyHash(domain.GatePolicy{}); h == h1 {
		t.Fatalf("zero policy hash must differ from default: %q", h)
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

func TestPolicyHashSensitiveToQuestions(t *testing.T) {
	base := DefaultPolicy()
	h1 := PolicyHash(base)

	mut := func(f func([]domain.Question)) string {
		cp := Questions()
		f(cp)
		return policyHashWithQuestions(Normalize(base), cp)
	}

	if h := mut(func(q []domain.Question) { q[0].Instructions += "!" }); h == h1 {
		t.Fatalf("hash did not change with question instructions")
	}
	if h := mut(func(q []domain.Question) { q[3].Options[0].Key = "PASS" }); h == h1 {
		t.Fatalf("hash did not change with option key")
	}
	if h := mut(func(q []domain.Question) { q[3].Options[0].Description += "!" }); h == h1 {
		t.Fatalf("hash did not change with option description")
	}
	if h := mut(func(q []domain.Question) {
		q[0].Options[0], q[0].Options[1] = q[0].Options[1], q[0].Options[0]
	}); h == h1 {
		t.Fatalf("hash did not change with option order")
	}
	if h := mut(func(q []domain.Question) {
		q[0], q[1] = q[1], q[0]
	}); h == h1 {
		t.Fatalf("hash did not change with question order")
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
