package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v", err)
	}
}

func TestValidateBad(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"apiVersion", func(c *Config) { c.APIVersion = "v0" }},
		{"mode", func(c *Config) { c.Mode = "paperX" }},
		{"live", func(c *Config) { c.Mode = "live" }},
		{"horizonZero", func(c *Config) { c.Outcome.HorizonBars = 0 }},
		{"horizonHigh", func(c *Config) { c.Outcome.HorizonBars = 1001 }},
		{"feeNeg", func(c *Config) { c.Outcome.FeeBps = -1 }},
		{"feeHigh", func(c *Config) { c.Outcome.FeeBps = 10001 }},
		{"labelZero", func(c *Config) { c.Outcome.LabelIntervalSec = 0 }},
		{"labelHigh", func(c *Config) { c.Outcome.LabelIntervalSec = 86401 }},
		{"gateVersion", func(c *Config) { c.Gate.Version = "" }},
		{"minPassZero", func(c *Config) { c.Gate.MinPass = 0 }},
		{"minPassHigh", func(c *Config) { c.Gate.MinPass = 1.5 }},
		{"minBlockHigh", func(c *Config) { c.Gate.MinBlock = 2 }},
		{"decisionID", func(c *Config) { c.Providers.Decision.ID = "" }},
		{"model", func(c *Config) { c.Providers.Decision.Model = "" }},
		{"modelLatest", func(c *Config) { c.Providers.Decision.Model = "drex-max-latest" }},
		{"profile", func(c *Config) { c.Providers.Decision.Profile = "openai" }},
		{"secretRef", func(c *Config) { c.Providers.Decision.SecretRef = "" }},
		{"priceBad", func(c *Config) { c.Providers.Decision.PricePerBillion = "abc" }},
		{"priceZero", func(c *Config) { c.Providers.Decision.PricePerBillion = "0" }},
		{"marketID", func(c *Config) { c.Providers.MarketData.ID = "" }},
		{"marketURL", func(c *Config) { c.Providers.MarketData.BaseURL = "ftp://x" }},
		{"tgChat", func(c *Config) { c.Notify.Telegram.Enabled = true }},
		{"tgSecret", func(c *Config) {
			c.Notify.Telegram.Enabled = true
			c.Notify.Telegram.ChatID = "123"
		}},
		{"tgBaseURLRemoteHTTP", func(c *Config) {
			c.Notify.Telegram.Enabled = true
			c.Notify.Telegram.ChatID = "123"
			c.Notify.Telegram.SecretRef = "conn/tg/token"
			c.Notify.Telegram.BaseURL = "http://api.telegram.org"
		}},
		{"tgBaseURLHTTPSBadHost", func(c *Config) {
			c.Notify.Telegram.Enabled = true
			c.Notify.Telegram.ChatID = "123"
			c.Notify.Telegram.SecretRef = "conn/tg/token"
			c.Notify.Telegram.BaseURL = "api.telegram.org"
		}},
		{"trendSymbols", func(c *Config) {
			c.Signals.Trend.Enabled = true
			c.Signals.Trend.Symbols = nil
		}},
		{"trendVenue", func(c *Config) {
			c.Signals.Trend.Enabled = true
			c.Signals.Trend.Venue = " "
			c.Signals.Trend.Symbols = []string{"BTCUSDT"}
		}},
		{"trendSymbolEmpty", func(c *Config) {
			c.Signals.Trend.Enabled = true
			c.Signals.Trend.Symbols = []string{"BTCUSDT", " "}
		}},
		{"trendTimeframe", func(c *Config) {
			c.Signals.Trend.Enabled = true
			c.Signals.Trend.Symbols = []string{"BTCUSDT"}
			c.Signals.Trend.Timeframe = ""
		}},
		{"trendFast", func(c *Config) {
			c.Signals.Trend.Enabled = true
			c.Signals.Trend.Symbols = []string{"BTCUSDT"}
			c.Signals.Trend.Fast = 0
		}},
		{"trendSlow", func(c *Config) {
			c.Signals.Trend.Enabled = true
			c.Signals.Trend.Symbols = []string{"BTCUSDT"}
			c.Signals.Trend.Slow = c.Signals.Trend.Fast
		}},
		{"trendSides", func(c *Config) {
			c.Signals.Trend.Enabled = true
			c.Signals.Trend.Symbols = []string{"BTCUSDT"}
			c.Signals.Trend.Sides = []string{"hold"}
		}},
		{"arenaProfile", func(c *Config) {
			c.Arena.Enabled = true
			c.Arena.Profile = "openai"
		}},
		{"arenaModel", func(c *Config) {
			c.Arena.Enabled = true
			c.Arena.Model = ""
		}},
		{"arenaLatest", func(c *Config) {
			c.Arena.Enabled = true
			c.Arena.Model = "drex-max-latest"
		}},
		{"arenaPreview", func(c *Config) {
			c.Arena.Enabled = true
			c.Arena.Model = "drex-max-preview"
		}},
		{"arenaSecret", func(c *Config) {
			c.Arena.Enabled = true
			c.Arena.SecretRef = ""
		}},
		{"arenaPrice", func(c *Config) {
			c.Arena.Enabled = true
			c.Arena.PricePerBillion = "0"
		}},
		{"arenaURL", func(c *Config) {
			c.Arena.Enabled = true
			c.Arena.BaseURL = "http://example.com"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mut(&c)
			if err := c.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
		})
	}
}

