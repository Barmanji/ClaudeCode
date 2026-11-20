// Package agent implements the tool-calling loop.
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/Barmanji/ClaudeCode/internal/lsp"
	"github.com/Barmanji/ClaudeCode/internal/skills"
	"github.com/openai/openai-go/v3"
)

const (
	// DefaultMaxTokens is used when the config does not specify a limit.
	DefaultMaxTokens = 4096
	// maxIterations bounds tool-call loops so a confused model cannot spin
	// forever on the user's API budget.
	maxIterations = 64
	// maxToolOutput keeps a single tool result from blowing the context window.
	maxToolOutput = 100_000
	// maxSubagentDepth prevents recursive Task tool explosions.
	maxSubagentDepth = 2
)

// Options configures an Agent.
type Options struct {
	Client          openai.Client
	Model           string
	MaxTokens       int
	Skills          []skills.Skill
	LSP             *lsp.State
	EnableSkillTool bool
	EnableTaskTool  bool
	Debug           bool
}

// Usage is cumulative token usage for a run.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
}

// Agent runs prompts against an OpenAI-compatible chat completions API.
type Agent struct {
	opts  Options
	usage Usage
}

// New creates an agent, applying defaults.
func New(opts Options) *Agent {
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = DefaultMaxTokens
	}
	return &Agent{opts: opts}
}

// Usage returns cumulative token usage.
func (a *Agent) Usage() Usage { return a.usage }

// Run executes one user prompt to completion.
func (a *Agent) Run(ctx context.Context, prompt string) (string, error) {
	prompt = skills.ResolvePrompt(prompt, a.opts.Skills)
	return a.runLoop(ctx, prompt, true, true, 0, "")
}

func (a *Agent) runLoop(ctx context.Context, prompt string, allowSkill, allowTask bool, depth int, skillDir string) (string, error) {
	tools := a.getTools(allowSkill, allowTask)
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(a.systemPrompt(allowSkill, depth)),
		openai.UserMessage(prompt),
	}

	for iteration := 0; iteration < maxIterations; iteration++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		resp, err := a.opts.Client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Model:     a.opts.Model,
			Messages:  messages,
			Tools:     tools,
			MaxTokens: openai.Int(int64(a.opts.MaxTokens)),
		})
		if err != nil {
			return "", err
		}
		a.usage.PromptTokens += resp.Usage.PromptTokens
		a.usage.CompletionTokens += resp.Usage.CompletionTokens

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("the model returned no choices")
		}
		message := resp.Choices[0].Message
		messages = append(messages, message.ToParam())
		if len(message.ToolCalls) == 0 {
			return strings.TrimSpace(message.Content), nil
		}

		for _, toolCall := range message.ToolCalls {
			result := a.executeToolCall(ctx, toolCall, allowSkill, allowTask, depth, skillDir)
			if a.opts.LSP != nil {
				feedback, pollErr := a.opts.LSP.PollDiagnostics()
				if pollErr != nil {
					if a.opts.Debug {
						fmt.Fprintf(os.Stderr, "lsp diagnostics: %v\n", pollErr)
					}
				} else if feedback != "" {
					result += "\n\n" + feedback
				}
			}
			messages = append(messages, openai.ToolMessage(truncate(result, maxToolOutput), toolCall.ID))
		}
	}
	return "", fmt.Errorf("stopped after %d tool-call rounds without a final answer", maxIterations)
}

func (a *Agent) systemPrompt(allowSkill bool, depth int) string {
	wd, _ := os.Getwd()
	var b strings.Builder
	b.WriteString("You are C-Lawd Code, a coding agent running in the user's terminal.\n")
	fmt.Fprintf(&b, "Working directory: %s\nPlatform: %s/%s\n", wd, runtime.GOOS, runtime.GOARCH)
	b.WriteString(`
You act by calling tools. Do not describe what you would do when you can do it.
- Read a file before changing it; never invent file contents.
- Keep edits small and consistent with the surrounding code.
- Use Bash to explore (ls, rg, git) and to run builds or tests after changes.
- When a task is done, reply with a short summary of what changed.`)
	if a.opts.LSP != nil {
		b.WriteString("\n- The LSP tool resolves symbol definitions and references. Positions are 1-based.")
	}
	if depth > 0 {
		b.WriteString("\n- You are a subagent: complete the assigned task and report the result concisely.")
	}
	if allowSkill {
		b.WriteString(skills.Catalog(a.opts.Skills))
	}
	return b.String()
}

// FormatError turns API failures into actionable messages.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401:
			return "the API key was rejected (401). Run `clawd setup` to update it."
		case 403:
			return "the API key does not have access to this model (403). Pick another model with `clawd models`."
		case 404:
			return "the model or endpoint was not found (404). Check the model name with `clawd models`."
		case 429:
			return "rate limited by the provider (429). Wait a moment and retry, or switch models."
		default:
			if apiErr.Message != "" {
				return fmt.Sprintf("%s (HTTP %d)", apiErr.Message, apiErr.StatusCode)
			}
		}
	}
	return err.Error()
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	omitted := len(s) - limit
	return s[:limit] + fmt.Sprintf("\n\n… output truncated (%d bytes omitted)", omitted)
}
