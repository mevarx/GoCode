//go:build unix

package tools

import (
	"os/exec"
	"syscall"
)

// configureShellProcess makes the shell the leader of a new process group, so a
// single negative-PID signal reaches every descendant it spawns.
func configureShellProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interruptShellProcess asks the shell's whole process group to exit.
func interruptShellProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// A negative pid targets the process group led by the shell, so
	// backgrounded children receive the signal too.
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// killShellProcess unconditionally kills the shell's process group.
func killShellProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
