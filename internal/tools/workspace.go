package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mevarx/GoCode/internal/ignore"
)

// isCaseInsensitiveFS reports whether the OS resolves filenames case-insensitively.
func isCaseInsensitiveFS() bool {
	return runtime.GOOS == "windows" || runtime.GOOS == "darwin"
}

// rejectNormalisationForms refuses spellings the OS opens differently than named (ADS, trailing space/dot).
// SECURITY: these spellings defeat name-based sensitive matching, so they are rejected outright.
func rejectNormalisationForms(path string) error {
	if strings.Contains(path, "::") {
		return fmt.Errorf("alternate data stream paths are not allowed: %s", path)
	}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == "" || part == "." || part == ".." {
			continue
		}
		if idx := strings.Index(part, ":"); idx >= 0 {
			// A drive letter ("D:") is fine; other colons mark a stream form.
			if !(idx == 1 && len(part) == 2) {
				return fmt.Errorf("alternate data stream paths are not allowed: %s", path)
			}
		}
		if strings.HasSuffix(part, " ") {
			return fmt.Errorf("trailing-space path components are not allowed: %s", path)
		}
		if strings.HasSuffix(part, ".") {
			return fmt.Errorf("trailing-dot path components are not allowed: %s", path)
		}
	}
	return nil
}

// canonicalPathForSensitiveCheck returns the spelling the filesystem will open, case-folded where needed.
func canonicalPathForSensitiveCheck(path string) string {
	p := filepath.Clean(path)
	if isCaseInsensitiveFS() {
		p = strings.ToLower(p)
	}
	return p
}

// CheckSensitiveFile rejects normalisation tricks, then applies ignore and case-folded sensitive checks.
func CheckSensitiveFile(m *ignore.SensitiveMatcher, path string, isDir bool) (bool, string, error) {
	if err := rejectNormalisationForms(path); err != nil {
		return true, "", err
	}
	if m == nil {
		return false, "", nil
	}
	if m.IsIgnored(path, isDir) {
		return true, "file is ignored by ignore rules (.gocodeignore/.gitignore)", nil
	}
	if pattern := m.IsSensitive(canonicalPathForSensitiveCheck(path)); pattern != "" {
		return true, `access denied: matches sensitive file pattern "` + pattern + `"`, nil
	}
	return false, "", nil
}

// SensitiveIdentitySet returns FileInfo of blocked files. SECURITY: hardlink aliases match by identity.
// Callers filter candidates by identity instead of by name.
func SensitiveIdentitySet(root string, m *ignore.SensitiveMatcher) []os.FileInfo {
	var set []os.FileInfo
	if root == "" || m == nil {
		return nil
	}
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		blocked, _, err := CheckSensitiveFile(m, path, false)
		if blocked || err != nil {
			if info, statErr := os.Stat(path); statErr == nil {
				set = append(set, info)
			}
		}
		return nil
	})
	return set
}

// IsBlockedByIdentity reports whether info is SameFile-equal to any entry in set.
func IsBlockedByIdentity(set []os.FileInfo, info os.FileInfo) bool {
	for _, fi := range set {
		if os.SameFile(fi, info) {
			return true
		}
	}
	return false
}

// ValidatePath ensures the requested path resolves inside the workspace root. Returns the absolute path.
func ValidatePath(workspaceRoot, requestedPath string) (string, error) {
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root is not configured")
	}

	if requestedPath == "" {
		return "", fmt.Errorf("path cannot be empty")
	}
	// Treat both slash styles as separators for cross-OS paths.
	requestedPath = strings.ReplaceAll(requestedPath, "\\", string(filepath.Separator))

	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace root: %w", err)
	}

	var absPath string
	if filepath.IsAbs(requestedPath) {
		absPath = filepath.Clean(requestedPath)
	} else {
		absPath = filepath.Clean(filepath.Join(absRoot, requestedPath))
	}

	if err := checkPathWithinRoot(absRoot, absPath); err != nil {
		return "", err
	}

	// SECURITY: reject ADS/trailing space-dot spellings that bypass sensitive checks.
	if err := rejectNormalisationForms(absPath); err != nil {
		return "", err
	}

	// Resolve the nearest existing ancestor so symlinked workspaces stay comparable.
	resolvedPath, err := resolveExistingAncestor(absPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve symlinks for %s: %w", requestedPath, err)
	}
	resolvedRoot, err := resolveExistingAncestor(absRoot)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace root symlinks: %w", err)
	}

	if err := checkPathWithinRoot(resolvedRoot, resolvedPath); err != nil {
		return "", fmt.Errorf("symlink %s resolves outside workspace: %w", requestedPath, err)
	}

	return absPath, nil
}

func resolveExistingAncestor(path string) (string, error) {
	candidate := filepath.Clean(path)
	var missing []string
	for {
		_, err := os.Lstat(candidate)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}

		parent := filepath.Dir(candidate)
		if parent == candidate {
			return filepath.Clean(path), nil
		}
		missing = append(missing, filepath.Base(candidate))
		candidate = parent
	}
}

func checkPathWithinRoot(absRoot, absPath string) error {
	// Normalize for comparison — on Windows, drive letter case may differ.
	normRoot := normalizeForComparison(absRoot)
	normPath := normalizeForComparison(absPath)

	if normPath == normRoot {
		return nil
	}

	rootPrefix := normRoot
	if !strings.HasSuffix(rootPrefix, string(filepath.Separator)) {
		rootPrefix += string(filepath.Separator)
	}

	if !strings.HasPrefix(normPath, rootPrefix) {
		return fmt.Errorf("path escapes workspace: %s", absPath)
	}

	return nil
}

// normalizeForComparison cleans and lowercases on Windows for case-insensitive comparison.
func normalizeForComparison(p string) string {
	p = filepath.Clean(p)
	if os.PathSeparator == '\\' {
		p = strings.ToLower(p)
	}
	return p
}
