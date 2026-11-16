// Package config loads and validates the vend JSON configuration.
//
// M0 note: JSON is used instead of YAML on purpose — YAML would require a
// new third-party dependency, which is forbidden in M0.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// APIVersion is the only accepted config apiVersion value in M0.
const APIVersion = "vending.lv24/v1"

// Config is the vend JSON configuration.
type Config struct {
	APIVersion string          `json:"apiVersion"`
	Mode       string          `json:"mode"`
	Gate       GateConfig      `json:"gate"`
	Providers  ProvidersConfig `json:"providers"`
}

// GateConfig tunes the risk gate thresholds.
type GateConfig struct {
	Version  string  `json:"version"`
	MinPass  float64 `json:"minPass"`
	MinBlock float64 `json:"minBlock"`
}

// ProvidersConfig holds provider connection settings.
type ProvidersConfig struct {
	Decision   DecisionConfig   `json:"decision"`
	MarketData MarketDataConfig `json:"marketdata"`
}

// DecisionConfig holds the decision provider connection settings.
type DecisionConfig struct {
	ID              string `json:"id"`
	Model           string `json:"model"`
	Profile         string `json:"profile"`
	BaseURL         string `json:"baseUrl"`
	SecretRef       string `json:"secretRef"`
	PricePerBillion string `json:"pricePerBillion"`
}

// MarketDataConfig holds the market data provider connection settings.
type MarketDataConfig struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseUrl"`
}

// Default returns the baseline M0 configuration.
func Default() Config {
	return Config{
		APIVersion: APIVersion,
		Mode:       "paper",
		Gate: GateConfig{
			Version:  "gate-v1",
			MinPass:  0.55,
			MinBlock: 0.50,
		},
		Providers: ProvidersConfig{
			Decision: DecisionConfig{
				ID:              "decision-drex",
				Model:           "drex-max-v1.1",
				Profile:         "drex",
				BaseURL:         "",
				SecretRef:       "conn/drex/apiKey",
				PricePerBillion: "60",
			},
			MarketData: MarketDataConfig{
				ID:      "marketdata-binance",
				BaseURL: "https://api.binance.com",
			},
		},
	}
}

// Validate checks the configuration and returns a *domain.Error with
// CodeInvalidRequest on the first violation.
func (c Config) Validate() error {
	invalid := func(msg string) error {
		return domain.NewError(domain.CodeInvalidRequest, "config: "+msg)
	}
	if c.APIVersion != APIVersion {
		return invalid(fmt.Sprintf("apiVersion must be %q", APIVersion))
	}
	switch c.Mode {
	case "paper", "sandbox":
		// ok
	case "live":
		return domain.NewError(domain.CodeInvalidRequest,
			"config: live mode is not available in M0")
	default:
		return invalid(fmt.Sprintf("mode must be paper or sandbox, got %q", c.Mode))
	}
	if strings.TrimSpace(c.Gate.Version) == "" {
		return invalid("gate.version must not be empty")
	}
	if !(c.Gate.MinPass > 0 && c.Gate.MinPass <= 1) {
		return invalid("gate.minPass must be in (0, 1]")
	}
	if !(c.Gate.MinBlock > 0 && c.Gate.MinBlock <= 1) {
		return invalid("gate.minBlock must be in (0, 1]")
	}
	d := c.Providers.Decision
	if strings.TrimSpace(d.ID) == "" {
		return invalid("providers.decision.id must not be empty")
	}
	if strings.TrimSpace(d.Model) == "" {
		return invalid("providers.decision.model must not be empty")
	}
	if d.Profile != "drex" && d.Profile != "jev" {
		return invalid(fmt.Sprintf("providers.decision.profile must be drex or jev, got %q", d.Profile))
	}
	if strings.TrimSpace(d.SecretRef) == "" {
		return invalid("providers.decision.secretRef must not be empty")
	}
	price, err := domain.ParseDecimal(strings.TrimSpace(d.PricePerBillion))
	if err != nil {
		return domain.WrapError(domain.CodeInvalidRequest,
			"config: providers.decision.pricePerBillion is not a decimal", err)
	}
	if price.Sign() <= 0 {
		return invalid("providers.decision.pricePerBillion must be > 0")
	}
	m := c.Providers.MarketData
	if strings.TrimSpace(m.ID) == "" {
		return invalid("providers.marketdata.id must not be empty")
	}
	if !isHTTPURL(m.BaseURL) {
		return invalid("providers.marketdata.baseUrl must be an http(s) URL")
	}
	if strings.TrimSpace(d.BaseURL) != "" && !isHTTPURL(d.BaseURL) {
		return invalid("providers.decision.baseUrl must be an http(s) URL")
	}
	return nil
}

