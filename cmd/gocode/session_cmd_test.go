package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/session"
)

func createTestStore(t *testing.T) (*session.SessionStore, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	store, err := session.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	return store, dbPath
}

func seedSession(t *testing.T, store *session.SessionStore, id, title string, msgs []provider.Message) {
	t.Helper()
	rec, err := store.CreateSession(id, title, "anthropic", "claude-3-7-sonnet")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	for _, m := range msgs {
		if err := store.AppendMessage(rec.ID, m); err != nil {
			t.Fatalf("failed to append message: %v", err)
		}
	}
}

func TestSessionCommand_Registration(t *testing.T) {
	cmd := newSessionCommand()
	if cmd.Name() != "session" {
		t.Errorf("expected command name 'session', got %q", cmd.Name())
	}

	exportCmd, _, err := cmd.Find([]string{"export"})
	if err != nil || exportCmd == nil {
		t.Fatalf("failed to find 'export' subcommand: %v", err)
	}

	for _, flagName := range []string{"format", "output", "force", "no-tools"} {
		if exportCmd.Flags().Lookup(flagName) == nil {
			t.Errorf("missing expected flag %q on export command", flagName)
		}
	}

	// Verify --include-system flag is strictly NOT implemented
	if exportCmd.Flags().Lookup("include-system") != nil {
		t.Error("flag --include-system must not be exposed in CLI")
	}
}

func TestExecuteSessionExport_LatestSessionMarkdown(t *testing.T) {
	store, _ := createTestStore(t)
	defer store.Close()

	seedSession(t, store, "sess_first", "First Session", []provider.Message{
		{Role: "user", Content: "First query"},
		{Role: "assistant", Content: "First answer"},
	})

	time.Sleep(10 * time.Millisecond)

	seedSession(t, store, "sess_second", "Second Session", []provider.Message{
		{Role: "system", Content: "SECRET SYSTEM PROMPT"},
		{Role: "user", Content: "Second query"},
		{
			Role:    "assistant",
			Content: "Second answer with tool",
			ToolCalls: []provider.ToolCall{
				{ID: "c1", Name: "file_read", Args: json.RawMessage(`{"path":"a.go"}`)},
			},
		},
		{Role: "tool", ToolCallID: "c1", Content: "package a"},
	})

	var out bytes.Buffer
	flags := sessionExportFlags{
		format: "markdown",
	}

	// No session ID -> export latest (sess_second)
	err := executeSessionExport(&out, store, true, "", flags)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "# Session: Second Session") {
		t.Errorf("expected latest session to be exported, got:\n%s", output)
	}
	if !strings.Contains(output, "Second query") {
		t.Errorf("missing user query in output:\n%s", output)
	}
	if !strings.Contains(output, "#### 🔧 Tool Call: `file_read`") {
		t.Errorf("missing tool call in output:\n%s", output)
	}
	if !strings.Contains(output, "#### 📥 Tool Result: `file_read` (`c1`)") {
		t.Errorf("missing tool result in output:\n%s", output)
	}

	// SECURITY: verify system prompt is never leaked
	if strings.Contains(output, "SECRET SYSTEM PROMPT") {
		t.Error("SECURITY VIOLATION: system prompt leaked into markdown export")
	}
}

func TestExecuteSessionExport_SpecificSessionJSON(t *testing.T) {
	store, _ := createTestStore(t)
	defer store.Close()

	seedSession(t, store, "sess_target", "Target Title", []provider.Message{
		{Role: "user", Content: "Hello from target"},
		{Role: "assistant", Content: "Target response"},
	})

	seedSession(t, store, "sess_other", "Other Title", []provider.Message{
		{Role: "user", Content: "Hello from other"},
	})

	var out bytes.Buffer
	flags := sessionExportFlags{
		format: "json",
	}

	err := executeSessionExport(&out, store, true, "sess_target", flags)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	var parsed session.JSONSessionExport
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to parse JSON export: %v\nOutput:\n%s", err, out.String())
	}

	if parsed.ID != "sess_target" {
		t.Errorf("expected session 'sess_target', got %q", parsed.ID)
	}
	if parsed.Title != "Target Title" {
		t.Errorf("expected title 'Target Title', got %q", parsed.Title)
	}
	if len(parsed.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(parsed.Messages))
	}
	if parsed.Messages[0].Content != "Hello from target" {
		t.Errorf("unexpected content: %q", parsed.Messages[0].Content)
	}
}

