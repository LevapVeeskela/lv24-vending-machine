package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDecimalValid(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		scale int
	}{
		{"0", "0", 0},
		{"-0", "0", 0},
		{"7", "7", 0},
		{"-7", "-7", 0},
		{"1.50", "1.50", 2},
		{"0.00", "0.00", 2},
		{"-0.00", "0.00", 2},
		{"123.456", "123.456", 3},
		{"-123.456", "-123.456", 3},
		{"007", "7", 0},
		{"10.10", "10.10", 2},
	}
	for _, c := range cases {
		d, err := ParseDecimal(c.in)
		if err != nil {
			t.Errorf("ParseDecimal(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got := d.String(); got != c.want {
			t.Errorf("ParseDecimal(%q).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseDecimalInvalid(t *testing.T) {
	invalid := []string{
		"", ".", "+1", "+1.5", "1.", ".5", "-",
		"1e3", "1E3", "0x1", " 1", "1 ", "1,5",
		"abc", "1.2.3", "--1", "-", "+.5", "NaN", "Inf",
	}
	for _, in := range invalid {
		if d, err := ParseDecimal(in); err == nil {
			t.Errorf("ParseDecimal(%q) = %v, want error", in, d)
		}
	}
}

func TestDecimalStringKeepsScale(t *testing.T) {
	d := MustParseDecimal("1.50")
	if got := d.String(); got != "1.50" {
		t.Fatalf("String() = %q, want %q", got, "1.50")
	}
	if got := MustParseDecimal("-0").String(); got != "0" {
		t.Fatalf("String(-0) = %q, want %q", got, "0")
	}
	var zero Decimal
	if got := zero.String(); got != "0" {
		t.Fatalf("zero String() = %q, want %q", got, "0")
	}
}

func TestDecimalAddSubMul(t *testing.T) {
	addCases := []struct {
		a, b, want string
	}{
		{"1.50", "2.5", "4.00"},
		{"1.5", "-1.50", "0.00"},
		{"0", "0.00", "0.00"},
		{"-1.25", "-1.25", "-2.50"},
		{"99999999999999999999", "1", "100000000000000000000"},
	}
	for _, c := range addCases {
		got := MustParseDecimal(c.a).Add(MustParseDecimal(c.b)).String()
		if got != c.want {
			t.Errorf("%s + %s = %s, want %s", c.a, c.b, got, c.want)
		}
	}
	subCases := []struct {
		a, b, want string
	}{
		{"1.50", "2.5", "-1.00"},
		{"5", "5", "0"},
		{"0.3", "0.1", "0.2"},
	}
	for _, c := range subCases {
		got := MustParseDecimal(c.a).Sub(MustParseDecimal(c.b)).String()
		if got != c.want {
			t.Errorf("%s - %s = %s, want %s", c.a, c.b, got, c.want)
		}
	}
	mulCases := []struct {
		a, b, want string
	}{
		{"1.50", "2", "3.00"},
		{"-2", "3", "-6"},
		{"0.1", "0.2", "0.02"},
		{"1.5", "1.5", "2.25"},
	}
	for _, c := range mulCases {
		got := MustParseDecimal(c.a).Mul(MustParseDecimal(c.b)).String()
		if got != c.want {
			t.Errorf("%s * %s = %s, want %s", c.a, c.b, got, c.want)
		}
	}
}

func TestDecimalCmpSignIsZero(t *testing.T) {
	if MustParseDecimal("1.50").Cmp(MustParseDecimal("1.5")) != 0 {
		t.Error("1.50 should equal 1.5")
	}
	if MustParseDecimal("1.5").Cmp(MustParseDecimal("1.51")) >= 0 {
		t.Error("1.5 should be less than 1.51")
	}
	if MustParseDecimal("-2").Cmp(MustParseDecimal("-3")) <= 0 {
		t.Error("-2 should be greater than -3")
	}
	var zero Decimal
	if !zero.IsZero() || zero.Sign() != 0 {
		t.Error("zero value must be zero with sign 0")
	}
	if !MustParseDecimal("0.00").IsZero() {
		t.Error("0.00 must be zero")
	}
	if got := MustParseDecimal("-5").Sign(); got != -1 {
		t.Errorf("Sign(-5) = %d, want -1", got)
	}
	if got := MustParseDecimal("5").Sign(); got != 1 {
		t.Errorf("Sign(5) = %d, want 1", got)
	}
}

func TestDecimalQuoRound(t *testing.T) {
	cases := []struct {
		a, b  string
		scale int
		want  string
	}{
		{"1", "2", 2, "0.50"},
		{"2", "3", 2, "0.67"},
		{"0.125", "1", 2, "0.13"},
		{"-0.125", "1", 2, "-0.13"},
		{"0.124", "1", 2, "0.12"},
		{"-0.124", "1", 2, "-0.12"},
		{"1", "4", 0, "0"},
		{"-1", "4", 0, "0"},
		{"5", "2", 0, "3"},
		{"-5", "2", 0, "-3"},
		{"4", "2", 0, "2"},
		{"10", "3", 4, "3.3333"},
	}
	for _, c := range cases {
		got, err := MustParseDecimal(c.a).QuoRound(MustParseDecimal(c.b), c.scale)
		if err != nil {
			t.Errorf("%s/%s unexpected error: %v", c.a, c.b, err)
			continue
		}
		if s := got.String(); s != c.want {
			t.Errorf("%s/%s scale %d = %s, want %s", c.a, c.b, c.scale, s, c.want)
		}
	}
	if _, err := MustParseDecimal("1").QuoRound(MustParseDecimal("0"), 2); err == nil {
		t.Error("division by zero must fail")
	}
	var zero Decimal
	if _, err := MustParseDecimal("1").QuoRound(zero, 2); err == nil {
		t.Error("division by zero value must fail")
	}
	if _, err := MustParseDecimal("1").QuoRound(MustParseDecimal("2"), -1); err == nil {
		t.Error("negative scale must fail")
	}
}

func TestDecimalJSONRoundTrip(t *testing.T) {
	d := MustParseDecimal("1.50")
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != `"1.50"` {
		t.Fatalf("Marshal = %s, want %q", b, "1.50")
	}
	var back Decimal
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.String() != "1.50" {
		t.Fatalf("round-trip = %q, want %q", back.String(), "1.50")
	}
}

func TestDecimalJSONRejectsNumber(t *testing.T) {
	for _, raw := range []string{`1.50`, `1`, `0`, `null`, `true`, `{}`, `""`} {
		var d Decimal
		if err := json.Unmarshal([]byte(raw), &d); err == nil {
			t.Errorf("Unmarshal(%s) must fail", raw)
		}
	}
}

func TestDecimalDoesNotMutate(t *testing.T) {
	a := MustParseDecimal("1.50")
	before := a.String()
	_ = a.Add(MustParseDecimal("2.5"))
	_ = a.Sub(MustParseDecimal("0.5"))
	_ = a.Mul(MustParseDecimal("2"))
	if _, err := a.QuoRound(MustParseDecimal("2"), 2); err != nil {
		t.Fatal(err)
	}
	if a.String() != before {
		t.Fatalf("receiver mutated: was %s, now %s", before, a.String())
	}
}

func TestDecimalZeroValueOps(t *testing.T) {
	var z Decimal
	if got := z.Add(MustParseDecimal("1.5")).String(); got != "1.5" {
		t.Errorf("0 + 1.5 = %s, want 1.5", got)
	}
	if !z.Add(z).IsZero() {
		t.Error("0 + 0 must be zero")
	}
	if !strings.EqualFold(z.String(), "0") {
		t.Errorf("zero String = %q", z.String())
	}
}
