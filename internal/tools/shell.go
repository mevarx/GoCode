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
	Timeout time.Duration
}

type shellExecArgs struct {
	Command string `json:"command"`
}

func (s *ShellExecTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "shell_exec",
		Description: "Execute a shell command and return its stdout and stderr. Use this to run build commands, tests, list files, inspect the system, etc. The command runs in the user's shell.",
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

func (s *ShellExecTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a shellExecArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Error: fmt.Sprintf("invalid arguments: %v", err)}, nil
	}

	if strings.TrimSpace(a.Command) == "" {
		return Result{Error: "command cannot be empty"}, nil
	}

	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(timeoutCtx, "cmd", "/C", a.Command)
	} else {
		cmd = exec.CommandContext(timeoutCtx, "sh", "-c", a.Command)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var output strings.Builder
	if stdout.Len() > 0 {
		output.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		output.WriteString("STDERR:\n")
		output.WriteString(stderr.String())
	}

	result := Result{
		Output: output.String(),
	}

	if err != nil {
		result.Error = fmt.Sprintf("command failed: %v", err)
		if output.Len() > 0 {
			result.Error = fmt.Sprintf("command failed: %v\n%s", err, output.String())
		}
	}

	return result, nil
}
