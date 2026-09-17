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

type FileWriteTool struct {
	IgnoreMatcher    ignore.Matcher
	SensitiveMatcher *ignore.SensitiveMatcher
	WorkspaceRoot    string
	OnModified       func(path string)
}

type fileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (f *FileWriteTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "file_write",
		Description: "Write content to a file. Creates the file if it doesn't exist, or overwrites it if it does. The user will see a diff of the changes before approving.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["path", "content"],
			"properties": {
				"path": {
					"type": "string",
					"description": "Absolute or relative path to the file to write (relative to workspace root)"
				},
				"content": {
					"type": "string",
					"description": "The complete content to write to the file"
				}
			}
		}`),
	}
}

func (f *FileWriteTool) RequiresApproval() bool {
	return true
}

// Preview generates a diff preview of the proposed change WITHOUT modifying
// any files. This is called before approval.
func (f *FileWriteTool) Preview(ctx context.Context, args json.RawMessage) (Preview, error) {
	a, path, err := f.validateArgs(args)
	if err != nil {
		return Preview{}, err
	}

	var oldContent string
	existingData, readErr := os.ReadFile(path)
	if readErr == nil {
		oldContent = string(existingData)
	}

	diff := generateUnifiedDiff(path, oldContent, a.Content)

	return Preview{
		Description: fmt.Sprintf("Write %d bytes to %s", len(a.Content), path),
		Diff:        diff,
	}, nil
}

// Execute writes the file. This is called ONLY after approval.
func (f *FileWriteTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	a, path, err := f.validateArgs(args)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}

	var oldContent string
	existingData, readErr := os.ReadFile(path)
	if readErr == nil {
		oldContent = string(existingData)
	}

	diff := generateUnifiedDiff(path, oldContent, a.Content)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{Error: fmt.Sprintf("cannot create directory %s: %v", dir, err)}, nil
	}

	if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil {
		return Result{Error: fmt.Sprintf("cannot write file: %v", err)}, nil
	}

	if f.OnModified != nil {
		f.OnModified(path)
	}

	return Result{
		Output: fmt.Sprintf("Successfully wrote %d bytes to %s", len(a.Content), path),
		Diff:   diff,
	}, nil
}

func (f *FileWriteTool) validateArgs(args json.RawMessage) (fileWriteArgs, string, error) {
	var a fileWriteArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return a, "", fmt.Errorf("invalid arguments: %v", err)
	}

	if a.Path == "" {
		return a, "", fmt.Errorf("path cannot be empty")
	}

	// Workspace confinement.
	path := a.Path
	if f.WorkspaceRoot != "" {
		validatedPath, err := ValidatePath(f.WorkspaceRoot, a.Path)
		if err != nil {
			return a, "", err
		}
		path = validatedPath
	} else {
		path = filepath.Clean(a.Path)
	}

	// Check sensitive file protection.
	if f.SensitiveMatcher != nil {
		if blocked, reason := f.SensitiveMatcher.ShouldBlock(path, false); blocked {
			return a, "", fmt.Errorf("%s: %s", path, reason)
		}
	} else if f.IgnoreMatcher != nil && f.IgnoreMatcher.IsIgnored(path, false) {
		return a, "", fmt.Errorf("file %s is ignored by ignore rules (.gocodeignore/.gitignore)", path)
	}

	return a, path, nil
}

func generateUnifiedDiff(filename, oldContent, newContent string) string {
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	var diff strings.Builder

	if oldContent == "" {
		diff.WriteString("--- /dev/null\n")
		diff.WriteString(fmt.Sprintf("+++ %s\n", filename))
		diff.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(newLines)))
		for _, line := range newLines {
			diff.WriteString(fmt.Sprintf("+%s\n", line))
		}
	} else {
		diff.WriteString(fmt.Sprintf("--- %s\n", filename))
		diff.WriteString(fmt.Sprintf("+++ %s\n", filename))

		inHunk := false
		oldHunkStart := 0
		newHunkStart := 0
		var hunkLines []string

		flushHunk := func() {
			if len(hunkLines) > 0 {
				oldCount, newCount := 0, 0
				for _, l := range hunkLines {
					if strings.HasPrefix(l, "-") {
						oldCount++
					} else if strings.HasPrefix(l, "+") {
						newCount++
					} else {
						oldCount++
						newCount++
					}
				}
				diff.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", oldHunkStart+1, oldCount, newHunkStart+1, newCount))
				for _, l := range hunkLines {
					diff.WriteString(l)
					diff.WriteString("\n")
				}
				hunkLines = nil
			}
		}

		oldIdx, newIdx := 0, 0
		for oldIdx < len(oldLines) || newIdx < len(newLines) {
			if oldIdx < len(oldLines) && newIdx < len(newLines) {
				if oldLines[oldIdx] == newLines[newIdx] {
					if inHunk {
						hunkLines = append(hunkLines, " "+oldLines[oldIdx])
					}
					oldIdx++
					newIdx++
				} else {
					if !inHunk {
						inHunk = true
						contextStart := oldIdx - 3
						newContextStart := newIdx - 3
						if contextStart < 0 {
							contextStart = 0
						}
						if newContextStart < 0 {
							newContextStart = 0
						}
						oldHunkStart = contextStart
						newHunkStart = newContextStart
						for i := contextStart; i < oldIdx; i++ {
							hunkLines = append(hunkLines, " "+oldLines[i])
						}
					}
					hunkLines = append(hunkLines, "-"+oldLines[oldIdx])
					hunkLines = append(hunkLines, "+"+newLines[newIdx])
					oldIdx++
					newIdx++
				}
			} else if oldIdx < len(oldLines) {
				if !inHunk {
					inHunk = true
					oldHunkStart = oldIdx
					newHunkStart = newIdx
				}
				hunkLines = append(hunkLines, "-"+oldLines[oldIdx])
				oldIdx++
			} else {
				if !inHunk {
					inHunk = true
					oldHunkStart = oldIdx
					newHunkStart = newIdx
				}
				hunkLines = append(hunkLines, "+"+newLines[newIdx])
				newIdx++
			}
		}

		flushHunk()
	}

	return diff.String()
}

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
