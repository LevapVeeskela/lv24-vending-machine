package domain

import "strings"

// CanonicalSymbol normalizes a venue symbol to its canonical comparison form:
// trimmed, upper-cased, with the futures suffix (everything from the first
// ":") dropped and every "/", "-" and "_" separator removed. Empty input
// returns "".
//
// This is the single canonicalization of the core: signal sources and
// providers must compare symbols through it instead of rolling their own.
func CanonicalSymbol(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToUpper(s) {
		switch r {
		case '/', '-', '_':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
