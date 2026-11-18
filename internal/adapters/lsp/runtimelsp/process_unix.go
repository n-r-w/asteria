//go:build unix

package runtimelsp

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// processTree binds a started command to asteria through a lifeline pipe that only asteria can write to.
type processTree struct {
	lifeline *os.File
}

// start runs cmd through the guard shell that kills the process group of cmd once the lifeline closes.
func (t *processTree) start(cmd *exec.Cmd) error {
	// A failed executable lookup must keep its standard error, so such a command starts unchanged and fails.
	if cmd.Err != nil {
		return cmd.Start()
	}

	lifelineReader, lifelineWriter, err := os.Pipe()
	if err != nil {
		return err
	}

	// The guard receives the resolved command as positional parameters, so no argument reaches the script text.
	cmd.Args = append(
		[]string{processGuardShell, "-c", processGuardScript, processGuardName, cmd.Path},
		cmd.Args[1:]...,
	)
	cmd.Path = processGuardShell
	cmd.ExtraFiles = []*os.File{lifelineReader}
	cmd.SysProcAttr = new(syscall.SysProcAttr)
	cmd.SysProcAttr.Setpgid = true

	startErr := cmd.Start()
	// The guard owns its own copy of the read end; the copy of asteria must close so that only the guard holds it.
	_ = lifelineReader.Close()
	if startErr != nil {
		_ = lifelineWriter.Close()
		return startErr
	}

	t.lifeline = lifelineWriter

	return nil
}

// release closes the lifeline, so the guard kills the processes that are left in the group and exits.
func (t *processTree) release() error {
	if t.lifeline == nil {
		return nil
	}

	err := t.lifeline.Close()
	t.lifeline = nil

	return err
}

// kill sends SIGKILL to the whole process group of the started command.
func (*processTree) kill(process *os.Process) error {
	err := syscall.Kill(-process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}

	return err
}
