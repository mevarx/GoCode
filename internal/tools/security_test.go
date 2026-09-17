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

// TestSecurity_PathTraversalPrevention verifies that all tools block path traversal
// attempts that would access or modify files outside the workspace root.
func TestSecurity_PathTraversalPrevention(t *testing.T) {
	wsDir := t.TempDir()
	outsideDir := t.TempDir()

	secretOutside := filepath.Join(outsideDir, "host_secret.txt")
	_ = os.WriteFile(secretOutside, []byte("super_secret_host_data"), 0o644)

	matcher := ignore.NewSensitiveMatcher(nil, nil)

	// 1. file_read
	readTool := &FileReadTool{
		WorkspaceRoot:    wsDir,
		SensitiveMatcher: matcher,
	}
	rArgs, _ := json.Marshal(fileReadArgs{Path: "../" + filepath.Base(outsideDir) + "/host_secret.txt"})
	rRes, err := readTool.Execute(context.Background(), rArgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rRes.Error, "escapes workspace") {
		t.Errorf("expected file_read to block traversal, got error: %q", rRes.Error)
	}

	// 2. file_write
	writeTool := &FileWriteTool{
		WorkspaceRoot:    wsDir,
		SensitiveMatcher: matcher,
	}
	wArgs, _ := json.Marshal(fileWriteArgs{
		Path:    "../../../../etc/passwd",
		Content: "root:x:0:0::/root:/bin/bash",
	})
	wRes, err := writeTool.Execute(context.Background(), wArgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(wRes.Error, "escapes workspace") {
		t.Errorf("expected file_write to block traversal, got error: %q", wRes.Error)
	}

	// 3. file_patch
	patchTool := &FilePatchTool{
		WorkspaceRoot:    wsDir,
		SensitiveMatcher: matcher,
	}
	pArgs, _ := json.Marshal(filePatchArgs{
		Path:    secretOutside,
		Find:    "super",
		Replace: "hacked",
	})
	pRes, err := patchTool.Execute(context.Background(), pArgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(pRes.Error, "escapes workspace") {
		t.Errorf("expected file_patch to block traversal, got error: %q", pRes.Error)
	}

	// 4. code_search
	searchTool := &CodeSearchTool{
		WorkspaceRoot:    wsDir,
		SensitiveMatcher: matcher,
	}
	sArgs, _ := json.Marshal(codeSearchArgs{
		Query: "super_secret",
		Path:  "../",
	})
	sRes, err := searchTool.Execute(context.Background(), sArgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sRes.Error, "escapes workspace") {
		t.Errorf("expected code_search to block traversal, got error: %q", sRes.Error)
	}
}

// TestSecurity_SensitiveFilesDenied verifies that credentials and secret files are denied.
func TestSecurity_SensitiveFilesDenied(t *testing.T) {
	wsDir := t.TempDir()

	sensitiveFiles := []string{
		".env",
		".env.local",
		"id_rsa",
		"id_ed25519",
		"server.key",
		"cert.pem",
		"credentials.json",
		"token.json",
	}

	matcher := ignore.NewSensitiveMatcher(nil, nil)
	readTool := &FileReadTool{WorkspaceRoot: wsDir, SensitiveMatcher: matcher}
	writeTool := &FileWriteTool{WorkspaceRoot: wsDir, SensitiveMatcher: matcher}
	patchTool := &FilePatchTool{WorkspaceRoot: wsDir, SensitiveMatcher: matcher}

	for _, sf := range sensitiveFiles {
		fullPath := filepath.Join(wsDir, sf)
		_ = os.WriteFile(fullPath, []byte("SECRET"), 0o644)

		// Read attempt
		rArgs, _ := json.Marshal(fileReadArgs{Path: sf})
		rRes, _ := readTool.Execute(context.Background(), rArgs)
		if !strings.Contains(rRes.Error, "sensitive file pattern") {
			t.Errorf("expected read of %s to be denied as sensitive, got error: %q", sf, rRes.Error)
		}

		// Write attempt
		wArgs, _ := json.Marshal(fileWriteArgs{Path: sf, Content: "HACKED"})
		wRes, _ := writeTool.Execute(context.Background(), wArgs)
		if !strings.Contains(wRes.Error, "sensitive file pattern") {
			t.Errorf("expected write of %s to be denied as sensitive, got error: %q", sf, wRes.Error)
		}

		// Patch attempt
		pArgs, _ := json.Marshal(filePatchArgs{Path: sf, Find: "SECRET", Replace: "EXPOSED"})
		pRes, _ := patchTool.Execute(context.Background(), pArgs)
		if !strings.Contains(pRes.Error, "sensitive file pattern") {
			t.Errorf("expected patch of %s to be denied as sensitive, got error: %q", sf, pRes.Error)
		}
	}
}

// TestSecurity_ApprovalBeforeExecution verifies that preview does not execute,
// and user denial leaves filesystem intact.
func TestSecurity_ApprovalBeforeExecution(t *testing.T) {
	wsDir := t.TempDir()
	targetFile := filepath.Join(wsDir, "target.txt")
	initialContent := "original content"
	_ = os.WriteFile(targetFile, []byte(initialContent), 0o644)

	matcher := ignore.NewSensitiveMatcher(nil, nil)
	writeTool := &FileWriteTool{WorkspaceRoot: wsDir, SensitiveMatcher: matcher}

	// 1. Preview generates a diff preview without changing file
	previewArgs, _ := json.Marshal(fileWriteArgs{Path: "target.txt", Content: "overwritten content"})
	prev, err := writeTool.Preview(context.Background(), previewArgs)
	if err != nil {
		t.Fatalf("unexpected preview error: %v", err)
	}
	if prev.Diff == "" {
		t.Fatal("expected non-empty diff in preview")
	}

	dataAfterPreview, _ := os.ReadFile(targetFile)
	if string(dataAfterPreview) != initialContent {
		t.Fatalf("preview modified the file! content=%q", string(dataAfterPreview))
	}

	// 2. Denied via approval gate
	gate := NewApprovalGate()
	gate.OnPresent = func(toolName string, args json.RawMessage, preview string) (bool, error) {
		return false, nil // Denied!
	}

	res, err := gate.WrapExecution(context.Background(), writeTool, previewArgs)
	if err != nil {
		t.Fatalf("unexpected gate error: %v", err)
	}
	if !strings.Contains(res.Output, "denied") {
		t.Errorf("expected denied message, got: %q", res.Output)
	}

	dataAfterDenial, _ := os.ReadFile(targetFile)
	if string(dataAfterDenial) != initialContent {
		t.Fatalf("denial modified the file! content=%q", string(dataAfterDenial))
	}
}

// TestSecurity_ShellExecutionBounded verifies shell execution runs inside workspace
// and is constrained by timeout and output limits.
func TestSecurity_ShellExecutionBounded(t *testing.T) {
	wsDir := t.TempDir()
	shellTool := &ShellExecTool{
		WorkspaceRoot:  wsDir,
		Timeout:        2 * time.Second,
		MaxOutputBytes: 100,
	}

	// Check CWD
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "cd"
	} else {
		cmd = "pwd"
	}
	args, _ := json.Marshal(shellExecArgs{Command: cmd})
	res, err := shellTool.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error: %v, %s", err, res.Error)
	}
	if !strings.Contains(strings.ToLower(res.Output), strings.ToLower(filepath.Clean(wsDir))) {
		t.Errorf("expected CWD %q, got output: %q", wsDir, res.Output)
	}

	// Check output truncation
	argsLong, _ := json.Marshal(shellExecArgs{Command: "echo " + strings.Repeat("A", 500)})
	resLong, _ := shellTool.Execute(context.Background(), argsLong)
	if !strings.Contains(resLong.Output, "truncated") {
		t.Errorf("expected output truncation, got: %q", resLong.Output)
	}
}
