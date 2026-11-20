package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AppName is the user-facing product name.
const AppName = "C-Lawd Code"

// DirName is used for the config directory and npm package prefix.
const DirName = "c-lawd-code"

// EnvPrefix is the prefix for every override this CLI understands.
const EnvPrefix = "CLAWD"

// ErrNeedsSetup is returned when no usable credentials are configured.
var ErrNeedsSetup = errors.New("no LLM credentials configured")

// Config is what lives on disk. The API key is stored with 0600 permissions;
// setting the provider's env var (e.g. OPENAI_API_KEY) always takes precedence.
type Config struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	APIKey    string `json:"api_key,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

// Settings is a fully resolved runtime configuration.
type Settings struct {
	Config
	Provider Provider
	// Source describes where the credentials came from: "env", "config" or
	// "detected".
	Source string
}

// Dir returns the per-user config directory.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, DirName), nil
}

// Path returns the config file path.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config file. A missing file yields an empty config.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes the config atomically with restrictive permissions.
func (c *Config) Save() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	path := filepath.Join(dir, "config.json")
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Clear removes the config file.
func Clear() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// MaskKey renders a key safely for terminals and logs.
func MaskKey(key string) string {
	key = strings.TrimSpace(key)
	switch {
	case key == "":
		return "(not set)"
	case len(key) <= 10:
		return strings.Repeat("•", len(key))
	default:
		return key[:5] + "…" + key[len(key)-4:]
	}
}

// Overrides are explicit CLI flags; they win over env and the config file.
type Overrides struct {
	Provider  string
	Model     string
	BaseURL   string
	APIKey    string
	MaxTokens int
}

// Resolve merges env overrides, the config file and auto-detected env keys.
func Resolve() (*Settings, error) {
	return ResolveWith(Overrides{})
}

// ResolveWith resolves configuration with CLI overrides applied last.
func ResolveWith(o Overrides) (*Settings, error) {
	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	s := &Settings{Config: *cfg, Source: "config"}

	baseExplicit := false
	modelExplicit := false
	keyExplicit := false
	providerExplicit := false
	providerID := cfg.Provider
	if v := strings.TrimSpace(os.Getenv(EnvPrefix + "_PROVIDER")); v != "" {
		providerID = v
		providerExplicit = true
	}
	if v := strings.TrimSpace(os.Getenv(EnvPrefix + "_BASE_URL")); v != "" {
		s.BaseURL = v
		baseExplicit = true
	}
	if v := strings.TrimSpace(os.Getenv(EnvPrefix + "_MODEL")); v != "" {
		s.Model = v
		modelExplicit = true
	}
	if v := strings.TrimSpace(os.Getenv(EnvPrefix + "_API_KEY")); v != "" {
		s.APIKey = v
		s.Source = "env"
		keyExplicit = true
	}
	if v := strings.TrimSpace(os.Getenv(EnvPrefix + "_MAX_TOKENS")); v != "" {
		fmt.Sscanf(v, "%d", &s.MaxTokens)
	}

	// CLI flags have the final say.
	if o.Provider != "" {
		providerID = o.Provider
		providerExplicit = true
	}
	if o.BaseURL != "" {
		s.BaseURL = o.BaseURL
		baseExplicit = true
	}
	if o.Model != "" {
		s.Model = o.Model
		modelExplicit = true
	}
	if o.APIKey != "" {
		s.APIKey = o.APIKey
		s.Source = "flags"
		keyExplicit = true
	}
	if o.MaxTokens > 0 {
		s.MaxTokens = o.MaxTokens
	}

	if providerID == "" {
		providerID = "openrouter"
	}
	provider, ok := LookupProvider(providerID)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (try `clawd setup`)", providerID)
	}
	s.Provider = provider

	// A base URL or model from the config file only applies to the provider it
	// was configured for; switching providers must not silently reuse it.
	if !baseExplicit && !strings.EqualFold(cfg.Provider, provider.ID) {
		s.BaseURL = ""
	}
	if !modelExplicit && !strings.EqualFold(cfg.Provider, provider.ID) {
		s.Model = ""
	}

	// A key from the provider's own env var beats a key from the config file,
	// and is enough to pick the provider when nothing is configured yet.
	if provider.KeyEnv != "" && provider.NeedsKey() && !keyExplicit {
		if key := strings.TrimSpace(os.Getenv(provider.KeyEnv)); key != "" {
			s.APIKey = key
			s.Source = "env"
		}
	}

	if s.APIKey == "" && provider.NeedsKey() && cfg.Provider == "" && !providerExplicit {
		if detected, key, found := DetectFromEnv(); found {
			provider = detected
			s.Provider = detected
			s.APIKey = key
			s.BaseURL = ""
			if !modelExplicit {
				s.Model = ""
			}
			s.Source = "detected"
		}
	}

	if provider.ID == "custom" && s.BaseURL == "" {
		return nil, fmt.Errorf("custom provider needs a base URL (set CLAWD_BASE_URL or run `clawd setup`)")
	}

	if provider.NeedsKey() && s.APIKey == "" {
		return nil, ErrNeedsSetup
	}
	if provider.Local && s.APIKey == "" {
		s.APIKey = "ollama" // the OpenAI SDK requires a non-empty token
	}

	if s.BaseURL == "" {
		s.BaseURL = provider.BaseURL
	}
	if s.Model == "" {
		s.Model = provider.DefaultModel
	}
	if s.Model == "" {
		return nil, fmt.Errorf("no model configured for %s (run `clawd setup`)", provider.Name)
	}
	if s.MaxTokens <= 0 {
		s.MaxTokens = 4096
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	return s, nil
}
