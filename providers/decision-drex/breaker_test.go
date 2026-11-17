package decisiondrex

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// baseTime is a fixed instant so breaker tests never depend on the wall clock.
var baseTime = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// tripBreaker drives b to the open state with consecutive failures.
func tripBreaker(b *breaker, now time.Time) {
	for i := 0; i < breakerOpenAfter; i++ {
		b.record(false, now)
	}
}

// snapshot reports the current phase for assertions.
func (b *breaker) snapshot() (breakerState, int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, b.failures, b.probing
}

func TestBreakerStaysClosedBelowThreshold(t *testing.T) {
	var b breaker
	now := baseTime
	for i := 0; i < breakerOpenAfter-1; i++ {
		if ok, _ := b.allow(now); !ok {
			t.Fatalf("failure %d/%d: breaker is open too early", i+1, breakerOpenAfter-1)
		}
		b.record(false, now)
		now = now.Add(time.Millisecond)
	}
	if ok, _ := b.allow(now); !ok {
		t.Fatalf("breaker opened after %d failures, want %d", breakerOpenAfter-1, breakerOpenAfter)
	}
}

func TestBreakerOpensOnThreshold(t *testing.T) {
	var b breaker
	tripBreaker(&b, baseTime)
	ok, wait := b.allow(baseTime.Add(time.Millisecond))
	if ok {
		t.Fatalf("breaker allowed a call while open")
	}
	if wait <= 0 || wait > breakerOpenFor {
		t.Fatalf("wait = %v, want (0, %v]", wait, breakerOpenFor)
	}
}

func TestBreakerHalfOpenSingleProbe(t *testing.T) {
	var b breaker
	tripBreaker(&b, baseTime)
	probeAt := baseTime.Add(breakerOpenFor)
	if ok, _ := b.allow(probeAt); !ok {
		t.Fatalf("cooldown elapsed, want one half-open probe")
	}
	if ok, _ := b.allow(probeAt); ok {
		t.Fatalf("second concurrent probe was allowed")
	}
}

func TestBreakerHalfOpenSuccessCloses(t *testing.T) {
	var b breaker
	tripBreaker(&b, baseTime)
	probeAt := baseTime.Add(breakerOpenFor)
	if ok, _ := b.allow(probeAt); !ok {
		t.Fatalf("want a half-open probe")
	}
	b.record(true, probeAt)
	if state, failures, probing := b.snapshot(); state != breakerClosed || failures != 0 || probing {
		t.Fatalf("after probe success: state=%v failures=%d probing=%v", state, failures, probing)
	}
	if ok, _ := b.allow(probeAt); !ok {
		t.Fatalf("closed breaker rejected a call")
	}
}

func TestBreakerHalfOpenFailureReopens(t *testing.T) {
	var b breaker
	tripBreaker(&b, baseTime)
	probeAt := baseTime.Add(breakerOpenFor)
	if ok, _ := b.allow(probeAt); !ok {
		t.Fatalf("want a half-open probe")
	}
	b.record(false, probeAt)
	if ok, wait := b.allow(probeAt.Add(time.Millisecond)); ok || wait <= 0 {
		t.Fatalf("failed probe did not reopen: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := b.allow(probeAt.Add(breakerOpenFor)); !ok {
		t.Fatalf("second cooldown did not allow a probe")
	}
}

func TestBreakerSuccessResetsStreak(t *testing.T) {
	var b breaker
	now := baseTime
	for i := 0; i < breakerOpenAfter-1; i++ {
		b.record(false, now)
		now = now.Add(time.Millisecond)
	}
	b.record(true, now)
	now = now.Add(time.Millisecond)
	for i := 0; i < breakerOpenAfter-1; i++ {
		b.record(false, now)
		now = now.Add(time.Millisecond)
	}
	if ok, _ := b.allow(now); !ok {
		t.Fatalf("success did not reset the failure streak")
	}
}

func TestBreakerIgnoresLateResultsWhileOpen(t *testing.T) {
	var b breaker
	tripBreaker(&b, baseTime)
	openedAt := baseTime
	b.record(true, baseTime.Add(breakerOpenFor/2))
	b.record(false, baseTime.Add(breakerOpenFor/2))
	if state, _, _ := b.snapshot(); state != breakerOpen {
		t.Fatalf("late result changed the open state to %v", state)
	}
	if ok, _ := b.allow(openedAt.Add(breakerOpenFor - time.Millisecond)); ok {
		t.Fatalf("late results extended or cut the cooldown")
	}
}

func TestBreakerConcurrentSingleProbe(t *testing.T) {
	var b breaker
	tripBreaker(&b, baseTime)
	probeAt := baseTime.Add(breakerOpenFor)
	const goroutines = 64
	var wg sync.WaitGroup
	var allowed atomic.Int64
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if ok, _ := b.allow(probeAt); ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 1 {
		t.Fatalf("half-open probes allowed = %d, want exactly 1", got)
	}
}

func TestBreakerConcurrentRecord(t *testing.T) {
	var b breaker
	var wg sync.WaitGroup
	const goroutines = 32
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			now := baseTime.Add(time.Duration(g) * time.Millisecond)
			for i := 0; i < 50; i++ {
				ok, _ := b.allow(now)
				if ok {
					b.record(i%4 != 0, now)
				}
			}
		}(g)
	}
	wg.Wait()
	if state, _, _ := b.snapshot(); state != breakerClosed && state != breakerOpen && state != breakerHalfOpen {
		t.Fatalf("invalid breaker state %v", state)
	}
}
