//go:build !windows

package runtime

import (
	"fmt"
	"os/exec"
	"syscall"
)

func configureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func childProcessGroupID(pid int) int {
	if pid <= 0 {
		return 0
	}
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 0 {
		return pgid
	}
	return pid
}

// terminateProcessGroup sends SIGTERM to the process group, giving the
// application a chance to clean up (e.g., sglang-kt's kill_process_tree).
func terminateProcessGroup(pgid int) error {
	if pgid <= 0 {
		return fmt.Errorf("invalid process group id %d", pgid)
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

func killProcessGroup(pgid int) error {
	if pgid <= 0 {
		return fmt.Errorf("invalid process group id %d", pgid)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
