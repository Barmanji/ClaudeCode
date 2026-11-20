package config

import (
	"os"
	"sort"
	"strings"
)

// Provider describes an OpenAI-compatible chat completions endpoint.
type Provider struct {
	ID           string
	Name         string
	Blurb        string
	BaseURL      string
	KeyEnv       string
	KeyURL       string
	DefaultModel string
	Models       []string
	// Local providers (Ollama) do not require an API key.
	Local bool
	// Filter drops non-chat models from the live /models listing. It is
	// best-effort: providers change their catalogues constantly, so an empty
	// result falls back to Models.
	Filter func(string) bool
}

// NeedsKey reports whether the provider requires an API key.
func (p Provider) NeedsKey() bool { return !p.Local }

// ModelsPath is the OpenAI-compatible model listing endpoint.
var providers = []Provider{
	{
		ID:           "openrouter",
		Name:         "OpenRouter",
		Blurb:        "one key, every model (recommended)",
		BaseURL:      "https://openrouter.ai/api/v1",
		KeyEnv:       "OPENROUTER_API_KEY",
		KeyURL:       "https://openrouter.ai/keys",
		DefaultModel: "anthropic/claude-haiku-4.5",
		Models: []string{
			"anthropic/claude-haiku-4.5",
			"anthropic/claude-sonnet-4.5",
			"openai/gpt-4o-mini",
			"google/gemini-2.5-flash",
			"deepseek/deepseek-chat",
		},
		Filter: excludeModels(":batch"),
	},
	{
		ID:           "openai",
		Name:         "OpenAI",
		Blurb:        "api.openai.com",
		BaseURL:      "https://api.openai.com/v1",
		KeyEnv:       "OPENAI_API_KEY",
		KeyURL:       "https://platform.openai.com/api-keys",
		DefaultModel: "gpt-4o-mini",
		Models:       []string{"gpt-4o-mini", "gpt-4o", "gpt-4.1"},
		Filter:       excludeModels("embed", "whisper", "tts", "dall-e", "moderation", "transcribe", "audio", "realtime", "image", "sora"),
	},
	{
		ID:           "groq",
		Name:         "Groq",
		Blurb:        "very fast open models",
		BaseURL:      "https://api.groq.com/openai/v1",
		KeyEnv:       "GROQ_API_KEY",
		KeyURL:       "https://console.groq.com/keys",
		DefaultModel: "llama-3.3-70b-versatile",
		Models:       []string{"llama-3.3-70b-versatile", "llama-3.1-8b-instant"},
		Filter:       excludeModels("whisper", "tts", "guard", "embed"),
	},
	{
		ID:           "deepseek",
		Name:         "DeepSeek",
		Blurb:        "cheap coding models",
		BaseURL:      "https://api.deepseek.com/v1",
		KeyEnv:       "DEEPSEEK_API_KEY",
		KeyURL:       "https://platform.deepseek.com/api_keys",
		DefaultModel: "deepseek-chat",
		Models:       []string{"deepseek-chat", "deepseek-reasoner"},
	},
	{
		ID:           "mistral",
		Name:         "Mistral",
		Blurb:        "api.mistral.ai",
		BaseURL:      "https://api.mistral.ai/v1",
		KeyEnv:       "MISTRAL_API_KEY",
		KeyURL:       "https://console.mistral.ai/api-keys",
		DefaultModel: "mistral-large-latest",
		Models:       []string{"mistral-large-latest", "mistral-small-latest", "codestral-latest"},
		Filter:       excludeModels("embed", "moderation", "ocr"),
	},
	{
		ID:           "xai",
		Name:         "xAI",
		Blurb:        "Grok models",
		BaseURL:      "https://api.x.ai/v1",
		KeyEnv:       "XAI_API_KEY",
		KeyURL:       "https://console.x.ai",
		DefaultModel: "grok-3",
		Models:       []string{"grok-3", "grok-3-mini"},
	},
	{
		ID:           "together",
		Name:         "Together AI",
		Blurb:        "wide open-model catalogue",
		BaseURL:      "https://api.together.xyz/v1",
		KeyEnv:       "TOGETHER_API_KEY",
		KeyURL:       "https://api.together.ai/settings/api-keys",
		DefaultModel: "meta-llama/Llama-3.3-70B-Instruct-Turbo",
		Models: []string{
			"meta-llama/Llama-3.3-70B-Instruct-Turbo",
			"Qwen/Qwen2.5-Coder-32B-Instruct",
		},
		Filter: excludeModels("embed", "rerank", "moderation", "image", "flux", "sd"),
	},
	{
		ID:           "ollama",
		Name:         "Ollama (local)",
		Blurb:        "no key, runs on your machine",
		BaseURL:      "http://localhost:11434/v1",
		DefaultModel: "qwen2.5-coder:7b",
		Models:       []string{"qwen2.5-coder:7b", "llama3.2", "deepseek-coder-v2"},
		Local:        true,
	},
	{
		ID:    "custom",
		Name:  "Custom endpoint",
		Blurb: "any OpenAI-compatible API (LiteLLM, vLLM, ...)",
		Local: false,
	},
}

// Providers returns the provider catalogue in menu order.
func Providers() []Provider {
	out := make([]Provider, len(providers))
	copy(out, providers)
	return out
}

// LookupProvider resolves a provider by ID.
func LookupProvider(id string) (Provider, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range providers {
		if p.ID == id {
			return p, true
		}
	}
	// Accept a few friendly aliases.
	switch id {
	case "open-router", "or":
		return LookupProvider("openrouter")
	case "together.ai", "togetherai":
		return LookupProvider("together")
	case "local":
		return LookupProvider("ollama")
	}
	return Provider{}, false
}

// DetectFromEnv returns the first well-known provider whose key is present in
// the environment. This makes `OPENAI_API_KEY=... clawd "hi"` work with no
// setup at all.
func DetectFromEnv() (Provider, string, bool) {
	for _, p := range providers {
		if p.KeyEnv == "" || p.Local {
			continue
		}
		if key := strings.TrimSpace(os.Getenv(p.KeyEnv)); key != "" {
			return p, key, true
		}
	}
	return Provider{}, "", false
}

func excludeModels(needles ...string) func(string) bool {
	return func(id string) bool {
		lower := strings.ToLower(id)
		for _, needle := range needles {
			if strings.Contains(lower, needle) {
				return false
			}
		}
		return true
	}
}

// FilterModels applies the provider's chat-model filter, de-duplicates and
// sorts the result. The caller's fallback list is merged in when the live
// listing is empty.
func FilterModels(provider Provider, live []string) []string {
	seen := make(map[string]bool, len(live)+len(provider.Models))
	out := make([]string, 0, len(live)+len(provider.Models))
	for _, id := range live {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if provider.Filter != nil && !provider.Filter(id) {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		out = append(out, provider.Models...)
	}
	sort.Strings(out)
	return out
}
