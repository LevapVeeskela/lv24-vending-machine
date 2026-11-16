package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseTimeframe parses a candle timeframe: a positive integer amount followed
// by one of m (minutes), h (hours), d (days) or w (weeks). Surrounding spaces
// are ignored; the unit is case-sensitive because candle providers such as
// Binance accept only the lowercase form. This is the single timeframe parser
// of the core: the labeler, trend source, shadow scheduler and report must not
// carry their own copy.
func ParseTimeframe(raw string) (time.Duration, error) {
	bad := func() (time.Duration, error) {
		return 0, fmt.Errorf("unsupported timeframe %q (want a positive amount followed by m, h, d or w)", raw)
	}
	s := strings.TrimSpace(raw)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) {
		return bad()
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil || n <= 0 {
		return bad()
	}
	var unit time.Duration
	switch s[i:] {
	case "m":
		unit = time.Minute
	case "h":
		unit = time.Hour
	case "d":
		unit = 24 * time.Hour
	case "w":
		unit = 7 * 24 * time.Hour
	default:
		return bad()
	}
	d := time.Duration(n) * unit
	// A wrapped product would silently become a different bar length.
	if d <= 0 || d/unit != time.Duration(n) {
		return bad()
	}
	return d, nil
}
