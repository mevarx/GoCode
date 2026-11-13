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

type FilePatchTool struct {
	IgnoreMatcher    ignore.Matcher
	SensitiveMatcher *ignore.SensitiveMatcher
	WorkspaceRoot    string
	OnModified       func(path string)

	snapshots fileSnapshotStore
}

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
					"description": "Absolute or relative path to the file to patch (relative to workspace root)"
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

// Preview shows a diff preview without modifying files.
func (f *FilePatchTool) Preview(ctx context.Context, args json.RawMessage) (Preview, error) {
	a, path, oldContent, newContent, count, err := f.computePatch(args)
	if err != nil {
		return Preview{}, err
	}

	diff := generateUnifiedDiff(path, oldContent, newContent)
	desc := fmt.Sprintf("Patch %s (%d occurrence(s) of find string, replacing first)", path, count)

	// SECURITY: remember the file the diff was computed from so Execute can
	// refuse to apply a patch the user did not approve.
	snap, err := snapshotForContent(path, []byte(oldContent))
	if err != nil {
		return Preview{}, fmt.Errorf("failed to resolve %s: %v", path, err)
	}
	f.snapshots.put(filePatchSnapshotKey(path, a.Find, a.Replace), snap)

	return Preview{
		Description: desc,
		Diff:        diff,
	}, nil
}

// Execute applies the patch. Called only after approval.
func (f *FilePatchTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	a, path, oldContent, newContent, count, err := f.computePatch(args)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}

	// SECURITY: refuse a patch whose precondition changed after preview.
	snapKey := filePatchSnapshotKey(path, a.Find, a.Replace)
	if snap, ok := f.snapshots.get(snapKey); ok {
		current, snapErr := snapshotForContent(path, []byte(oldContent))
		if snapErr != nil {
			return Result{Error: fmt.Sprintf("%s: cannot verify file state: %v", path, snapErr)}, nil
		}
		if !snap.matches(current) {
			return Result{Error: fileChangedSinceApprovalError(path)}, nil
		}
	}

	diff := generateUnifiedDiff(path, oldContent, newContent)

	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return Result{Error: fmt.Sprintf("cannot write file: %v", err)}, nil
	}
	f.snapshots.remove(snapKey)

	// SECURITY: drop pre-write identities so the next check cannot serve stale hardlinks.
	InvalidateSensitiveIdentityCache(f.WorkspaceRoot)

	if f.OnModified != nil {
		f.OnModified(path)
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

// filePatchSnapshotKey identifies a preview by target path and patch operation.
func filePatchSnapshotKey(path, find, replace string) string {
	return path + "\x00" + find + "\x00" + replace
}

func (f *FilePatchTool) computePatch(args json.RawMessage) (filePatchArgs, string, string, string, int, error) {
	var a filePatchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return a, "", "", "", 0, fmt.Errorf("invalid arguments: %v", err)
	}

	if a.Path == "" {
		return a, "", "", "", 0, fmt.Errorf("path cannot be empty")
	}
	if a.Find == "" {
		return a, "", "", "", 0, fmt.Errorf("find string cannot be empty")
	}

	var path string
	if f.WorkspaceRoot != "" {
		validatedPath, err := ValidatePath(f.WorkspaceRoot, a.Path)
		if err != nil {
			return a, "", "", "", 0, err
		}
		path = validatedPath
	} else {
		path = filepath.Clean(a.Path)
	}

	if f.SensitiveMatcher != nil {
		blocked, reason, err := CheckSensitiveFile(f.SensitiveMatcher, path, false)
		if err != nil {
			return a, "", "", "", 0, err
		}
		if blocked {
			return a, "", "", "", 0, fmt.Errorf("%s: %s", path, reason)
		}
		// SECURITY: hardlink aliases share inodes with sensitive files.
		if f.WorkspaceRoot != "" {
			if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
				if IsBlockedByIdentity(SensitiveIdentitySet(f.WorkspaceRoot, f.SensitiveMatcher), info) {
					return a, "", "", "", 0, fmt.Errorf("%s: access denied: matches a sensitive file (hardlink alias)", path)
				}
			}
		}
	} else if f.IgnoreMatcher != nil && f.IgnoreMatcher.IsIgnored(path, false) {
		return a, "", "", "", 0, fmt.Errorf("file %s is ignored by ignore rules (.gocodeignore/.gitignore)", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return a, "", "", "", 0, fmt.Errorf("file not found: %s", path)
		}
		return a, "", "", "", 0, fmt.Errorf("cannot read file: %v", err)
	}

	oldContent := string(data)

	count := strings.Count(oldContent, a.Find)
	if count == 0 {
		return a, "", "", "", 0, fmt.Errorf("find string not found in %s", path)
	}

	newContent := strings.Replace(oldContent, a.Find, a.Replace, 1)

	return a, path, oldContent, newContent, count, nil
}
