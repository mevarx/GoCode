package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileReadTool reads the contents of a file or lists a directory.
type FileReadTool struct{}

type fileReadArgs struct {
	Path string `json:"path"`
}

func (f *FileReadTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "file_read",
		Description: "Read the contents of a file. Returns the full file content as text. Use this to inspect source code, configuration files, documentation, etc.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["path"],
			"properties": {
				"path": {
					"type": "string",
					"description": "Absolute or relative path to the file to read"
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

	path := filepath.Clean(a.Path)

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Error: fmt.Sprintf("file not found: %s", path)}, nil
		}
		return Result{Error: fmt.Sprintf("cannot access file: %v", err)}, nil
	}

	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return Result{Error: fmt.Sprintf("cannot read directory: %v", err)}, nil
		}

		var listing []string
		for _, entry := range entries {
			prefix := "  "
			if entry.IsDir() {
				prefix = "📁"
			} else {
				prefix = "📄"
			}
			listing = append(listing, fmt.Sprintf("%s %s", prefix, entry.Name()))
		}
		return Result{Output: fmt.Sprintf("Directory listing for %s:\n%s", path, joinLines(listing))}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Error: fmt.Sprintf("cannot read file: %v", err)}, nil
	}

	if len(data) > 100*1024 {
		return Result{
			Output: fmt.Sprintf("File %s is %d bytes. Showing first 100KB:\n\n%s\n\n... (truncated)", path, len(data), string(data[:100*1024])),
		}, nil
	}

	return Result{Output: string(data)}, nil
}

func joinLines(lines []string) string {
	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n"
		}
		result += line
	}
	return result
}
