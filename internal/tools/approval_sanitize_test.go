package tools

import (
	"bufio"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout swaps os.Stdout for a pipe while fn runs.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(out)
}

func TestRequestApprovalSanitizesUntrustedText(t *testing.T) {
	gate := NewApprovalGate()

	t.Run("preview and unparseable args", func(t *testing.T) {
		gate.SetInputReader(bufio.NewReader(strings.NewReader("y\n")))
		const spoof = "SPOOFED Approve? [y/N]"
		// Invalid JSON (raw ESC) exercises the raw-args fallback.
		args := []byte("{\"command\":\"echo hi\x1b[2J\"}")
		preview := "\x1b[2J\x1b[1;1H" + spoof

		var out string
		var approved bool
		var err error
		out = captureStdout(t, func() {
			approved, err = gate.RequestApproval("shell_\x1b[2Jexec", args, preview)
		})
		if err != nil {
			t.Fatalf("RequestApproval: %v", err)
		}
		if !approved {
			t.Fatal("expected 'y' answer to approve")
		}
		if strings.ContainsRune(out, '\x1b') {
			t.Fatalf("approval output contains ESC: %q", out)
		}
		if !strings.Contains(out, spoof) {
			t.Fatalf("visible spoofed text lost: %q", out)
		}
		if !strings.Contains(out, "─── preview begin ───") || !strings.Contains(out, "─── preview end ───") {
			t.Fatalf("preview block delimiters missing: %q", out)
		}
		// The injected text cannot reach the prompt line itself.
		if strings.Count(out, "Approve? [") != 2 {
			t.Fatalf("expected the real prompt plus one visible spoof occurrence, got %d: %q",
				strings.Count(out, "Approve? ["), out)
		}
	})

	t.Run("parsed arg values", func(t *testing.T) {
		gate.SetInputReader(bufio.NewReader(strings.NewReader("y\n")))
		args := []byte(`{"command":"echo \u001b[2J hi","path":"\u001b]52;c;SGVsbG8=\u0007file"}`)
		var out string
		var approved bool
		out = captureStdout(t, func() {
			var err error
			approved, err = gate.RequestApproval("shell_exec", args, "plain preview")
			if err != nil {
				t.Errorf("RequestApproval: %v", err)
			}
		})
		if !approved {
			t.Fatal("expected 'y' answer to approve")
		}
		if strings.ContainsRune(out, '\x1b') {
			t.Fatalf("approval output contains ESC: %q", out)
		}
		if !strings.Contains(out, "echo ") || !strings.Contains(out, " hi") {
			t.Fatalf("visible arg text lost: %q", out)
		}
	})
}

func TestRequestApprovalReadsInjectedAnswer(t *testing.T) {
	gate := NewApprovalGate()
	gate.SetInputReader(bufio.NewReader(strings.NewReader("y\n")))
	var approved bool
	captureStdout(t, func() {
		var err error
		approved, err = gate.RequestApproval("noop", nil, "")
		if err != nil {
			t.Errorf("RequestApproval: %v", err)
		}
	})
	if !approved {
		t.Fatal("expected approval from injected reader")
	}
}
