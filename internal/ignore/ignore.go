package ignore

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// Matcher evaluates whether file paths match ignore patterns.
type Matcher interface {
	IsIgnored(path string, isDir bool) bool
}

// IsCaseInsensitiveFS reports whether the OS resolves filenames case-insensitively by default.
// In GoCode, Windows and macOS are treated as case-insensitive filesystems per project
// platform policy (NTFS and default APFS/HFS+ are case-insensitive).
// Note: While macOS supports case-sensitive APFS volumes as an opt-in format, standard
// installations are case-insensitive, matching Git's core.ignoreCase default.
func IsCaseInsensitiveFS() bool {
	return runtime.GOOS == "windows" || runtime.GOOS == "darwin"
}

type gitIgnoreMatcher struct {
	matcher gitignore.Matcher
	root    string
}

// LoadMatcher searches for .gocodeignore (or .gitignore as fallback) starting from dir.
func LoadMatcher(dir string) Matcher {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}

	ignoreFile, rootDir := findIgnoreFile(absDir)
	if ignoreFile == "" {
		return &noopMatcher{}
	}

	patterns, err := readIgnorePatterns(ignoreFile, rootDir)
	if err != nil || len(patterns) == 0 {
		return &noopMatcher{}
	}

	return &gitIgnoreMatcher{
		matcher: gitignore.NewMatcher(patterns),
		root:    rootDir,
	}
}

func findIgnoreFile(startDir string) (string, string) {
	curr := startDir
	for {
		gocodeIgnore := filepath.Join(curr, ".gocodeignore")
		if _, err := os.Stat(gocodeIgnore); err == nil {
			return gocodeIgnore, curr
		}

		gitIgnore := filepath.Join(curr, ".gitignore")
		if _, err := os.Stat(gitIgnore); err == nil {
			return gitIgnore, curr
		}

		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	return "", ""
}

func readIgnorePatterns(ignoreFilePath, rootDir string) ([]gitignore.Pattern, error) {
	f, err := os.Open(ignoreFilePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var patterns []gitignore.Pattern

	scanner := bufio.NewScanner(f)
	caseFold := IsCaseInsensitiveFS()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if caseFold {
			line = strings.ToLower(line)
		}
		patterns = append(patterns, gitignore.ParsePattern(line, nil))
	}

	return patterns, scanner.Err()
}

func (m *gitIgnoreMatcher) IsIgnored(path string, isDir bool) bool {
	if m.matcher == nil {
		return false
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	relPath, err := filepath.Rel(m.root, absPath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		relPath = filepath.Clean(path)
	}

	slashPath := filepath.ToSlash(relPath)
	slashPath = strings.TrimPrefix(slashPath, "./")
	slashPath = strings.TrimPrefix(slashPath, "/")
	if IsCaseInsensitiveFS() {
		slashPath = strings.ToLower(slashPath)
	}
	parts := strings.Split(slashPath, "/")
	return m.matcher.Match(parts, isDir)
}

type noopMatcher struct{}

func (n *noopMatcher) IsIgnored(path string, isDir bool) bool {
	return false
}
