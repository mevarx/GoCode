//go:build unix

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SECURITY: a timeout must kill the shell's whole process group. A backgrounded
// child that outlives the shell keeps doing work after the agent gave up.
func TestShellExecTimeoutKillsProcessGroup(t *testing.T) {
	assertShellTerminationKillsChild(t, time.Second, 0)
}

// SECURITY: cancellation is the same trust boundary as a timeout.
func TestShellExecCancellationKillsProcessGroup(t *testing.T) {
	const cancelAfter = 200 * time.Millisecond
	assertShellTerminationKillsChild(t, 30*time.Second, cancelAfter)
}

// assertShellTerminationKillsChild runs a shell whose backgrounded child records
// its PID, waits for Execute to return, and then requires that child to be gone.
func assertShellTerminationKillsChild(t *testing.T, timeout, cancelAfter time.Duration) {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	t.Cleanup(func() {
		// Best effort: never leave the sleeper behind when an assertion fails.
		if pid, err := readPIDFile(pidFile); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if cancelAfter > 0 {
		go func() {
			timer := time.NewTimer(cancelAfter)
			defer timer.Stop()
			select {
			case <-timer.C:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	tool := &ShellExecTool{Timeout: timeout, WorkspaceRoot: dir, MaxOutputBytes: 4096}
	// `wait` keeps the shell alive; the backgrounded sleep is the witness that
	// must not survive termination.
	command := fmt.Sprintf("sleep 30 & echo $! > %s; wait", shellQuote(pidFile))
	args, err := json.Marshal(shellExecArgs{Command: command})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	start := time.Now()
	res, execErr := tool.Execute(ctx, args)
	elapsed := time.Since(start)
	if execErr != nil {
		t.Fatalf("unexpected error: %v", execErr)
	}

	if cancelAfter > 0 {
		if !strings.Contains(res.Error, "cancelled") {
			t.Errorf("expected cancellation error, got %q", res.Error)
		}
		if elapsed > 10*time.Second {
			t.Errorf("cancellation took too long: %v", elapsed)
		}
	} else {
		if !strings.Contains(res.Error, "timed out") {
			t.Errorf("expected timeout error, got %q", res.Error)
		}
		if elapsed > 10*time.Second {
			t.Errorf("timeout took too long: %v", elapsed)
		}
	}

	pid := waitForChildPID(t, pidFile)
	waitForProcessExit(t, pid)
}

// shellQuote wraps s in POSIX single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func readPIDFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, err
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d in %s", pid, path)
	}
	return pid, nil
}

func waitForChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if pid, err := readPIDFile(path); err == nil {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("backgrounded child never wrote its pid to %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForProcessExit requires kill(2) to report ESRCH, i.e. the child is gone
// rather than merely stopped.
func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		switch {
		case errors.Is(err, syscall.ESRCH):
			return
		case err == nil, errors.Is(err, syscall.EPERM):
			// Still alive (or a live zombie we cannot signal); keep polling.
		default:
			t.Fatalf("kill(%d, 0): %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d survived shell termination", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
