//go:build windows

package runner

import (
	"os/exec"
	"strconv"
	"syscall"
)

const createNewProcessGroup = 0x00000200

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

func terminateProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// cmd.Process.Kill() terminates only the launched process, leaving the
	// agent's child process tree orphaned on timeout/cancel. Use taskkill /T to
	// kill the whole tree (the Unix path does the equivalent via the process
	// group). Fall back to Kill if taskkill is unavailable.
	pid := cmd.Process.Pid
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
