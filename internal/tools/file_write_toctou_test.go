package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustMarshalWriteArgs(t *testing.T, path, content string) json.RawMessage {
	t.Helper()
	args, err := json.Marshal(fileWriteArgs{Path: path, Content: content})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return args
}

func TestFileWriteTool_ExecuteRefusesWhenContentChangedAfterPreview(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("approved base"), 0o644); err != nil {
		t.Fatal(err)
	}

	modified := 0
	tool := &FileWriteTool{
		WorkspaceRoot: dir,
		OnModified:    func(string) { modified++ },
	}
	args := mustMarshalWriteArgs(t, "note.txt", "approved result")

	if _, err := tool.Preview(context.Background(), args); err != nil {
		t.Fatalf("preview: %v", err)
	}

	// External writer changes the file between preview and execution.
	if err := os.WriteFile(target, []byte("external change"), 0o644); err != nil {
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
	if string(data) != "external change" {
		t.Errorf("file must keep the externally written content, got %q", string(data))
	}
	if modified != 0 {
		t.Errorf("OnModified must not fire on refused write, fired %d time(s)", modified)
	}
}

func TestFileWriteTool_PreviewThenExecuteSucceeds(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	modified := 0
	tool := &FileWriteTool{
		WorkspaceRoot: dir,
		OnModified:    func(string) { modified++ },
	}
	args := mustMarshalWriteArgs(t, "note.txt", "new content")

	if _, err := tool.Preview(context.Background(), args); err != nil {
		t.Fatalf("preview: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}

	data, _ := os.ReadFile(target)
	if string(data) != "new content" {
		t.Errorf("expected 'new content', got %q", string(data))
	}
	if modified != 1 {
		t.Errorf("OnModified should fire once, fired %d time(s)", modified)
	}
}

func TestFileWriteTool_ExecuteRefusesWhenFileCreatedAfterPreview(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "created.txt")

	tool := &FileWriteTool{WorkspaceRoot: dir}
	args := mustMarshalWriteArgs(t, "created.txt", "tool content")

	if _, err := tool.Preview(context.Background(), args); err != nil {
		t.Fatalf("preview: %v", err)
	}

	// File appears between preview and execution.
	if err := os.WriteFile(target, []byte("external creation"), 0o644); err != nil {
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
	if string(data) != "external creation" {
		t.Errorf("file must keep the externally written content, got %q", string(data))
	}
}

func TestFileWriteTool_ExecuteRefusesInWorkspaceSymlinkSwapAfterPreview(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "swap.txt")
	if err := os.WriteFile(target, []byte("approved base"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second in-workspace file is still an unapproved write destination even
	// though ValidatePath cannot tell it apart from the approved path.
	other := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(other, []byte("other content"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &FileWriteTool{WorkspaceRoot: dir}
	args := mustMarshalWriteArgs(t, "swap.txt", "tool content")

	if _, err := tool.Preview(context.Background(), args); err != nil {
		t.Fatalf("preview: %v", err)
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, target); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Error, "changed since approval") {
		t.Fatalf("expected stale-approval refusal, got %q", result.Error)
	}

	data, _ := os.ReadFile(other)
	if string(data) != "other content" {
		t.Errorf("unapproved symlink target must be untouched, got %q", string(data))
	}
}

func TestFileWriteTool_ExecuteRefusesSymlinkSwapAfterPreview(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()
	target := filepath.Join(dir, "swap.txt")
	if err := os.WriteFile(target, []byte("approved base"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside content"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &FileWriteTool{WorkspaceRoot: dir}
	args := mustMarshalWriteArgs(t, "swap.txt", "tool content")

	if _, err := tool.Preview(context.Background(), args); err != nil {
		t.Fatalf("preview: %v", err)
	}

	// Swap the approved regular file for a symlink to a file outside the workspace.
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
	if string(data) != "outside content" {
		t.Errorf("outside target must be untouched, got %q", string(data))
	}
}
