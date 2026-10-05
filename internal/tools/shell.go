package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type ShellExecTool struct {
	Timeout        time.Duration
	WorkspaceRoot  string
	MaxOutputBytes int
	// Guard redacts secrets and warns about sensitive paths in previews. May be nil.
	Guard *ShellGuard
}

type shellExecArgs struct {
	Command string `json:"command"`
}

func (s *ShellExecTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "shell_exec",
		Description: "Execute a shell command and return its stdout and stderr. Use this to run build commands, tests, list files, inspect the system, etc. The command runs in the user's shell with the workspace as its working directory. The shell is NOT confined to the workspace: it can read any file the user can. Do not use it to read credential or secret files — use file_read, which enforces the workspace boundary and sensitive-file protection.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["command"],
			"properties": {
				"command": {
					"type": "string",
					"description": "The shell command to execute"
				}
			}
		}`),
	}
}

func (s *ShellExecTool) RequiresApproval() bool {
	return true
}

// Preview returns command execution metadata without running the command.
func (s *ShellExecTool) Preview(ctx context.Context, args json.RawMessage) (Preview, error) {
	var a shellExecArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Preview{}, fmt.Errorf("invalid arguments: %v", err)
	}

	if strings.TrimSpace(a.Command) == "" {
		return Preview{}, fmt.Errorf("command cannot be empty")
	}

	timeout := s.timeout()
	workDir := s.WorkspaceRoot
	if workDir == "" {
		workDir = "(current directory)"
	}

	desc := fmt.Sprintf("Command: %s\nWorking directory: %s\nTimeout: %s", a.Command, workDir, timeout)
	desc += DescribeFlags(s.Guard.FlagSensitivePaths(a.Command))
	return Preview{
		Description: desc,
		Command:     a.Command,
		WorkDir:     workDir,
	}, nil
}

func (s *ShellExecTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a shellExecArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Error: fmt.Sprintf("invalid arguments: %v", err)}, nil
	}

	if strings.TrimSpace(a.Command) == "" {
		return Result{Error: "command cannot be empty"}, nil
	}

	timeout := s.timeout()
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(timeoutCtx, "cmd", "/C", a.Command)
	} else {
		cmd = exec.CommandContext(timeoutCtx, "sh", "-c", a.Command)
	}

	if s.WorkspaceRoot != "" {
		cmd.Dir = s.WorkspaceRoot
	}

	// SECURITY: WaitDelay bounds Wait() so a backgrounded grandchild inheriting the pipe cannot hang Execute.
	cmd.WaitDelay = waitDelayGrace

	// SECURITY: cap capture during execution so a command emitting gigabytes is never fully buffered.
	maxOutput := s.maxOutput()
	stdoutBuf := &cappedWriter{max: maxOutput}
	stderrBuf := &cappedWriter{max: maxOutput}
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	err := cmd.Run()

	stdoutStr := stdoutBuf.String()
	if stdoutBuf.capped {
		stdoutStr += "\n... (output truncated)"
	}
	stderrStr := stderrBuf.String()
	if stderrBuf.capped {
		stderrStr += "\n... (output truncated)"
	}

	// SECURITY: redact credentials before they reach the model or transcript.
	var redactions []string
	if s.Guard != nil {
		var hits []string
		stdoutStr, hits = s.Guard.RedactSecrets(stdoutStr)
		redactions = append(redactions, hits...)
		stderrStr, hits = s.Guard.RedactSecrets(stderrStr)
		redactions = append(redactions, hits...)
	}

	var output strings.Builder
	if len(stdoutStr) > 0 {
		output.WriteString("STDOUT:\n")
		output.WriteString(stdoutStr)
	}
	if len(stderrStr) > 0 {
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		output.WriteString("STDERR:\n")
		output.WriteString(stderrStr)
	}

	result := Result{
		Output: output.String(),
	}
	result.Output += DescribeRedactions(redactions)

	if err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			result.Error = fmt.Sprintf("command timed out after %s", timeout)
		} else if ctx.Err() == context.Canceled {
			result.Error = "command was cancelled"
		} else {
			result.Error = fmt.Sprintf("command failed: %v", err)
		}
		if output.Len() > 0 {
			result.Error = fmt.Sprintf("%s\n%s", result.Error, output.String())
		}
	}

	return result, nil
}

// waitDelayGrace bounds pipe release after exit/cancellation.
const waitDelayGrace = 2 * time.Second

// cappedWriter discards bytes beyond max instead of buffering them.
type cappedWriter struct {
	buf    []byte
	max    int
	capped bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.max > 0 && len(w.buf) >= w.max {
		w.capped = true
		return len(p), nil
	}
	if w.max > 0 {
		if room := w.max - len(w.buf); len(p) > room {
			w.buf = append(w.buf, p[:room]...)
			w.capped = true
			return len(p), nil
		}
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *cappedWriter) String() string {
	return string(w.buf)
}

func (s *ShellExecTool) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 30 * time.Second
}

func (s *ShellExecTool) maxOutput() int {
	if s.MaxOutputBytes > 0 {
		return s.MaxOutputBytes
	}
	return 1024 * 1024 // 1MB default
}
