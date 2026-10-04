//go:build windows

package keeper

import (
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// terminate on Windows is a hard kill - there's no SIGTERM. The runner
// tolerates it: sessions are saved every 2s and agents exit when their
// stdin closes (see internal/session/store.go).
func terminate(p *os.Process) { _ = p.Kill() }

// RelaunchSelf starts the freshly installed keeperd as a detached process
// and exits. Nothing restarts keeperd on Windows (the scheduled task only
// fires at boot), so it must hand over to its successor itself.
func RelaunchSelf(path string) error {
	if err := startDetached(path); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

// RelaunchWatcher restarts `keeperd -watch-input` from path (the freshly
// installed keeperd) and exits, so the old watcher stops holding the
// previous binary open - see clearOld.
func RelaunchWatcher(path string) error {
	if err := startDetached(path, "-watch-input"); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func startDetached(path string, args ...string) error {
	const detachedProcess = 0x00000008
	const createNoWindow = 0x08000000
	// Task Scheduler runs its task inside a job object; breaking away keeps
	// the successor alive when this process (the task's) exits. Not every
	// job allows breakaway, so fall back to a plain detached start.
	const breakawayFromJob = 0x01000000
	base := uint32(detachedProcess | createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP)
	var err error
	for _, flags := range []uint32{base | breakawayFromJob, base} {
		cmd := exec.Command(path, args...)
		cmd.Env = os.Environ()
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		if err = cmd.Start(); err == nil {
			break
		}
	}
	return err
}

// hideWindow keeps the runner child from opening a console window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// killJob is a job object that Windows kills, with everything in it, once
// its last handle closes - i.e. whenever keeperd exits, however it exits.
// Without it a stopped or crashed keeperd leaves the runner holding the port,
// and the next keeperd's runner can't start. The handle isn't inheritable, so
// the runner doesn't keep the job alive itself.
var (
	killJobOnce sync.Once
	killJob     windows.Handle
)

// bindToKeeper puts the runner into killJob; the agents it spawns inherit
// the job. Failure only loses the orphan protection, so it's just logged.
func bindToKeeper(p *os.Process) {
	killJobOnce.Do(func() {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			log.Printf("keeper: create job object: %v", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			log.Printf("keeper: set job kill-on-close: %v", err)
			windows.CloseHandle(job)
			return
		}
		killJob = job
	})
	if killJob == 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		log.Printf("keeper: open runner process: %v", err)
		return
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(killJob, h); err != nil {
		log.Printf("keeper: assign runner to job: %v", err)
	}
}