func TestLiveMessage(t *testing.T) {
	c := Default()
	c.Mode = "live"
	err := c.Validate()
	if err == nil {
		t.Fatalf("Validate() = nil")
	}
	if got := err.Error(); !contains(got, "live mode is not available in this release") {
		t.Fatalf("error %q misses the release wording", got)
	}
}

func TestModelAliasBanned(t *testing.T) {
	c := Default()
	c.Providers.Decision.Model = "drex-max-latest"
	err := c.Validate()
	if err == nil {
		t.Fatalf("Validate() = nil, want error")
	}
	if got := err.Error(); !contains(got, "aliases like *-latest are not allowed") {
		t.Fatalf("error %q misses alias text", got)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if got.Providers.Decision.Model != "drex-max-v1.1" {
		t.Fatalf("model = %q", got.Providers.Decision.Model)
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatalf("Load() = nil, want error")
	}
}

func TestSaveAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	path := filepath.Join(dir, "config.json")
	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	// A second save must replace the file in place, not add siblings.
	cfg := Default()
	cfg.Gate.MinPass = 0.7
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() overwrite = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got.Gate.MinPass != 0.7 {
		t.Fatalf("minPass = %v, want 0.7", got.Gate.MinPass)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("dir entries = %v, want only config.json (no temp leftovers)", entries)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() = %v", err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Fatalf("config perm = %o, want 600", perm)
		}
	}
}

func TestGetPaths(t *testing.T) {
	c := Default()
	for key, want := range map[string]string{
		"apiVersion":                   "vending.lv24/v1",
		"mode":                         "shadow",
		"gate.version":                 "gate-v1",
		"providers.decision.model":     "drex-max-v1.1",
		"providers.decision.secretRef": "conn/drex/apiKey",
		"providers.marketdata.id":      "marketdata-binance",
	} {
		v, err := c.Get(key)
		if err != nil {
			t.Fatalf("Get(%q) = %v", key, err)
		}
		if s, ok := v.(string); !ok || s != want {
			t.Fatalf("Get(%q) = %v, want %q", key, v, want)
		}
	}
	if v, err := c.Get("gate.minPass"); err != nil {
		t.Fatalf("Get(gate.minPass) = %v", err)
	} else if f, ok := v.(float64); !ok || f != 0.55 {
		t.Fatalf("Get(gate.minPass) = %v (%T)", v, v)
	}
	if _, err := c.Get("gate.nope"); err == nil {
		t.Fatalf("Get(gate.nope) = nil, want error")
	}
	if _, err := c.Get("mode.deeper"); err == nil {
		t.Fatalf("Get(mode.deeper) = nil, want error")
	}
}

func TestShadowModeAccepted(t *testing.T) {
	for _, mode := range []string{"paper", "sandbox", "shadow"} {
		c := Default()
		c.Mode = mode
		if err := c.Validate(); err != nil {
			t.Fatalf("mode %q: Validate() = %v", mode, err)
		}
	}
	if got := Default().Mode; got != "shadow" {
		t.Fatalf("Default().Mode = %q, want shadow", got)
	}
}

