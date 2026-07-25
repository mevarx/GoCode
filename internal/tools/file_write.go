package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileWriteTool writes or overwrites content to a file after approval.
type FileWriteTool struct{}

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
					"description": "Absolute or relative path to the file to write"
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

func (f *FileWriteTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a fileWriteArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Error: fmt.Sprintf("invalid arguments: %v", err)}, nil
	}

	if a.Path == "" {
		return Result{Error: "path cannot be empty"}, nil
	}

	path := filepath.Clean(a.Path)

	var oldContent string
	existingData, err := os.ReadFile(path)
	if err == nil {
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

	return Result{
		Output: fmt.Sprintf("Successfully wrote %d bytes to %s", len(a.Content), path),
		Diff:   diff,
	}, nil
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
		hunkStart := 0
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
				diff.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", hunkStart+1, oldCount, hunkStart+1, newCount))
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
						if contextStart < 0 {
							contextStart = 0
						}
						hunkStart = contextStart
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
					hunkStart = oldIdx
				}
				hunkLines = append(hunkLines, "-"+oldLines[oldIdx])
				oldIdx++
			} else {
				if !inHunk {
					inHunk = true
					hunkStart = newIdx
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
