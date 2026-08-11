//go:build windows

package runtime

import (
	"os/exec"
	"syscall"
)

// Win32 process-creation flags not exported by the syscall package.
const (
	createNewProcessGroup = 0x00000200 // CREATE_NEW_PROCESS_GROUP
	detachedProcess       = 0x00000008 // DETACHED_PROCESS
)

// configureDetachedProcess makes a directly-launched engine self-standing:
// no inherited console (so no window flashes on an interactive desktop, and the
// engine is not torn down with AIMA's console) and its own process group.
func configureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
		HideWindow:    true,
	}
}

func childProcessGroupID(pid int) int { return 0 }

func terminateProcessGroup(pgid int) error { return nil }

func killProcessGroup(pgid int) error { return nil }
