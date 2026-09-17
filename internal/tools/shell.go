package tools

import (
	"bytes"
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
}

type shellExecArgs struct {
	Command string `json:"command"`
}

func (s *ShellExecTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "shell_exec",
		Description: "Execute a shell command and return its stdout and stderr. Use this to run build commands, tests, list files, inspect the system, etc. The command runs in the user's shell within the workspace directory.",
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

	// Set working directory to workspace root.
	if s.WorkspaceRoot != "" {
		cmd.Dir = s.WorkspaceRoot
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	maxOutput := s.maxOutput()
	stdoutStr := truncateOutput(stdout.String(), maxOutput)
	stderrStr := truncateOutput(stderr.String(), maxOutput)

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

	if err != nil {
		// Distinguish timeout from other errors.
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

func truncateOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n... (output truncated)"
}
