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

	// Determine and validate search root.
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

	// Compile search regex.
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

	var results []string
	matchCount := 0
	hitLimit := false

	// Single file search.
	if !stat.IsDir() {
		if c.isBlocked(rootDir, false) {
			return Result{Error: "access denied: file is ignored or sensitive"}, nil
		}
		fileMatches, err := c.searchFile(rootDir, re, a.ContextLines, maxResults-matchCount)
		if err == nil {
			results = append(results, fileMatches...)
			matchCount += len(fileMatches)
		}
	} else {
		// Directory walk.
		err = filepath.WalkDir(rootDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}

			// Check context cancellation.
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

			// Skip common large or hidden directories.
			if d.IsDir() {
				if name == ".git" || name == "node_modules" || name == "vendor" {
					return filepath.SkipDir
				}
				if c.isBlocked(path, true) {
					return filepath.SkipDir
				}
				return nil
			}

			// Check file block rules.
			if c.isBlocked(path, false) {
				return nil
			}

			// Apply glob filter if specified.
			if a.Glob != "" {
				matched, globErr := filepath.Match(a.Glob, name)
				if globErr != nil || !matched {
					return nil
				}
			}

			fileMatches, err := c.searchFile(path, re, a.ContextLines, maxResults-matchCount)
			if err != nil {
				return nil
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

	if err != nil && err != filepath.SkipAll && ctx.Err() == nil {
		return Result{Error: fmt.Sprintf("search failed: %v", err)}, nil
	}

	if len(results) == 0 {
		return Result{Output: fmt.Sprintf("No matches found for %q", a.Query)}, nil
	}

	var out strings.Builder
	out.WriteString(strings.Join(results, "\n"))
	if hitLimit {
		out.WriteString(fmt.Sprintf("\n... (results capped at %d matches)", maxResults))
	}

	return Result{Output: out.String()}, nil
}

func (c *CodeSearchTool) isBlocked(path string, isDir bool) bool {
	if c.SensitiveMatcher != nil {
		if blocked, _ := c.SensitiveMatcher.ShouldBlock(path, isDir); blocked {
			return true
		}
	} else if c.IgnoreMatcher != nil && c.IgnoreMatcher.IsIgnored(path, isDir) {
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

	// Check if binary file: read first 512 bytes for null byte.
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
	// Buffer large lines up to 1MB.
	bufLarge := make([]byte, 64*1024)
	scanner.Buffer(bufLarge, 1024*1024)

	var matchIndices []int
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		allLines = append(allLines, line)
		if re.MatchString(line) {
			matchIndices = append(matchIndices, lineNum-1) // 0-indexed
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
		// Context lines display.
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