func TestExecuteSessionExport_SessionNotFound(t *testing.T) {
	store, _ := createTestStore(t)
	defer store.Close()

	var out bytes.Buffer
	flags := sessionExportFlags{}

	err := executeSessionExport(&out, store, true, "sess_nonexistent", flags)
	if err == nil {
		t.Fatal("expected error for nonexistent session, got nil")
	}
	if !strings.Contains(err.Error(), `session "sess_nonexistent" not found`) {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

func TestExecuteSessionExport_NoSavedSessions(t *testing.T) {
	store, _ := createTestStore(t)
	defer store.Close()

	var out bytes.Buffer
	flags := sessionExportFlags{}

	err := executeSessionExport(&out, store, true, "", flags)
	if err == nil {
		t.Fatal("expected error when no sessions exist")
	}
	if !strings.Contains(err.Error(), "no saved sessions found") {
		t.Errorf("expected 'no saved sessions found', got %v", err)
	}
}

func TestExecuteSessionExport_PersistenceDisabled(t *testing.T) {
	var out bytes.Buffer
	flags := sessionExportFlags{}

	err := executeSessionExport(&out, nil, false, "", flags)
	if err == nil {
		t.Fatal("expected error when persistence is disabled")
	}
	if !strings.Contains(err.Error(), "session persistence is disabled in config") {
		t.Errorf("expected persistence disabled message, got %v", err)
	}
}

func TestExecuteSessionExport_OutputFileCreationAndForce(t *testing.T) {
	store, _ := createTestStore(t)
	defer store.Close()

	seedSession(t, store, "sess_file", "File Export Test", []provider.Message{
		{Role: "user", Content: "Persisted message"},
	})

	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "export.md")

	var out bytes.Buffer
	flags := sessionExportFlags{
		format: "markdown",
		output: dest,
		force:  false,
	}

	// First export: file does not exist -> should succeed
	err := executeSessionExport(&out, store, true, "sess_file", flags)
	if err != nil {
		t.Fatalf("initial file export failed: %v", err)
	}

	content, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed to read created export file: %v", err)
	}
	if !strings.Contains(string(content), "Persisted message") {
		t.Errorf("export file content missing expected text: %s", string(content))
	}

	// Second export without --force: must fail
	out.Reset()
	err = executeSessionExport(&out, store, true, "sess_file", flags)
	if err == nil {
		t.Fatal("expected error when overwriting without --force, got nil")
	}
	if !strings.Contains(err.Error(), "already exists (use --force to overwrite)") {
		t.Errorf("unexpected overwrite error message: %v", err)
	}

	// Third export with --force: must succeed
	flags.force = true
	out.Reset()
	err = executeSessionExport(&out, store, true, "sess_file", flags)
	if err != nil {
		t.Fatalf("force overwrite failed: %v", err)
	}
}

func TestExecuteSessionExport_POSIXPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions do not apply on Windows")
	}

	store, _ := createTestStore(t)
	defer store.Close()

	seedSession(t, store, "sess_perm", "Permission Test", []provider.Message{
		{Role: "user", Content: "Hello"},
	})

	dest := filepath.Join(t.TempDir(), "perm_test.md")
	var out bytes.Buffer
	flags := sessionExportFlags{
		output: dest,
	}

	if err := executeSessionExport(&out, store, true, "sess_perm", flags); err != nil {
		t.Fatalf("export failed: %v", err)
	}

	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}

	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected file permission 0600, got %04o", perm)
	}
}

func TestExecuteSessionExport_NoToolsFlag(t *testing.T) {
	store, _ := createTestStore(t)
	defer store.Close()

	seedSession(t, store, "sess_notools", "No Tools Test", []provider.Message{
		{Role: "user", Content: "query"},
		{
			Role:    "assistant",
			Content: "response",
			ToolCalls: []provider.ToolCall{
				{ID: "c1", Name: "shell_exec", Args: json.RawMessage(`{"command":"ls"}`)},
			},
		},
		{Role: "tool", ToolCallID: "c1", Content: "file1.txt"},
	})

	var out bytes.Buffer
	flags := sessionExportFlags{
		format:  "markdown",
		noTools: true,
	}

	if err := executeSessionExport(&out, store, true, "sess_notools", flags); err != nil {
		t.Fatalf("export failed: %v", err)
	}

	output := out.String()
	if strings.Contains(output, "shell_exec") {
		t.Error("tool invocation was not omitted with --no-tools")
	}
	if strings.Contains(output, "file1.txt") {
		t.Error("tool result was not omitted with --no-tools")
	}
	if !strings.Contains(output, "query") || !strings.Contains(output, "response") {
		t.Error("conversation content lost with --no-tools")
	}
}

