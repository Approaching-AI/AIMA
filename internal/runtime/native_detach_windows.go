//go:build windows

package runtime

import "os/exec"

func configureDetachedProcess(cmd *exec.Cmd) {}

func childProcessGroupID(pid int) int { return 0 }

func terminateProcessGroup(pgid int) error { return nil }

func killProcessGroup(pgid int) error { return nil }
