package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/ignore"
)

func TestCodeSearchTool_Spec(t *testing.T) {
	tool := &CodeSearchTool{}
	spec := tool.Spec()
	if spec.Name != "code_search" {
		t.Errorf("expected name 'code_search', got %q", spec.Name)
	}
	if tool.RequiresApproval() {
		t.Error("code_search should not require approval")
	}
}

func TestCodeSearchTool_BasicSearch(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "hello.txt")
	fileB := filepath.Join(dir, "sub", "world.txt")
	os.MkdirAll(filepath.Dir(fileB), 0o755)

	os.WriteFile(fileA, []byte("func main() {\n\tprintln(\"Hello World\")\n}\n"), 0o644)
	os.WriteFile(fileB, []byte("type Greeter struct{}\nfunc (g Greeter) Greet() {\n\tprintln(\"hello\")\n}\n"), 0o644)

	tool := &CodeSearchTool{WorkspaceRoot: dir}

	// Case-insensitive search should find both
	args, _ := json.Marshal(codeSearchArgs{Query: "hello"})
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected tool error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "hello.txt:2:") || !strings.Contains(res.Output, "world.txt:3:") {
		t.Errorf("expected matches in both files, got:\n%s", res.Output)
	}

	// Case-sensitive search should only match lowercase
	argsCS, _ := json.Marshal(codeSearchArgs{Query: "Hello", CaseSensitive: true})
	resCS, err := tool.Execute(context.Background(), argsCS)
	if err != nil || resCS.Error != "" {
		t.Fatalf("unexpected error: %v, %s", err, resCS.Error)
	}
	if !strings.Contains(resCS.Output, "hello.txt") {
		t.Errorf("expected match in hello.txt, got:\n%s", resCS.Output)
	}
	if strings.Contains(resCS.Output, "world.txt") {
		t.Errorf("expected NO match in world.txt for case-sensitive 'Hello', got:\n%s", resCS.Output)
	}
}

func TestCodeSearchTool_GlobFilter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "match.go"), []byte("needle in go file"), 0o644)
	os.WriteFile(filepath.Join(dir, "skip.js"), []byte("needle in js file"), 0o644)

	tool := &CodeSearchTool{WorkspaceRoot: dir}
	args, _ := json.Marshal(codeSearchArgs{Query: "needle", Glob: "*.go"})

	res, err := tool.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error: %v, %s", err, res.Error)
	}
	if !strings.Contains(res.Output, "match.go") {
		t.Errorf("expected match in match.go, got: %s", res.Output)
	}
	if strings.Contains(res.Output, "skip.js") {
		t.Errorf("expected skip.js to be excluded by glob, got: %s", res.Output)
	}
}

func TestCodeSearchTool_ContextLines(t *testing.T) {
	dir := t.TempDir()
	content := "line 1\nline 2\nTARGET LINE\nline 4\nline 5\n"
	os.WriteFile(filepath.Join(dir, "context.txt"), []byte(content), 0o644)

	tool := &CodeSearchTool{WorkspaceRoot: dir}
	args, _ := json.Marshal(codeSearchArgs{Query: "TARGET", ContextLines: 1})

	res, err := tool.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error: %v, %s", err, res.Error)
	}
	if !strings.Contains(res.Output, "line 2") || !strings.Contains(res.Output, "line 4") {
		t.Errorf("expected context lines 2 and 4, got:\n%s", res.Output)
	}
}

func TestCodeSearchTool_MaxResults(t *testing.T) {
	dir := t.TempDir()
	var content strings.Builder
	for i := 1; i <= 20; i++ {
		content.WriteString("repeated match item\n")
	}
	os.WriteFile(filepath.Join(dir, "large.txt"), []byte(content.String()), 0o644)

	tool := &CodeSearchTool{WorkspaceRoot: dir}
	args, _ := json.Marshal(codeSearchArgs{Query: "repeated", MaxResults: 5})

	res, err := tool.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error: %v, %s", err, res.Error)
	}
	if !strings.Contains(res.Output, "results capped at 5 matches") {
		t.Errorf("expected output to indicate results capped at 5, got:\n%s", res.Output)
	}
}

func TestCodeSearchTool_SensitiveAndIgnore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("API_SECRET_TOKEN=xyz"), 0o644)
	os.WriteFile(filepath.Join(dir, "safe.txt"), []byte("API_SECRET_TOKEN=xyz"), 0o644)

	matcher := ignore.NewSensitiveMatcher(nil, nil)
	tool := &CodeSearchTool{
		WorkspaceRoot:    dir,
		SensitiveMatcher: matcher,
	}

	args, _ := json.Marshal(codeSearchArgs{Query: "API_SECRET_TOKEN"})
	res, err := tool.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error: %v, %s", err, res.Error)
	}
	if strings.Contains(res.Output, ".env") {
		t.Errorf("expected .env to be blocked from search results, got:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "safe.txt") {
		t.Errorf("expected safe.txt to be included, got:\n%s", res.Output)
	}
}

func TestCodeSearchTool_WorkspaceConfinement(t *testing.T) {
	dir := t.TempDir()
	tool := &CodeSearchTool{WorkspaceRoot: dir}

	args, _ := json.Marshal(codeSearchArgs{Query: "anything", Path: "../outside"})
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Error, "escapes workspace") {
		t.Errorf("expected escapes workspace error, got: %s", res.Error)
	}
}

func TestCodeSearchTool_SearchFileScannerError(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "oversized.txt")
	// Write a line that exceeds the 1MB buffer capacity
	oversized := make([]byte, 1024*1024+10)
	for i := range oversized {
		oversized[i] = 'a'
	}
	oversized[len(oversized)-1] = '\n'
	if err := os.WriteFile(filePath, oversized, 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	tool := &CodeSearchTool{WorkspaceRoot: dir}
	re := regexp.MustCompile("a")
	_, err := tool.searchFile(filePath, re, 0, 10)
	if err == nil {
		t.Fatal("expected error from scanner on oversized line, got nil")
	}
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("expected bufio.ErrTooLong, got %v", err)
	}
}
