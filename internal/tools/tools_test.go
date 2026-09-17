package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/ignore"
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
	if !strings.Contains(result.Output, content) {
		t.Errorf("expected output to contain %q, got %q", content, result.Output)
	}
}

func TestFileReadTool_OffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "multiline.txt")
	content := "line 1\nline 2\nline 3\nline 4\nline 5"
	os.WriteFile(testFile, []byte(content), 0o644)

	tool := &FileReadTool{}
	args, _ := json.Marshal(fileReadArgs{Path: testFile, Offset: 2, Limit: 2})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected result error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "line 2\nline 3") {
		t.Errorf("expected lines 2 and 3, got %q", result.Output)
	}
	if strings.Contains(result.Output, "line 1") || strings.Contains(result.Output, "line 4") {
		t.Errorf("expected output to only have lines 2 and 3, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "lines: 2-3 of 5") {
		t.Errorf("expected metadata to indicate lines 2-3 of 5, got %q", result.Output)
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

	data, _ := os.ReadFile(testFile)
	if string(data) != content {
		t.Errorf("expected %q, got %q", content, string(data))
	}

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

func TestShellExecTool_WorkspaceCWD(t *testing.T) {
	dir := t.TempDir()
	tool := &ShellExecTool{WorkspaceRoot: dir}

	var cmdStr string
	if runtime.GOOS == "windows" {
		cmdStr = "cd"
	} else {
		cmdStr = "pwd"
	}
	args, _ := json.Marshal(shellExecArgs{Command: cmdStr})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected result error: %s", result.Error)
	}
	// Case-insensitive check for Windows drive letters/paths
	cleanExpected := filepath.Clean(dir)
	if !strings.Contains(strings.ToLower(result.Output), strings.ToLower(cleanExpected)) {
		t.Errorf("expected CWD %q in output, got %q", cleanExpected, result.Output)
	}
}

func TestShellExecTool_OutputTruncation(t *testing.T) {
	tool := &ShellExecTool{MaxOutputBytes: 20}
	args, _ := json.Marshal(shellExecArgs{Command: "echo 1234567890abcdefghijklmnopqrstuvwxyz"})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Output, "truncated") {
		t.Errorf("expected output to be truncated, got %q", result.Output)
	}
}

func TestShellExecTool_Timeout(t *testing.T) {
	tool := &ShellExecTool{Timeout: 50 * time.Millisecond}
	var cmdStr string
	if runtime.GOOS == "windows" {
		cmdStr = "ping -n 5 127.0.0.1 > nul"
	} else {
		cmdStr = "sleep 2"
	}
	args, _ := json.Marshal(shellExecArgs{Command: cmdStr})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Error, "timed out") {
		t.Errorf("expected timeout error, got %q", result.Error)
	}
}

func TestShellExecTool_Cancellation(t *testing.T) {
	tool := &ShellExecTool{Timeout: 10 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	var cmdStr string
	if runtime.GOOS == "windows" {
		cmdStr = "ping -n 5 127.0.0.1 > nul"
	} else {
		cmdStr = "sleep 2"
	}
	args, _ := json.Marshal(shellExecArgs{Command: cmdStr})

	result, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Error, "cancelled") && !strings.Contains(result.Error, "failed") {
		t.Errorf("expected cancelled error, got %q", result.Error)
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

func TestApprovalGate_Permissions(t *testing.T) {
	gate := NewApprovalGateWithPermissions([]string{"file_write"}, []string{"shell_exec"})

	if !gate.IsAutoApproved("file_write") {
		t.Error("expected file_write to be auto-approved")
	}
	if !gate.IsDenied("shell_exec") {
		t.Error("expected shell_exec to be denied")
	}

	writeTool := &FileWriteTool{}
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auto.txt")
	args, _ := json.Marshal(fileWriteArgs{Path: path, Content: "auto content"})

	res, err := gate.WrapExecution(context.Background(), writeTool, args)
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error on auto-approved tool: %v, res: %+v", err, res)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "auto content" {
		t.Errorf("expected file to be written")
	}

	shellTool := &ShellExecTool{}
	shellArgs, _ := json.Marshal(shellExecArgs{Command: "echo denied"})
	resDenied, err := gate.WrapExecution(context.Background(), shellTool, shellArgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resDenied.Error, "denied by permissions") {
		t.Errorf("expected denied error message, got %q", resDenied.Error)
	}
}

type dummyIgnoreMatcher struct {
	ignoredPaths map[string]bool
}

func (d *dummyIgnoreMatcher) IsIgnored(path string, isDir bool) bool {
	return d.ignoredPaths[path]
}

func TestTools_IgnoreRules(t *testing.T) {
	tmpDir := t.TempDir()
	secretFile := filepath.Join(tmpDir, "secret.key")
	_ = os.WriteFile(secretFile, []byte("supersecret"), 0o644)

	matcher := &dummyIgnoreMatcher{ignoredPaths: map[string]bool{secretFile: true}}

	readTool := &FileReadTool{IgnoreMatcher: matcher}
	args, _ := json.Marshal(fileReadArgs{Path: secretFile})
	res, _ := readTool.Execute(context.Background(), args)
	if !strings.Contains(res.Error, "ignored by ignore rules") {
		t.Errorf("expected file_read to block ignored file, got: %s", res.Error)
	}

	writeTool := &FileWriteTool{IgnoreMatcher: matcher}
	writeArgs, _ := json.Marshal(fileWriteArgs{Path: secretFile, Content: "new"})
	resW, _ := writeTool.Execute(context.Background(), writeArgs)
	if !strings.Contains(resW.Error, "ignored by ignore rules") {
		t.Errorf("expected file_write to block ignored file, got: %s", resW.Error)
	}

	patchTool := &FilePatchTool{IgnoreMatcher: matcher}
	patchArgs, _ := json.Marshal(filePatchArgs{Path: secretFile, Find: "secret", Replace: "pub"})
	resP, _ := patchTool.Execute(context.Background(), patchArgs)
	if !strings.Contains(resP.Error, "ignored by ignore rules") {
		t.Errorf("expected file_patch to block ignored file, got: %s", resP.Error)
	}
}

func TestTools_OnModified(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "tracked.txt")

	var modified []string
	onMod := func(p string) {
		modified = append(modified, p)
	}

	writeTool := &FileWriteTool{OnModified: onMod}
	wArgs, _ := json.Marshal(fileWriteArgs{Path: filePath, Content: "initial"})
	_, _ = writeTool.Execute(context.Background(), wArgs)

	if len(modified) != 1 || modified[0] != filePath {
		t.Errorf("expected onModified to be called for write, got %+v", modified)
	}

	patchTool := &FilePatchTool{OnModified: onMod}
	pArgs, _ := json.Marshal(filePatchArgs{Path: filePath, Find: "initial", Replace: "updated"})
	_, _ = patchTool.Execute(context.Background(), pArgs)

	if len(modified) != 2 {
		t.Errorf("expected onModified to be called for patch, got %+v", modified)
	}
}

