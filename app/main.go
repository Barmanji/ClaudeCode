package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// The CodeCrafters-provided lsp_client.go supplies LSPClient and NewLSPClient.
type WriteFileToolArgs struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

type BashToolArgs struct {
	Command string `json:"command"`
}

type SkillToolArgs struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type TaskToolArgs struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

type LSPToolArgs struct {
	Operation string `json:"operation"`
	FilePath  string `json:"filePath"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

type Skill struct {
	Name        string
	Description string
	Body        string
	Directory   string
	Context     string
	Agent       string
}

type LSPDocument struct {
	URI        string
	LanguageID string
	Version    int
}

type Diagnostic struct {
	URI       string
	Version   *int
	Message   string
	Line      int // zero-based internally
	Character int // zero-based internally
	Severity  int
}

type LSPState struct {
	Client     *LSPClient
	WorkingDir string

	mu        sync.Mutex
	documents map[string]*LSPDocument

	diagnosticCursor int
	// Current latest diagnostic report per URI.
	diagnostics map[string][]Diagnostic
}

func newLSPState(client *LSPClient) *LSPState {
	wd, _ := os.Getwd()
	return &LSPState{
		Client:      client,
		WorkingDir:  wd,
		documents:   make(map[string]*LSPDocument),
		diagnostics: make(map[string][]Diagnostic),
	}
}

func (s *LSPState) languageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py":
		return "python"
	case ".go":
		return "go"
	case ".js":
		return "javascript"
	case ".ts":
		return "typescript"
	default:
		return ""
	}
}

func (s *LSPState) absolutePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.WorkingDir, path)
	}
	return filepath.Abs(path)
}

func (s *LSPState) documentURI(path string) (string, error) {
	abs, err := s.absolutePath(path)
	if err != nil {
		return "", err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String(), nil
}

func (s *LSPState) relativePath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	path := filepath.FromSlash(u.Path)
	rel, err := filepath.Rel(s.WorkingDir, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func (s *LSPState) openDocument(path, text string) (string, error) {
	uri, err := s.documentURI(path)
	if err != nil {
		return "", err
	}
	languageID := s.languageID(path)
	if languageID == "" {
		return uri, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.documents[uri]; ok {
		return uri, nil
	}
	payload := map[string]any{
		"method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": uri, "languageId": languageID, "version": 1, "text": text,
		}},
	}
	if _, err := s.Client.Call("notify", payload); err != nil {
		return "", err
	}
	s.documents[uri] = &LSPDocument{URI: uri, LanguageID: languageID, Version: 1}
	return uri, nil
}

// syncDocument must be called only after the disk write succeeded.
func (s *LSPState) syncDocument(path, text string) error {
	uri, err := s.documentURI(path)
	if err != nil {
		return err
	}
	languageID := s.languageID(path)
	if languageID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	doc, opened := s.documents[uri]
	if !opened {
		payload := map[string]any{
			"method": "textDocument/didOpen",
			"params": map[string]any{"textDocument": map[string]any{
				"uri": uri, "languageId": languageID, "version": 1, "text": text,
			}},
		}
		if _, err := s.Client.Call("notify", payload); err != nil {
			return err
		}
		s.documents[uri] = &LSPDocument{URI: uri, LanguageID: languageID, Version: 1}
		return nil
	}

	nextVersion := doc.Version + 1
	payload := map[string]any{
		"method": "textDocument/didChange",
		"params": map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": nextVersion},
			"contentChanges": []any{map[string]any{"text": text}},
		},
	}
	if _, err := s.Client.Call("notify", payload); err != nil {
		return err
	}
	doc.Version = nextVersion
	return nil
}

type LSPPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type LSPRange struct {
	Start LSPPosition `json:"start"`
	End   LSPPosition `json:"end"`
}

type LSPLocation struct {
	URI   string   `json:"uri"`
	Range LSPRange `json:"range"`
}

func (s *LSPState) requestPosition(args LSPToolArgs) (string, LSPPosition, error) {
	if args.Line < 1 || args.Character < 1 {
		return "", LSPPosition{}, fmt.Errorf("line and character must be 1-based positive values")
	}
	uri, err := s.documentURI(args.FilePath)
	if err != nil {
		return "", LSPPosition{}, err
	}
	content, err := os.ReadFile(args.FilePath)
	if err != nil {
		abs, absErr := s.absolutePath(args.FilePath)
		if absErr != nil {
			return "", LSPPosition{}, err
		}
		content, err = os.ReadFile(abs)
		if err != nil {
			return "", LSPPosition{}, err
		}
	}
	if _, err := s.openDocument(args.FilePath, string(content)); err != nil {
		return "", LSPPosition{}, err
	}
	return uri, LSPPosition{Line: args.Line - 1, Character: args.Character - 1}, nil
}

func (s *LSPState) goToDefinition(args LSPToolArgs) (string, error) {
	uri, position, err := s.requestPosition(args)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"method": "textDocument/definition",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": position.Line, "character": position.Character},
		},
	}
	response, err := s.Client.Call("request", payload)
	if err != nil {
		return "", err
	}
	var location LSPLocation
	if err := json.Unmarshal(response, &location); err != nil {
		return "", fmt.Errorf("parse definition response: %w", err)
	}
	return fmt.Sprintf("Defined in %s:%d:%d", s.relativePath(location.URI), location.Range.Start.Line+1, location.Range.Start.Character+1), nil
}

func (s *LSPState) findReferences(args LSPToolArgs) (string, error) {
	uri, position, err := s.requestPosition(args)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"method": "textDocument/references",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": position.Line, "character": position.Character},
			"context":      map[string]any{"includeDeclaration": true},
		},
	}
	response, err := s.Client.Call("request", payload)
	if err != nil {
		return "", err
	}
	var locations []LSPLocation
	if err := json.Unmarshal(response, &locations); err != nil {
		return "", fmt.Errorf("parse references response: %w", err)
	}
	if len(locations) == 0 {
		return "No references found", nil
	}
	files := make(map[string][]LSPLocation)
	order := make([]string, 0)
	for _, loc := range locations {
		path := s.relativePath(loc.URI)
		if _, exists := files[path]; !exists {
			order = append(order, path)
		}
		files[path] = append(files[path], loc)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d references across %d files:\n", len(locations), len(files))
	for _, path := range order {
		fmt.Fprintf(&b, "\n%s:\n", path)
		for _, loc := range files[path] {
			fmt.Fprintf(&b, "  Line %d:%d\n", loc.Range.Start.Line+1, loc.Range.Start.Character+1)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

type diagnosticNotificationBatch struct {
	Cursor        int `json:"cursor"`
	Notifications []struct {
		Method string `json:"method"`
		Params struct {
			URI         string `json:"uri"`
			Version     *int   `json:"version"`
			Diagnostics []struct {
				Message  string `json:"message"`
				Severity int    `json:"severity"`
				Range    struct {
					Start LSPPosition `json:"start"`
				} `json:"range"`
			} `json:"diagnostics"`
		} `json:"params"`
	} `json:"notifications"`
}

func diagnosticKey(d Diagnostic) string {
	return fmt.Sprintf("%s:%d:%d:%d:%s", d.URI, d.Line, d.Character, d.Severity, d.Message)
}

// pollDiagnostics reads queued publishDiagnostics events. It emits only diagnostics
// that were not present in the previous report for the same document.
func (s *LSPState) pollDiagnostics() (string, error) {
	response, err := s.Client.Call("notifications", map[string]any{
		"after": s.diagnosticCursor, "timeoutMs": 0,
	})
	if err != nil {
		return "", err
	}
	var batch diagnosticNotificationBatch
	if err := json.Unmarshal(response, &batch); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diagnosticCursor = batch.Cursor

	var newlyReported []Diagnostic
	for _, notification := range batch.Notifications {
		if notification.Method != "textDocument/publishDiagnostics" {
			continue
		}
		p := notification.Params
		if p.Version != nil {
			if doc, ok := s.documents[p.URI]; !ok || doc.Version != *p.Version {
				continue // stale diagnostic report
			}
		}

		current := make([]Diagnostic, 0, len(p.Diagnostics))
		for _, raw := range p.Diagnostics {
			d := Diagnostic{
				URI: p.URI, Version: p.Version, Message: raw.Message,
				Line: raw.Range.Start.Line, Character: raw.Range.Start.Character,
				Severity: raw.Severity,
			}
			current = append(current, d)
		}

		previous := make(map[string]bool)
		for _, d := range s.diagnostics[p.URI] {
			previous[diagnosticKey(d)] = true
		}
		for _, d := range current {
			if !previous[diagnosticKey(d)] {
				newlyReported = append(newlyReported, d)
			}
		}
		// Replace the latest report, including clearing on an empty array.
		s.diagnostics[p.URI] = current
	}

	if len(newlyReported) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("<new-diagnostics>The following new diagnostic issues were detected:\n\n")
	for _, d := range newlyReported {
		fmt.Fprintf(&b, "%s:\n  [Line %d:%d] %s\n", s.relativePath(d.URI), d.Line+1, d.Character+1, d.Message)
	}
	b.WriteString("</new-diagnostics>")
	return b.String(), nil
}

// ------------------------------------------------------------
// Skill loading and prompt handling
// ------------------------------------------------------------

func readSkillFile(path string) (Skill, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	text := string(content)
	if !strings.HasPrefix(text, "---") {
		return Skill{}, fmt.Errorf("missing frontmatter in %s", path)
	}
	rest := text[3:]
	closingIndex := strings.Index(rest, "---")
	if closingIndex == -1 {
		return Skill{}, fmt.Errorf("missing closing frontmatter in %s", path)
	}
	frontmatter := strings.TrimSpace(rest[:closingIndex])
	body := strings.TrimSpace(rest[closingIndex+3:])
	var skill Skill
	for _, line := range strings.Split(frontmatter, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			skill.Name = value
		case "description":
			skill.Description = value
		case "context":
			skill.Context = value
		case "agent":
			skill.Agent = value
		}
	}
	skill.Body = body
	skill.Directory = filepath.Dir(path)
	if skill.Name == "" {
		return Skill{}, fmt.Errorf("missing skill name in %s", path)
	}
	return skill, nil
}

func loadSkills() ([]Skill, error) {
	const skillsDir = ".claude/skills"
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Skill{}, nil
		}
		return nil, err
	}
	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skill, err := readSkillFile(filepath.Join(skillsDir, entry.Name(), "SKILL.md"))
		if err == nil {
			skills = append(skills, skill)
		}
	}
	return skills, nil
}

func findSkill(skills []Skill, name string) (Skill, bool) {
	for _, skill := range skills {
		if skill.Name == name {
			return skill, true
		}
	}
	return Skill{}, false
}

func buildSkillSystemPrompt(skills []Skill) string {
	var b strings.Builder
	b.WriteString("You are a coding assistant. You have access to these skills:\n\n")
	for _, skill := range skills {
		fmt.Fprintf(&b, "- %s: %s\n", skill.Name, skill.Description)
	}
	b.WriteString("\nUse the Skill tool when a skill is relevant to the user's request.")
	return b.String()
}

func replaceSkillArguments(body, rawArgs string) string {
	args := strings.Fields(rawArgs)
	for i, arg := range args {
		body = strings.ReplaceAll(body, fmt.Sprintf("$ARGUMENTS[%d]", i), arg)
		body = strings.ReplaceAll(body, fmt.Sprintf("$%d", i), arg)
	}
	return strings.ReplaceAll(body, "$ARGUMENTS", rawArgs)
}

func resolveSkillPrompt(prompt string, skills []Skill) string {
	if !strings.HasPrefix(prompt, "/") {
		return prompt
	}
	parts := strings.Fields(prompt)
	var selected []Skill
	var args []string
	for _, part := range parts {
		if strings.HasPrefix(part, "/") {
			if skill, ok := findSkill(skills, strings.TrimPrefix(part, "/")); ok {
				selected = append(selected, skill)
				continue
			}
		}
		args = append(args, part)
	}
	if len(selected) == 0 {
		return prompt
	}
	rawArgs := strings.Join(args, " ")
	var b strings.Builder
	for _, skill := range selected {
		fmt.Fprintf(&b, "\n\n# Skill: %s\nSkill directory: %s\n\n", skill.Name, skill.Directory)
		b.WriteString(replaceSkillArguments(skill.Body, rawArgs))
	}
	return b.String()
}

// ------------------------------------------------------------
// Tool definitions
// ------------------------------------------------------------

func functionTool(name, description string, properties map[string]any, required []string) openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
		Name:        name,
		Description: openai.String(description),
		Parameters: openai.FunctionParameters{
			"type": "object", "properties": properties, "required": required,
		},
	})
}

func getTools(includeSkillTool, includeTaskTool bool, includeLSP bool) []openai.ChatCompletionToolUnionParam {
	tools := []openai.ChatCompletionToolUnionParam{
		functionTool("Read", "Read and return the contents of a file", map[string]any{
			"file_path": map[string]any{"type": "string", "description": "Path to the file to read"},
		}, []string{"file_path"}),
		functionTool("Write", "Write content to a file", map[string]any{
			"file_path": map[string]any{"type": "string", "description": "Path of the file to write"},
			"content":   map[string]any{"type": "string", "description": "Content to write"},
		}, []string{"file_path", "content"}),
		functionTool("Bash", "Execute a shell command", map[string]any{
			"command": map[string]any{"type": "string", "description": "Shell command"},
		}, []string{"command"}),
	}
	if includeSkillTool {
		tools = append(tools, functionTool("Skill", "Load and execute a skill by name. Use when relevant.", map[string]any{
			"name":      map[string]any{"type": "string", "description": "Skill name"},
			"arguments": map[string]any{"type": "string", "description": "Arguments to pass to the skill"},
		}, []string{"name"}))
	}
	if includeTaskTool {
		tools = append(tools, functionTool("Task", "Launch a subagent to perform a task independently and return its result.", map[string]any{
			"description": map[string]any{"type": "string", "description": "Short task description"},
			"prompt":      map[string]any{"type": "string", "description": "Instructions for the subagent"},
		}, []string{"description", "prompt"}))
	}
	if includeLSP {
		tools = append(tools, functionTool("LSP", "Returns the file, line, and character where a symbol is defined, or finds references. Line and character are 1-based.", map[string]any{
			"operation": map[string]any{"type": "string", "enum": []string{"goToDefinition", "findReferences"}},
			"filePath":  map[string]any{"type": "string", "description": "Absolute path or path relative to working directory"},
			"line":      map[string]any{"type": "integer", "minimum": 1},
			"character": map[string]any{"type": "integer", "minimum": 1},
		}, []string{"operation", "filePath", "line", "character"}))
	}
	return tools
}

// ------------------------------------------------------------
// Tool execution
// ------------------------------------------------------------

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

func executeWrite(argsJSON string, lsp *LSPState) string {
	var args WriteFileToolArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("Error parsing arguments: %v", err)
	}
	if err := os.WriteFile(args.FilePath, []byte(args.Content), 0644); err != nil {
		return fmt.Sprintf("Error writing file: %v", err)
	}
	if lsp != nil {
		if err := lsp.syncDocument(args.FilePath, args.Content); err != nil {
			return fmt.Sprintf("File written, but LSP synchronization failed: %v", err)
		}
	}
	return "OK"
}

func executeBash(argsJSON, skillDir string) string {
	var args BashToolArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("Error parsing arguments: %v", err)
	}
	cmd := exec.Command("bash", "-c", args.Command)
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
	return result
}

// ------------------------------------------------------------
// Agent loop
// ------------------------------------------------------------

func runAgent(ctx context.Context, client openai.Client, initialPrompt string, skills []Skill, includeSkillTool, includeTaskTool bool, skillDir string, lsp *LSPState) (string, error) {
	systemPrompt := buildSkillSystemPrompt(skills)
	if lsp != nil {
		systemPrompt += "\n\nUse the LSP tool for symbol definitions and references. LSP positions are 1-based."
	}
	messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(systemPrompt), openai.UserMessage(initialPrompt)}
	tools := getTools(includeSkillTool, includeTaskTool, lsp != nil)

	for {
		resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Model: "anthropic/claude-haiku-4.5", Messages: messages, Tools: tools, MaxTokens: openai.Int(2048),
		})
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("LLM returned no choices")
		}
		message := resp.Choices[0].Message
		messages = append(messages, message.ToParam())
		if len(message.ToolCalls) == 0 {
			return message.Content, nil
		}

		for _, toolCall := range message.ToolCalls {
			var result string
			switch toolCall.Function.Name {
			case "Read":
				result = executeRead(toolCall.Function.Arguments)
			case "Write":
				result = executeWrite(toolCall.Function.Arguments, lsp)
			case "Bash":
				result = executeBash(toolCall.Function.Arguments, skillDir)
			case "LSP":
				if lsp == nil {
					result = "LSP is not implemented yet"
					break
				}
				var args LSPToolArgs
				if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
					result = fmt.Sprintf("Error parsing LSP arguments: %v", err)
					break
				}
				switch args.Operation {
				case "goToDefinition":
					result, err = lsp.goToDefinition(args)
				case "findReferences":
					result, err = lsp.findReferences(args)
				default:
					result = fmt.Sprintf("Unsupported LSP operation: %s", args.Operation)
				}
				if err != nil {
					result = fmt.Sprintf("LSP error: %v", err)
				}
			case "Skill":
				var args SkillToolArgs
				if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
					result = fmt.Sprintf("Error parsing Skill arguments: %v", err)
					break
				}
				skill, found := findSkill(skills, args.Name)
				if !found {
					result = fmt.Sprintf("Skill not found: %s", args.Name)
					break
				}
				skillPrompt := replaceSkillArguments(skill.Body, args.Arguments)
				if skill.Context == "fork" {
					subagentPrompt := fmt.Sprintf("# Skill: %s\nSkill directory: %s\n\n%s", skill.Name, skill.Directory, skillPrompt)
					subagentResult, subErr := runAgent(ctx, client, subagentPrompt, []Skill{}, false, false, skill.Directory, nil)
					if subErr != nil {
						result = fmt.Sprintf("Subagent error: %v", subErr)
					} else {
						result = subagentResult
					}
				} else {
					result = fmt.Sprintf("Skill loaded: %s\nSkill directory: %s\n\n%s", skill.Name, skill.Directory, skillPrompt)
				}
			case "Task":
				var args TaskToolArgs
				if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
					result = fmt.Sprintf("Error parsing Task arguments: %v", err)
					break
				}
				subagentResult, subErr := runAgent(ctx, client, args.Prompt, []Skill{}, false, false, "", nil)
				if subErr != nil {
					result = fmt.Sprintf("Subagent error: %v", subErr)
				} else {
					result = fmt.Sprintf("Task completed: %s\n\n%s", args.Description, subagentResult)
				}
			default:
				result = fmt.Sprintf("Unknown tool: %s", toolCall.Function.Name)
			}
			// Read diagnostics before the next model request and attach them
			// to the latest tool result instead of sending a separate user message.
			if lsp != nil {
				feedback, pollErr := lsp.pollDiagnostics()
				if pollErr != nil {
					fmt.Fprintf(os.Stderr, "Error polling LSP diagnostics: %v\n", pollErr)
				} else if feedback != "" {
					result += "\n\n" + feedback
				}
			}

			messages = append(messages, openai.ToolMessage(result, toolCall.ID))
		}
	}
}

func main() {
	var prompt string
	flag.StringVar(&prompt, "p", "", "Prompt to send to LLM")
	flag.Parse()
	if prompt == "" {
		panic("Prompt must not be empty")
	}

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	baseURL := os.Getenv("OPENROUTER_BASE_URL")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	if apiKey == "" {
		panic("Env variable OPENROUTER_API_KEY not found")
	}

	skills, err := loadSkills()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading skills: %v\n", err)
		os.Exit(1)
	}
	prompt = resolveSkillPrompt(prompt, skills)

	client := openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL))

	var lsp *LSPState

	lspClient, lspErr := NewLSPClient()
	if lspErr == nil && lspClient != nil {
		config, configErr := lspClient.Call("configuration", map[string]any{})
		if configErr != nil {
			fmt.Fprintf(os.Stderr, "Failed to configure LSP: %v\n", configErr)
			os.Exit(1)
		}

		_ = config // We'll parse the configuration when implementing language mapping.

		lsp = newLSPState(lspClient)
		fmt.Fprintln(os.Stderr, "LSP client connected")
	} else if os.Getenv("CODECRAFTERS_LSP_URL") != "" {
		fmt.Fprintf(os.Stderr, "Failed to initialize LSP: %v\n", lspErr)
		os.Exit(1)
	}

	result, err := runAgent(context.Background(), client, prompt, skills, true, true, "", lsp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(result)
}
