package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mevarx/GoCode/internal/ignore"
)

// CodeSearchTool searches for patterns across workspace files.
type CodeSearchTool struct {
	IgnoreMatcher    ignore.Matcher
	SensitiveMatcher *ignore.SensitiveMatcher
	WorkspaceRoot    string
}

type codeSearchArgs struct {
	Query         string `json:"query"`
	Path          string `json:"path,omitempty"`
	Glob          string `json:"glob,omitempty"`
	CaseSensitive bool   `json:"case_sensitive,omitempty"`
	MaxResults    int    `json:"max_results,omitempty"`
	ContextLines  int    `json:"context_lines,omitempty"`
}

func (c *CodeSearchTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "code_search",
		Description: "Search for text or regex patterns across files in the workspace. Supports glob filtering, case sensitivity, and context lines.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["query"],
			"properties": {
				"query": {
					"type": "string",
					"description": "The search query (plain text or regular expression)"
				},
				"path": {
					"type": "string",
					"description": "Subdirectory or file to search in (relative to workspace root, defaults to entire workspace)"
				},
				"glob": {
					"type": "string",
					"description": "File glob pattern to filter files, e.g. '*.go' or '*.ts'"
				},
				"case_sensitive": {
					"type": "boolean",
					"description": "Whether the search is case-sensitive (default: false)"
				},
				"max_results": {
					"type": "integer",
					"description": "Maximum number of matching lines to return (default: 50)"
				},
				"context_lines": {
					"type": "integer",
					"description": "Number of context lines to display before and after each match (default: 0)"
				}
			}
		}`),
	}
}

func (c *CodeSearchTool) RequiresApproval() bool {
	return false
}

func (c *CodeSearchTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a codeSearchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Error: fmt.Sprintf("invalid arguments: %v", err)}, nil
	}

	if strings.TrimSpace(a.Query) == "" {
		return Result{Error: "query cannot be empty"}, nil
	}

	maxResults := a.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	}

	searchPath := a.Path
	if searchPath == "" {
		searchPath = "."
	}

	var rootDir string
	if c.WorkspaceRoot != "" {
		validated, err := ValidatePath(c.WorkspaceRoot, searchPath)
		if err != nil {
			return Result{Error: err.Error()}, nil
		}
		rootDir = validated
	} else {
		abs, err := filepath.Abs(searchPath)
		if err != nil {
			return Result{Error: fmt.Sprintf("invalid path: %v", err)}, nil
		}
		rootDir = abs
	}

	pattern := a.Query
	if !a.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		// If query is not a valid regex, treat as literal text.
		literal := regexp.QuoteMeta(a.Query)
		if !a.CaseSensitive {
			literal = "(?i)" + literal
		}
		re, err = regexp.Compile(literal)
		if err != nil {
			return Result{Error: fmt.Sprintf("invalid search pattern: %v", err)}, nil
		}
	}

	stat, err := os.Stat(rootDir)
	if err != nil {
		return Result{Error: fmt.Sprintf("cannot access path: %v", err)}, nil
	}

	// SECURITY: hardlink aliases share inodes with sensitive files and must be skipped by identity.
	var blockedIDs []os.FileInfo
	resolvedRoot, err := resolveExistingAncestor(rootDir)
	if err != nil {
		resolvedRoot = rootDir
	}
	if c.SensitiveMatcher != nil {
		blockedIDs = SensitiveIdentitySet(rootDir, c.SensitiveMatcher)
	}

	// checkFile enforces workspace confinement, sensitive checks, and hardlink-identity blocking.
	checkFile := func(path string) bool {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return false
		}
		if err := checkPathWithinRoot(resolvedRoot, resolved); err != nil {
			return false
		}
		if c.SensitiveMatcher != nil {
			if blocked, _, err := CheckSensitiveFile(c.SensitiveMatcher, resolved, false); blocked || err != nil {
				return false
			}
		}
		if len(blockedIDs) > 0 {
			if info, statErr := os.Stat(path); statErr == nil {
				if IsBlockedByIdentity(blockedIDs, info) {
					return false
				}
			}
		}
		return true
	}

	var results []string
	matchCount := 0
	hitLimit := false

	if !stat.IsDir() {
		if c.isBlocked(rootDir, false) || !checkFile(rootDir) {
			return Result{Error: "access denied: file is ignored or sensitive"}, nil
		}
		fileMatches, err := c.searchFile(rootDir, re, a.ContextLines, maxResults-matchCount)
		if err != nil {
			return Result{Error: fmt.Sprintf("search failed: %s: %v", rootDir, err)}, nil
		}
		results = append(results, fileMatches...)
		matchCount += len(fileMatches)
	} else {
		err = filepath.WalkDir(rootDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return fmt.Errorf("search %s: %w", path, walkErr)
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			if matchCount >= maxResults {
				hitLimit = true
				return filepath.SkipAll
			}

			name := d.Name()

			if d.IsDir() {
				if name == ".git" || name == "node_modules" || name == "vendor" {
					return filepath.SkipDir
				}
				if c.isBlocked(path, true) {
					return filepath.SkipDir
				}
				// SECURITY: directory symlinks are never descended into.
				if d.Type()&os.ModeSymlink != 0 {
					if resolved, err := filepath.EvalSymlinks(path); err != nil || checkPathWithinRoot(resolvedRoot, resolved) != nil {
						return filepath.SkipDir
					}
				}
				return nil
			}

			// SECURITY: enforces resolved-target confinement and hardlink-identity blocking.
			if c.isBlocked(path, false) || !checkFile(path) {
				return nil
			}

			if a.Glob != "" {
				matched, globErr := filepath.Match(a.Glob, name)
				if globErr != nil || !matched {
					return nil
				}
			}

			fileMatches, err := c.searchFile(path, re, a.ContextLines, maxResults-matchCount)
			if err != nil {
				return fmt.Errorf("search %s: %w", path, err)
			}

			results = append(results, fileMatches...)
			matchCount += len(fileMatches)
			if matchCount >= maxResults {
				hitLimit = true
				return filepath.SkipAll
			}

			return nil
		})
	}

	if err != nil && err != filepath.SkipAll {
		return Result{Error: fmt.Sprintf("search failed: %v", err)}, nil
	}

	if len(results) == 0 {
		return Result{Output: fmt.Sprintf("No matches found for %q", a.Query)}, nil
	}

	var out strings.Builder
	truncated := false
	for _, line := range results {
		if out.Len()+len(line)+1 > maxSearchOutputBytes {
			truncated = true
			break
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(line)
	}
	if hitLimit {
		out.WriteString(fmt.Sprintf("\n... (results capped at %d matches)", maxResults))
	}
	if truncated {
		out.WriteString(fmt.Sprintf("\n... (output capped at %d bytes)", maxSearchOutputBytes))
	}

	return Result{Output: out.String()}, nil
}

// maxSearchOutputBytes bounds one result so it cannot dominate the context budget.
const maxSearchOutputBytes = 64 * 1024

func (c *CodeSearchTool) isBlocked(path string, isDir bool) bool {
	if c.SensitiveMatcher != nil {
		blocked, _, err := CheckSensitiveFile(c.SensitiveMatcher, path, isDir)
		return blocked || err != nil
	}
	if c.IgnoreMatcher != nil && c.IgnoreMatcher.IsIgnored(path, isDir) {
		return true
	}
	return false
}

func (c *CodeSearchTool) searchFile(path string, re *regexp.Regexp, contextLines, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if bytes.IndexByte(buf[:n], 0) != -1 {
		return nil, nil // skip binary file
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	displayPath := path
	if c.WorkspaceRoot != "" {
		if rel, err := filepath.Rel(c.WorkspaceRoot, path); err == nil {
			displayPath = filepath.ToSlash(rel)
		}
	}

	var allLines []string
	scanner := bufio.NewScanner(f)
	bufLarge := make([]byte, 64*1024)
	scanner.Buffer(bufLarge, 1024*1024)

	var matchIndices []int
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		allLines = append(allLines, line)
		if re.MatchString(line) {
			matchIndices = append(matchIndices, lineNum-1)
			if len(matchIndices) >= limit {
				break
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(matchIndices) == 0 {
		return nil, nil
	}

	var out []string
	if contextLines <= 0 {
		for _, idx := range matchIndices {
			out = append(out, fmt.Sprintf("%s:%d: %s", displayPath, idx+1, allLines[idx]))
		}
	} else {
		for _, idx := range matchIndices {
			start := idx - contextLines
			if start < 0 {
				start = 0
			}
			end := idx + contextLines
			if end >= len(allLines) {
				end = len(allLines) - 1
			}

			for i := start; i <= end; i++ {
				prefix := " "
				if i == idx {
					prefix = ">"
				}
				out = append(out, fmt.Sprintf("%s:%d:%s %s", displayPath, i+1, prefix, allLines[i]))
			}
			out = append(out, "---")
		}
	}

	return out, nil
}
