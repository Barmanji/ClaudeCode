package lsp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Args are the tool arguments accepted by the LSP tool.
type Args struct {
	Operation string `json:"operation"`
	FilePath  string `json:"filePath"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

type document struct {
	URI        string
	LanguageID string
	Version    int
}

// Diagnostic is one published LSP diagnostic, with 1-based positions.
type Diagnostic struct {
	URI       string
	Version   *int
	Message   string
	Line      int // zero-based internally
	Character int // zero-based internally
	Severity  int
}

// State tracks open documents and diagnostics for one agent run.
type State struct {
	Client     *Client
	WorkingDir string

	mu        sync.Mutex
	documents map[string]*document

	diagnosticCursor int
	// Current latest diagnostic report per URI.
	diagnostics map[string][]Diagnostic
}

// NewState creates LSP state rooted at the current working directory.
func NewState(client *Client) *State {
	wd, _ := os.Getwd()
	return &State{
		Client:      client,
		WorkingDir:  wd,
		documents:   make(map[string]*document),
		diagnostics: make(map[string][]Diagnostic),
	}
}

func (s *State) languageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py":
		return "python"
	case ".go":
		return "go"
	case ".js":
		return "javascript"
	case ".ts":
		return "typescript"
	}
	return ""
}

func (s *State) absolutePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.WorkingDir, path)
	}
	return filepath.Abs(path)
}

func (s *State) documentURI(path string) (string, error) {
	abs, err := s.absolutePath(path)
	if err != nil {
		return "", err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String(), nil
}

func (s *State) relativePath(uri string) string {
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

func (s *State) openDocument(path, text string) (string, error) {
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
	s.documents[uri] = &document{URI: uri, LanguageID: languageID, Version: 1}
	return uri, nil
}

// SyncDocument must be called only after the disk write succeeded.
func (s *State) SyncDocument(path, text string) error {
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
		s.documents[uri] = &document{URI: uri, LanguageID: languageID, Version: 1}
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

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type location struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

func (s *State) requestPosition(args Args) (string, position, error) {
	if args.Line < 1 || args.Character < 1 {
		return "", position{}, fmt.Errorf("line and character must be 1-based positive values")
	}
	uri, err := s.documentURI(args.FilePath)
	if err != nil {
		return "", position{}, err
	}
	content, err := os.ReadFile(args.FilePath)
	if err != nil {
		abs, absErr := s.absolutePath(args.FilePath)
		if absErr != nil {
			return "", position{}, err
		}
		content, err = os.ReadFile(abs)
		if err != nil {
			return "", position{}, err
		}
	}
	if _, err := s.openDocument(args.FilePath, string(content)); err != nil {
		return "", position{}, err
	}
	return uri, position{Line: args.Line - 1, Character: args.Character - 1}, nil
}

// GoToDefinition resolves the symbol at the 1-based position.
func (s *State) GoToDefinition(args Args) (string, error) {
	uri, pos, err := s.requestPosition(args)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"method": "textDocument/definition",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": pos.Line, "character": pos.Character},
		},
	}
	response, err := s.Client.Call("request", payload)
	if err != nil {
		return "", err
	}
	var loc location
	if err := json.Unmarshal(response, &loc); err != nil {
		return "", fmt.Errorf("parse definition response: %w", err)
	}
	return fmt.Sprintf("Defined in %s:%d:%d", s.relativePath(loc.URI), loc.Range.Start.Line+1, loc.Range.Start.Character+1), nil
}

// FindReferences lists references for the symbol at the 1-based position.
func (s *State) FindReferences(args Args) (string, error) {
	uri, pos, err := s.requestPosition(args)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"method": "textDocument/references",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": pos.Line, "character": pos.Character},
			"context":      map[string]any{"includeDeclaration": true},
		},
	}
	response, err := s.Client.Call("request", payload)
	if err != nil {
		return "", err
	}
	var locations []location
	if err := json.Unmarshal(response, &locations); err != nil {
		return "", fmt.Errorf("parse references response: %w", err)
	}
	if len(locations) == 0 {
		return "No references found", nil
	}
	files := make(map[string][]location)
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
					Start position `json:"start"`
				} `json:"range"`
			} `json:"diagnostics"`
		} `json:"params"`
	} `json:"notifications"`
}

func diagnosticKey(d Diagnostic) string {
	return fmt.Sprintf("%s:%d:%d:%d:%s", d.URI, d.Line, d.Character, d.Severity, d.Message)
}

// PollDiagnostics reads queued publishDiagnostics events. It emits only
// diagnostics that were not present in the previous report for the document.
func (s *State) PollDiagnostics() (string, error) {
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
			current = append(current, Diagnostic{
				URI: p.URI, Version: p.Version, Message: raw.Message,
				Line: raw.Range.Start.Line, Character: raw.Range.Start.Character,
				Severity: raw.Severity,
			})
		}

		previous := make(map[string]bool, len(s.diagnostics[p.URI]))
		for _, d := range s.diagnostics[p.URI] {
			previous[diagnosticKey(d)] = true
		}
		for _, d := range current {
			if !previous[diagnosticKey(d)] {
				newlyReported = append(newlyReported, d)
			}
		}
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
