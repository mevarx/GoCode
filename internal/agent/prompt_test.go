package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemPrompt(t *testing.T) {
	prompt1 := BuildSystemPrompt("", "")
	if !strings.Contains(prompt1, "You are GoCode") {
		t.Errorf("expected base prompt, got %s", prompt1)
	}
	if strings.Contains(prompt1, "## Project Context") || strings.Contains(prompt1, "## Global Context") {
		t.Errorf("unexpected headers in base prompt: %s", prompt1)
	}

	prompt2 := BuildSystemPrompt("Project guidelines here", "")
	if !strings.HasPrefix(prompt2, "## Project Context\nProject guidelines here\n\n") {
		t.Errorf("expected project context header at top, got: %s", prompt2)
	}

	prompt3 := BuildSystemPrompt("", "User prefers Go 1.26")
	if !strings.HasSuffix(prompt3, "## Global Context\nUser prefers Go 1.26") {
		t.Errorf("expected global context header at bottom, got: %s", prompt3)
	}

	prompt4 := BuildSystemPrompt("Project guidelines", "User global pref")
	if !strings.HasPrefix(prompt4, "## Project Context\nProject guidelines\n\n") {
		t.Errorf("expected project context at top: %s", prompt4)
	}
	if !strings.HasSuffix(prompt4, "## Global Context\nUser global pref") {
		t.Errorf("expected global context at bottom: %s", prompt4)
	}
}

func TestFindProjectContext_DirectAndAscending(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "pkg", "subpkg")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create subdirs: %v", err)
	}

	content, foundPath, err := FindProjectContext(subDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "" || foundPath != "" {
		t.Errorf("expected empty context, got content=%q, path=%q", content, foundPath)
	}

	rootAgents := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(rootAgents, []byte("Root project instructions"), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	content, foundPath, err = FindProjectContext(subDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "Root project instructions" {
		t.Errorf("expected 'Root project instructions', got %q", content)
	}
	if foundPath != rootAgents {
		t.Errorf("expected path %s, got %s", rootAgents, foundPath)
	}

	docsDir := filepath.Join(tmpDir, "pkg", "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("failed to create docs dir: %v", err)
	}
	docsAgents := filepath.Join(docsDir, "AGENTS.md")
	if err := os.WriteFile(docsAgents, []byte("Docs instructions"), 0o644); err != nil {
		t.Fatalf("failed to write docs/AGENTS.md: %v", err)
	}

	contentPkg, _, _ := FindProjectContext(filepath.Join(tmpDir, "pkg"))
	if contentPkg != "Docs instructions" {
		t.Errorf("expected Docs instructions, got %q", contentPkg)
	}
}
