package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoreMatcher_GocodeIgnorePreference(t *testing.T) {
	tmpDir := t.TempDir()

	_ = os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("*.log\n"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, ".gocodeignore"), []byte("*.secret\nbuild/\n"), 0o644)

	matcher := LoadMatcher(tmpDir)

	if !matcher.IsIgnored(filepath.Join(tmpDir, "passwords.secret"), false) {
		t.Errorf("expected passwords.secret to be ignored by .gocodeignore")
	}

	if !matcher.IsIgnored(filepath.Join(tmpDir, "build"), true) {
		t.Errorf("expected build/ dir to be ignored")
	}

	if matcher.IsIgnored(filepath.Join(tmpDir, "main.go"), false) {
		t.Errorf("expected main.go NOT to be ignored")
	}
}

func TestIgnoreMatcher_GitignoreFallback(t *testing.T) {
	tmpDir := t.TempDir()

	_ = os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("dist/\n*.tmp\n"), 0o644)

	matcher := LoadMatcher(tmpDir)

	if !matcher.IsIgnored(filepath.Join(tmpDir, "temp.tmp"), false) {
		t.Errorf("expected temp.tmp to be ignored by fallback .gitignore")
	}

	if !matcher.IsIgnored(filepath.Join(tmpDir, "dist"), true) {
		t.Errorf("expected dist/ to be ignored by fallback .gitignore")
	}

	if matcher.IsIgnored(filepath.Join(tmpDir, "file.go"), false) {
		t.Errorf("expected file.go NOT to be ignored")
	}
}

func TestIgnoreMatcher_NoIgnoreFile(t *testing.T) {
	tmpDir := t.TempDir()
	matcher := LoadMatcher(tmpDir)

	if matcher.IsIgnored(filepath.Join(tmpDir, "anything.txt"), false) {
		t.Errorf("expected no files to be ignored when no ignore file is present")
	}
}
