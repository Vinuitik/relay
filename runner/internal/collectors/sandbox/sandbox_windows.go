//go:build windows

// Windows sandbox: one AppContainer profile ("relay.collector") with NO
// capabilities (so no network), run as a Less Privileged AppContainer (LPAC)
// when Python starts under it, plain AppContainer otherwise (decided once by
// the availability probe). Filesystem access is granted per run by adding
// ACEs for the AppContainer SID; limits come from a Job Object.
//
// Paths are NOT remapped: the caller must pass host paths in Spec.Args
// (e.g. "--inputs C:\...\in"), unlike bwrap's "/in".
package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	profileName = "relay.collector"

	procThreadAttributeSecurityCapabilities      = 0x00020009
	procThreadAttributeAllApplicationPackagesPol = 0x0002000F
	processCreationAllApplicationPackagesOptOut  = 1

	hresultAlreadyExists = 0x800700B7 // HRESULT_FROM_WIN32(ERROR_ALREADY_EXISTS)

	fileAllAccess = 0x1F01FF
	// Read + traverse; traverse matters for nested input dirs (aw/...).
	fileReadExec = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE

	defaultTimeout  = 60 * time.Second
	defaultMemoryMB = 256
	maxOutput       = 4 << 20 // per stream; the rest is discarded
	cpuRatePercent  = 50      // hard cap of total machine CPU

	probeRetryAfter = time.Minute

	// PythonEnv overrides which interpreter the availability probe uses.
	// The engine should point Spec.Python at the same one.
	PythonEnv = "RELAY_COLLECTOR_PYTHON"
)

var (
	userenv                         = windows.NewLazySystemDLL("userenv.dll")
	procCreateAppContainerProfile   = userenv.NewProc("CreateAppContainerProfile")
	procDeriveAppContainerSidFromAC = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
)

// securityCapabilities mirrors SECURITY_CAPABILITIES:
// { PSID AppContainerSid; PSID_AND_ATTRIBUTES Capabilities; DWORD CapabilityCount; DWORD Reserved; }
type securityCapabilities struct {
	AppContainerSid *windows.SID
	Capabilities    *windows.SIDAndAttributes
	CapabilityCount uint32
	Reserved        uint32
}

type jobUIRestrictions struct{ UIRestrictionsClass uint32 }

type jobCPURate struct {
	ControlFlags uint32
	CpuRate      uint32 // percent * 100
}

func kind() string { return "appcontainer" }

// ---- profile ----

var (
	sidOnce sync.Once
	acSID   *windows.SID
	sidErr  error
)

// containerSID creates the profile on first use, or derives the SID if it
// already exists. The SID is kept for the process lifetime (never freed).
func containerSID() (*windows.SID, error) {
	sidOnce.Do(func() {
		if err := procCreateAppContainerProfile.Find(); err != nil {
			sidErr = fmt.Errorf("userenv: %w", err)
			return
		}
		name, _ := windows.UTF16PtrFromString(profileName)
		display, _ := windows.UTF16PtrFromString("Relay KPI collector")
		desc, _ := windows.UTF16PtrFromString("Sandbox for Relay KPI collectors (no network)")
		var sid *windows.SID
		hr, _, _ := procCreateAppContainerProfile.Call(
			uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(display)), uintptr(unsafe.Pointer(desc)),
			0, 0, uintptr(unsafe.Pointer(&sid)))
		if uint32(hr) == hresultAlreadyExists {
			hr, _, _ = procDeriveAppContainerSidFromAC.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&sid)))
		}
		if hr != 0 {
			sidErr = fmt.Errorf("AppContainer profile %q: HRESULT 0x%08X", profileName, uint32(hr))
			return
		}
		acSID = sid
	})
	return acSID, sidErr
}

// ---- ACLs ----

func editACE(path string, sid *windows.SID, mask windows.ACCESS_MASK, mode windows.ACCESS_MODE) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read ACL %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read DACL %s: %w", path, err)
	}
	ea := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: mask,
		AccessMode:        mode,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}
	acl, err := windows.ACLFromEntries(ea, dacl)
	if err != nil {
		return fmt.Errorf("build ACL %s: %w", path, err)
	}
	// SetNamedSecurityInfo propagates inheritable ACEs to the whole subtree.
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("write ACL %s: %w", path, err)
	}
	return nil
}

func grant(path string, sid *windows.SID, mask windows.ACCESS_MASK) error {
	return editACE(path, sid, mask, windows.GRANT_ACCESS)
}

