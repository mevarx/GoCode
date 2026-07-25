package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FilePatchTool replaces text occurrences in a file after approval.
type FilePatchTool struct{}

type filePatchArgs struct {
	Path    string `json:"path"`
	Find    string `json:"find"`
	Replace string `json:"replace"`
}

func (f *FilePatchTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "file_patch",
		Description: "Apply a find-and-replace edit to a file. Finds the exact 'find' string in the file and replaces it with 'replace'. Use this for targeted edits instead of rewriting the entire file. The user will see a diff of the changes before approving.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["path", "find", "replace"],
			"properties": {
				"path": {
					"type": "string",
					"description": "Absolute or relative path to the file to patch"
				},
				"find": {
					"type": "string",
					"description": "The exact string to find in the file (must be an exact match)"
				},
				"replace": {
					"type": "string",
					"description": "The string to replace the found text with"
				}
			}
		}`),
	}
}

func (f *FilePatchTool) RequiresApproval() bool {
	return true
}

func (f *FilePatchTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a filePatchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Error: fmt.Sprintf("invalid arguments: %v", err)}, nil
	}

	if a.Path == "" {
		return Result{Error: "path cannot be empty"}, nil
	}
	if a.Find == "" {
		return Result{Error: "find string cannot be empty"}, nil
	}

	path := filepath.Clean(a.Path)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Error: fmt.Sprintf("file not found: %s", path)}, nil
		}
		return Result{Error: fmt.Sprintf("cannot read file: %v", err)}, nil
	}

	oldContent := string(data)

	count := strings.Count(oldContent, a.Find)
	if count == 0 {
		return Result{Error: fmt.Sprintf("find string not found in %s", path)}, nil
	}

	newContent := strings.Replace(oldContent, a.Find, a.Replace, 1)
	diff := generateUnifiedDiff(path, oldContent, newContent)

	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return Result{Error: fmt.Sprintf("cannot write file: %v", err)}, nil
	}

	result := Result{
		Output: fmt.Sprintf("Successfully patched %s (%d occurrence(s) of find string, replaced first)", path, count),
		Diff:   diff,
	}

	if count > 1 {
		result.Output += fmt.Sprintf("\nNote: %d additional occurrence(s) were NOT replaced. Use file_patch again to replace them.", count-1)
	}

	return result, nil
}
