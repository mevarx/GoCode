package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidatePath ensures that the requested path resolves to a location within
// the workspace root. It converts relative paths to absolute, cleans them,
// evaluates symlinks, and rejects any path that escapes the workspace boundary.
//
// Returns the cleaned absolute path if valid.
func ValidatePath(workspaceRoot, requestedPath string) (string, error) {
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root is not configured")
	}

	if requestedPath == "" {
		return "", fmt.Errorf("path cannot be empty")
	}
	// Treat both slash styles as separators so traversal checks behave the same
	// when paths come from prompts or configuration created on another OS.
	requestedPath = strings.ReplaceAll(requestedPath, "\\", string(filepath.Separator))

	// Ensure workspace root is absolute.
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace root: %w", err)
	}

	// Resolve the requested path relative to the workspace root.
	var absPath string
	if filepath.IsAbs(requestedPath) {
		absPath = filepath.Clean(requestedPath)
	} else {
		absPath = filepath.Clean(filepath.Join(absRoot, requestedPath))
	}

	// First check: the cleaned path must be within the workspace.
	if err := checkPathWithinRoot(absRoot, absPath); err != nil {
		return "", err
	}

	// Evaluate symlinks if the path exists.
	// If the path doesn't exist yet (e.g., for file_write creating a new file),
	// we evaluate the parent directory's symlinks.
	resolvedPath := absPath
	if _, err := os.Lstat(absPath); err == nil {
		resolved, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			return "", fmt.Errorf("failed to resolve symlinks for %s: %w", requestedPath, err)
		}
		resolvedPath = resolved
	} else if os.IsNotExist(err) {
		// Path doesn't exist yet — evaluate parent directory.
		parentDir := filepath.Dir(absPath)
		if _, statErr := os.Stat(parentDir); statErr == nil {
			resolvedParent, evalErr := filepath.EvalSymlinks(parentDir)
			if evalErr != nil {
				return "", fmt.Errorf("failed to resolve symlinks for parent %s: %w", parentDir, evalErr)
			}
			resolvedPath = filepath.Join(resolvedParent, filepath.Base(absPath))
		}
		// If parent doesn't exist either, we'll check the cleaned path directly
		// (file_write will create directories as needed, but we still validate).
	}

	// Second check: the resolved (symlink-resolved) path must also be within workspace.
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		// If workspace root itself can't be resolved, use the original.
		resolvedRoot = absRoot
	}
	if err := checkPathWithinRoot(resolvedRoot, resolvedPath); err != nil {
		return "", fmt.Errorf("symlink %s resolves outside workspace: %w", requestedPath, err)
	}

	return absPath, nil
}

// checkPathWithinRoot verifies that absPath is at or under absRoot.
func checkPathWithinRoot(absRoot, absPath string) error {
	// Normalize for comparison — on Windows, drive letter case may differ.
	normRoot := normalizeForComparison(absRoot)
	normPath := normalizeForComparison(absPath)

	// The path must equal the root or be a child of it.
	if normPath == normRoot {
		return nil
	}

	// Ensure the root ends with a separator for prefix check.
	rootPrefix := normRoot
	if !strings.HasSuffix(rootPrefix, string(filepath.Separator)) {
		rootPrefix += string(filepath.Separator)
	}

	if !strings.HasPrefix(normPath, rootPrefix) {
		return fmt.Errorf("path escapes workspace: %s", absPath)
	}

	return nil
}

// normalizeForComparison returns a path suitable for case-insensitive comparison
// on Windows or exact comparison on other platforms.
func normalizeForComparison(p string) string {
	p = filepath.Clean(p)
	// On Windows, normalize to lowercase for case-insensitive comparison.
	if os.PathSeparator == '\\' {
		p = strings.ToLower(p)
	}
	return p
}
