//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"syscall"
)

// createNewProcessGroup is the Windows CREATE_NEW_PROCESS_GROUP creation flag.
const createNewProcessGroup = 0x00000200

// configureShellProcess isolates the shell in its own process group.
func configureShellProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// interruptShellProcess force-terminates the shell and all of its descendants.
// taskkill is the only way to walk the child tree on Windows; if it fails (not
// on PATH, policy), the shell itself is still killed.
func interruptShellProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	taskkill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	taskkill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	// SECURITY: the helper never sees provider credentials either.
	taskkill.Env = ScrubbedEnv(nil)
	if err := taskkill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// killShellProcess escalates to killing the shell directly. interruptShellProcess
// already used taskkill /T, which is the only handle Windows gives us on the
// rest of the tree.
func killShellProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
