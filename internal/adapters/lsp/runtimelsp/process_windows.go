//go:build windows

package runtimelsp

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// bindAsteriaToJob puts asteria into its own kill-on-close job once, so each process that asteria starts is created
// inside this job and Windows kills it when asteria dies.
//
//nolint:gochecknoglobals // The job binds the whole asteria process, and sync.OnceFunc makes it safe for all callers.
var bindAsteriaToJob = sync.OnceFunc(func() {
	job, err := newKillOnCloseJob()
	if err == nil {
		// The handle stays open for the life of asteria: Windows closes it when asteria ends and then kills the job.
		err = windows.AssignProcessToJobObject(job, windows.CurrentProcess())
		if err != nil {
			err = errors.Join(err, windows.CloseHandle(job))
		}
	}
	if err != nil {
		slog.WarnContext(context.Background(), "bind asteria to a kill-on-close job; started processes are bound "+
			"only after they start", "err", err)
	}
})

// processTree binds a started command to its own job object, so a forced stop terminates the whole tree, and
// Windows terminates it once the last handle of the job closes, including when asteria dies.
type processTree struct {
	job windows.Handle
}

// start starts cmd and puts it into a new job object that kills all processes of the job on close.
func (t *processTree) start(cmd *exec.Cmd) error {
	bindAsteriaToJob()

	job, err := newKillOnCloseJob()
	if err != nil {
		return err
	}

	if err = cmd.Start(); err != nil {
		return errors.Join(err, windows.CloseHandle(job))
	}

	if err = assignProcessToJob(job, cmd.Process); err != nil {
		// A process outside its job cannot be stopped as a tree, so it must not keep running; the caller reaps it.
		return errors.Join(err, cmd.Process.Kill(), windows.CloseHandle(job))
	}

	t.job = job

	return nil
}

// release closes the job handle, so Windows kills the processes that are left in the job.
func (t *processTree) release() error {
	if t.job == 0 {
		return nil
	}

	err := windows.CloseHandle(t.job)
	t.job = 0

	return err
}

// kill terminates all processes of the job of the started command, or only the process when it has no job.
func (t *processTree) kill(process *os.Process) error {
	if t.job == 0 {
		return process.Kill()
	}

	return windows.TerminateJobObject(t.job, processTreeKillExitCode)
}

// newKillOnCloseJob creates a job object whose processes end when its last handle closes.
func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), //nolint:gosec // SetInformationJobObject takes the address of this struct.
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return 0, errors.Join(err, windows.CloseHandle(job))
	}

	return job, nil
}

// assignProcessToJob puts a started process into the job.
func assignProcessToJob(job windows.Handle, process *os.Process) error {
	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(process.Pid), //nolint:gosec // Windows process IDs are DWORD values, so the conversion is lossless.
	)
	if err != nil {
		return err
	}

	return errors.Join(windows.AssignProcessToJobObject(job, handle), windows.CloseHandle(handle))
}
