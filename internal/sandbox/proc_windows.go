//go:build windows

package sandbox

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW          = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject   = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject  = kernel32.NewProc("AssignProcessToJobObject")
)

const (
	JobObjectExtendedLimitInformation  = 9
	JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x00002000
	PROCESS_SET_QUOTA                  = 0x0100
	PROCESS_TERMINATE                  = 0x0001
)

type JOBOBJECT_BASIC_LIMIT_INFORMATION struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type JOBOBJECT_EXTENDED_LIMIT_INFORMATION struct {
	BasicLimitInformation  JOBOBJECT_BASIC_LIMIT_INFORMATION
	IoInfo                 [96]byte // IO_COUNTERS
	ProcessMemoryLimit     uintptr
	JobMemoryLimit         uintptr
	PeakProcessMemoryLimit uintptr
	PeakJobMemoryLimit     uintptr
}

var (
	jobsMu sync.Mutex
	jobs   = make(map[*exec.Cmd]syscall.Handle)
)

func setProcGroup(cmd *exec.Cmd) {
	job, _, _ := procCreateJobObjectW.Call(0, 0)
	if job != 0 {
		var info JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE

		_, _, _ = procSetInformationJobObject.Call(job, JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Sizeof(info)))

		jobsMu.Lock()
		jobs[cmd] = syscall.Handle(job)
		jobsMu.Unlock()
	}
}

func killProcGroup(cmd *exec.Cmd) error {
	jobsMu.Lock()
	job, ok := jobs[cmd]
	if ok {
		delete(jobs, cmd)
	}
	jobsMu.Unlock()

	if ok {
		_ = syscall.CloseHandle(job)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}

func runCommand(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}

	jobsMu.Lock()
	job, ok := jobs[cmd]
	jobsMu.Unlock()

	if ok {
		hProcess, err := syscall.OpenProcess(PROCESS_SET_QUOTA|PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if err == nil {
			_, _, _ = procAssignProcessToJobObject.Call(uintptr(job), uintptr(hProcess))
			_ = syscall.CloseHandle(hProcess)
		}
	}

	err := cmd.Wait()

	jobsMu.Lock()
	if job, ok := jobs[cmd]; ok {
		delete(jobs, cmd)
		_ = syscall.CloseHandle(job)
	}
	jobsMu.Unlock()

	return err
}
