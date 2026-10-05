package tools

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mevarx/GoCode/internal/ignore"
)

func TestSensitiveCheck_NormalisationForms(t *testing.T) {
	m := ignore.NewSensitiveMatcher(nil, nil)

	// Rejected on every platform before name matching.
	rejected := []string{
		".env::$DATA",
		"credentials.json::$DATA",
		"id_rsa::$DATA",
		".npmrc::$DATA",
		"planted.pem::$DATA",
		"file:secret",
		".env ",
		"dir /config.json",
		"id_rsa.",
		"credentials.JSON.",
	}
	for _, p := range rejected {
		t.Run("reject_"+p, func(t *testing.T) {
			blocked, _, err := CheckSensitiveFile(m, p, false)
			if err == nil {
				t.Errorf("expected error for %q, got blocked=%v err=nil", p, blocked)
			}
		})
	}

	// Blocked on case-insensitive platforms via canonicalised matching.
	caseVariants := []string{
		".ENV",
		".Env",
		"credentials.JSON",
		"Credentials.json",
		"cert.PEM",
		"ID_RSA",
		"TOKEN.JSON",
	}
	for _, p := range caseVariants {
		t.Run("case_"+p, func(t *testing.T) {
			blocked, reason, err := CheckSensitiveFile(m, p, false)
			if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
				if err != nil || !blocked {
					t.Errorf("expected %q to be blocked on %s, got blocked=%v err=%v", p, runtime.GOOS, blocked, err)
				}
				if !strings.Contains(reason, "sensitive file pattern") {
					t.Errorf("expected sensitive-file reason, got %q", reason)
				}
			} else {
				if err != nil || blocked {
					t.Errorf("expected %q NOT blocked on %s, got blocked=%v err=%v", p, runtime.GOOS, blocked, err)
				}
			}
		})
	}

	for _, p := range []string{".env", "id_rsa", "credentials.json", "token.json", ".npmrc"} {
		blocked, _, err := CheckSensitiveFile(m, p, false)
		if err != nil || !blocked {
			t.Errorf("expected %q blocked everywhere, got blocked=%v err=%v", p, blocked, err)
		}
	}

	for _, p := range []string{"main.go", "README.md", "src/util.go", "token.json.bak"} {
		blocked, _, err := CheckSensitiveFile(m, p, false)
		if err != nil || blocked {
			t.Errorf("expected %q allowed, got blocked=%v err=%v", p, blocked, err)
		}
	}
}

func TestValidatePath_RejectsADSAndTrailingSpace(t *testing.T) {
	root := t.TempDir()
	forms := []string{
		".env::$DATA",
		".npmrc::$DATA",
		"id_rsa::$DATA",
		"planted.pem::$DATA",
		".aws/credentials::$DATA",
		".env ",
		"subdir./file",
	}
	for _, f := range forms {
		if _, err := ValidatePath(root, f); err == nil {
			t.Errorf("ValidatePath(%q) should fail", f)
		}
	}
}

func TestFileWriteCannotPlantViaADS(t *testing.T) {
	ws := t.TempDir()
	w := &FileWriteTool{WorkspaceRoot: ws, SensitiveMatcher: ignore.NewSensitiveMatcher(nil, nil)}
	args, _ := json.Marshal(fileWriteArgs{Path: ".env::$DATA", Content: "SECRET=1"})
	res, err := w.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected error for ADS write path")
	}
	if _, statErr := os.Stat(filepath.Join(ws, ".env")); !os.IsNotExist(statErr) {
		t.Fatal(".env was created by an ADS write")
	}
}

func TestFileReadHardlinkAliasBlocked(t *testing.T) {
	ws := t.TempDir()
	secret := filepath.Join(ws, ".env")
	os.WriteFile(secret, []byte("API_KEY=leak"), 0o644)
	alias := filepath.Join(ws, "notes.txt")
	if err := os.Link(secret, alias); err != nil {
		t.Skipf("hardlinks unsupported: %v", err)
	}

	r := &FileReadTool{WorkspaceRoot: ws, SensitiveMatcher: ignore.NewSensitiveMatcher(nil, nil)}
	args, _ := json.Marshal(fileReadArgs{Path: "notes.txt"})
	res, _ := r.Execute(context.Background(), args)
	if !strings.Contains(res.Error, "sensitive") {
		t.Errorf("expected hardlink alias blocked, got %q", res.Error)
	}

	// code_search must not expose the alias's content either.
	c := &CodeSearchTool{WorkspaceRoot: ws, SensitiveMatcher: ignore.NewSensitiveMatcher(nil, nil)}
	args, _ = json.Marshal(codeSearchArgs{Query: "API_KEY"})
	res, _ = c.Execute(context.Background(), args)
	if strings.Contains(res.Output, "notes.txt") {
		t.Errorf("expected code_search to skip hardlink alias, got:\n%s", res.Output)
	}
}

