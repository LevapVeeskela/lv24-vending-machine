// Package domain holds shared value types for the vending-machine core.
//
// The package depends only on the standard library.
package domain

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

// Decimal is an exact fixed-point decimal number for money and prices.
//
// The value equals unscaled / 10^scale. The zero value is usable and
// represents 0. Methods never mutate the receiver; a nil unscaled
// coefficient is treated as 0.
type Decimal struct {
	unscaled *big.Int
	scale    int
}

// validateDecimalString reports whether s matches ^-?[0-9]+(\.[0-9]+)?$.
func validateDecimalString(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '-' {
		i = 1
		if len(s) == 1 {
			return false
		}
	} else if s[0] == '+' {
		return false
	}
	digits := 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		digits++
	}
	if digits == 0 {
		return false
	}
	if i == len(s) {
		return true
	}
	if s[i] != '.' {
		return false
	}
	i++
	frac := 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		frac++
	}
	if frac == 0 {
		return false
	}
	return i == len(s)
}

// ParseDecimal parses s in the form ^-?[0-9]+(\.[0-9]+)?$.
//
// Empty strings, a lone dot, exponents and a leading plus are rejected.
func ParseDecimal(s string) (Decimal, error) {
	if !validateDecimalString(s) {
		return Decimal{}, errors.New("domain: invalid decimal: " + quoteForError(s))
	}
	neg := false
	t := s
	if strings.HasPrefix(t, "-") {
		neg = true
		t = t[1:]
	}
	intPart := t
	fracPart := ""
	if j := strings.IndexByte(t, '.'); j >= 0 {
		intPart = t[:j]
		fracPart = t[j+1:]
	}
	digits := intPart + fracPart
	// Strip leading zeros for big.Int, keep at least one digit.
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		digits = "0"
	}
	v, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Decimal{}, errors.New("domain: invalid decimal: " + quoteForError(s))
	}
	if neg && v.Sign() != 0 {
		v.Neg(v)
	}
	return Decimal{unscaled: v, scale: len(fracPart)}, nil
}

func quoteForError(s string) string {
	if len(s) > 64 {
		s = s[:64] + "..."
	}
	return `"` + s + `"`
}

// MustParseDecimal parses s and panics on error.
func MustParseDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

// coeff returns a copy of the unscaled coefficient, treating nil as 0.
func (d Decimal) coeff() *big.Int {
	if d.unscaled == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(d.unscaled)
}

// pow10 returns 10^n for n >= 0.
func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// String returns the decimal in plain notation preserving the scale.
//
// It never uses exponents: "1.50" keeps its trailing zero and "-0"
// normalizes to "0".
func (d Decimal) String() string {
	scale := d.scale
	if scale < 0 {
		scale = 0
	}
	u := d.coeff()
	if u.Sign() == 0 {
		if scale == 0 {
			return "0"
		}
		return "0." + strings.Repeat("0", scale)
	}
	neg := u.Sign() < 0
	abs := new(big.Int).Abs(u).String()
	if scale == 0 {
		if neg {
			return "-" + abs
		}
		return abs
	}
	if len(abs) <= scale {
		abs = strings.Repeat("0", scale-len(abs)+1) + abs
	}
	intPart := abs[:len(abs)-scale]
	fracPart := abs[len(abs)-scale:]
	s := intPart + "." + fracPart
	if neg {
		return "-" + s
	}
	return s
}

// rescaled returns v * 10^n for n >= 0.
func rescaled(v *big.Int, n int) *big.Int {
	if n == 0 {
		return v
	}
	return new(big.Int).Mul(v, pow10(n))
}

// Add returns d + o, aligned to the larger scale.
func (d Decimal) Add(o Decimal) Decimal {
	a, b := d.coeff(), o.coeff()
	sa, sb := d.scale, o.scale
	if sa < 0 {
		sa = 0
	}
	if sb < 0 {
		sb = 0
	}
	if sa == sb {
		return Decimal{unscaled: new(big.Int).Add(a, b), scale: sa}
	}
	if sa > sb {
		return Decimal{unscaled: new(big.Int).Add(a, rescaled(b, sa-sb)), scale: sa}
	}
	return Decimal{unscaled: new(big.Int).Add(rescaled(a, sb-sa), b), scale: sb}
}