// isHTTPURL reports whether s is an absolute http or https URL.
func isHTTPURL(s string) bool {
	u, err := url.ParseRequestURI(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// Load reads the JSON config at path. A missing file is an error.
func Load(path string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, domain.WrapError(domain.CodeInvalidRequest,
			fmt.Sprintf("config: parse %s", path), err)
	}
	return c, nil
}

// Save writes c as indented JSON, creating parent directories.
func Save(path string, c Config) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	raw = append(raw, '\n')
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("config: mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// Get returns the value at a dotted JSON path (e.g. "gate.minPass",
// "providers.decision.model"). A missing path is an error.
func (c Config) Get(key string) (any, error) {
	if strings.TrimSpace(key) == "" {
		return nil, domain.NewError(domain.CodeInvalidRequest, "config: empty key")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("config: encode: %w", err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("config: decode: %w", err)
	}
	cur := v
	for _, part := range strings.Split(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, domain.NewError(domain.CodeInvalidRequest,
				fmt.Sprintf("config: unknown key %q", key))
		}
		cur, ok = m[part]
		if !ok {
			return nil, domain.NewError(domain.CodeInvalidRequest,
				fmt.Sprintf("config: unknown key %q", key))
		}
	}
	return cur, nil
}

// DefaultConfigPath resolves the config file path:
// VENDING_CONFIG_FILE, otherwise the OS-specific default.
func DefaultConfigPath(getenv func(string) string) (string, error) {
	get := getenvOrOS(getenv)
	if p := strings.TrimSpace(get("VENDING_CONFIG_FILE")); p != "" {
		return p, nil
	}
	dir, err := configBaseDir(get)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lv24-vending", "config.json"), nil
}

// DefaultDataDir resolves the data directory:
// VENDING_DATA_DIR, otherwise the OS-specific default.
func DefaultDataDir(getenv func(string) string) (string, error) {
	get := getenvOrOS(getenv)
	if p := strings.TrimSpace(get("VENDING_DATA_DIR")); p != "" {
		return p, nil
	}
	dir, err := dataBaseDir(get)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lv24-vending"), nil
}

// DBPath returns the sqlite database file inside dataDir.
func DBPath(dataDir string) string {
	return filepath.Join(dataDir, "vending.db")
}

func getenvOrOS(getenv func(string) string) func(string) string {
	if getenv != nil {
		return getenv
	}
	return os.Getenv
}

func configBaseDir(get func(string) string) (string, error) {
	switch runtime.GOOS {
	case "windows":
		if v := strings.TrimSpace(get("APPDATA")); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("config: APPDATA is not set")
	case "darwin":
		home, err := homeDir(get)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	default:
		if v := strings.TrimSpace(get("XDG_CONFIG_HOME")); v != "" {
			return v, nil
		}
		home, err := homeDir(get)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config"), nil
	}
}

func dataBaseDir(get func(string) string) (string, error) {
	switch runtime.GOOS {
	case "windows":
		if v := strings.TrimSpace(get("LOCALAPPDATA")); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("config: LOCALAPPDATA is not set")
	case "darwin":
		home, err := homeDir(get)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	default:
		if v := strings.TrimSpace(get("XDG_DATA_HOME")); v != "" {
			return v, nil
		}
		home, err := homeDir(get)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	}
}

func homeDir(get func(string) string) (string, error) {
	if v := strings.TrimSpace(get("HOME")); v != "" {
		return v, nil
	}
	if runtime.GOOS == "windows" {
		if v := strings.TrimSpace(get("USERPROFILE")); v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("config: home directory is not set")
}

// SecretEnvName maps a secret reference to its M0 environment variable name:
// "VENDING_SECRET_" + uppercase(ref) with every non-[A-Za-z0-9] char
// replaced by "_".
func SecretEnvName(ref string) string {
	var sb strings.Builder
	sb.WriteString("VENDING_SECRET_")
	for _, r := range strings.ToUpper(ref) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	return sb.String()
}

// EnvSecretStore resolves secrets from environment variables (M0).
// Keychain support is M1.
type EnvSecretStore struct {
	Getenv func(string) string
}

// Get returns the secret for ref or a domain auth error. The value is
// never logged by this package.
func (s EnvSecretStore) Get(_ context.Context, ref string) (string, error) {
	name := SecretEnvName(ref)
	get := getenvOrOS(s.Getenv)
	if v := get(name); v != "" {
		return v, nil
	}
	return "", domain.NewError(domain.CodeAuth,
		fmt.Sprintf("secret %q is not set (expected env %s)", ref, name))
}
