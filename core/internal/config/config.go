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
	Outcome    OutcomeConfig   `json:"outcome"`
	Providers  ProvidersConfig `json:"providers"`
	Notify     NotifyConfig    `json:"notify"`
	Signals    SignalsConfig   `json:"signals"`
	Arena      ArenaConfig     `json:"arena"`
}

// GateConfig tunes the risk gate thresholds.
type GateConfig struct {
	Version  string  `json:"version"`
	MinPass  float64 `json:"minPass"`
	MinBlock float64 `json:"minBlock"`
}

// OutcomeConfig tunes outcome labeling for shadow evaluation.
type OutcomeConfig struct {
	HorizonBars      int `json:"horizonBars"`
	FeeBps           int `json:"feeBps"`
	LabelIntervalSec int `json:"labelIntervalSec"`
}

// NotifyConfig holds outbound operator notification settings.
type NotifyConfig struct {
	Telegram TelegramConfig `json:"telegram"`
}

// TelegramConfig holds the Telegram notification channel settings.
// ChatID and SecretRef carry no secret values: SecretRef is a
// conn/<id>/<field> reference resolved via the secret store.
type TelegramConfig struct {
	Enabled   bool   `json:"enabled"`
	ChatID    string `json:"chatId"`
	SecretRef string `json:"secretRef"`
	// BaseURL overrides the Bot API endpoint (fixtures, local proxies).
	// Empty means the provider default.
	BaseURL string `json:"baseUrl"`
}

// SignalsConfig holds local signal source settings.
type SignalsConfig struct {
	Trend TrendConfig `json:"trend"`
}

// TrendConfig tunes the built-in trend signal source.
type TrendConfig struct {
	Enabled   bool     `json:"enabled"`
	Venue     string   `json:"venue"`
	Symbols   []string `json:"symbols"`
	Timeframe string   `json:"timeframe"`
	Fast      int      `json:"fast"`
	Slow      int      `json:"slow"`
	Sides     []string `json:"sides"`
}

