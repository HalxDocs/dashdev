//go:build windows

package platform

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Job is a Windows Job Object holding one service's process tree. Closing
// the last handle to a job configured with kill-on-close terminates every
// process in it, which is what stops grandchildren (a `go run` binary, a
// shell's dev server) from outliving their service and hogging its ports.
type Job struct {
	handle windows.Handle
}

// NewJob creates an empty job that kills its processes when closed.
func NewJob() (Job, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return Job{}, fmt.Errorf("platform: cannot create a job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err = windows.SetInformationJobObject(
		handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return Job{}, fmt.Errorf("platform: cannot arm kill-on-close: %w", err)
	}
	return Job{handle: handle}, nil
}

// Assign puts an already started process into the job. It must run before
// the process can meaningfully spawn children; the window between process
// start and assignment is inherent to the API.
func (j Job) Assign(proc *os.Process) error {
	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(proc.Pid),
	)
	if err != nil {
		return fmt.Errorf("platform: cannot open pid %d: %w", proc.Pid, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	if err := windows.AssignProcessToJobObject(j.handle, handle); err != nil {
		return fmt.Errorf("platform: cannot contain pid %d in a job object: %w", proc.Pid, err)
	}
	return nil
}

// Close releases the job. With kill-on-close armed, any process still in
// the job is terminated by the operating system. It is safe to call more
// than once.
func (j *Job) Close() error {
	if j.handle == 0 {
		return nil
	}
	handle := j.handle
	j.handle = 0
	return windows.CloseHandle(handle)
}
