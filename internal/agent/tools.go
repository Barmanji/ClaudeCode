package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Barmanji/ClaudeCode/internal/lsp"
	"github.com/Barmanji/ClaudeCode/internal/skills"
	"github.com/openai/openai-go/v3"
)

type writeFileArgs struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

type bashArgs struct {
	Command string `json:"command"`
}

type skillArgs struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type taskArgs struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

func functionTool(name, description string, properties map[string]any, required []string) openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
		Name:        name,
		Description: openai.String(description),
		Parameters: openai.FunctionParameters{
			"type": "object", "properties": properties, "required": required,
		},
	})
}

func (a *Agent) getTools(allowSkill, allowTask bool) []openai.ChatCompletionToolUnionParam {
	tools := []openai.ChatCompletionToolUnionParam{
		functionTool("Read", "Read and return the contents of a file", map[string]any{
			"file_path": map[string]any{"type": "string", "description": "Path to the file to read"},
		}, []string{"file_path"}),
		functionTool("Write", "Write content to a file, creating parent directories if needed", map[string]any{
			"file_path": map[string]any{"type": "string", "description": "Path of the file to write"},
			"content":   map[string]any{"type": "string", "description": "Content to write"},
		}, []string{"file_path", "content"}),
		functionTool("Bash", "Execute a shell command and return its combined output", map[string]any{
			"command": map[string]any{"type": "string", "description": "Shell command"},
		}, []string{"command"}),
	}
	if allowSkill {
		tools = append(tools, functionTool("Skill", "Load and execute a skill by name. Use when relevant.", map[string]any{
			"name":      map[string]any{"type": "string", "description": "Skill name"},
			"arguments": map[string]any{"type": "string", "description": "Arguments to pass to the skill"},
		}, []string{"name"}))
	}
	if allowTask {
		tools = append(tools, functionTool("Task", "Launch a subagent to perform a task independently and return its result.", map[string]any{
			"description": map[string]any{"type": "string", "description": "Short task description"},
			"prompt":      map[string]any{"type": "string", "description": "Instructions for the subagent"},
		}, []string{"description", "prompt"}))
	}
	if a.opts.LSP != nil {
		tools = append(tools, functionTool("LSP", "Returns the file, line, and character where a symbol is defined, or finds references. Line and character are 1-based.", map[string]any{
			"operation": map[string]any{"type": "string", "enum": []string{"goToDefinition", "findReferences"}},
			"filePath":  map[string]any{"type": "string", "description": "Absolute path or path relative to working directory"},
			"line":      map[string]any{"type": "integer", "minimum": 1},
			"character": map[string]any{"type": "integer", "minimum": 1},
		}, []string{"operation", "filePath", "line", "character"}))
	}
	return tools
}

func (a *Agent) executeToolCall(ctx context.Context, toolCall openai.ChatCompletionMessageToolCallUnion, allowSkill, allowTask bool, depth int, skillDir string) string {
	switch toolCall.Function.Name {
	case "Read":
		return executeRead(toolCall.Function.Arguments)
	case "Write":
		return executeWrite(toolCall.Function.Arguments, a.opts.LSP)
	case "Bash":
		return executeBash(toolCall.Function.Arguments, skillDir)
	case "LSP":
		if a.opts.LSP == nil {
			return "LSP is not available in this environment"
		}
		var args lsp.Args
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("Error parsing LSP arguments: %v", err)
		}
		var (
			result string
			err    error
		)
		switch args.Operation {
		case "goToDefinition":
			result, err = a.opts.LSP.GoToDefinition(args)
		case "findReferences":
			result, err = a.opts.LSP.FindReferences(args)
		default:
			return fmt.Sprintf("Unsupported LSP operation: %s", args.Operation)
		}
		if err != nil {
			return fmt.Sprintf("LSP error: %v", err)
		}
		return result
	case "Skill":
		if !allowSkill {
			return "The Skill tool is not available here"
		}
		var args skillArgs
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("Error parsing Skill arguments: %v", err)
		}
		skill, found := skills.Find(a.opts.Skills, args.Name)
		if !found {
			return fmt.Sprintf("Skill not found: %s", args.Name)
		}
		skillPrompt := skills.ReplaceArguments(skill.Body, args.Arguments)
		if skill.Context == "fork" {
			if depth+1 > maxSubagentDepth {
				return "Subagent depth limit reached"
			}
			subagentPrompt := fmt.Sprintf("# Skill: %s\nSkill directory: %s\n\n%s", skill.Name, skill.Directory, skillPrompt)
			result, err := a.runLoop(ctx, subagentPrompt, false, false, depth+1, skill.Directory)
			if err != nil {
				return fmt.Sprintf("Subagent error: %v", FormatError(err))
			}
			return result
		}
		return fmt.Sprintf("Skill loaded: %s\nSkill directory: %s\n\n%s", skill.Name, skill.Directory, skillPrompt)
	case "Task":
		if !allowTask {
			return "The Task tool is not available here"
		}
		var args taskArgs
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("Error parsing Task arguments: %v", err)
		}
		if depth+1 > maxSubagentDepth {
			return "Subagent depth limit reached"
		}
		result, err := a.runLoop(ctx, args.Prompt, false, false, depth+1, "")
		if err != nil {
			return fmt.Sprintf("Subagent error: %v", FormatError(err))
		}
		return fmt.Sprintf("Task completed: %s\n\n%s", args.Description, result)
	default:
		return fmt.Sprintf("Unknown tool: %s", toolCall.Function.Name)
	}
}

func executeRead(argsJSON string) string {
	var args struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("Error parsing arguments: %v", err)
	}
	content, err := os.ReadFile(args.FilePath)
	if err != nil {
		return fmt.Sprintf("Error reading file: %v", err)
	}
	return string(content)
}

func executeWrite(argsJSON string, lspState *lsp.State) string {
	var args writeFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("Error parsing arguments: %v", err)
	}
	if dir := filepath.Dir(args.FilePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Sprintf("Error creating directory: %v", err)
		}
	}
	if err := os.WriteFile(args.FilePath, []byte(args.Content), 0o644); err != nil {
		return fmt.Sprintf("Error writing file: %v", err)
	}
	if lspState != nil {
		if err := lspState.SyncDocument(args.FilePath, args.Content); err != nil {
			return fmt.Sprintf("File written, but LSP synchronization failed: %v", err)
		}
	}
	return "OK"
}

func executeBash(argsJSON, skillDir string) string {
	var args bashArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("Error parsing arguments: %v", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "Error: empty command"
	}
	cmd := shellCommand(args.Command)
	cmd.Env = os.Environ()
	if skillDir != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_SKILL_DIR="+skillDir)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := stdout.String() + stderr.String()
	if err != nil {
		result += fmt.Sprintf("\nCommand failed: %v", err)
	}
	if strings.TrimSpace(result) == "" {
		return "(no output)"
	}
	return result
}

func shellCommand(command string) *exec.Cmd {
	if custom := strings.TrimSpace(os.Getenv("CLAWD_SHELL")); custom != "" {
		return exec.Command(custom, "-c", command)
	}
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", command)
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return exec.Command("bash", "-c", command)
	}
	return exec.Command("sh", "-c", command)
}
