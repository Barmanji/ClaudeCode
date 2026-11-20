// Package skills loads Claude-style SKILL.md bundles.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Barmanji/ClaudeCode/internal/config"
)

// Skill is a loaded SKILL.md bundle.
type Skill struct {
	Name        string
	Description string
	Body        string
	Directory   string
	Context     string
	Agent       string
}

// DefaultDirs returns the skill directories searched, project first.
func DefaultDirs() []string {
	dirs := []string{filepath.Join(".claude", "skills")}
	if dir, err := config.Dir(); err == nil {
		dirs = append(dirs, filepath.Join(dir, "skills"))
	}
	return dirs
}

// Load reads every skill bundle it can find. Broken bundles are skipped so a
// bad SKILL.md never blocks the agent.
func Load(dirs []string) ([]Skill, error) {
	seen := make(map[string]bool)
	var loaded []Skill
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skill, err := readSkillFile(filepath.Join(dir, entry.Name(), "SKILL.md"))
			if err != nil || seen[skill.Name] {
				continue
			}
			seen[skill.Name] = true
			loaded = append(loaded, skill)
		}
	}
	return loaded, nil
}

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

// Find returns the skill with the given name.
func Find(all []Skill, name string) (Skill, bool) {
	for _, skill := range all {
		if skill.Name == name {
			return skill, true
		}
	}
	return Skill{}, false
}

// Catalog is the system-prompt section listing available skills.
func Catalog(all []Skill) string {
	if len(all) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nYou have access to these skills:\n")
	for _, skill := range all {
		fmt.Fprintf(&b, "- %s: %s\n", skill.Name, skill.Description)
	}
	b.WriteString("\nUse the Skill tool when a skill is relevant to the user's request.")
	return b.String()
}

// ReplaceArguments expands $ARGUMENTS, $ARGUMENTS[N] and $N placeholders.
func ReplaceArguments(body, rawArgs string) string {
	args := strings.Fields(rawArgs)
	for i, arg := range args {
		body = strings.ReplaceAll(body, fmt.Sprintf("$ARGUMENTS[%d]", i), arg)
		body = strings.ReplaceAll(body, fmt.Sprintf("$%d", i), arg)
	}
	return strings.ReplaceAll(body, "$ARGUMENTS", rawArgs)
}

// ResolvePrompt expands slash commands such as "/review src/main.go".
func ResolvePrompt(prompt string, all []Skill) string {
	if !strings.HasPrefix(prompt, "/") {
		return prompt
	}
	parts := strings.Fields(prompt)
	var selected []Skill
	var args []string
	for _, part := range parts {
		if strings.HasPrefix(part, "/") {
			if skill, ok := Find(all, strings.TrimPrefix(part, "/")); ok {
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
		b.WriteString(ReplaceArguments(skill.Body, rawArgs))
	}
	return b.String()
}
