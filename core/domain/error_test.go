package domain

import (
	"fmt"
	"strings"
	"testing"
)

func TestErrorStringHidesCause(t *testing.T) {
	cause := fmt.Errorf("secret-token-abc123")
	err := WrapError(CodeAuth, "bad key", cause)
	s := err.Error()
	if s != "auth: bad key" {
		t.Fatalf("Error() = %q, want %q", s, "auth: bad key")
	}
	if strings.Contains(s, "secret-token") {
		t.Fatalf("Error() leaks cause: %q", s)
	}
	if err.Unwrap() != cause {
		t.Fatal("Unwrap must return the cause")
	}
}

func TestCodeOf(t *testing.T) {
	inner := NewError(CodeRateLimit, "slow down")
	wrapped := fmt.Errorf("call: %w", inner)
	if got := CodeOf(wrapped); got != CodeRateLimit {
		t.Errorf("CodeOf(wrapped) = %q, want rate_limit", got)
	}
	if got := CodeOf(fmt.Errorf("boom")); got != CodeUnknown {
		t.Errorf("CodeOf(plain) = %q, want unknown", got)
	}
	if got := CodeOf(nil); got != CodeUnknown {
		t.Errorf("CodeOf(nil) = %q, want unknown", got)
	}
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		code ErrorCode
		want bool
	}{
		{CodeAuth, false},
		{CodePaymentRequired, false},
		{CodeInvalidRequest, false},
		{CodeInvalidOutput, false},
		{CodeRateLimit, true},
		{CodeUnavailable, true},
		{CodeTimeout, true},
		{CodeUnknown, false},
	}
	for _, c := range cases {
		err := NewError(c.code, "m")
		if got := IsRetryable(err); got != c.want {
			t.Errorf("IsRetryable(%q) = %v, want %v", c.code, got, c.want)
		}
	}
	if IsRetryable(fmt.Errorf("plain")) {
		t.Error("plain error must not be retryable")
	}
	wrapped := fmt.Errorf("ctx: %w", NewError(CodeTimeout, "t"))
	if !IsRetryable(wrapped) {
		t.Error("wrapped timeout must be retryable")
	}
}