func TestExecuteSessionExport_InvalidFormatFlag(t *testing.T) {
	store, _ := createTestStore(t)
	defer store.Close()

	seedSession(t, store, "sess_fmt", "Format Test", []provider.Message{
		{Role: "user", Content: "hi"},
	})

	var out bytes.Buffer
	flags := sessionExportFlags{
		format: "pdf",
	}

	err := executeSessionExport(&out, store, true, "sess_fmt", flags)
	if err == nil {
		t.Fatal("expected error for unsupported format 'pdf'")
	}
	if !strings.Contains(err.Error(), "unsupported export format") {
		t.Errorf("expected unsupported format error, got %v", err)
	}
}

func TestExecuteSessionExport_StdoutDash(t *testing.T) {
	store, _ := createTestStore(t)
	defer store.Close()

	seedSession(t, store, "sess_dash", "Dash Output Test", []provider.Message{
		{Role: "user", Content: "dash query"},
	})

	var out bytes.Buffer
	flags := sessionExportFlags{
		output: "-",
	}

	if err := executeSessionExport(&out, store, true, "sess_dash", flags); err != nil {
		t.Fatalf("export failed: %v", err)
	}

	if !strings.Contains(out.String(), "dash query") {
		t.Errorf("expected output to be written to stdout writer when output is '-', got:\n%s", out.String())
	}
}

func TestExecuteSessionExport_StdoutPurity(t *testing.T) {
	store, _ := createTestStore(t)
	defer store.Close()

	seedSession(t, store, "sess_pure", "Pure Stdout Test", []provider.Message{
		{Role: "user", Content: "echo test"},
	})

	var out bytes.Buffer
	flags := sessionExportFlags{
		format: "json",
		output: "", // stdout
	}

	err := executeSessionExport(&out, store, true, "sess_pure", flags)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Must be pure JSON parseable directly by jq without any leading banners or warnings
	var raw map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("stdout output is not pure JSON: %v\nOutput:\n%s", err, out.String())
	}
}

func TestWriteExportFile_DirectoryCollision(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "colliding_dir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := writeExportFile(subDir, []byte("data"), true)
	if err == nil {
		t.Fatal("expected error when output path is a directory")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("expected 'is a directory' error, got %v", err)
	}
}

func TestWriteExportFile_InvalidPathFailsCleanly(t *testing.T) {
	tmpDir := t.TempDir()
	regularFile := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(regularFile, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	// An impossible path treating a regular file as a parent directory
	impossiblePath := filepath.Join(regularFile, "sub", "export.md")
	err := writeExportFile(impossiblePath, []byte("data"), false)
	if err == nil {
		t.Fatal("expected error for impossible path inside regular file")
	}
}

func TestWriteExportFile_SymlinkReplacedWithForce(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "sensitive_target.txt")
	if err := os.WriteFile(targetFile, []byte("ORIGINAL_TARGET_CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}

	symlinkPath := filepath.Join(tmpDir, "link_to_target.txt")
	if err := os.Symlink(targetFile, symlinkPath); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}

	// 1. Without force: must refuse to overwrite
	err := writeExportFile(symlinkPath, []byte("NEW_EXPORT_DATA"), false)
	if err == nil {
		t.Fatal("expected error when writing to existing symlink without --force")
	}

	targetContent, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(targetContent) != "ORIGINAL_TARGET_CONTENT" {
		t.Errorf("symlink target was modified without force: %q", string(targetContent))
	}

	// 2. With force: must replace the symlink itself, leaving the original target file untouched!
	err = writeExportFile(symlinkPath, []byte("NEW_EXPORT_DATA"), true)
	if err != nil {
		t.Fatalf("writeExportFile failed with force on symlink: %v", err)
	}

	// Target file must remain untouched!
	targetContent, err = os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(targetContent) != "ORIGINAL_TARGET_CONTENT" {
		t.Errorf("SECURITY DEFECT: writing with force followed the symlink and modified target: %q", string(targetContent))
	}

	// The symlink path should now be a regular file with the new content
	linkContent, err := os.ReadFile(symlinkPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(linkContent) != "NEW_EXPORT_DATA" {
		t.Errorf("expected link path to hold new export data, got %q", string(linkContent))
	}
}
