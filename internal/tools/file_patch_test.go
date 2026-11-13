package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustMarshalPatchArgs(t *testing.T, path, find, replace string) json.RawMessage {
	t.Helper()
	args, err := json.Marshal(filePatchArgs{Path: path, Find: find, Replace: replace})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return args
}

func TestFilePatchTool_ExecuteRefusesWhenContentChangedAfterPreview(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "patch.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	modified := 0
	tool := &FilePatchTool{
		WorkspaceRoot: dir,
		OnModified:    func(string) { modified++ },
	}
	args := mustMarshalPatchArgs(t, "patch.txt", "world", "GoCode")

	if _, err := tool.Preview(context.Background(), args); err != nil {
		t.Fatalf("preview: %v", err)
	}

	// External writer changes the file but keeps the find string, so only the
	// snapshot hash can detect the divergence.
	if err := os.WriteFile(target, []byte("hello world, plus external note\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Error, "changed since approval") {
		t.Fatalf("expected stale-approval refusal, got %q", result.Error)
	}

	data, _ := os.ReadFile(target)
	if string(data) != "hello world, plus external note\n" {
		t.Errorf("file must keep the externally written content, got %q", string(data))
	}
	if modified != 0 {
		t.Errorf("OnModified must not fire on refused patch, fired %d time(s)", modified)
	}
}

func TestFilePatchTool_PreviewThenExecuteSucceeds(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "patch.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &FilePatchTool{WorkspaceRoot: dir}
	args := mustMarshalPatchArgs(t, "patch.txt", "world", "GoCode")

	preview, err := tool.Preview(context.Background(), args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Diff == "" {
		t.Fatal("expected a diff from preview")
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}

	data, _ := os.ReadFile(target)
	if string(data) != "hello GoCode" {
		t.Errorf("expected 'hello GoCode', got %q", string(data))
	}
}

func TestFilePatchTool_ExecuteRefusesSymlinkSwapAfterPreview(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()
	target := filepath.Join(dir, "swap.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("hello world outside"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &FilePatchTool{WorkspaceRoot: dir}
	args := mustMarshalPatchArgs(t, "swap.txt", "world", "GoCode")

	if _, err := tool.Preview(context.Background(), args); err != nil {
		t.Fatalf("preview: %v", err)
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Error == "" {
		t.Fatal("expected execution to be refused after symlink swap")
	}

	data, _ := os.ReadFile(outside)
	if string(data) != "hello world outside" {
		t.Errorf("outside target must be untouched, got %q", string(data))
	}
}
