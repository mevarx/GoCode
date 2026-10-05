package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mevarx/GoCode/internal/ignore"
)

type FileReadTool struct {
	IgnoreMatcher    ignore.Matcher
	SensitiveMatcher *ignore.SensitiveMatcher
	WorkspaceRoot    string
}

type fileReadArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"` // 1-based line offset
	Limit  int    `json:"limit,omitempty"`  // maximum lines to return
}

func (f *FileReadTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "file_read",
		Description: "Read the contents of a file or list a directory. Returns file content as text with metadata. Supports line-based offset and limit for large files.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["path"],
			"properties": {
				"path": {
					"type": "string",
					"description": "Absolute or relative path to the file to read (relative to workspace root)"
				},
				"offset": {
					"type": "integer",
					"description": "1-based starting line number (default: 1)"
				},
				"limit": {
					"type": "integer",
					"description": "Maximum number of lines to return (default: all)"
				}
			}
		}`),
	}
}

func (f *FileReadTool) RequiresApproval() bool {
	return false
}

func (f *FileReadTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a fileReadArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Error: fmt.Sprintf("invalid arguments: %v", err)}, nil
	}

	if a.Path == "" {
		return Result{Error: "path cannot be empty"}, nil
	}

	var path string
	if f.WorkspaceRoot != "" {
		validatedPath, err := ValidatePath(f.WorkspaceRoot, a.Path)
		if err != nil {
			return Result{Error: err.Error()}, nil
		}
		path = validatedPath
	} else {
		path = filepath.Clean(a.Path)
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Error: fmt.Sprintf("file not found: %s", path)}, nil
		}
		return Result{Error: fmt.Sprintf("cannot access file: %v", err)}, nil
	}

	if f.SensitiveMatcher != nil {
		blocked, reason, err := CheckSensitiveFile(f.SensitiveMatcher, path, info.IsDir())
		if err != nil {
			return Result{Error: err.Error()}, nil
		}
		if blocked {
			return Result{Error: fmt.Sprintf("%s: %s", path, reason)}, nil
		}
		// SECURITY: hardlink aliases share inodes with sensitive files.
		if !info.IsDir() && f.WorkspaceRoot != "" {
			if IsBlockedByIdentity(SensitiveIdentitySet(f.WorkspaceRoot, f.SensitiveMatcher), info) {
				return Result{Error: fmt.Sprintf("%s: access denied: matches a sensitive file (hardlink alias)", path)}, nil
			}
		}
	} else if f.IgnoreMatcher != nil && f.IgnoreMatcher.IsIgnored(path, info.IsDir()) {
		return Result{Error: fmt.Sprintf("file %s is ignored by ignore rules (.gocodeignore/.gitignore)", path)}, nil
	}

	if info.IsDir() {
		return f.readDirectory(path)
	}

	return f.readFile(path, info, a.Offset, a.Limit)
}

func (f *FileReadTool) readDirectory(path string) (Result, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return Result{Error: fmt.Sprintf("cannot read directory: %v", err)}, nil
	}

	var listing []string
	for _, entry := range entries {
		entryPath := filepath.Join(path, entry.Name())

		if f.SensitiveMatcher != nil {
			if blocked, _, err := CheckSensitiveFile(f.SensitiveMatcher, entryPath, entry.IsDir()); blocked || err != nil {
				continue
			}
		} else if f.IgnoreMatcher != nil && f.IgnoreMatcher.IsIgnored(entryPath, entry.IsDir()) {
			continue
		}

		prefix := "📄"
		if entry.IsDir() {
			prefix = "📁"
		}
		listing = append(listing, fmt.Sprintf("%s %s", prefix, entry.Name()))
	}
	return Result{Output: fmt.Sprintf("Directory listing for %s:\n%s", path, joinLines(listing))}, nil
}

const maxReadBytes = 100 * 1024

func (f *FileReadTool) readFile(path string, info os.FileInfo, offset, limit int) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Error: fmt.Sprintf("cannot read file: %v", err)}, nil
	}

	totalBytes := len(data)
	content := string(data)
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// Guard bounds before slicing: a huge or negative limit can overflow endLine and panic.
	startLine := 1
	endLine := totalLines
	truncated := false

	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	if offset > 0 {
		startLine = offset
	}
	if startLine > totalLines {
		startLine = totalLines
	}

	if limit > 0 {
		if limit > totalLines {
			limit = totalLines
		}
		endLine = startLine + limit - 1
		if endLine > totalLines {
			endLine = totalLines
		}
	}

	selectedLines := lines[startLine-1 : endLine]
	selectedContent := strings.Join(selectedLines, "\n")

	if len(selectedContent) > maxReadBytes {
		selectedContent = selectedContent[:maxReadBytes]
		truncated = true
	}
	if endLine < totalLines && limit > 0 {
		truncated = true
	}

	meta := fmt.Sprintf("path: %s | lines: %d-%d of %d | bytes: %d",
		path, startLine, endLine, totalLines, totalBytes)
	if truncated {
		meta += " | truncated: true"
	}

	output := fmt.Sprintf("[%s]\n%s", meta, selectedContent)
	return Result{Output: output}, nil
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}