// ArenaConfig holds the cross-model arena comparison settings.
type ArenaConfig struct {
	Enabled         bool   `json:"enabled"`
	Profile         string `json:"profile"`
	Model           string `json:"model"`
	BaseURL         string `json:"baseUrl"`
	SecretRef       string `json:"secretRef"`
	PricePerBillion string `json:"pricePerBillion"`
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
		Mode:       "shadow",
		Gate: GateConfig{
			Version:  "gate-v1",
			MinPass:  0.55,
			MinBlock: 0.50,
		},
		Outcome: OutcomeConfig{
			HorizonBars:      12,
			FeeBps:           10,
			LabelIntervalSec: 300,
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
		Notify: NotifyConfig{
			Telegram: TelegramConfig{
				Enabled:   false,
				ChatID:    "",
				SecretRef: "",
			},
		},
		Signals: SignalsConfig{
			Trend: TrendConfig{
				Enabled:   false,
				Venue:     "binance",
				Symbols:   []string{},
				Timeframe: "1h",
				Fast:      12,
				Slow:      26,
				Sides:     []string{"buy"},
			},
		},
		Arena: ArenaConfig{
			Enabled:         false,
			Profile:         "drex",
			Model:           "drex-max-v1.1",
			BaseURL:         "",
			SecretRef:       "conn/drex/apiKey",
			PricePerBillion: "60",
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
	case "paper", "sandbox", "shadow":
		// ok
	case "live":
		return domain.NewError(domain.CodeInvalidRequest,
			"config: live mode is not available in this release; use shadow, paper or sandbox")
	default:
		return invalid(fmt.Sprintf("mode must be paper, sandbox or shadow, got %q", c.Mode))
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
	if c.Outcome.HorizonBars < 1 || c.Outcome.HorizonBars > 1000 {
		return invalid("outcome.horizonBars must be in [1, 1000]")
	}
	if c.Outcome.FeeBps < 0 || c.Outcome.FeeBps > 10000 {
		return invalid("outcome.feeBps must be in [0, 10000]")
	}
	if c.Outcome.LabelIntervalSec < 1 || c.Outcome.LabelIntervalSec > 86400 {
		return invalid("outcome.labelIntervalSec must be in [1, 86400]")
	}
	d := c.Providers.Decision
	if strings.TrimSpace(d.ID) == "" {
		return invalid("providers.decision.id must not be empty")
	}
	if strings.TrimSpace(d.Model) == "" {
		return invalid("providers.decision.model must not be empty")
	}
	if strings.HasSuffix(strings.TrimSpace(d.Model), "-latest") {
		return invalid("providers.decision.model must pin a concrete model id, aliases like *-latest are not allowed")
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
	if tg := c.Notify.Telegram; tg.Enabled {
		if strings.TrimSpace(tg.ChatID) == "" {
			return invalid("notify.telegram.chatId must not be empty")
		}
		if strings.TrimSpace(tg.SecretRef) == "" {
			return invalid("notify.telegram.secretRef must not be empty")
		}
		if strings.TrimSpace(tg.BaseURL) != "" && !isLoopbackHTTPURL(tg.BaseURL) {
			return invalid("notify.telegram.baseUrl must be an https URL or a loopback http URL (127.0.0.1, ::1)")
		}
	}
	if tr := c.Signals.Trend; tr.Enabled {
		if strings.TrimSpace(tr.Venue) == "" {
			return invalid("signals.trend.venue must not be empty")
		}
		if len(tr.Symbols) == 0 {
			return invalid("signals.trend.symbols must not be empty")
		}
		for _, s := range tr.Symbols {
			if strings.TrimSpace(s) == "" {
				return invalid("signals.trend.symbols must not contain empty entries")
			}
		}
		if strings.TrimSpace(tr.Timeframe) == "" {
			return invalid("signals.trend.timeframe must not be empty")
		}
		if tr.Fast <= 0 {
			return invalid("signals.trend.fast must be > 0")
		}
		if tr.Slow <= tr.Fast {
			return invalid("signals.trend.slow must be > fast")
		}
		if len(tr.Sides) == 0 {
			return invalid("signals.trend.sides must not be empty")
		}
		for _, s := range tr.Sides {
			if s != "buy" && s != "sell" {
				return invalid(fmt.Sprintf("signals.trend.sides must be buy|sell, got %q", s))
			}
		}
	}
	if a := c.Arena; a.Enabled {
		if a.Profile != "drex" && a.Profile != "jev" {
			return invalid(fmt.Sprintf("arena.profile must be drex or jev, got %q", a.Profile))
		}
		if strings.TrimSpace(a.Model) == "" {
			return invalid("arena.model must not be empty")
		}
		am := strings.TrimSpace(a.Model)
		if strings.HasSuffix(am, "-latest") || strings.HasSuffix(am, "-preview") {
			return invalid("arena.model must pin a concrete model id, aliases like *-latest/*-preview are not allowed")
		}
		if strings.TrimSpace(a.SecretRef) == "" {
			return invalid("arena.secretRef must not be empty")
		}
		price, err := domain.ParseDecimal(strings.TrimSpace(a.PricePerBillion))
		if err != nil {
			return domain.WrapError(domain.CodeInvalidRequest,
				"config: arena.pricePerBillion is not a decimal", err)
		}
		if price.Sign() <= 0 {
			return invalid("arena.pricePerBillion must be > 0")
		}
		if strings.TrimSpace(a.BaseURL) != "" && !isLoopbackHTTPURL(a.BaseURL) {
			return invalid("arena.baseUrl must be an https URL or a loopback http URL (127.0.0.1, ::1)")
		}
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

// isLoopbackHTTPURL reports whether s is an absolute http(s) URL without
// userinfo, query or fragment, where plain http is restricted to the
// 127.0.0.1 and ::1 loopback hosts (mirrors the decision providers).
func isLoopbackHTTPURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || !u.IsAbs() {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return false
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if scheme == "http" && host != "127.0.0.1" && host != "::1" {
		return false
	}
	return true
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
			fmt.Sprintf("config: invalid JSON in %s", path), err)
	}
	return c, nil
}

// Save writes c as indented JSON, creating parent directories.
//
// The file is written to a temp file in the same directory and renamed into
// place, so a concurrent reader never sees a truncated config; the result is
// always 0600.
func Save(path string, c Config) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	raw = append(raw, '\n')
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("config: mkdir %s: %w", dir, err)
		}
	}
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// writeFileAtomic writes data to path through a temp file in the same
// directory followed by a rename, so an interrupted save cannot leave a
// partial config behind.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	discard := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}
	if err := f.Chmod(perm); err != nil {
		discard()
		return err
	}
	if _, err := f.Write(data); err != nil {
		discard()
		return err
	}
	if err := f.Sync(); err != nil {
		discard()
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
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