func TestCodeSearchSymlinkToOutsideSkipped(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("topsecret"), 0o644)
	link := filepath.Join(ws, "link.txt")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	c := &CodeSearchTool{WorkspaceRoot: ws, SensitiveMatcher: ignore.NewSensitiveMatcher(nil, nil)}
	args, _ := json.Marshal(codeSearchArgs{Query: "topsecret"})
	res, _ := c.Execute(context.Background(), args)
	if res.Error != "" {
		t.Fatalf("unexpected search error: %q", res.Error)
	}
	// The no-match message echoes the query, so assert on the absence of a
	// match line for the symlink rather than on the query string itself.
	if strings.Contains(res.Output, "link.txt") {
		t.Errorf("expected symlink to outside file to be skipped, got:\n%s", res.Output)
	}
}

func TestFileReadOffsetLimitBounds(t *testing.T) {
	ws := t.TempDir()
	f := filepath.Join(ws, "f.txt")
	os.WriteFile(f, []byte("l1\nl2\nl3\n"), 0o644)
	r := &FileReadTool{WorkspaceRoot: ws}

	cases := []struct {
		offset, limit int
		want          string // substring of the meta line
	}{
		{2, math.MaxInt64, "lines: 2-4 of 4"},
		{-5, 2, "lines: 1-2 of 4"},
		{0, -1, "lines: 1-4 of 4"},
		{1, 0, "lines: 1-4 of 4"},
		{100, 5, "lines: 4-4 of 4"},
	}
	for _, tc := range cases {
		args, _ := json.Marshal(fileReadArgs{Path: "f.txt", Offset: tc.offset, Limit: tc.limit})
		res, err := r.Execute(context.Background(), args)
		if err != nil || res.Error != "" {
			t.Errorf("offset=%d limit=%d: unexpected error %v %q", tc.offset, tc.limit, err, res.Error)
			continue
		}
		if !strings.Contains(res.Output, tc.want) {
			t.Errorf("offset=%d limit=%d: expected meta %q, got %q", tc.offset, tc.limit, tc.want, res.Output)
		}
	}
}

type panickingTool struct{}

func (p *panickingTool) Spec() ToolSpec {
	return ToolSpec{Name: "panic_tool", Description: "panics", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (p *panickingTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	panic("boom")
}
func (p *panickingTool) RequiresApproval() bool { return false }

type panickingPreviewerTool struct{ panickingTool }

func (p *panickingPreviewerTool) Spec() ToolSpec {
	return ToolSpec{Name: "panic_preview_tool", Description: "panics", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (p *panickingPreviewerTool) Preview(ctx context.Context, args json.RawMessage) (Preview, error) {
	panic("preview boom")
}
func (p *panickingPreviewerTool) RequiresApproval() bool { return true }

func TestDispatchRecoversToolPanic(t *testing.T) {
	gate := NewApprovalGate()
	res, err := gate.WrapExecution(context.Background(), &panickingTool{}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Error, "panic while executing") {
		t.Errorf("expected panic converted to error, got %q", res.Error)
	}

	gate2 := NewApprovalGate()
	gate2.OnPresent = func(string, json.RawMessage, string) (bool, error) { return true, nil }
	res2, err2 := gate2.WrapExecution(context.Background(), &panickingPreviewerTool{}, json.RawMessage(`{}`))
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if !strings.Contains(res2.Error, "preview error") || !strings.Contains(res2.Error, "panic") {
		t.Errorf("expected preview panic converted to error, got %q", res2.Error)
	}
}

func TestRedactToolOutput_PatternTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"openai classic", "key: sk-abcdefghijklmnopqrstuvwx"},
		{"openai project", "sk-proj-AbCdEfGhIjKlMnOpQrStUvWx"},
		{"openai service account", "sk-svcacct-AbCdEfGhIjKlMnOpQrStUvWx"},
		{"github ghp", "ghp_" + strings.Repeat("aB3", 12)},
		{"github fine grained", "github_pat_" + strings.Repeat("a1_", 10)},
		{"slack xoxb", "xoxb-1234567890-abcdef"},
		{"slack xapp", "xapp-1234567890-abcdef"},
		{"aws id", "AKIAIOSFODNN7EXAMPLE"},
		{"stripe live", "sk_live_" + strings.Repeat("4eC39HqL", 3)},
		{"stripe restricted", "rk_live_" + strings.Repeat("4eC39HqL", 3)},
		{"sendgrid", "SG." + strings.Repeat("aB9_-", 6)},
		{"npm", "npm_" + strings.Repeat("a1", 12)},
		{"pypi", "pypi-" + strings.Repeat("a1_-", 6)},
		{"digitalocean", "dop_v1_" + strings.Repeat("a1", 12)},
		{"google oauth", "ya29." + strings.Repeat("aB_-", 6)},
		{"jwt", "eyJ" + strings.Repeat("a", 8) + "." + strings.Repeat("b", 8) + "." + strings.Repeat("c", 8)},
		{"json apiKey", `{"apiKey": "abcdef1234567890"}`},
		{"json password", `{"password": "hunter2hunter2"}`},
		{"env-style", "token: ABCDEFGHIJ1234567890"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, hits := RedactToolOutput(tc.in)
			if !strings.Contains(out, redactedMarker) {
				t.Errorf("expected %q redacted, got %q", tc.in, out)
			}
			if len(hits) == 0 {
				t.Errorf("expected hit recorded for %q", tc.in)
			}
		})
	}

	benign := []string{"the quick brown fox", "line count: 42", "TODO: refactor parser"}
	for _, b := range benign {
		out, hits := RedactToolOutput(b)
		if out != b || len(hits) != 0 {
			t.Errorf("expected %q untouched, got %q %v", b, out, hits)
		}
	}
}

