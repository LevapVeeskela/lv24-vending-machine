package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

var stubNow = time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)

// decisionBody is a gate-v1 style decision request with the four choice
// questions used by the live smoke runbook.
const decisionBody = `{"model":"drex-max-v1.1","state":{},"questions":{` +
	`"data_quality":{"type":"choice","criteria":{"ok":"usable","insufficient":"not enough"}},` +
	`"signal_alignment":{"type":"choice","criteria":{"aligned":"same direction","neutral":"flat","conflict":"against"}},` +
	`"market_regime":{"type":"choice","criteria":{"favorable":"tailwind","neutral":"flat","adverse":"headwind"}},` +
	`"risk_check":{"type":"choice","criteria":{"pass":"within limits","block":"over limit"}}}}`

// doJSON performs one request and returns the status and body.
func doJSON(t *testing.T, method, url, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, url, err)
	}
	return resp.StatusCode, raw
}

// TestDecisionRole checks a valid decision-drex envelope with gate answers.
func TestDecisionRole(t *testing.T) {
	srv := httptest.NewServer(newStub(func() time.Time { return stubNow }, nil))
	defer srv.Close()

	status, raw := doJSON(t, http.MethodPost, srv.URL+"/v1/systemone", decisionBody)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, raw)
	}
	var resp struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type          string             `json:"type"`
			Choice        string             `json:"choice"`
			Probabilities map[string]float64 `json:"probabilities"`
			Confidence    *float64           `json:"confidence"`
		} `json:"answers"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Model != "drex-max-v1.1" {
		t.Fatalf("model = %q, want the request model", resp.Model)
	}
	if len(resp.Answers) != 4 {
		t.Fatalf("answers = %d, want 4", len(resp.Answers))
	}
	if got := resp.Answers["risk_check"].Probabilities["pass"]; got != 0.9 {
		t.Fatalf("p(pass) = %v, want 0.9", got)
	}
	if got := resp.Answers["data_quality"].Probabilities["insufficient"]; got != 0.1 {
		t.Fatalf("p(insufficient) = %v, want 0.1", got)
	}
	for id, ans := range resp.Answers {
		if ans.Type != "choice" {
			t.Fatalf("%s: type = %q, want choice", id, ans.Type)
		}
		sum, bestKey, best := 0.0, "", -1.0
		for key, p := range ans.Probabilities {
			sum += p
			if p > best {
				best, bestKey = p, key
			}
		}
		if math.Abs(sum-1) > 0.001 {
			t.Fatalf("%s: probabilities sum = %v, want 1", id, sum)
		}
		if ans.Choice != bestKey {
			t.Fatalf("%s: choice = %q, want argmax %q", id, ans.Choice, bestKey)
		}
		n := float64(len(ans.Probabilities))
		want := (best - 1/n) / (1 - 1/n)
		if ans.Confidence == nil || math.Abs(*ans.Confidence-want) > 0.02 {
			t.Fatalf("%s: confidence = %v, want %v", id, ans.Confidence, want)
		}
	}
	if resp.Usage.InputTokens == 0 || resp.Usage.OutputTokens == 0 {
		t.Fatalf("usage = %+v, want positive counters", resp.Usage)
	}
}

// TestKlinesRole checks deterministic closed candles for every supported
// interval and the Binance-style rejection of another symbol.
func TestKlinesRole(t *testing.T) {
	srv := httptest.NewServer(newStub(func() time.Time { return stubNow }, nil))
	defer srv.Close()

	cases := []struct {
		interval string
		step     time.Duration
		start    time.Time
	}{
		{"1m", time.Minute, stubNow.Add(-3 * time.Minute)},
		{"5m", 5 * time.Minute, stubNow.Add(-15 * time.Minute)},
		{"1h", time.Hour, stubNow.Add(-3 * time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.interval, func(t *testing.T) {
			url := fmt.Sprintf("%s/api/v3/klines?symbol=BTCUSDT&interval=%s&limit=3&startTime=%d",
				srv.URL, tc.interval, tc.start.UnixMilli())
			status, first := doJSON(t, http.MethodGet, url, "")
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", status, first)
			}
			_, second := doJSON(t, http.MethodGet, url, "")
			if !bytes.Equal(first, second) {
				t.Fatalf("candles are not deterministic:\n%s\n%s", first, second)
			}
			var rows [][]json.RawMessage
			if err := json.Unmarshal(first, &rows); err != nil {
				t.Fatalf("decode klines: %v", err)
			}
			if len(rows) != 3 {
				t.Fatalf("rows = %d, want 3", len(rows))
			}
			for i, row := range rows {
				if len(row) < 7 {
					t.Fatalf("row %d has %d columns, want at least 7", i, len(row))
				}
				var openMs, closeMs int64
				if err := json.Unmarshal(row[0], &openMs); err != nil {
					t.Fatalf("row %d open time: %v", i, err)
				}
				if err := json.Unmarshal(row[6], &closeMs); err != nil {
					t.Fatalf("row %d close time: %v", i, err)
				}
				wantOpen := tc.start.Add(time.Duration(i) * tc.step).UnixMilli()
				if openMs != wantOpen {
					t.Fatalf("row %d open = %d, want %d", i, openMs, wantOpen)
				}
				if closeMs != openMs+tc.step.Milliseconds()-1 || closeMs >= stubNow.UnixMilli() {
					t.Fatalf("row %d close = %d, want a closed bar before now", i, closeMs)
				}
				var open, high, low, closePx, volume string
				for j, dst := range []*string{&open, &high, &low, &closePx, &volume} {
					if err := json.Unmarshal(row[j+1], dst); err != nil {
						t.Fatalf("row %d column %d: %v", i, j+1, err)
					}
				}
				o, err := strconv.ParseInt(open, 10, 64)
				if err != nil {
					t.Fatalf("row %d open price %q: %v", i, open, err)
				}
				h, _ := strconv.ParseInt(high, 10, 64)
				l, _ := strconv.ParseInt(low, 10, 64)
				c, _ := strconv.ParseInt(closePx, 10, 64)
				v, _ := strconv.ParseInt(volume, 10, 64)
				if h < o || h < c || l > o || l > c {
					t.Fatalf("row %d OHLC is inconsistent: %s/%s/%s/%s", i, open, high, low, closePx)
				}
				if v <= 0 {
					t.Fatalf("row %d volume = %q, want positive", i, volume)
				}
			}
		})
	}

	status, raw := doJSON(t, http.MethodGet, srv.URL+"/api/v3/klines?symbol=ETHUSDT&interval=1m", "")
	if status != http.StatusBadRequest {
		t.Fatalf("other symbol status = %d, want 400", status)
	}
	if !strings.Contains(string(raw), `"code":-1121`) || !strings.Contains(string(raw), "Invalid symbol.") {
		t.Fatalf("other symbol body = %s, want a Binance invalid-symbol body", raw)
	}
}

// TestTelegramRole checks ok=true and that the token and message text never
// reach the log.
func TestTelegramRole(t *testing.T) {
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	srv := httptest.NewServer(newStub(func() time.Time { return stubNow }, logger))
	defer srv.Close()

	const token = "123456:AAH-rehearsal-only"
	const text = "rehearsal message CANARY"
	body := `{"chat_id":"42","text":"` + text + `"}`
	status, raw := doJSON(t, http.MethodPost, srv.URL+"/bot"+token+"/sendMessage", body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, raw)
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || !resp.OK {
		t.Fatalf("telegram response = %s, want ok=true", raw)
	}
	if got := logs.String(); strings.Contains(got, token) || strings.Contains(got, text) {
		t.Fatalf("log leaks the token or the message text: %q", got)
	}
}

// TestCtlSwitchesRoles checks the status switch, its effect on both roles and
// that klines stay available.
func TestCtlSwitchesRoles(t *testing.T) {
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	srv := httptest.NewServer(newStub(func() time.Time { return stubNow }, logger))
	defer srv.Close()

	status, raw := doJSON(t, http.MethodGet, srv.URL+"/ctl?decision=500&telegram=401", "")
	if status != http.StatusOK {
		t.Fatalf("ctl status = %d, want 200: %s", status, raw)
	}
	var state map[string]int
	if err := json.Unmarshal(raw, &state); err != nil || state["decision"] != 500 || state["telegram"] != 401 {
		t.Fatalf("ctl = %s, want decision=500 telegram=401", raw)
	}

	if status, raw := doJSON(t, http.MethodPost, srv.URL+"/v1/systemone", decisionBody); status != 500 {
		t.Fatalf("decision status = %d, want 500: %s", status, raw)
	}
	if status, raw := doJSON(t, http.MethodPost, srv.URL+"/bot123/sendMessage", `{}`); status != 401 {
		t.Fatalf("telegram status = %d, want 401: %s", status, raw)
	}
	if status, raw := doJSON(t, http.MethodGet, srv.URL+"/api/v3/klines?symbol=BTCUSDT&interval=1m&limit=1", ""); status != http.StatusOK {
		t.Fatalf("klines status = %d, want 200 without a role switch: %s", status, raw)
	}

	if status, raw := doJSON(t, http.MethodGet, srv.URL+"/ctl?decision=200&telegram=200", ""); status != http.StatusOK {
		t.Fatalf("reset status = %d, want 200: %s", status, raw)
	}
	if status, raw := doJSON(t, http.MethodPost, srv.URL+"/v1/systemone", decisionBody); status != http.StatusOK {
		t.Fatalf("decision after reset = %d, want 200: %s", status, raw)
	}

	if status, _ := doJSON(t, http.MethodGet, srv.URL+"/ctl?decision=99", ""); status != http.StatusBadRequest {
		t.Fatalf("invalid ctl status = %d, want 400", status)
	}
}

// TestValidateAddr keeps the rehearsal server on loopback.
func TestValidateAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:28471", "[::1]:28471"} {
		if err := validateAddr(addr); err != nil {
			t.Fatalf("validateAddr(%q) = %v, want nil", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:28471", "localhost:28471", "127.0.0.1", "example.com:80"} {
		if err := validateAddr(addr); err == nil {
			t.Fatalf("validateAddr(%q) = nil, want a loopback error", addr)
		}
	}
}
