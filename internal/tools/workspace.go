package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mevarx/GoCode/internal/ignore"
)

// sensitiveIdentityCacheTTL bounds how long a cached hardlink identity set may
// be served. Writes invalidate explicitly; the TTL covers changes GoCode did
// not make (external processes creating or removing hardlinks).
const sensitiveIdentityCacheTTL = 30 * time.Second

// externalSensitiveRelPaths are credential files outside the workspace whose
// identities are probed so a hardlink brought into the workspace is still
// recognised as sensitive. Relative to the user's home directory.
var externalSensitiveRelPaths = []string{
	".ssh/id_rsa",
	".ssh/id_ed25519",
	".aws/credentials",
	".kube/config",
	".netrc",
	".npmrc",
}

// userHomeDir is a seam so tests can probe a synthetic home directory
// deterministically instead of the real one.
var userHomeDir = os.UserHomeDir

// sensitiveIdentityWalks counts workspace walks actually performed. Test-only
// observability for the identity cache; never read by production code.
var sensitiveIdentityWalks atomic.Int64

type sensitiveIdentityCacheKey struct {
	root    string
	matcher *ignore.SensitiveMatcher
}

type sensitiveIdentityCacheEntry struct {
	set       []os.FileInfo
	expiresAt time.Time
}

var sensitiveIdentityCache = struct {
	mu      sync.Mutex
	entries map[sensitiveIdentityCacheKey]sensitiveIdentityCacheEntry
}{entries: make(map[sensitiveIdentityCacheKey]sensitiveIdentityCacheEntry)}

// isCaseInsensitiveFS reports whether the OS resolves filenames case-insensitively.
func isCaseInsensitiveFS() bool {
	return ignore.IsCaseInsensitiveFS()
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
//
// The set is cached per (normalized root, matcher) for sensitiveIdentityCacheTTL
// and MUST be treated as read-only: the returned slice may be shared with other
// callers, so callers must never mutate or append to it. Invalidate the cache
// with InvalidateSensitiveIdentityCache after modifying the workspace.
func SensitiveIdentitySet(root string, m *ignore.SensitiveMatcher) []os.FileInfo {
	if root == "" || m == nil {
		return nil
	}
	key := sensitiveIdentityCacheKey{root: normalizeIdentityRoot(root), matcher: m}

	sensitiveIdentityCache.mu.Lock()
	defer sensitiveIdentityCache.mu.Unlock()

	if entry, ok := sensitiveIdentityCache.entries[key]; ok && time.Now().Before(entry.expiresAt) {
		return entry.set
	}

	set := walkSensitiveIdentities(key.root, m)
	set = append(set, externalSensitiveFileInfos()...)
	now := time.Now()
	for k, e := range sensitiveIdentityCache.entries {
		if now.After(e.expiresAt) {
			delete(sensitiveIdentityCache.entries, k)
		}
	}
	sensitiveIdentityCache.entries[key] = sensitiveIdentityCacheEntry{
		set:       set,
		expiresAt: now.Add(sensitiveIdentityCacheTTL),
	}
	return set
}

// InvalidateSensitiveIdentityCache drops cached identity sets for root. It is
// called after a successful file_write/file_patch so approvals and subsequent
// checks never rely on pre-write identities for the TTL window.
func InvalidateSensitiveIdentityCache(root string) {
	if root == "" {
		return
	}
	normalized := normalizeIdentityRoot(root)
	sensitiveIdentityCache.mu.Lock()
	defer sensitiveIdentityCache.mu.Unlock()
	for key := range sensitiveIdentityCache.entries {
		if key.root == normalized {
			delete(sensitiveIdentityCache.entries, key)
		}
	}
}

// normalizeIdentityRoot keys the cache by the absolute, cleaned root so the
// same workspace spelled differently does not produce a cross-root hit.
func normalizeIdentityRoot(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return filepath.Clean(root)
	}
	return filepath.Clean(abs)
}

// walkSensitiveIdentities walks the workspace and collects sensitive file identities.
func walkSensitiveIdentities(root string, m *ignore.SensitiveMatcher) []os.FileInfo {
	sensitiveIdentityWalks.Add(1)
	var set []os.FileInfo
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

// externalSensitiveFileInfos returns FileInfos for a fixed list of credential
// files outside the workspace. SECURITY: a hardlink to one of these planted in
// the workspace shares the inode, so the identity check blocks it even though
// the in-workspace walk cannot see the original path.
func externalSensitiveFileInfos() []os.FileInfo {
	home, err := userHomeDir()
	if err != nil || home == "" {
		return nil
	}
	var infos []os.FileInfo
	for _, rel := range externalSensitiveRelPaths {
		info, statErr := os.Stat(filepath.Join(home, filepath.FromSlash(rel)))
		if statErr != nil || info.IsDir() {
			continue
		}
		infos = append(infos, info)
	}
	return infos
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
