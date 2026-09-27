package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/ignore"
)

func newTestGuard(t *testing.T) *ShellGuard {
	t.Helper()
	m := ignore.NewSensitiveMatcher(nil, nil)
	return &ShellGuard{SensitiveMatcher: m, RedactSecretOutput: true}
}

// Test credentials are assembled at runtime from these fragments.
//
// Writing the literals inline made secret scanners flag every commit: strings
// beginning with a provider prefix and followed by enough alphanumerics look
// exactly like live credentials to a detector, even when the body is obviously
// fake. Building them here keeps the suite honest without shipping anything
// that trips a scanner on every push. These are not real credentials and are
// not derived from any.
var (
	fragAnthropic = "sk" + "-ant-" + strings.Repeat("A", 24)
	fragOpenAI    = "sk" + "-" + strings.Repeat("B", 32)
	fragGitHub    = "gh" + "p" + "_" + strings.Repeat("C", 36)
	fragGoogle    = "AI" + "za" + strings.Repeat("D", 35)
	fragAWS       = "AK" + "IA" + strings.Repeat("E", 16)
	fragJWT       = "eyJ" + strings.Repeat("G", 12) + "." + strings.Repeat("H", 12) + "." + strings.Repeat("I", 12)
)

func TestShellGuardRedactsKnownSecretShapes(t *testing.T) {
	guard := newTestGuard(t)

	cases := []struct {
		name    string
		input   string
		wantHit string
		absent  string
	}{
		{"anthropic key", "ANTHROPIC_API_KEY=" + fragAnthropic, "anthropic-api-key", fragAnthropic},
		{"openai key", "key: " + fragOpenAI, "openai-api-key", fragOpenAI},
		{"github token", "token " + fragGitHub, "github-token", fragGitHub},
		{"google key", fragGoogle, "google-api-key", fragGoogle},
		{"aws access key", "AWS_ACCESS_KEY_ID=" + fragAWS, "aws-access-key-id", fragAWS},
		{"jwt", "auth=" + fragJWT, "jwt", fragJWT},
		{"assigned secret", "api_key = 9f8b7a6d5e4f3a2b1c0d", "assigned-secret", "9f8b7a6d5e4f3a2b1c0d"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, hits := guard.RedactSecrets(tc.input)
			if strings.Contains(out, tc.absent) {
				t.Errorf("secret not redacted: %q", out)
			}
			if !strings.Contains(out, redactedMarker) {
				t.Errorf("expected marker in output, got %q", out)
			}
			found := false
			for _, h := range hits {
				if h == tc.wantHit {
					found = true
				}
			}
			if !found {
				t.Errorf("expected hit %q, got %v", tc.wantHit, hits)
			}
		})
	}
}

func TestShellGuardRedactsPrivateKeyBlock(t *testing.T) {
	guard := newTestGuard(t)
	input := "-----BEGIN RSA PRIVATE KEY-----\nMIIEow...\n-----END RSA PRIVATE KEY-----"
	out, hits := guard.RedactSecrets(input)
	if strings.Contains(out, "MIIEow") {
		t.Errorf("private key body not redacted: %q", out)
	}
	if len(hits) == 0 {
		t.Error("expected private-key-block hit")
	}
}

func TestShellGuardLeavesBenignOutputAlone(t *testing.T) {
	guard := newTestGuard(t)
	benign := []string{
		"go build ./...",
		"PASS\nok  github.com/mevarx/GoCode/internal/tools  2.103s",
		"total 24\ndrwxr-xr-x 3 user staff 96 Jan 1 12:00 .",
		"password: short",
		"version = 1.0.3",
	}
	for _, in := range benign {
		out, hits := guard.RedactSecrets(in)
		if out != in {
			t.Errorf("benign output modified\n in: %q\nout: %q", in, out)
		}
		if len(hits) != 0 {
			t.Errorf("benign output flagged: %v (in=%q)", hits, in)
		}
	}
}

func TestShellGuardRedactionDisabled(t *testing.T) {
	guard := &ShellGuard{SensitiveMatcher: ignore.NewSensitiveMatcher(nil, nil), RedactSecretOutput: false}
	in := fragAnthropic
	out, hits := guard.RedactSecrets(in)
	if out != in {
		t.Errorf("expected output unchanged when disabled, got %q", out)
	}
	if len(hits) != 0 {
		t.Errorf("expected no hits when disabled, got %v", hits)
	}
}

func TestShellGuardNilReceiverSafe(t *testing.T) {
	var guard *ShellGuard
	if out, hits := guard.RedactSecrets(fragAnthropic); out == "" || hits != nil {
		t.Errorf("nil guard should pass through, got %q / %v", out, hits)
	}
	if flagged := guard.FlagSensitivePaths("cat .env"); flagged != nil {
		t.Errorf("nil guard should return no flags, got %v", flagged)
	}
}

