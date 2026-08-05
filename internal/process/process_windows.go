//go:build windows

package process

import (
	"os/exec"
	"strconv"
	"syscall"
)

var generateConsoleCtrlEvent = syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func gracefulStop(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// The child is created as its own process group, allowing Ctrl+Break to be
	// delivered to the group without a shell or CGO.
	result, _, callErr := generateConsoleCtrlEvent.Call(1, uintptr(cmd.Process.Pid))
	if result == 0 {
		return callErr
	}
	return nil
}

func forceStop(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// taskkill /T is the most reliable built-in way to terminate descendants.
	kill := exec.Command("taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	kill.Stdout = nil
	kill.Stderr = nil
	return kill.Run()
}
