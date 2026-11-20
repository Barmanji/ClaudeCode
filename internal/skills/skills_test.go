package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, dir, body string) {
	t.Helper()
	path := filepath.Join(root, dir)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSkipsBrokenBundles(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "review", "---\nname: review\ndescription: Review code\n---\nReview $ARGUMENTS")
	writeSkill(t, root, "broken", "no frontmatter here")

	all, err := Load([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "review" {
		t.Fatalf("unexpected skills: %+v", all)
	}
}

func TestResolvePromptExpandsArguments(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "review", "---\nname: review\ndescription: Review code\n---\nReview $ARGUMENTS\nFirst: $0\nDir: $1")

	all, err := Load([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	got := ResolvePrompt("/review src/main.go internal", all)
	for _, want := range []string{"# Skill: review", "Review src/main.go internal", "First: src/main.go", "Dir: internal"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestResolvePromptLeavesPlainTextAlone(t *testing.T) {
	got := ResolvePrompt("explain main.go", nil)
	if got != "explain main.go" {
		t.Fatalf("got %q", got)
	}
}

func TestResolvePromptUnknownSlashCommand(t *testing.T) {
	got := ResolvePrompt("/nope do something", nil)
	if got != "/nope do something" {
		t.Fatalf("got %q", got)
	}
}

func TestCatalogEmpty(t *testing.T) {
	if Catalog(nil) != "" {
		t.Fatal("expected empty catalogue")
	}
}