func TestTools_SensitiveFileProtection(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	_ = os.WriteFile(envFile, []byte("SECRET_KEY=12345"), 0o644)

	matcher := ignore.NewSensitiveMatcher(nil, nil)

	// file_read
	readTool := &FileReadTool{SensitiveMatcher: matcher}
	args, _ := json.Marshal(fileReadArgs{Path: envFile})
	res, _ := readTool.Execute(context.Background(), args)
	if !strings.Contains(res.Error, "sensitive file pattern") {
		t.Errorf("expected file_read to block .env, got: %s", res.Error)
	}

	// file_write
	writeTool := &FileWriteTool{SensitiveMatcher: matcher}
	writeArgs, _ := json.Marshal(fileWriteArgs{Path: envFile, Content: "new"})
	resW, _ := writeTool.Execute(context.Background(), writeArgs)
	if !strings.Contains(resW.Error, "sensitive file pattern") {
		t.Errorf("expected file_write to block .env, got: %s", resW.Error)
	}

	// file_patch
	patchTool := &FilePatchTool{SensitiveMatcher: matcher}
	patchArgs, _ := json.Marshal(filePatchArgs{Path: envFile, Find: "SECRET", Replace: "PUBLIC"})
	resP, _ := patchTool.Execute(context.Background(), patchArgs)
	if !strings.Contains(resP.Error, "sensitive file pattern") {
		t.Errorf("expected file_patch to block .env, got: %s", resP.Error)
	}
}

func TestApprovalGate_DenialDoesNotModifyFiles(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "precious.txt")
	originalContent := "original precious content"
	_ = os.WriteFile(filePath, []byte(originalContent), 0o644)

	gate := NewApprovalGate()
	// Always deny in OnPresent
	gate.OnPresent = func(toolName string, args json.RawMessage, preview string) (bool, error) {
		if preview == "" {
			t.Errorf("expected preview to be generated before approval")
		}
		return false, nil // DENY
	}

	// file_write denial
	writeTool := &FileWriteTool{}
	writeArgs, _ := json.Marshal(fileWriteArgs{Path: filePath, Content: "malicious overwrite"})
	res, err := gate.WrapExecution(context.Background(), writeTool, writeArgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Output, "denied") {
		t.Errorf("expected denied message, got: %q", res.Output)
	}
	data, _ := os.ReadFile(filePath)
	if string(data) != originalContent {
		t.Errorf("file was modified despite denial: %q", string(data))
	}

	// file_patch denial
	patchTool := &FilePatchTool{}
	patchArgs, _ := json.Marshal(filePatchArgs{Path: filePath, Find: "precious", Replace: "destroyed"})
	resPatch, err := gate.WrapExecution(context.Background(), patchTool, patchArgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resPatch.Output, "denied") {
		t.Errorf("expected denied message, got: %q", resPatch.Output)
	}
	data, _ = os.ReadFile(filePath)
	if string(data) != originalContent {
		t.Errorf("file was patched despite denial: %q", string(data))
	}
}
