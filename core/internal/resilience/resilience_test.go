package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

func retryable() error {
	e := domain.NewError(domain.CodeUnavailable, "down")
	e.Retryable = true
	return e
}

func TestSuccessFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), DefaultPolicy(), func(context.Context) error {
		calls++
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestNonRetryableOnce(t *testing.T) {
	calls := 0
	want := domain.NewError(domain.CodeInvalidRequest, "bad")
	err := Do(context.Background(), DefaultPolicy(), func(context.Context) error {
		calls++
		return want
	})
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestRetryableThenSuccess(t *testing.T) {
	p := Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	calls := 0
	err := Do(context.Background(), p, func(context.Context) error {
		calls++
		if calls < 3 {
			return retryable()
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestAttemptLimit(t *testing.T) {
	p := Policy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	calls := 0
	err := Do(context.Background(), p, func(context.Context) error {
		calls++
		return retryable()
	})
	if err == nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := Policy{MaxAttempts: 5, BaseDelay: time.Hour, MaxDelay: time.Hour}
	calls := 0
	err := Do(ctx, p, func(context.Context) error {
		calls++
		return retryable()
	})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
