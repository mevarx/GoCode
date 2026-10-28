package ignore

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestIgnoreMatcher_CaseInsensitiveOnSupportedOS(t *testing.T) {
	if !IsCaseInsensitiveFS() {
		t.Skip("skipping case-insensitive tests on case-sensitive filesystem")
	}

	tmpDir := t.TempDir()
	ignoreContent := `
secrets/
custom_secret.txt
*.confidential
notes/*
!notes/public.txt
`
	_ = os.WriteFile(filepath.Join(tmpDir, ".gocodeignore"), []byte(ignoreContent), 0o644)
	matcher := LoadMatcher(tmpDir)

	tests := []struct {
		path  string
		isDir bool
		want  bool
		desc  string
	}{
		// Filename variants
		{"custom_secret.txt", false, true, "exact filename"},
		{"CUSTOM_SECRET.TXT", false, true, "uppercase filename"},
		{"Custom_Secret.txt", false, true, "mixed case filename"},

		// Directory variants
		{"secrets", true, true, "exact directory"},
		{"SECRETS", true, true, "uppercase directory"},
		{"Secrets", true, true, "mixed case directory"},
		{"secrets/key.txt", false, true, "file in exact directory"},
		{"SECRETS/key.txt", false, true, "file in uppercase directory"},
		{"Secrets/Key.txt", false, true, "file with mixed case in mixed directory"},

		// Wildcard variants
		{"report.confidential", false, true, "exact wildcard extension"},
		{"REPORT.CONFIDENTIAL", false, true, "uppercase wildcard extension"},
		{"Report.Confidential", false, true, "mixed case wildcard extension"},

		// Negation variants
		{"notes/private.txt", false, true, "ignored under notes/*"},
		{"NOTES/Private.txt", false, true, "ignored under notes/* with cased directory"},
		{"notes/public.txt", false, false, "un-ignored via !notes/public.txt"},
		{"Notes/Public.txt", false, false, "un-ignored via !notes/public.txt with cased path"},
		{"NOTES/PUBLIC.TXT", false, false, "un-ignored via !notes/public.txt all caps"},

		// Non-ignored files
		{"main.go", false, false, "ordinary file"},
		{"MAIN.GO", false, false, "ordinary file cased"},
	}

	for _, tt := range tests {
		targetPath := filepath.Join(tmpDir, filepath.FromSlash(tt.path))
		got := matcher.IsIgnored(targetPath, tt.isDir)
		if got != tt.want {
			t.Errorf("[%s] path %q (isDir=%v): got %v, want %v", tt.desc, tt.path, tt.isDir, got, tt.want)
		}
	}
}

func TestIgnoreMatcher_LinuxCaseSensitive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skipping Linux-only case-sensitive test")
	}

	tmpDir := t.TempDir()
	ignoreContent := "secret.txt\nprivate/\n"
	_ = os.WriteFile(filepath.Join(tmpDir, ".gocodeignore"), []byte(ignoreContent), 0o644)
	matcher := LoadMatcher(tmpDir)

	// On Linux, exact case matches
	if !matcher.IsIgnored(filepath.Join(tmpDir, "secret.txt"), false) {
		t.Error("expected secret.txt to be ignored on Linux")
	}
	if !matcher.IsIgnored(filepath.Join(tmpDir, "private", "data.txt"), false) {
		t.Error("expected private/data.txt to be ignored on Linux")
	}

	// On Linux, altered case does NOT match
	if matcher.IsIgnored(filepath.Join(tmpDir, "SECRET.TXT"), false) {
		t.Error("expected SECRET.TXT NOT to be ignored on Linux")
	}
	if matcher.IsIgnored(filepath.Join(tmpDir, "Private", "data.txt"), false) {
		t.Error("expected Private/data.txt NOT to be ignored on Linux")
	}
}
