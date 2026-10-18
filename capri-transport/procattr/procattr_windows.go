//go:build windows

// Package procattr suppresses the console window Windows would otherwise
// create for a child process.
package procattr

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW. The child still gets stdio pipes, but
// its console has no visible window.
const createNoWindow = 0x08000000

// HideConsole marks cmd so starting it creates no console window. Safe to call
// on a command that already has a SysProcAttr; the flag is merged.
func HideConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
