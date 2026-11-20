package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Barmanji/ClaudeCode/internal/agent"
	"github.com/Barmanji/ClaudeCode/internal/config"
	"github.com/Barmanji/ClaudeCode/internal/lsp"
	"github.com/Barmanji/ClaudeCode/internal/skills"
	"github.com/Barmanji/ClaudeCode/internal/ui"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `C-Lawd Code — a tiny coding agent for your terminal.

Usage:
  clawd [flags] "prompt"        run one prompt
  clawd setup [flags]           choose provider, key and model
  clawd config [path|clear]     show or edit configuration
  clawd models                  list models available with your key
  clawd version                 print the version

Flags:
  -p string          prompt (positional arguments are joined as the prompt)
  --provider string  override the configured provider
  --model string     override the configured model
  --max-tokens int   response token budget (default 4096)
  --verbose          print token usage

Environment:
  CLAWD_API_KEY, CLAWD_PROVIDER, CLAWD_MODEL, CLAWD_BASE_URL, CLAWD_MAX_TOKENS
  plus each provider's native key variable (OPENAI_API_KEY, OPENROUTER_API_KEY,
  GROQ_API_KEY, DEEPSEEK_API_KEY, MISTRAL_API_KEY, XAI_API_KEY, TOGETHER_API_KEY)

Piped input:
  echo "summarise this repo" | clawd
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "setup":
			return cmdSetup(args[1:])
		case "config":
			return cmdConfig(args[1:])
		case "models":
			return cmdModels()
		case "version", "--version", "-v":
			fmt.Printf("clawd-code %s\n", version)
			return 0
		case "help", "--help", "-h":
			fmt.Print(usage)
			return 0
		}
	}
	return cmdRun(args)
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("clawd", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	var (
		prompt    string
		provider  string
		model     string
		maxTokens int
		verbose   bool
	)
	fs.StringVar(&prompt, "p", "", "prompt")
	fs.StringVar(&provider, "provider", "", "override provider")
	fs.StringVar(&model, "model", "", "override model")
	fs.IntVar(&maxTokens, "max-tokens", 0, "response token budget")
	fs.BoolVar(&verbose, "verbose", false, "print token usage")
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}

	if prompt == "" && fs.NArg() > 0 {
		prompt = strings.Join(fs.Args(), " ")
	}
	if prompt == "" && !ui.Interactive() {
		if data, err := io.ReadAll(os.Stdin); err == nil {
			prompt = strings.TrimSpace(string(data))
		}
	}
	if strings.TrimSpace(prompt) == "" {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	overrides := config.Overrides{
		Provider:  provider,
		Model:     model,
		MaxTokens: maxTokens,
	}
	settings, err := config.ResolveWith(overrides)
	if errors.Is(err, config.ErrNeedsSetup) {
		if !ui.Interactive() {
			fmt.Fprintln(os.Stderr, "No API key configured.")
			fmt.Fprintln(os.Stderr, "Set one of the provider key variables (for example OPENAI_API_KEY)")
			fmt.Fprintln(os.Stderr, "or run `clawd setup` in a terminal.")
			return 1
		}
		if _, setupErr := ui.Setup(version, &config.Config{Provider: provider, Model: model}); setupErr != nil {
			fmt.Fprintln(os.Stderr, "setup failed:", setupErr)
			return 1
		}
		if settings, err = config.ResolveWith(overrides); err != nil {
			fmt.Fprintln(os.Stderr, "configuration error:", err)
			return 1
		}
	} else if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		return 1
	}

	client := newClient(settings)
	loadedSkills, err := skills.Load(skills.DefaultDirs())
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not load skills:", err)
	}

	lspState := connectLSP()

	fmt.Fprintf(os.Stderr, "%s · %s/%s\n",
		dim("clawd-code "+version), settings.Provider.ID, settings.Model)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := agent.New(agent.Options{
		Client:          client,
		Model:           settings.Model,
		MaxTokens:       settings.MaxTokens,
		Skills:          loadedSkills,
		LSP:             lspState,
		EnableSkillTool: true,
		EnableTaskTool:  true,
		Debug:           verbose,
	})
	result, err := runner.Run(ctx, prompt)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "interrupted")
			return 130
		}
		fmt.Fprintln(os.Stderr, "error:", agent.FormatError(err))
		return 1
	}
	fmt.Println(result)
	if verbose {
		usage := runner.Usage()
		fmt.Fprintf(os.Stderr, "%s prompt=%d completion=%d\n", dim("tokens"), usage.PromptTokens, usage.CompletionTokens)
	}
	return 0
}

func newClient(settings *config.Settings) openai.Client {
	opts := []option.RequestOption{
		option.WithAPIKey(settings.APIKey),
		option.WithBaseURL(settings.BaseURL),
	}
	if strings.Contains(settings.BaseURL, "openrouter.ai") {
		opts = append(opts,
			option.WithHeader("HTTP-Referer", "https://github.com/Barmanji/ClaudeCode"),
			option.WithHeader("X-Title", config.AppName),
		)
	}
	return openai.NewClient(opts...)
}

func connectLSP() *lsp.State {
	client, err := lsp.NewClient()
	if err != nil {
		if os.Getenv("CODECRAFTERS_LSP_URL") != "" {
			fmt.Fprintln(os.Stderr, "warning: LSP unavailable:", err)
		}
		return nil
	}
	if _, err := client.Call("configuration", map[string]any{}); err != nil {
		fmt.Fprintln(os.Stderr, "warning: LSP configuration failed:", err)
		return nil
	}
	return lsp.NewState(client)
}

func cmdSetup(args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		provider  string
		model     string
		apiKey    string
		baseURL   string
		maxTokens int
		noInput   bool
	)
	fs.StringVar(&provider, "provider", "", "provider id (openrouter, openai, groq, ...)")
	fs.StringVar(&model, "model", "", "model id")
	fs.StringVar(&apiKey, "api-key", "", "API key (prefer the env var or the wizard)")
	fs.StringVar(&baseURL, "base-url", "", "custom provider base URL")
	fs.IntVar(&maxTokens, "max-tokens", 0, "response token budget")
	fs.BoolVar(&noInput, "no-input", false, "do not prompt; requires --provider and --api-key")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if noInput {
		return saveNonInteractive(provider, model, apiKey, baseURL, maxTokens)
	}

	existing, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	// Flags pre-fill the wizard.
	if provider != "" {
		existing.Provider = provider
	}
	if model != "" {
		existing.Model = model
	}
	if apiKey != "" {
		existing.APIKey = apiKey
	}
	if baseURL != "" {
		existing.BaseURL = baseURL
	}
	if maxTokens > 0 {
		existing.MaxTokens = maxTokens
	}
	if _, err := ui.Setup(version, existing); err != nil {
		if errors.Is(err, ui.ErrCancelled) {
			return 130
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func saveNonInteractive(provider, model, apiKey, baseURL string, maxTokens int) int {
	if provider == "" {
		provider = os.Getenv(config.EnvPrefix + "_PROVIDER")
	}
	if provider == "" {
		fmt.Fprintln(os.Stderr, "error: --no-input requires --provider")
		return 2
	}
	p, ok := config.LookupProvider(provider)
	if !ok {
		fmt.Fprintln(os.Stderr, "error: unknown provider", provider)
		return 2
	}
	if model == "" {
		model = p.DefaultModel
	}
	if apiKey == "" {
		apiKey = os.Getenv(config.EnvPrefix + "_API_KEY")
	}
	if apiKey == "" && p.KeyEnv != "" {
		apiKey = os.Getenv(p.KeyEnv)
	}
	if p.NeedsKey() && apiKey == "" {
		fmt.Fprintf(os.Stderr, "error: no key for %s (set %s or pass --api-key)\n", p.Name, p.KeyEnv)
		return 1
	}
	cfg := &config.Config{
		Provider:  p.ID,
		Model:     model,
		APIKey:    apiKey,
		BaseURL:   baseURL,
		MaxTokens: maxTokens,
	}
	if err := cfg.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	path, _ := config.Path()
	fmt.Printf("Saved %s\n", path)
	return 0
}

func cmdConfig(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "path":
			path, err := config.Path()
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				return 1
			}
			fmt.Println(path)
			return 0
		case "clear":
			if err := config.Clear(); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				return 1
			}
			fmt.Println("Configuration cleared.")
			return 0
		}
	}
	settings, err := config.Resolve()
	if err != nil {
		path, _ := config.Path()
		if errors.Is(err, config.ErrNeedsSetup) {
			fmt.Println("Not configured.")
			fmt.Println("Run `clawd setup` (config file:", path+")")
			return 0
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	ui.PrintConfig(settings)
	return 0
}

func cmdModels() int {
	settings, err := config.Resolve()
	if errors.Is(err, config.ErrNeedsSetup) {
		fmt.Fprintln(os.Stderr, "No API key configured. Run `clawd setup`.")
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := config.ProbeModels(ctx, settings.Provider, settings.BaseURL, settings.APIKey)
	if err != nil && !errors.Is(err, config.ErrModelsUnavailable) {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if len(models) == 0 {
		models = settings.Provider.Models
	}
	for _, id := range models {
		if id == settings.Model {
			fmt.Printf("* %s\n", id)
			continue
		}
		fmt.Printf("  %s\n", id)
	}
	return 0
}

func dim(s string) string {
	if !ui.Interactive() {
		return s
	}
	return "\x1b[2m" + s + "\x1b[0m"
}

// reorderArgs moves known flags in front of positional arguments so
// `clawd "do something" --model x` behaves like `clawd --model x "do something"`.
func reorderArgs(args []string) []string {
	valueFlags := map[string]bool{
		"-p": true, "--p": true,
		"-provider": true, "--provider": true,
		"-model": true, "--model": true,
		"-max-tokens": true, "--max-tokens": true,
	}
	boolFlags := map[string]bool{
		"-verbose": true, "--verbose": true,
	}

	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		name := arg
		if eq := strings.IndexByte(arg, '='); eq >= 0 {
			name = arg[:eq]
		}
		switch {
		case valueFlags[name]:
			flags = append(flags, arg)
			if !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		case boolFlags[name]:
			flags = append(flags, arg)
		default:
			positional = append(positional, arg)
		}
	}
	return append(flags, positional...)
}
