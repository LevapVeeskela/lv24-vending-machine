// Package resilience retries retryable provider calls with a capped
// exponential backoff.
package resilience

import (
	"context"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// Policy tunes the retry loop.
type Policy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultPolicy returns 3 attempts, 200ms base delay, 2s cap.
func DefaultPolicy() Policy {
	return Policy{MaxAttempts: 3, BaseDelay: 200 * time.Millisecond, MaxDelay: 2 * time.Second}
}

// Do runs fn until it succeeds, fails non-retryably, or the attempts run
// out. Only domain.IsRetryable errors are retried; the backoff doubles
// from BaseDelay and is capped at MaxDelay. The context is respected
// while waiting between attempts. No error content is logged here.
func Do(ctx context.Context, p Policy, fn func(context.Context) error) error {
	max := p.MaxAttempts
	if max < 1 {
		max = 1
	}
	var err error
	for attempt := 1; attempt <= max; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil && attempt > 1 {
			return ctxErr
		}
		err = fn(ctx)
		if err == nil {
			return nil
		}
		if !domain.IsRetryable(err) {
			return err
		}
		if attempt == max {
			return err
		}
		delay := p.BaseDelay << (attempt - 1)
		if delay < 0 {
			delay = p.BaseDelay
		}
		if p.MaxDelay > 0 && delay > p.MaxDelay {
			delay = p.MaxDelay
		}
		if delay <= 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return err
}
