// Package ui renders the first-run wizard and small status messages.
package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Barmanji/ClaudeCode/internal/config"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

var (
	brandStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF7A45"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#7D8590"))
	okStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#3FB950"))
	warnStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#D29922"))
	errStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F85149"))
)

// Interactive reports whether a wizard can be shown.
func Interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// ErrCancelled is returned when the user aborts the wizard.
var ErrCancelled = errors.New("setup cancelled")

func banner(version string) string {
	title := brandStyle.Render("C-Lawd Code")
	meta := dimStyle.Render(" " + version)
	tagline := dimStyle.Render("a tiny coding agent for your terminal")
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#FF7A45")).
		Padding(0, 2).
		Render(title + meta + "\n" + tagline)
	return box
}

func newForm(groups ...*huh.Group) *huh.Form {
	form := huh.NewForm(groups...).WithTheme(huh.ThemeCharm()).WithShowHelp(true)
	if os.Getenv("CLAWD_ACCESSIBLE") != "" || os.Getenv("ACCESSIBLE") != "" {
		form = form.WithAccessible(true)
	}
	return form
}

func providerOptions() []huh.Option[string] {
	providers := config.Providers()
	options := make([]huh.Option[string], 0, len(providers))
	for _, p := range providers {
		label := p.Name
		if p.Blurb != "" {
			label += " · " + p.Blurb
		}
		options = append(options, huh.NewOption(label, p.ID))
	}
	return options
}

// Setup runs the interactive configuration wizard and saves the result.
func Setup(version string, existing *config.Config) (*config.Config, error) {
	if existing == nil {
		existing = &config.Config{}
	}
	if !Interactive() {
		return nil, errors.New("no interactive terminal available; set the provider's API key env var, or run `clawd setup --no-input` with flags")
	}

	fmt.Fprintln(os.Stderr, banner(version))
	fmt.Fprintln(os.Stderr, dimStyle.Render("Nothing leaves this machine except your prompts to the provider.\n"))

	providerID := existing.Provider
	if _, ok := config.LookupProvider(providerID); !ok {
		providerID = "openrouter"
		if detected, _, found := config.DetectFromEnv(); found {
			providerID = detected.ID
		}
	}

	selectForm := newForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Which provider should power the agent?").
			Description("Any OpenAI-compatible endpoint works. Pick where your key lives.").
			Options(providerOptions()...).
			Height(len(config.Providers()) + 2).
			Value(&providerID),
	))
	if err := selectForm.Run(); err != nil {
		return nil, abort(err)
	}
	provider, _ := config.LookupProvider(providerID)

	baseURL := existing.BaseURL
	if provider.ID == "custom" {
		if baseURL == "" {
			baseURL = os.Getenv(config.EnvPrefix + "_BASE_URL")
		}
		urlForm := newForm(huh.NewGroup(
			huh.NewInput().
				Title("Base URL").
				Description("Must end with /v1, e.g. http://localhost:8000/v1").
				Placeholder("https://api.example.com/v1").
				Value(&baseURL).
				Validate(func(s string) error {
					s = strings.TrimSpace(s)
					if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
						return errors.New("URL must start with http:// or https://")
					}
					return nil
				}),
		))
		if err := urlForm.Run(); err != nil {
			return nil, abort(err)
		}
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	} else {
		baseURL = provider.BaseURL
	}

	key := existing.APIKey
	if key == "" && provider.KeyEnv != "" {
		key = strings.TrimSpace(os.Getenv(provider.KeyEnv))
	}

	var models []string
	for {
		if provider.NeedsKey() {
			keyTitle := "Paste your API key"
			if provider.ID != "custom" {
				keyTitle = "Paste your " + provider.Name + " API key"
			}
			keyHint := "It is stored with 0600 permissions in your config directory."
			if provider.ID == "custom" {
				keyHint = "Used only for " + baseURL
			}
			if provider.KeyURL != "" {
				keyHint = "Get one at " + provider.KeyURL
			}
			if key != "" && provider.KeyEnv != "" && os.Getenv(provider.KeyEnv) != "" {
				keyHint = "Using " + provider.KeyEnv + " from your environment. Press enter to keep it."
			}
			keyForm := newForm(huh.NewGroup(
				huh.NewInput().
					Title(keyTitle).
					Description(keyHint).
					EchoMode(huh.EchoModePassword).
					Value(&key).
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return errors.New("an API key is required")
						}
						return nil
					}),
			))
			if err := keyForm.Run(); err != nil {
				return nil, abort(err)
			}
			key = strings.TrimSpace(key)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		fetched, err := withSpinner("Checking the key and fetching models", func() ([]string, error) {
			return config.ProbeModels(ctx, provider, baseURL, key)
		})
		cancel()

		switch {
		case err == nil:
			models = fetched
		case errors.Is(err, config.ErrInvalidKey):
			fmt.Fprintln(os.Stderr, errStyle.Render("✗ ")+"That key was rejected by "+provider.Name+".")
			key = ""
			continue
		case errors.Is(err, config.ErrModelsUnavailable):
			models = config.FilterModels(provider, nil)
			fmt.Fprintln(os.Stderr, warnStyle.Render("! ")+"Could not verify the key (no /models endpoint), continuing.")
		default:
			models = config.FilterModels(provider, nil)
			fmt.Fprintln(os.Stderr, warnStyle.Render("! ")+"Could not reach the provider: "+err.Error())
			keepGoing := true
			confirm := newForm(huh.NewGroup(
				huh.NewConfirm().
					Title("Continue with this configuration anyway?").
					Affirmative("Yes, save it").
					Negative("Change the key").
					Value(&keepGoing),
			))
			if formErr := confirm.Run(); formErr != nil {
				return nil, abort(formErr)
			}
			if !keepGoing {
				key = ""
				continue
			}
		}
		break
	}

	modelValue := pickModel(existing.Model, provider, models)
	const customModel = "\x00custom"
	if len(models) > 0 {
		modelOptions := make([]huh.Option[string], 0, len(models)+1)
		for _, id := range models {
			modelOptions = append(modelOptions, huh.NewOption(id, id))
		}
		modelOptions = append(modelOptions, huh.NewOption("Other — type a model ID…", customModel))

		modelForm := newForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Which model?").
				Description("Type / to filter. Lists come live from the provider.").
				Options(modelOptions...).
				Height(12).
				Filtering(true).
				Value(&modelValue),
		))
		if err := modelForm.Run(); err != nil {
			return nil, abort(err)
		}
	}
	if modelValue == customModel || modelValue == "" {
		modelValue = ""
		customForm := newForm(huh.NewGroup(
			huh.NewInput().
				Title("Model ID").
				Placeholder("e.g. gpt-4o-mini").
				Value(&modelValue).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("model ID is required")
					}
					return nil
				}),
		))
		if err := customForm.Run(); err != nil {
			return nil, abort(err)
		}
		modelValue = strings.TrimSpace(modelValue)
	}

	cfg := &config.Config{
		Provider:  provider.ID,
		Model:     modelValue,
		APIKey:    key,
		MaxTokens: existing.MaxTokens,
	}
	if provider.ID == "custom" {
		cfg.BaseURL = baseURL
	}
	if err := cfg.Save(); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}

	path, _ := config.Path()
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, okStyle.Render("✓ Saved"))
	fmt.Fprintf(os.Stderr, "  %s %s\n", dimStyle.Render("config "), path)
	fmt.Fprintf(os.Stderr, "  %s %s (%s)\n", dimStyle.Render("provider"), provider.Name, provider.ID)
	fmt.Fprintf(os.Stderr, "  %s %s\n", dimStyle.Render("model   "), cfg.Model)
	fmt.Fprintf(os.Stderr, "  %s %s\n", dimStyle.Render("key     "), config.MaskKey(cfg.APIKey))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  %s clawd \"explain this repository\"\n", dimStyle.Render("try:"))
	fmt.Fprintf(os.Stderr, "  %s clawd setup\n\n", dimStyle.Render("change:"))
	return cfg, nil
}

