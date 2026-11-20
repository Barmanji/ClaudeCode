package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	for _, key := range []string{
		"CLAWD_API_KEY", "CLAWD_PROVIDER", "CLAWD_MODEL", "CLAWD_BASE_URL", "CLAWD_MAX_TOKENS",
		"OPENROUTER_API_KEY", "OPENAI_API_KEY", "GROQ_API_KEY", "DEEPSEEK_API_KEY",
		"MISTRAL_API_KEY", "XAI_API_KEY", "TOGETHER_API_KEY",
	} {
		t.Setenv(key, "")
	}
}

func TestResolveNeedsSetup(t *testing.T) {
	isolate(t)
	if _, err := Resolve(); !errors.Is(err, ErrNeedsSetup) {
		t.Fatalf("expected ErrNeedsSetup, got %v", err)
	}
}

func TestResolveDetectsProviderEnv(t *testing.T) {
	isolate(t)
	t.Setenv("OPENAI_API_KEY", "sk-test")

	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider.ID != "openai" {
		t.Fatalf("provider = %q, want openai", got.Provider.ID)
	}
	if got.Model != "gpt-4o-mini" {
		t.Fatalf("model = %q", got.Model)
	}
	if got.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("base url = %q", got.BaseURL)
	}
}

func TestFileConfigRoundTrip(t *testing.T) {
	isolate(t)
	want := &Config{Provider: "groq", Model: "llama-3.3-70b-versatile", APIKey: "gsk-1"}
	if err := want.Save(); err != nil {
		t.Fatal(err)
	}

	path, _ := Path()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions too open: %v", info.Mode().Perm())
	}

	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider.ID != "groq" || got.Model != want.Model || got.Source != "config" {
		t.Fatalf("unexpected settings: %+v", got)
	}
}

func TestProviderEnvBeatsFile(t *testing.T) {
	isolate(t)
	if err := (&Config{Provider: "openai", Model: "gpt-4o", APIKey: "file-key"}).Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "env-key")

	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "env-key" || got.Source != "env" {
		t.Fatalf("expected env key to win, got %+v", got)
	}
}

func TestOverridesBeatEverything(t *testing.T) {
	isolate(t)
	if err := (&Config{Provider: "openai", Model: "gpt-4o", APIKey: "file-key"}).Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "env-key")

	got, err := ResolveWith(Overrides{Provider: "groq", Model: "llama-3.1-8b-instant", APIKey: "flag-key", MaxTokens: 123})
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider.ID != "groq" || got.Model != "llama-3.1-8b-instant" || got.APIKey != "flag-key" || got.MaxTokens != 123 {
		t.Fatalf("unexpected settings: %+v", got)
	}
}

func TestClawdKeyBeatsProviderEnv(t *testing.T) {
	isolate(t)
	t.Setenv("CLAWD_API_KEY", "clawd-key")
	t.Setenv("OPENAI_API_KEY", "provider-key")

	got, err := ResolveWith(Overrides{Provider: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "clawd-key" {
		t.Fatalf("CLAWD_API_KEY should win, got %q", got.APIKey)
	}
}

func TestExplicitProviderSuppressesDetection(t *testing.T) {
	isolate(t)
	t.Setenv("CLAWD_PROVIDER", "groq")
	t.Setenv("OPENAI_API_KEY", "provider-key")

	if _, err := Resolve(); !errors.Is(err, ErrNeedsSetup) {
		t.Fatalf("expected ErrNeedsSetup for groq without a groq key, got %v", err)
	}
}

func TestSwitchingProviderDropsFileModelAndURL(t *testing.T) {
	isolate(t)
	if err := (&Config{
		Provider: "custom",
		Model:    "file-model",
		APIKey:   "file-key",
		BaseURL:  "http://localhost:9999/v1",
	}).Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROQ_API_KEY", "gsk-env")

	got, err := ResolveWith(Overrides{Provider: "groq"})
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "https://api.groq.com/openai/v1" {
		t.Fatalf("base url should follow the provider, got %q", got.BaseURL)
	}
	if got.Model != "llama-3.3-70b-versatile" {
		t.Fatalf("model should fall back to the provider default, got %q", got.Model)
	}
	if got.APIKey != "gsk-env" {
		t.Fatalf("key should come from GROQ_API_KEY, got %q", got.APIKey)
	}
}

func TestSwitchingProviderKeepsExplicitModel(t *testing.T) {
	isolate(t)
	if err := (&Config{Provider: "openai", Model: "gpt-4o", APIKey: "k"}).Save(); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveWith(Overrides{Provider: "groq", Model: "custom-model"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "custom-model" {
		t.Fatalf("explicit model should survive a provider switch, got %q", got.Model)
	}
}

func TestCustomProviderRequiresBaseURL(t *testing.T) {
	isolate(t)
	if err := (&Config{Provider: "custom", Model: "x", APIKey: "k"}).Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(); err == nil {
		t.Fatal("expected an error for a custom provider without base_url")
	}
}

func TestLocalProviderGetsPlaceholderKey(t *testing.T) {
	isolate(t)
	if err := (&Config{Provider: "ollama"}).Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey == "" || got.Model == "" {
		t.Fatalf("local provider should be usable: %+v", got)
	}
}

func TestFilterModels(t *testing.T) {
	openai, _ := LookupProvider("openai")
	models := FilterModels(openai, []string{
		"gpt-4o-mini", "text-embedding-3-small", "whisper-1", "gpt-4o",
	})
	if len(models) != 2 || models[0] != "gpt-4o" || models[1] != "gpt-4o-mini" {
		t.Fatalf("unexpected filtered list: %v", models)
	}
}

func TestFilterModelsFallsBack(t *testing.T) {
	openrouter, _ := LookupProvider("openrouter")
	models := FilterModels(openrouter, nil)
	if len(models) == 0 {
		t.Fatal("expected fallback models")
	}
}

func TestMaskKey(t *testing.T) {
	if got := MaskKey("sk-1234567890abcdef"); got != "sk-12…cdef" {
		t.Fatalf("MaskKey = %q", got)
	}
	if got := MaskKey(""); got != "(not set)" {
		t.Fatalf("MaskKey empty = %q", got)
	}
	if got := MaskKey("short"); got == "short" {
		t.Fatalf("short keys should be masked: %q", got)
	}
}

func TestSaveCreatesPrivateDir(t *testing.T) {
	isolate(t)
	if err := (&Config{Provider: "openai", Model: "gpt-4o", APIKey: "k"}).Save(); err != nil {
		t.Fatal(err)
	}
	dir, _ := Dir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("dir permissions = %v", info.Mode().Perm())
	}
	if filepath.Base(dir) != DirName {
		t.Fatalf("dir = %q", dir)
	}
}
