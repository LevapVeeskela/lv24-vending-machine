package config

import (
	"context"
	"path/filepath"
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
		{"gateVersion", func(c *Config) { c.Gate.Version = "" }},
		{"minPassZero", func(c *Config) { c.Gate.MinPass = 0 }},
		{"minPassHigh", func(c *Config) { c.Gate.MinPass = 1.5 }},
		{"minBlockHigh", func(c *Config) { c.Gate.MinBlock = 2 }},
		{"decisionID", func(c *Config) { c.Providers.Decision.ID = "" }},
		{"model", func(c *Config) { c.Providers.Decision.Model = "" }},
		{"profile", func(c *Config) { c.Providers.Decision.Profile = "openai" }},
		{"secretRef", func(c *Config) { c.Providers.Decision.SecretRef = "" }},
		{"priceBad", func(c *Config) { c.Providers.Decision.PricePerBillion = "abc" }},
		{"priceZero", func(c *Config) { c.Providers.Decision.PricePerBillion = "0" }},
		{"marketID", func(c *Config) { c.Providers.MarketData.ID = "" }},
		{"marketURL", func(c *Config) { c.Providers.MarketData.BaseURL = "ftp://x" }},
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
	if got := err.Error(); !contains(got, "live mode is not available in M0") {
		t.Fatalf("error %q misses live M0 text", got)
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

func TestGetPaths(t *testing.T) {
	c := Default()
	for key, want := range map[string]string{
		"apiVersion":                   "vending.lv24/v1",
		"mode":                         "paper",
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
