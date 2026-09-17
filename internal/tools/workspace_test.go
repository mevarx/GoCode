package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePath_NormalRelativePaths(t *testing.T) {
	root := t.TempDir()
	subDir := filepath.Join(root, "src")
	os.MkdirAll(subDir, 0o755)
	os.WriteFile(filepath.Join(subDir, "main.go"), []byte("package main"), 0o644)

	tests := []struct {
		name string
		path string
	}{
		{"simple file", "src/main.go"},
		{"directory", "src"},
		{"root itself", "."},
		{"root dot-slash", "./"},
		{"nested relative", "src/../src/main.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ValidatePath(root, tt.path)
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			if !filepath.IsAbs(result) {
				t.Errorf("expected absolute path, got %q", result)
			}
		})
	}
}

func TestValidatePath_AbsolutePathInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	subFile := filepath.Join(root, "file.txt")
	os.WriteFile(subFile, []byte("content"), 0o644)

	result, err := ValidatePath(root, subFile)
	if err != nil {
		t.Fatalf("expected no error for abs path inside workspace, got: %v", err)
	}
	if result != subFile {
		t.Errorf("expected %q, got %q", subFile, result)
	}
}

func TestValidatePath_TraversalAttempt(t *testing.T) {
	root := t.TempDir()

	traversalPaths := []string{
		"../../../etc/passwd",
		"src/../../..",
		"../sibling/file.txt",
		"..\\..\\..\\windows\\system32",
	}

	for _, p := range traversalPaths {
		t.Run(p, func(t *testing.T) {
			_, err := ValidatePath(root, p)
			if err == nil {
				t.Fatalf("expected error for traversal path %q, got nil", p)
			}
			if !strings.Contains(err.Error(), "escapes workspace") {
				t.Errorf("expected 'escapes workspace' error, got: %v", err)
			}
		})
	}
}

func TestValidatePath_AbsolutePathOutsideWorkspace(t *testing.T) {
	root := t.TempDir()

	// Create a separate directory outside the workspace.
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0o644)

	_, err := ValidatePath(root, outsideFile)
	if err == nil {
		t.Fatal("expected error for absolute path outside workspace, got nil")
	}
	if !strings.Contains(err.Error(), "escapes workspace") {
		t.Errorf("expected 'escapes workspace' error, got: %v", err)
	}
}

func TestValidatePath_SymlinkEscape(t *testing.T) {
	root := t.TempDir()

	// Create a directory outside the workspace.
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	os.WriteFile(outsideFile, []byte("secret data"), 0o644)

	// Create a symlink inside the workspace pointing outside.
	symlinkPath := filepath.Join(root, "escape_link")
	err := os.Symlink(outsideDir, symlinkPath)
	if err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}

	_, err = ValidatePath(root, "escape_link/secret.txt")
	if err == nil {
		t.Fatal("expected error for symlink escaping workspace, got nil")
	}
	if !strings.Contains(err.Error(), "resolves outside workspace") && !strings.Contains(err.Error(), "escapes workspace") {
		t.Errorf("expected symlink escape error, got: %v", err)
	}
}

func TestValidatePath_SymlinkInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	os.MkdirAll(realDir, 0o755)
	os.WriteFile(filepath.Join(realDir, "file.txt"), []byte("content"), 0o644)

	// Create a symlink inside workspace pointing to another place inside workspace.
	symlinkPath := filepath.Join(root, "linked")
	err := os.Symlink(realDir, symlinkPath)
	if err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}

	result, err := ValidatePath(root, "linked/file.txt")
	if err != nil {
		t.Fatalf("expected no error for symlink within workspace, got: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestValidatePath_EmptyPath(t *testing.T) {
	root := t.TempDir()
	_, err := ValidatePath(root, "")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestValidatePath_EmptyWorkspaceRoot(t *testing.T) {
	_, err := ValidatePath("", "some/file.txt")
	if err == nil {
		t.Fatal("expected error for empty workspace root")
	}
}

func TestValidatePath_WorkspaceRootItself(t *testing.T) {
	root := t.TempDir()
	result, err := ValidatePath(root, root)
	if err != nil {
		t.Fatalf("expected workspace root itself to be allowed, got: %v", err)
	}
	// Normalize both paths for comparison.
	absRoot, _ := filepath.Abs(root)
	if filepath.Clean(result) != filepath.Clean(absRoot) {
		t.Errorf("expected %q, got %q", absRoot, result)
	}
}

func TestValidatePath_NewFileInWorkspace(t *testing.T) {
	root := t.TempDir()

	// This file doesn't exist yet — should still validate.
	result, err := ValidatePath(root, "new_subdir/new_file.txt")
	if err != nil {
		t.Fatalf("expected no error for new file path, got: %v", err)
	}
	expected := filepath.Join(root, "new_subdir", "new_file.txt")
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}