// Sub returns d - o, aligned to the larger scale.
func (d Decimal) Sub(o Decimal) Decimal {
	a, b := d.coeff(), o.coeff()
	sa, sb := d.scale, o.scale
	if sa < 0 {
		sa = 0
	}
	if sb < 0 {
		sb = 0
	}
	if sa == sb {
		return Decimal{unscaled: new(big.Int).Sub(a, b), scale: sa}
	}
	if sa > sb {
		return Decimal{unscaled: new(big.Int).Sub(a, rescaled(b, sa-sb)), scale: sa}
	}
	return Decimal{unscaled: new(big.Int).Sub(rescaled(a, sb-sa), b), scale: sb}
}

// Mul returns d * o with scale sa+sb.
func (d Decimal) Mul(o Decimal) Decimal {
	sa, sb := d.scale, o.scale
	if sa < 0 {
		sa = 0
	}
	if sb < 0 {
		sb = 0
	}
	return Decimal{unscaled: new(big.Int).Mul(d.coeff(), o.coeff()), scale: sa + sb}
}

// Cmp compares d and o and returns -1, 0 or 1.
func (d Decimal) Cmp(o Decimal) int {
	a, b := d.coeff(), o.coeff()
	sa, sb := d.scale, o.scale
	if sa < 0 {
		sa = 0
	}
	if sb < 0 {
		sb = 0
	}
	if sa == sb {
		return a.Cmp(b)
	}
	if sa > sb {
		return a.Cmp(rescaled(b, sa-sb))
	}
	return rescaled(a, sb-sa).Cmp(b)
}

// Sign returns -1, 0 or 1 according to the sign of d.
func (d Decimal) Sign() int {
	if d.unscaled == nil {
		return 0
	}
	return d.unscaled.Sign()
}

// IsZero reports whether d equals 0.
func (d Decimal) IsZero() bool {
	return d.Sign() == 0
}

// QuoRound divides d by o and rounds to scale decimal places,
// half away from zero. Division by zero returns an error.
func (d Decimal) QuoRound(o Decimal, scale int) (Decimal, error) {
	if scale < 0 {
		return Decimal{}, errors.New("domain: negative scale")
	}
	if o.IsZero() {
		return Decimal{}, errors.New("domain: division by zero")
	}
	sa, sb := d.scale, o.scale
	if sa < 0 {
		sa = 0
	}
	if sb < 0 {
		sb = 0
	}
	a, b := d.coeff(), o.coeff()
	exp := sb - sa + scale
	var num, den *big.Int
	if exp >= 0 {
		num = rescaled(a, exp)
		den = new(big.Int).Set(b)
	} else {
		num = new(big.Int).Set(a)
		den = rescaled(b, -exp)
	}
	// Truncated quotient plus rounding half away from zero.
	q := new(big.Int)
	r := new(big.Int)
	q.QuoRem(num, den, r)
	if r.Sign() != 0 {
		absR := new(big.Int).Abs(r)
		absD := new(big.Int).Abs(den)
		// 2*|r| >= |den| -> round away from zero.
		absR.Lsh(absR, 1)
		if absR.Cmp(absD) >= 0 {
			sign := num.Sign() * den.Sign()
			if sign > 0 {
				q.Add(q, big.NewInt(1))
			} else {
				q.Sub(q, big.NewInt(1))
			}
		}
	}
	return Decimal{unscaled: q, scale: scale}, nil
}

// MarshalJSON encodes the decimal as a JSON string.
func (d Decimal) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON decodes a decimal from a JSON string only.
//
// JSON numbers and other types are rejected with an error.
func (d *Decimal) UnmarshalJSON(b []byte) error {
	if d == nil {
		return errors.New("domain: nil decimal")
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("domain: decimal must be a JSON string")
	}
	v, err := ParseDecimal(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}