func TestShellGuardFlagsSensitivePaths(t *testing.T) {
	guard := newTestGuard(t)

	cases := []struct {
		command string
		want    string
	}{
		{"cat .env", ".env"},
		{"cat ./secrets/.env", ".env"},
		{"type config/id_rsa", "id_rsa"},
		{"openssl rsa -in server.pem", "server.pem"},
		{"cat ~/.aws/credentials", "credentials"},
	}
	for _, tc := range cases {
		flagged := guard.FlagSensitivePaths(tc.command)
		found := false
		for _, f := range flagged {
			if f == tc.want || filepath.Base(f) == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("command %q: expected flag for %q, got %v", tc.command, tc.want, flagged)
		}
	}
}

func TestShellGuardDoesNotFlagOrdinaryCommands(t *testing.T) {
	guard := newTestGuard(t)
	for _, cmd := range []string{"go test ./...", "ls -la", "git commit -m 'x'", "make build"} {
		if flagged := guard.FlagSensitivePaths(cmd); len(flagged) != 0 {
			t.Errorf("ordinary command %q flagged: %v", cmd, flagged)
		}
	}
}

func TestShellGuardDescribeHelpers(t *testing.T) {
	if s := DescribeFlags(nil); s != "" {
		t.Errorf("expected empty for no flags, got %q", s)
	}
	s := DescribeFlags([]string{".env"})
	if !strings.Contains(s, ".env") || !strings.Contains(s, "not confined") {
		t.Errorf("flag description missing content: %q", s)
	}
	if s := DescribeRedactions(nil); s != "" {
		t.Errorf("expected empty for no redactions, got %q", s)
	}
	r := DescribeRedactions([]string{"openai-api-key"})
	if !strings.Contains(r, "redacted") || !strings.Contains(r, "openai-api-key") {
		t.Errorf("redaction description missing content: %q", r)
	}
}

// TestShellExecRedactsRealCommandOutput exercises the guard end-to-end through
// the tool itself, using a real command rather than a stub.
func TestShellExecRedactsRealCommandOutput(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("requires POSIX shell")
	}
	dir := t.TempDir()
	guard := &ShellGuard{SensitiveMatcher: ignore.NewSensitiveMatcher(nil, nil), RedactSecretOutput: true}
	tool := &ShellExecTool{Timeout: 10_000_000_000, WorkspaceRoot: dir, Guard: guard}

	args, _ := json.Marshal(shellExecArgs{Command: "echo " + fragAnthropic})
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected tool error: %s", res.Error)
	}
	if strings.Contains(res.Output, fragAnthropic) {
		t.Errorf("secret leaked through shell_exec: %q", res.Output)
	}
	if !strings.Contains(res.Output, redactedMarker) {
		t.Errorf("expected redaction marker, got %q", res.Output)
	}
}

// TestShellExecPreviewWarnsOnSensitiveCommand proves the advisory warning is
// surfaced at approval time, which is the only real protection the shell has.
func TestShellExecPreviewWarnsOnSensitiveCommand(t *testing.T) {
	dir := t.TempDir()
	guard := &ShellGuard{SensitiveMatcher: ignore.NewSensitiveMatcher(nil, nil), RedactSecretOutput: true}
	tool := &ShellExecTool{Timeout: 1, WorkspaceRoot: dir, Guard: guard}

	args, _ := json.Marshal(shellExecArgs{Command: "cat .env"})
	p, err := tool.Preview(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(p.String(), "WARNING") || !strings.Contains(p.String(), ".env") {
		t.Errorf("preview missing sensitive-path warning: %q", p.String())
	}
}

// TestShellExecNilGuardStillWorks ensures the guard is genuinely optional and
// does not change existing behaviour when absent.
func TestShellExecNilGuardStillWorks(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("requires POSIX shell")
	}
	dir := t.TempDir()
	tool := &ShellExecTool{Timeout: 10_000_000_000, WorkspaceRoot: dir}
	args, _ := json.Marshal(shellExecArgs{Command: "echo hello"})
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Output, "hello") {
		t.Errorf("expected passthrough output, got %q", res.Output)
	}
	args2, _ := json.Marshal(shellExecArgs{Command: "cat .env"})
	p, err := tool.Preview(context.Background(), args2)
	if err != nil {
		t.Fatalf("unexpected preview error: %v", err)
	}
	if !strings.Contains(p.String(), "cat .env") {
		t.Errorf("expected command in preview, got %q", p.String())
	}
}
