// Package guard makes sure the hosts takeover never outlives the forwarder.
//
// A normal exit, Ctrl+C or closing the console window is handled by the run itself.
// Two extra layers cover the rest:
//
//   - a watchdog process that waits for the run to die (crash, End task, taskkill /F)
//     and then removes the hosts block, if that run still owns it;
//   - a scheduled task that runs "mhyacc off -boot" at the next system start,
//     for power loss or a blue screen. It is deleted again on a normal exit.
package guard

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"mhyacc/internal/sysconf"
	"mhyacc/internal/winutil"
)

const taskName = `mhyAccelerator\BootCleanup`

const (
	detachedProcess        = 0x00000008
	createNewProcessGroup  = 0x00000200
	createBreakawayFromJob = 0x01000000
)

// StartWatchdog launches "<exe> watchdog -pid <our pid>" as a process that is not
// attached to our console, so it survives Ctrl+C, the window closing and the
// terminal killing our process tree.
func StartWatchdog() (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	args := []string{"watchdog", "-pid", strconv.Itoa(os.Getpid())}
	start := func(flags uint32) (int, error) {
		cmd := exec.Command(exe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		pid := cmd.Process.Pid
		return pid, cmd.Process.Release()
	}
	// Terminals often run us inside a job object that kills every member when it
	// closes. Break away from it if allowed; otherwise start inside it anyway.
	if pid, err := start(detachedProcess | createNewProcessGroup | createBreakawayFromJob); err == nil {
		return pid, nil
	}
	return start(detachedProcess | createNewProcessGroup)
}

// Watch blocks until process pid exits, then removes the hosts block if pid still owns it.
func Watch(pid int) error {
	if p, err := os.FindProcess(pid); err == nil {
		p.Wait() // returns when the process exits (or immediately if it already has)
	}
	time.Sleep(500 * time.Millisecond) // let a clean shutdown finish its own cleanup first
	removed, err := sysconf.RemoveIfOwner(pid)
	if removed {
		RemoveBootTask()
		if !sysconf.LauncherRunning() {
			sysconf.FlushLauncherCache()
		}
	}
	return err
}

// InstallBootTask registers a task that removes the hosts block at the next boot.
func InstallBootTask() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	out, err := winutil.Command("schtasks", "/Create", "/F", "/TN", taskName,
		"/SC", "ONSTART", "/RU", "SYSTEM", "/RL", "HIGHEST",
		"/TR", fmt.Sprintf(`"%s" off -boot`, exe)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks /Create: %v: %s", err, bytes.TrimSpace(out))
	}
	// Do not trust the exit code alone: confirm the task is really there.
	if q, err := winutil.Command("schtasks", "/Query", "/TN", taskName).CombinedOutput(); err != nil {
		return fmt.Errorf("创建命令返回成功，但查询不到任务：create=%q query=%q (%v)",
			bytes.TrimSpace(out), bytes.TrimSpace(q), err)
	}
	return nil
}

// RemoveBootTask deletes the boot task if it exists.
func RemoveBootTask() {
	winutil.Command("schtasks", "/Delete", "/F", "/TN", taskName).Run()
}

// BootTaskInstalled reports whether the boot task exists.
func BootTaskInstalled() bool {
	return winutil.Command("schtasks", "/Query", "/TN", taskName).Run() == nil
}