// WrapExecution is the redaction boundary.
func TestFileReadRedactsViaDispatch(t *testing.T) {
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "config.txt"), []byte("api_key: ABCDEFGHIJ1234567890\nname: x\n"), 0o644)
	r := &FileReadTool{WorkspaceRoot: ws}
	gate := NewApprovalGate()
	res, err := gate.WrapExecution(context.Background(), r, json.RawMessage(`{"path":"config.txt"}`))
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error: %v %q", err, res.Error)
	}
	if !strings.Contains(res.Output, redactedMarker) {
		t.Errorf("expected redaction marker in file_read output, got %q", res.Output)
	}
}

func TestShellExecTimeoutEnforced(t *testing.T) {
	tool := &ShellExecTool{Timeout: time.Second, MaxOutputBytes: 4096}
	var cmdStr string
	if runtime.GOOS == "windows" {
		// ping orphans the pipe, which hangs Wait() without WaitDelay.
		cmdStr = "ping -n 30 127.0.0.1"
	} else {
		cmdStr = "sleep 30"
	}
	args, _ := json.Marshal(shellExecArgs{Command: cmdStr})
	start := time.Now()
	res, err := tool.Execute(context.Background(), args)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Error, "timed out") {
		t.Errorf("expected timeout error, got %q", res.Error)
	}
	if elapsed < 900*time.Millisecond || elapsed > 15*time.Second {
		t.Errorf("expected ~1s timeout, got %v", elapsed)
	}
}

func TestShellExecBackgroundedChildDoesNotHang(t *testing.T) {
	tool := &ShellExecTool{Timeout: 30 * time.Second, MaxOutputBytes: 4096}
	var cmdStr string
	if runtime.GOOS == "windows" {
		cmdStr = `start /b ping -n 30 127.0.0.1`
	} else {
		cmdStr = `sleep 30 &`
	}
	args, _ := json.Marshal(shellExecArgs{Command: cmdStr})
	start := time.Now()
	_, _ = tool.Execute(context.Background(), args)
	elapsed := time.Since(start)
	if elapsed > 15*time.Second {
		t.Errorf("backgrounded child kept Execute hanging past max_output/timeout grace: %v", elapsed)
	}
}

func TestCodeSearchOutputCapped(t *testing.T) {
	ws := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("needle match line\n")
	}
	os.WriteFile(filepath.Join(ws, "big.txt"), []byte(sb.String()), 0o644)
	c := &CodeSearchTool{WorkspaceRoot: ws}
	args, _ := json.Marshal(codeSearchArgs{Query: "needle", ContextLines: 5, MaxResults: 100})
	res, err := c.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error: %v %q", err, res.Error)
	}
	if len(res.Output) > maxSearchOutputBytes+256 {
		t.Errorf("expected output capped at %d bytes, got %d", maxSearchOutputBytes, len(res.Output))
	}
}