func revoke(path string, sid *windows.SID) error {
	return editACE(path, sid, 0, windows.REVOKE_ACCESS)
}

// Shared dirs (Python install, collector scripts) keep their read grant:
// concurrent runs share them and the grant is read-only. Granting walks the
// whole tree, so it is done once per process per dir.
var sharedGrants sync.Map // path -> struct{}

func grantShared(path string, sid *windows.SID) error {
	key := strings.ToLower(filepath.Clean(path))
	if _, ok := sharedGrants.Load(key); ok {
		return nil
	}
	if err := grant(path, sid, fileReadExec); err != nil {
		return err
	}
	sharedGrants.Store(key, struct{}{})
	return nil
}

// ---- availability ----

var (
	probeMu   sync.Mutex
	probeErr  error
	probeAt   time.Time
	probeDone bool
	useLPAC   bool
)

func windowsAppsPython(p string) bool {
	return strings.Contains(strings.ToLower(filepath.Clean(p)), `\windowsapps\`)
}

func probePython() (string, error) {
	if p := os.Getenv(PythonEnv); p != "" {
		return p, nil
	}
	p, err := exec.LookPath("python.exe")
	if err != nil {
		return "", fmt.Errorf("no python found (set %s)", PythonEnv)
	}
	return p, nil
}

// available caches success forever and failure for probeRetryAfter, so
// installing Python later is picked up without a runner restart.
func available() error {
	probeMu.Lock()
	defer probeMu.Unlock()
	if probeDone && (probeErr == nil || time.Since(probeAt) < probeRetryAfter) {
		return probeErr
	}
	probeErr = probe()
	probeAt = time.Now()
	probeDone = true
	return probeErr
}

func probe() error {
	unavailable := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrUnavailable, fmt.Sprintf(format, a...))
	}
	v := windows.RtlGetVersion()
	if v.MajorVersion < 6 || (v.MajorVersion == 6 && v.MinorVersion < 2) {
		return unavailable("AppContainer needs Windows 8+, have %d.%d", v.MajorVersion, v.MinorVersion)
	}
	py, err := probePython()
	if err != nil {
		return unavailable("%v", err)
	}
	if windowsAppsPython(py) {
		return unavailable("%s is a Microsoft Store python; it cannot run inside an AppContainer, use the embedded python zip", py)
	}
	sid, err := containerSID()
	if err != nil {
		return unavailable("%v", err)
	}
	if err := grantShared(filepath.Dir(py), sid); err != nil {
		return unavailable("grant python dir: %v", err)
	}
	scratch, err := os.MkdirTemp("", "relay-sbx-probe-")
	if err != nil {
		return unavailable("probe dir: %v", err)
	}
	defer os.RemoveAll(scratch)
	if err := grant(scratch, sid, fileAllAccess); err != nil {
		return unavailable("grant probe dir: %v", err)
	}
	try := func(lpac bool) error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, err := launch(ctx, sid, lpac, []string{py, "-c", "print(1)"}, filepath.Dir(py), scratch, 20*time.Second, 64)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 || strings.TrimSpace(string(res.Stdout)) != "1" {
			return fmt.Errorf("exit %d, stdout %q, stderr %q", res.ExitCode, res.Stdout, res.Stderr)
		}
		return nil
	}
	lpacErr := try(true)
	if lpacErr == nil {
		useLPAC = true
		return nil
	}
	if acErr := try(false); acErr != nil {
		return unavailable("trial run failed (LPAC: %v; AppContainer: %v)", lpacErr, acErr)
	}
	useLPAC = false
	return nil
}

// ---- run ----

func run(ctx context.Context, s Spec) (Result, error) {
	for name, p := range map[string]string{"Python": s.Python, "ScriptDir": s.ScriptDir, "InputsDir": s.InputsDir, "ScratchDir": s.ScratchDir} {
		if p == "" || !filepath.IsAbs(p) {
			return Result{}, fmt.Errorf("sandbox: %s must be an absolute path, got %q", name, p)
		}
	}
	if windowsAppsPython(s.Python) {
		return Result{}, fmt.Errorf("%w: %s is a Microsoft Store python, use the embedded python zip", ErrUnavailable, s.Python)
	}
	if err := available(); err != nil {
		return Result{}, err
	}
	probeMu.Lock()
	lpac := useLPAC
	probeMu.Unlock()
	sid, err := containerSID()
	if err != nil {
		return Result{}, err
	}
	if err := grantShared(filepath.Dir(s.Python), sid); err != nil {
		return Result{}, err
	}
	if err := grantShared(s.ScriptDir, sid); err != nil {
		return Result{}, err
	}
	if err := grant(s.InputsDir, sid, fileReadExec); err != nil {
		return Result{}, err
	}
	defer revoke(s.InputsDir, sid)
	if err := grant(s.ScratchDir, sid, fileAllAccess); err != nil {
		return Result{}, err
	}
	defer revoke(s.ScratchDir, sid)

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	mem := s.MemoryMB
	if mem <= 0 {
		mem = defaultMemoryMB
	}
	argv := append([]string{s.Python, filepath.Join(s.ScriptDir, "collector.py")}, s.Args...)
	return launch(ctx, sid, lpac, argv, filepath.Dir(s.Python), s.ScratchDir, timeout, mem)
}

func envBlock(pyHome, scratch string) *uint16 {
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	env := []string{
		"PATH=" + pyHome + ";" + filepath.Join(sysRoot, "System32"),
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONHOME=" + pyHome,
		"PYTHONIOENCODING=utf-8",
		"PYTHONNOUSERSITE=1",
		"SystemRoot=" + sysRoot, // winsock/crypto misbehave without it
		"TEMP=" + scratch,
		"TMP=" + scratch,
	}
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	var b []uint16
	for _, kv := range env {
		u, _ := windows.UTF16FromString(kv) // includes the trailing NUL
		b = append(b, u...)
	}
	b = append(b, 0)
	return &b[0]
}

type capBuf struct {
	b []byte
}

func (c *capBuf) Write(p []byte) (int, error) {
	if room := maxOutput - len(c.b); room > 0 {
		if len(p) > room {
			c.b = append(c.b, p[:room]...)
		} else {
			c.b = append(c.b, p...)
		}
	}
	return len(p), nil
}

// inheritablePipe returns a parent read end (not inheritable) and a child
// write end (inheritable).
func inheritablePipe() (r, w windows.Handle, err error) {
	sa := windows.SecurityAttributes{InheritHandle: 1}
	sa.Length = uint32(unsafe.Sizeof(sa))
	if err = windows.CreatePipe(&r, &w, &sa, 0); err != nil {
		return 0, 0, err
	}
	if err = windows.SetHandleInformation(r, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		windows.CloseHandle(r)
		windows.CloseHandle(w)
		return 0, 0, err
	}
	return r, w, nil
}

func newJob(memMB int) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("CreateJobObject: %w", err)
	}
	var lim windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	lim.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
		windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY |
		windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS |
		windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	lim.BasicLimitInformation.ActiveProcessLimit = 1 // collectors may not spawn children
	lim.ProcessMemoryLimit = uintptr(memMB) << 20
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&lim)), uint32(unsafe.Sizeof(lim))); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("job limits: %w", err)
	}
	ui := jobUIRestrictions{UIRestrictionsClass: windows.JOB_OBJECT_UILIMIT_DESKTOP |
		windows.JOB_OBJECT_UILIMIT_DISPLAYSETTINGS | windows.JOB_OBJECT_UILIMIT_EXITWINDOWS |
		windows.JOB_OBJECT_UILIMIT_GLOBALATOMS | windows.JOB_OBJECT_UILIMIT_HANDLES |
		windows.JOB_OBJECT_UILIMIT_READCLIPBOARD | windows.JOB_OBJECT_UILIMIT_SYSTEMPARAMETERS |
		windows.JOB_OBJECT_UILIMIT_WRITECLIPBOARD}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions,
		uintptr(unsafe.Pointer(&ui)), uint32(unsafe.Sizeof(ui))); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("job UI limits: %w", err)
	}
	// CPU hard cap is best effort (Windows 8+; may be refused in nested jobs).
	cpu := jobCPURate{ControlFlags: 0x1 | 0x4, CpuRate: cpuRatePercent * 100} // ENABLE | HARD_CAP
	_, _ = windows.SetInformationJobObject(job, windows.JobObjectCpuRateControlInformation,
		uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu)))
	return job, nil
}

// launch starts argv inside the AppContainer, suspended, puts it in a job,
// resumes it and waits for exit, timeout or ctx. A non-zero exit is not an
// error; timeout returns context.DeadlineExceeded (wrapped), cancel ctx.Err().
func launch(ctx context.Context, sid *windows.SID, lpac bool, argv []string, pyHome, cwd string, timeout time.Duration, memMB int) (Result, error) {
	job, err := newJob(memMB)
	if err != nil {
		return Result{}, err
	}
	defer windows.CloseHandle(job) // KILL_ON_JOB_CLOSE: nothing outlives us

	outR, outW, err := inheritablePipe()
	if err != nil {
		return Result{}, fmt.Errorf("stdout pipe: %w", err)
	}
	errR, errW, err := inheritablePipe()
	if err != nil {
		windows.CloseHandle(outR)
		windows.CloseHandle(outW)
		return Result{}, fmt.Errorf("stderr pipe: %w", err)
	}
	nulName, _ := windows.UTF16PtrFromString("NUL")
	nulSA := windows.SecurityAttributes{InheritHandle: 1}
	nulSA.Length = uint32(unsafe.Sizeof(nulSA))
	nul, err := windows.CreateFile(nulName, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &nulSA, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		for _, h := range []windows.Handle{outR, outW, errR, errW} {
			windows.CloseHandle(h)
		}
		return Result{}, fmt.Errorf("open NUL: %w", err)
	}
	childEnds := []windows.Handle{nul, outW, errW}
	closeChildEnds := func() {
		for _, h := range childEnds {
			windows.CloseHandle(h)
		}
		childEnds = nil
	}
	defer func() { closeChildEnds() }()
	stdout, stderr := os.NewFile(uintptr(outR), "sandbox-stdout"), os.NewFile(uintptr(errR), "sandbox-stderr")
	defer stdout.Close()
	defer stderr.Close()

	nAttr := uint32(2)
	if lpac {
		nAttr++
	}
	attrs, err := windows.NewProcThreadAttributeList(nAttr)
	if err != nil {
		return Result{}, fmt.Errorf("attribute list: %w", err)
	}
	defer attrs.Delete()
	caps := &securityCapabilities{AppContainerSid: sid}
	if err := attrs.Update(procThreadAttributeSecurityCapabilities, unsafe.Pointer(caps), unsafe.Sizeof(*caps)); err != nil {
		return Result{}, fmt.Errorf("security capabilities attribute: %w", err)
	}
	inherit := []windows.Handle{nul, outW, errW}
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&inherit[0]), uintptr(len(inherit))*unsafe.Sizeof(inherit[0])); err != nil {
		return Result{}, fmt.Errorf("handle list attribute: %w", err)
	}
	if lpac {
		policy := new(uint32)
		*policy = processCreationAllApplicationPackagesOptOut
		if err := attrs.Update(procThreadAttributeAllApplicationPackagesPol, unsafe.Pointer(policy), unsafe.Sizeof(*policy)); err != nil {
			return Result{}, fmt.Errorf("LPAC attribute: %w", err)
		}
	}

	si := new(windows.StartupInfoEx)
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = nul, outW, errW
	si.ProcThreadAttributeList = attrs.List()

	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return Result{}, err
	}
	appName, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		return Result{}, err
	}
	cwdPtr, err := windows.UTF16PtrFromString(cwd)
	if err != nil {
		return Result{}, err
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_SUSPENDED |
		windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(appName, cmdLine, nil, nil, true, flags, envBlock(pyHome, cwd), cwdPtr, &si.StartupInfo, &pi); err != nil {
		return Result{}, fmt.Errorf("CreateProcess in AppContainer: %w", err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		return Result{}, fmt.Errorf("assign job: %w", err)
	}
	closeChildEnds() // so the readers see EOF when the child exits

	var outBuf, errBuf capBuf
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); io.Copy(&outBuf, stdout) }()
	go func() { defer readers.Done(); io.Copy(&errBuf, stderr) }()

	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		windows.TerminateJobObject(job, 1)
		readers.Wait()
		return Result{}, fmt.Errorf("resume: %w", err)
	}

	exited := make(chan struct{})
	go func() {
		windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		close(exited)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var runErr error
	select {
	case <-exited:
	case <-timer.C:
		runErr = fmt.Errorf("sandbox: killed after %s: %w", timeout, context.DeadlineExceeded)
	case <-ctx.Done():
		runErr = ctx.Err()
	}
	// Kill anything left in the job (also covers the normal-exit case).
	windows.TerminateJobObject(job, 1)
	<-exited
	readers.Wait()

	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil && runErr == nil {
		runErr = fmt.Errorf("exit code: %w", err)
	}
	return Result{Stdout: outBuf.b, Stderr: errBuf.b, ExitCode: int(int32(code))}, runErr
}
