package subagents

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"slices"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/sandbox"
)

// runExternalCommand runs command through the bash upstream's bash tool uses on
// win32 (Git Bash). A kill-on-close job object stands in for the POSIX process
// group: the shell starts suspended and joins the job before running anything,
// so every descendant ends with it once the shell exits or ctx is done.
func runExternalCommand(ctx context.Context, cwd, command string, env map[string]string, mode sandbox.Mode, stdin io.Reader, stdout, stderr io.Writer) (externalRun, error) {
	shell, err := tools.GetShellConfig("")
	if err != nil {
		return externalRun{}, unavailableError{err}
	}
	if shell.CommandTransport == tools.ShellCommandStdin {
		return externalRun{}, unavailableError{errors.New("the legacy WSL bash reads its command from stdin, which carries the task")}
	}
	command, env = sandbox.Wrap(mode, cwd, shell.Shell, command, env)
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return externalRun{}, unavailableError{err}
	}
	defer func() { _ = windows.CloseHandle(job) }()
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return externalRun{}, unavailableError{err}
	}
	process := exec.CommandContext(ctx, shell.Shell, append(slices.Clone(shell.Args), command)...)
	// WaitDelay only matters if a pipe outlives the job; the job kill closes them.
	process.Dir, process.Stdin, process.Stdout, process.Stderr, process.WaitDelay = cwd, stdin, stdout, stderr, 5*time.Second
	for name, value := range env {
		process.Env = append(process.Env, name+"="+value)
	}
	process.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW}
	process.Cancel = func() error { return windows.TerminateJobObject(job, 1) }
	if err := process.Start(); err != nil {
		return externalRun{}, err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(process.Process.Pid))
	if err == nil {
		defer func() { _ = windows.CloseHandle(handle) }()
		err = windows.AssignProcessToJobObject(job, handle)
	}
	// A cancellation before the assignment found the job empty; the shell is
	// then never resumed and dies with the job below.
	if err == nil && ctx.Err() == nil {
		err = resumeProcess(uint32(process.Process.Pid))
	}
	if err != nil {
		_ = process.Process.Kill()
		_ = process.Wait()
		return externalRun{}, unavailableError{err}
	}
	var run externalRun
	if run.statusErr = ctx.Err(); run.statusErr == nil {
		if _, err := windows.WaitForSingleObject(handle, windows.INFINITE); err != nil {
			run.statusErr = err
		} else if run.statusErr = ctx.Err(); run.statusErr == nil {
			var code uint32
			run.statusErr = windows.GetExitCodeProcess(handle, &code)
			run.status = int(code)
		}
	}
	run.cleanupErr = windows.TerminateJobObject(job, 1)
	run.waitErr = process.Wait()
	return run, nil
}

// resumeProcess resumes the threads of a process created suspended; os/exec
// closes the thread handle CreateProcess returns, so a snapshot finds them.
func resumeProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := false
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return openErr
		}
		_, resumeErr := windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if resumeErr != nil {
			return resumeErr
		}
		resumed = true
	}
	if !resumed {
		return errors.New("the suspended shell has no thread to resume")
	}
	return nil
}