func pickModel(current string, provider config.Provider, models []string) string {
	has := func(want string) bool {
		for _, id := range models {
			if id == want {
				return true
			}
		}
		return false
	}
	switch {
	case current != "" && has(current):
		return current
	case provider.DefaultModel != "" && has(provider.DefaultModel):
		return provider.DefaultModel
	case len(models) > 0:
		return models[0]
	default:
		return ""
	}
}

func abort(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrCancelled
	}
	return err
}

func withSpinner(label string, fn func() ([]string, error)) ([]string, error) {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		ticker := time.NewTicker(90 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-done:
				fmt.Fprint(os.Stderr, "\r\033[2K")
				return
			case <-ticker.C:
				fmt.Fprintf(os.Stderr, "\r\033[2K  %s %s", brandStyle.Render(frames[i%len(frames)]), dimStyle.Render(label))
				i++
			}
		}
	}()
	result, err := fn()
	close(done)
	<-stopped
	return result, err
}

// PrintConfig summary for `clawd config`.
func PrintConfig(settings *config.Settings) {
	path, _ := config.Path()
	fmt.Printf("%s\n", brandStyle.Render("C-Lawd Code"))
	fmt.Printf("  %s %s\n", dimStyle.Render("config  "), path)
	fmt.Printf("  %s %s (%s)\n", dimStyle.Render("provider"), settings.Provider.Name, settings.Provider.ID)
	fmt.Printf("  %s %s\n", dimStyle.Render("model   "), settings.Model)
	fmt.Printf("  %s %s\n", dimStyle.Render("base url"), settings.BaseURL)
	fmt.Printf("  %s %s\n", dimStyle.Render("key     "), config.MaskKey(settings.APIKey))
	fmt.Printf("  %s %s\n", dimStyle.Render("source  "), settings.Source)
	if settings.Provider.KeyEnv != "" {
		fmt.Printf("\n  %s %s\n", dimStyle.Render("override with"), settings.Provider.KeyEnv+"=... clawd \"...\"")
	}
}