func TestOutcomeDefaults(t *testing.T) {
	c := Default()
	if c.Outcome.HorizonBars != 12 {
		t.Fatalf("HorizonBars = %d, want 12", c.Outcome.HorizonBars)
	}
	if c.Outcome.FeeBps != 10 {
		t.Fatalf("FeeBps = %d, want 10", c.Outcome.FeeBps)
	}
	if c.Outcome.LabelIntervalSec != 300 {
		t.Fatalf("LabelIntervalSec = %d, want 300", c.Outcome.LabelIntervalSec)
	}
	for key, want := range map[string]float64{
		"outcome.horizonBars":      12,
		"outcome.feeBps":           10,
		"outcome.labelIntervalSec": 300,
	} {
		v, err := c.Get(key)
		if err != nil {
			t.Fatalf("Get(%q) = %v", key, err)
		}
		if f, ok := v.(float64); !ok || f != want {
			t.Fatalf("Get(%q) = %v (%T), want %v", key, v, v, want)
		}
	}
}

func TestSecretEnvName(t *testing.T) {
	if got := SecretEnvName("conn/drex/apiKey"); got != "VENDING_SECRET_CONN_DREX_APIKEY" {
		t.Fatalf("SecretEnvName = %q", got)
	}
}

func TestEnvSecretStore(t *testing.T) {
	t.Setenv("VENDING_SECRET_CONN_DREX_APIKEY", "s3cr3t")
	s := EnvSecretStore{Getenv: nil} // nil falls back to os.Getenv
	if v, err := s.Get(context.Background(), "conn/drex/apiKey"); err != nil || v != "s3cr3t" {
		t.Fatalf("Get() = %q, %v", v, err)
	}
	s2 := EnvSecretStore{Getenv: func(string) string { return "" }}
	if _, err := s2.Get(context.Background(), "conn/drex/apiKey"); err == nil {
		t.Fatalf("Get() = nil, want error")
	} else if got := err.Error(); !contains(got, "VENDING_SECRET_CONN_DREX_APIKEY") {
		t.Fatalf("error %q misses env name", got)
	}
}

func TestDefaultPaths(t *testing.T) {
	get := func(k string) string {
		switch k {
		case "VENDING_CONFIG_FILE":
			return `C:\cfg\config.json`
		case "VENDING_DATA_DIR":
			return `C:\data`
		}
		return ""
	}
	p, err := DefaultConfigPath(get)
	if err != nil || p != `C:\cfg\config.json` {
		t.Fatalf("DefaultConfigPath = %q, %v", p, err)
	}
	d, err := DefaultDataDir(get)
	if err != nil || d != `C:\data` {
		t.Fatalf("DefaultDataDir = %q, %v", d, err)
	}
	if got := DBPath(d); got != filepath.Join(d, "vending.db") {
		t.Fatalf("DBPath = %q", got)
	}
	// OS defaults resolve without error when home-ish vars are set.
	home := t.TempDir()
	p2, err := DefaultConfigPath(func(k string) string {
		switch k {
		case "HOME", "APPDATA", "LOCALAPPDATA", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME":
			return home
		}
		return ""
	})
	if err != nil || p2 == "" {
		t.Fatalf("DefaultConfigPath fallback = %q, %v", p2, err)
	}
}

func TestSectionDefaults(t *testing.T) {
	c := Default()
	if c.Notify.Telegram.Enabled {
		t.Fatalf("Notify.Telegram.Enabled = true, want false")
	}
	tr := c.Signals.Trend
	if tr.Enabled {
		t.Fatalf("Signals.Trend.Enabled = true, want false")
	}
	if tr.Venue != "binance" {
		t.Fatalf("Signals.Trend.Venue = %q, want binance", tr.Venue)
	}
	if tr.Symbols == nil || len(tr.Symbols) != 0 {
		t.Fatalf("Signals.Trend.Symbols = %#v, want non-nil empty", tr.Symbols)
	}
	if tr.Timeframe != "1h" || tr.Fast != 12 || tr.Slow != 26 {
		t.Fatalf("Trend defaults = %+v, want 1h/12/26", tr)
	}
	if len(tr.Sides) != 1 || tr.Sides[0] != "buy" {
		t.Fatalf("Trend.Sides = %v, want [buy]", tr.Sides)
	}
	if c.Arena.Enabled {
		t.Fatalf("Arena.Enabled = true, want false")
	}
}

