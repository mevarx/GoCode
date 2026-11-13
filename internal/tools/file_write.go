package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"github.com/mevarx/GoCode/internal/ignore"
)

type FileWriteTool struct {
	IgnoreMatcher    ignore.Matcher
	SensitiveMatcher *ignore.SensitiveMatcher
	WorkspaceRoot    string
	OnModified       func(path string)

	snapshots fileSnapshotStore
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

// Preview shows a diff preview without modifying files.
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

	// SECURITY: remember the state the diff was computed from so Execute can
	// refuse to apply a change that no longer matches what the user approved.
	snap, err := snapshotFromRead(path, existingData, readErr)
	if err != nil {
		return Preview{}, fmt.Errorf("failed to resolve %s: %v", path, err)
	}
	f.snapshots.put(fileWriteSnapshotKey(path, a.Content), snap)

	return Preview{
		Description: fmt.Sprintf("Write %d bytes to %s", len(a.Content), path),
		Diff:        diff,
	}, nil
}

// Execute writes the file. Called only after approval.
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

	// SECURITY: refuse a write whose precondition changed after preview.
	snapKey := fileWriteSnapshotKey(path, a.Content)
	if snap, ok := f.snapshots.get(snapKey); ok {
		current, snapErr := snapshotFromRead(path, existingData, readErr)
		if snapErr != nil {
			return Result{Error: fmt.Sprintf("%s: cannot verify file state: %v", path, snapErr)}, nil
		}
		if !snap.matches(current) {
			return Result{Error: fileChangedSinceApprovalError(path)}, nil
		}
	}

	diff := generateUnifiedDiff(path, oldContent, a.Content)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{Error: fmt.Sprintf("cannot create directory %s: %v", dir, err)}, nil
	}

	if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil {
		return Result{Error: fmt.Sprintf("cannot write file: %v", err)}, nil
	}
	f.snapshots.remove(snapKey)

	// SECURITY: drop pre-write identities so the next check cannot serve stale hardlinks.
	InvalidateSensitiveIdentityCache(f.WorkspaceRoot)

	if f.OnModified != nil {
		f.OnModified(path)
	}

	return Result{
		Output: fmt.Sprintf("Successfully wrote %d bytes to %s", len(a.Content), path),
		Diff:   diff,
	}, nil
}

// fileWriteSnapshotKey identifies a preview by target path and new content.
func fileWriteSnapshotKey(path, content string) string {
	return path + "\x00" + hashFileContent([]byte(content))
}

func (f *FileWriteTool) validateArgs(args json.RawMessage) (fileWriteArgs, string, error) {
	var a fileWriteArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return a, "", fmt.Errorf("invalid arguments: %v", err)
	}

	if a.Path == "" {
		return a, "", fmt.Errorf("path cannot be empty")
	}

	var path string
	if f.WorkspaceRoot != "" {
		validatedPath, err := ValidatePath(f.WorkspaceRoot, a.Path)
		if err != nil {
			return a, "", err
		}
		path = validatedPath
	} else {
		path = filepath.Clean(a.Path)
	}

	if f.SensitiveMatcher != nil {
		blocked, reason, err := CheckSensitiveFile(f.SensitiveMatcher, path, false)
		if err != nil {
			return a, "", err
		}
		if blocked {
			return a, "", fmt.Errorf("%s: %s", path, reason)
		}
		// SECURITY: hardlink aliases share inodes with sensitive files.
		if f.WorkspaceRoot != "" {
			if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
				if IsBlockedByIdentity(SensitiveIdentitySet(f.WorkspaceRoot, f.SensitiveMatcher), info) {
					return a, "", fmt.Errorf("%s: access denied: matches a sensitive file (hardlink alias)", path)
				}
			}
		}
	} else if f.IgnoreMatcher != nil && f.IgnoreMatcher.IsIgnored(path, false) {
		return a, "", fmt.Errorf("file %s is ignored by ignore rules (.gocodeignore/.gitignore)", path)
	}

	return a, path, nil
}

func generateUnifiedDiff(filename, oldContent, newContent string) string {
	// Diff always operates on LF-normalized text so hunk line math is stable
	// across platforms; callers keep the original content for the write itself.
	oldContent = normalizeDiffNewlines(oldContent)
	newContent = normalizeDiffNewlines(newContent)

	if oldContent == "" && newContent == "" {
		return ""
	}

	// New file: the standard /dev/null header is not produced by gotextdiff.
	if oldContent == "" {
		lines := strings.Split(strings.TrimSuffix(newContent, "\n"), "\n")
		var diff strings.Builder
		diff.WriteString("--- /dev/null\n")
		fmt.Fprintf(&diff, "+++ %s\n", filename)
		fmt.Fprintf(&diff, "@@ -0,0 +1,%d @@\n", len(lines))
		for _, line := range lines {
			diff.WriteString("+" + line + "\n")
		}
		return diff.String()
	}

	edits := myers.ComputeEdits(span.URIFromPath(filename), oldContent, newContent)
	unified := gotextdiff.ToUnified(filename, filename, oldContent, edits)
	if len(unified.Hunks) == 0 {
		return ""
	}

	// Render the hunks ourselves: gotextdiff's own Format omits zero counts and
	// leaves the start line at 1 for an empty side, producing invalid headers
	// such as "@@ -1,3 +1 @@" instead of "@@ -1,3 +0,0 @@".
	var diff strings.Builder
	fmt.Fprintf(&diff, "--- %s\n", unified.From)
	fmt.Fprintf(&diff, "+++ %s\n", unified.To)
	for _, hunk := range unified.Hunks {
		oldCount, newCount := 0, 0
		for _, line := range hunk.Lines {
			switch line.Kind {
			case gotextdiff.Delete:
				oldCount++
			case gotextdiff.Insert:
				newCount++
			default:
				oldCount++
				newCount++
			}
		}
		oldStart, newStart := hunk.FromLine, hunk.ToLine
		if oldCount == 0 {
			oldStart = 0
		}
		if newCount == 0 {
			newStart = 0
		}
		fmt.Fprintf(&diff, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, line := range hunk.Lines {
			switch line.Kind {
			case gotextdiff.Delete:
				diff.WriteString("-")
			case gotextdiff.Insert:
				diff.WriteString("+")
			default:
				diff.WriteString(" ")
			}
			diff.WriteString(line.Content)
			if !strings.HasSuffix(line.Content, "\n") {
				diff.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	return diff.String()
}

func normalizeDiffNewlines(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}
