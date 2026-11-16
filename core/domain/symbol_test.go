package domain

import "testing"

func TestCanonicalSymbol(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{":", ""},
		{"btcusdt", "BTCUSDT"},
		{" BTC/USDT ", "BTCUSDT"},
		{"btc-usdt", "BTCUSDT"},
		{"btc_usdt", "BTCUSDT"},
		{"BTCUSDT:USDT", "BTCUSDT"},
		{"btc/usdt:usdt", "BTCUSDT"},
		{"BTCUSDT:USDT:2026-12-31", "BTCUSDT"},
		{"ETH/USD_spot", "ETHUSDSPOT"},
	}
	for _, tc := range cases {
		if got := CanonicalSymbol(tc.in); got != tc.want {
			t.Errorf("CanonicalSymbol(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