func TestTrendSymbolsMarshalEmpty(t *testing.T) {
	raw, err := json.Marshal(Default())
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	var doc struct {
		Signals struct {
			Trend struct {
				Venue   string          `json:"venue"`
				Symbols json.RawMessage `json:"symbols"`
			} `json:"trend"`
		} `json:"signals"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if got := string(doc.Signals.Trend.Symbols); got != "[]" {
		t.Fatalf("signals.trend.symbols = %s, want []", got)
	}
	if got := doc.Signals.Trend.Venue; got != "binance" {
		t.Fatalf("signals.trend.venue = %q, want binance", got)
	}
	v, err := Default().Get("signals.trend.venue")
	if err != nil {
		t.Fatalf("Get(signals.trend.venue) = %v", err)
	}
	if s, ok := v.(string); !ok || s != "binance" {
		t.Fatalf("Get(signals.trend.venue) = %v (%T)", v, v)
	}
}

func TestEnabledSectionsValid(t *testing.T) {
	c := Default()
	c.Notify.Telegram.Enabled = true
	c.Notify.Telegram.ChatID = "123456"
	c.Notify.Telegram.SecretRef = "conn/tg/token"
	c.Signals.Trend.Enabled = true
	c.Signals.Trend.Symbols = []string{"BTCUSDT"}
	c.Signals.Trend.Sides = []string{"buy", "sell"}
	c.Arena.Enabled = true
	c.Arena.BaseURL = "http://127.0.0.1:11434/v1"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	c.Arena.BaseURL = "https://example.com/v1"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() https arena = %v", err)
	}
	c.Signals.Trend.Symbols = []string{"BTCUSDT", "ETHUSDT"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() multi-symbol = %v", err)
	}
	c.Notify.Telegram.BaseURL = "http://127.0.0.1:8081"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() loopback telegram baseUrl = %v", err)
	}
	c.Notify.Telegram.BaseURL = "https://api.telegram.org"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() https telegram baseUrl = %v", err)
	}
}

func TestLoadInvalidJSONMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() accepted broken JSON")
	}
	if !contains(err.Error(), "invalid JSON in") {
		t.Fatalf("error %q misses the invalid-JSON wording", err)
	}
}

func TestTrendTimeframeValidation(t *testing.T) {
	base := func() Config {
		c := Default()
		c.Signals.Trend.Enabled = true
		c.Signals.Trend.Symbols = []string{"BTCUSDT"}
		return c
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("default trend config: Validate() = %v", err)
	}
	// 1x was accepted before: config validate said ok, serve then failed on
	// the same timeframe, which made the hint circular.
	bad := base()
	bad.Signals.Trend.Timeframe = "1x"
	err := bad.Validate()
	if err == nil {
		t.Fatal("Validate() accepted timeframe 1x")
	}
	for _, want := range []string{"signals.trend.timeframe", "1x", "m, h, d or w"} {
		if !contains(err.Error(), want) {
			t.Fatalf("error %q misses %q", err, want)
		}
	}

	// A candle must hold two label windows; 10m is shorter than 2x600s.
	short := base()
	short.Signals.Trend.Timeframe = "10m"
	short.Outcome.LabelIntervalSec = 600
	if err := short.Validate(); err == nil {
		t.Fatal("Validate() accepted a timeframe shorter than twice labelIntervalSec")
	} else if !contains(err.Error(), "twice outcome.labelIntervalSec") {
		t.Fatalf("error %q misses the label-window guard", err)
	}

	// Exactly 2x is allowed.
	edge := base()
	edge.Signals.Trend.Timeframe = "1h"
	edge.Outcome.LabelIntervalSec = 1800
	if err := edge.Validate(); err != nil {
		t.Fatalf("Validate() at exactly 2x label interval = %v", err)
	}

	// A disabled trend section is not used by serve, so its stale timeframe
	// must not block unrelated commands.
	disabled := Default()
	disabled.Signals.Trend.Timeframe = "1x"
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled trend timeframe: Validate() = %v", err)
	}
}

// TestConfigDoesNotCheckProviderID documents the split: config validate has no
// provider registry, so an unknown providers.decision.id is doctor's finding.
func TestConfigDoesNotCheckProviderID(t *testing.T) {
	c := Default()
	c.Providers.Decision.ID = "decision-unknown"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (doctor checks the registry)", err)
	}
}

func TestLoadBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw, err := json.Marshal(Default())
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	if err := os.WriteFile(path, append([]byte("\xef\xbb\xbf"), raw...), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(BOM) = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestLoadBOMInvalidJSONMentionsBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbf{"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() accepted broken JSON")
	}
	if !contains(err.Error(), "file has BOM") {
		t.Fatalf("error %q misses the BOM hint", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
