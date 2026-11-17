package domain

import (
	"strings"
	"testing"
	"time"
)

func TestParseTimeframe(t *testing.T) {
	cases := map[string]time.Duration{
		"1m":  time.Minute,
		"15m": 15 * time.Minute,
		"4h":  4 * time.Hour,
		"1d":  24 * time.Hour,
		"2w":  14 * 24 * time.Hour,
	}
	for raw, want := range cases {
		got, err := ParseTimeframe(raw)
		if err != nil {
			t.Fatalf("ParseTimeframe(%q) = %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseTimeframe(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestParseTimeframeUppercaseErrorSuggestsLowercase(t *testing.T) {
	_, err := ParseTimeframe("4H")
	if err == nil {
		t.Fatal("ParseTimeframe(\"4H\") succeeded, want an error")
	}
	for _, want := range []string{"lowercase", `"4h"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q misses %q", err, want)
		}
	}
}
