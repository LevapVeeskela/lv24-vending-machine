package marketdatabinance

// Fixtures in testdata/ were built by hand from the public Binance Spot API
// documentation for GET /api/v3/klines (array of arrays:
// [openTimeMs, "open", "high", "low", "close", "volume", closeTimeMs, ...])
// and the documented -1003 error body. No network access is used.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestParseKlinesPageFixture parses the hand-built klines page without network.
func TestParseKlinesPageFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "klines_page.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := parseKlinesPage(data, "BTCUSDT", "1m")
	if err != nil {
		t.Fatalf("parseKlinesPage: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if ms := got[0].OpenTime.UnixMilli(); ms != 1704067200000 {
		t.Errorf("candles[0].OpenTime = %d, want 1704067200000", ms)
	}
	if ms := got[0].CloseTime.UnixMilli(); ms != 1704067259999 {
		t.Errorf("candles[0].CloseTime = %d, want 1704067259999", ms)
	}
	if got[0].Open.String() != "43210.12" {
		t.Errorf("open = %q, want %q", got[0].Open.String(), "43210.12")
	}
	if got[1].Close.String() != "43260.77" {
		t.Errorf("close = %q, want %q", got[1].Close.String(), "43260.77")
	}
	if got[1].Volume.String() != "9.87600000" {
		t.Errorf("volume = %q, want %q", got[1].Volume.String(), "9.87600000")
	}
}

// TestRateLimitFixture checks the shape of the hand-built 429 error body.
func TestRateLimitFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "rate_limit_429.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var body struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if body.Code != -1003 {
		t.Errorf("code = %d, want -1003", body.Code)
	}
	if body.Msg == "" {
		t.Error("msg is empty")
	}
}
