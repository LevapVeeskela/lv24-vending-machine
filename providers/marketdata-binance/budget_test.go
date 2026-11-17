package marketdatabinance

import (
	"strings"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// budgetNow is a fixed clock between budget test steps.
var budgetNow = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

// TestRateBudgetWeightGate checks that the weight ceiling stops a call once
// spent weight would exceed the per-minute budget and releases it next minute.
func TestRateBudgetWeightGate(t *testing.T) {
	now := budgetNow
	b := newRateBudget(func() time.Time { return now })
	b.weightPerMinute = 6
	b.requestsPerMinute = 100

	for i := 0; i < 3; i++ {
		if err := b.reserve(klinesWeight); err != nil {
			t.Fatalf("reserve #%d: %v", i+1, err)
		}
	}
	err := b.reserve(klinesWeight)
	if got := domain.CodeOf(err); got != domain.CodeRateLimit {
		t.Fatalf("code = %q, want rate_limit (err %v)", got, err)
	}
	if !domain.IsRetryable(err) {
		t.Error("IsRetryable = false, want true")
	}
	if ra := domain.RetryAfterOf(err); ra <= 0 || ra > time.Minute {
		t.Errorf("RetryAfter = %s, want (0, 1m]", ra)
	}
	if !strings.Contains(err.Error(), "weight budget") {
		t.Errorf("error = %q, want it to name the weight budget", err)
	}
	now = now.Add(time.Minute)
	if err := b.reserve(klinesWeight); err != nil {
		t.Fatalf("reserve after window: %v", err)
	}
}

// TestRateBudgetRequestGate checks the independent request-count ceiling.
func TestRateBudgetRequestGate(t *testing.T) {
	now := budgetNow
	b := newRateBudget(func() time.Time { return now })
	b.weightPerMinute = 1000
	b.requestsPerMinute = 2

	if err := b.reserve(klinesWeight); err != nil {
		t.Fatalf("reserve #1: %v", err)
	}
	if err := b.reserve(klinesWeight); err != nil {
		t.Fatalf("reserve #2: %v", err)
	}
	err := b.reserve(klinesWeight)
	if got := domain.CodeOf(err); got != domain.CodeRateLimit {
		t.Fatalf("code = %q, want rate_limit (err %v)", got, err)
	}
	if !strings.Contains(err.Error(), "request budget") {
		t.Errorf("error = %q, want it to name the request budget", err)
	}
}

// TestRateBudgetCooldownGate checks that the cooldown blocks calls with the
// remaining delay and expires with the clock.
func TestRateBudgetCooldownGate(t *testing.T) {
	now := budgetNow
	b := newRateBudget(func() time.Time { return now })
	wait := b.cooldown(2*time.Hour, false)
	if wait != maxCooldown {
		t.Fatalf("cooldown = %s, want the %s cap", wait, maxCooldown)
	}
	err := b.reserve(klinesWeight)
	if got := domain.CodeOf(err); got != domain.CodeRateLimit {
		t.Fatalf("code = %q, want rate_limit (err %v)", got, err)
	}
	if !domain.IsRetryable(err) {
		t.Error("IsRetryable = false, want true")
	}
	if got := domain.RetryAfterOf(err); got != maxCooldown {
		t.Errorf("RetryAfter = %s, want %s", got, maxCooldown)
	}
	now = now.Add(maxCooldown)
	if err := b.reserve(klinesWeight); err != nil {
		t.Fatalf("reserve after cooldown: %v", err)
	}
}

// TestRateBudgetDefaultAndBanCooldown checks the fallback delay and that a ban
// keeps the blocker non-retryable while it lasts.
func TestRateBudgetDefaultAndBanCooldown(t *testing.T) {
	now := budgetNow
	b := newRateBudget(func() time.Time { return now })
	if got := b.cooldown(0, true); got != defaultCooldown {
		t.Fatalf("cooldown = %s, want the %s default", got, defaultCooldown)
	}
	err := b.reserve(klinesWeight)
	if got := domain.CodeOf(err); got != domain.CodeIPBanned {
		t.Fatalf("code = %q, want ip_banned (err %v)", got, err)
	}
	if domain.IsRetryable(err) {
		t.Error("IsRetryable = true, want false")
	}
	if got := domain.RetryAfterOf(err); got != defaultCooldown {
		t.Errorf("RetryAfter = %s, want %s", got, defaultCooldown)
	}
	now = now.Add(defaultCooldown)
	if err := b.reserve(klinesWeight); err != nil {
		t.Fatalf("reserve after ban cooldown: %v", err)
	}
}
