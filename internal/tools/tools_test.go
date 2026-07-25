package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileReadTool_Spec(t *testing.T) {
	tool := &FileReadTool{}
	spec := tool.Spec()

	if spec.Name != "file_read" {
		t.Errorf("expected name 'file_read', got %q", spec.Name)
	}
	if tool.RequiresApproval() {
		t.Error("file_read should not require approval")
	}
}

func TestFileReadTool_Execute(t *testing.T) {
	// Create a temp file
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	content := "hello world\nline two\n"
	os.WriteFile(testFile, []byte(content), 0o644)

	tool := &FileReadTool{}
	args, _ := json.Marshal(fileReadArgs{Path: testFile})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected result error: %s", result.Error)
	}
	if result.Output != content {
		t.Errorf("expected %q, got %q", content, result.Output)
	}
}

func TestFileReadTool_NotFound(t *testing.T) {
	tool := &FileReadTool{}
	args, _ := json.Marshal(fileReadArgs{Path: "/nonexistent/file.txt"})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error for nonexistent file")
	}
}

func TestFileReadTool_Directory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0o755)

	tool := &FileReadTool{}
	args, _ := json.Marshal(fileReadArgs{Path: dir})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected result error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "a.txt") {
		t.Error("directory listing should contain a.txt")
	}
}

func TestFileWriteTool_Spec(t *testing.T) {
	tool := &FileWriteTool{}
	spec := tool.Spec()

	if spec.Name != "file_write" {
		t.Errorf("expected name 'file_write', got %q", spec.Name)
	}
	if !tool.RequiresApproval() {
		t.Error("file_write should require approval")
	}
}

func TestFileWriteTool_NewFile(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "new.txt")
	content := "new file content"

	tool := &FileWriteTool{}
	args, _ := json.Marshal(fileWriteArgs{Path: testFile, Content: content})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected result error: %s", result.Error)
	}

	// Verify file was written
	data, _ := os.ReadFile(testFile)
	if string(data) != content {
		t.Errorf("expected %q, got %q", content, string(data))
	}

	// Should have a diff
	if result.Diff == "" {
		t.Error("expected a diff for new file")
	}
}

func TestFileWriteTool_Overwrite(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "existing.txt")
	os.WriteFile(testFile, []byte("old content"), 0o644)

	tool := &FileWriteTool{}
	args, _ := json.Marshal(fileWriteArgs{Path: testFile, Content: "new content"})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected result error: %s", result.Error)
	}
	if result.Diff == "" {
		t.Error("expected a diff for overwrite")
	}

	data, _ := os.ReadFile(testFile)
	if string(data) != "new content" {
		t.Errorf("expected 'new content', got %q", string(data))
	}
}

func TestFilePatchTool_Spec(t *testing.T) {
	tool := &FilePatchTool{}
	spec := tool.Spec()

	if spec.Name != "file_patch" {
		t.Errorf("expected name 'file_patch', got %q", spec.Name)
	}
	if !tool.RequiresApproval() {
		t.Error("file_patch should require approval")
	}
}

func TestFilePatchTool_Execute(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "patch.txt")
	os.WriteFile(testFile, []byte("hello world"), 0o644)

	tool := &FilePatchTool{}
	args, _ := json.Marshal(filePatchArgs{
		Path:    testFile,
		Find:    "world",
		Replace: "GoCode",
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected result error: %s", result.Error)
	}

	data, _ := os.ReadFile(testFile)
	if string(data) != "hello GoCode" {
		t.Errorf("expected 'hello GoCode', got %q", string(data))
	}
}

func TestFilePatchTool_NotFound(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "patch.txt")
	os.WriteFile(testFile, []byte("hello world"), 0o644)

	tool := &FilePatchTool{}
	args, _ := json.Marshal(filePatchArgs{
		Path:    testFile,
		Find:    "nonexistent",
		Replace: "replacement",
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error when find string not found")
	}
}

func TestShellExecTool_Spec(t *testing.T) {
	tool := &ShellExecTool{}
	spec := tool.Spec()

	if spec.Name != "shell_exec" {
		t.Errorf("expected name 'shell_exec', got %q", spec.Name)
	}
	if !tool.RequiresApproval() {
		t.Error("shell_exec should require approval")
	}
}

func TestShellExecTool_Execute(t *testing.T) {
	tool := &ShellExecTool{}
	args, _ := json.Marshal(shellExecArgs{Command: "echo hello"})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected result error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "hello") {
		t.Errorf("expected output to contain 'hello', got %q", result.Output)
	}
}

func TestToolRegistry(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&FileReadTool{})
	reg.Register(&ShellExecTool{})

	if tool := reg.Get("file_read"); tool == nil {
		t.Error("expected to find file_read tool")
	}
	if tool := reg.Get("nonexistent"); tool != nil {
		t.Error("expected nil for nonexistent tool")
	}

	specs := reg.Specs()
	if len(specs) != 2 {
		t.Errorf("expected 2 specs, got %d", len(specs))
	}
}
