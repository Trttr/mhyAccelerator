// Package sysconf points the launcher's download hosts at the local forwarder
// through the hosts file, and undoes it.
package sysconf

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mhyacc/internal/winutil"
)

// ListenIP is a dedicated loopback address, so we do not collide with other
// programs that listen on 127.0.0.1:443.
const ListenIP = "127.66.0.1"

const (
	// The begin line also records the PID of the run that owns the block, so the
	// watchdog of an old run never removes a block written by a newer one.
	beginPrefix = "# >>> mhyAccelerator"
	beginFormat = beginPrefix + " pid=%d (managed block; if downloads fail, run: mhyacc off)"
	endMark     = "# <<< mhyAccelerator"
)

// hostsOverride lets tests point the package at a scratch file.
var hostsOverride string

// HostsPath returns the system hosts file location.
func HostsPath() string {
	if hostsOverride != "" {
		return hostsOverride
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "drivers", "etc", "hosts")
}

// Apply writes (or replaces) our block mapping every host to ListenIP, owned by pid.
func Apply(hosts []string, pid int) error {
	block := []string{fmt.Sprintf(beginFormat, pid)}
	for _, h := range hosts {
		block = append(block, ListenIP+" "+h)
	}
	return rewrite(append(block, endMark))
}

// Remove deletes our block whoever owns it.
func Remove() error { return rewrite(nil) }

// RemoveIfOwner deletes our block only if pid owns it.
func RemoveIfOwner(pid int) (bool, error) {
	owner, present, err := Owner()
	if err != nil || !present || owner != pid {
		return false, err
	}
	return true, rewrite(nil)
}

// Present reports whether our block is in the hosts file.
func Present() (bool, error) {
	_, present, err := Owner()
	return present, err
}

// Owner returns the PID recorded in our block (0 if the block predates PIDs).
func Owner() (pid int, present bool, err error) {
	data, err := os.ReadFile(HostsPath())
	if err != nil {
		return 0, false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, beginPrefix) {
			fmt.Sscanf(strings.TrimPrefix(line, beginPrefix), " pid=%d", &pid)
			return pid, true, nil
		}
	}
	return 0, false, nil
}

func rewrite(block []string) error {
	path := HostsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if backup := path + ".mhyacc.bak"; !fileExists(backup) {
		if err := os.WriteFile(backup, data, 0o644); err != nil {
			return friendly(err)
		}
	}

	eol := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		eol = "\r\n"
	}
	var keep []string
	inside := false
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, beginPrefix):
			inside = true
			continue
		case t == endMark:
			inside = false
			continue
		}
		if !inside {
			keep = append(keep, line)
		}
	}
	for len(keep) > 0 && strings.TrimSpace(keep[len(keep)-1]) == "" {
		keep = keep[:len(keep)-1]
	}
	if len(block) > 0 {
		keep = append(keep, "")
		keep = append(keep, block...)
	}
	out := strings.Join(keep, eol) + eol
	if out == string(data) {
		return nil
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return friendly(err)
	}
	FlushDNS()
	return nil
}

func friendly(err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("没有权限修改 hosts，请用管理员身份运行（%w）", err)
	}
	return err
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// FlushDNS clears the Windows DNS client cache.
func FlushDNS() {
	winutil.Command("ipconfig", "/flushdns").Run()
}

// LauncherRunning reports whether the official launcher's main process is running.
func LauncherRunning() bool {
	return winutil.ProcessRunning("HYP.exe")
}

// FlushLauncherCache moves aside the DNS cache that the launcher's download module
// persists across restarts; otherwise it can keep using nodes resolved before we
// changed the hosts file. The launcher recreates the file. It must not be running.
func FlushLauncherCache() (bool, error) {
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		return false, nil
	}
	p := filepath.Join(appdata, "miHoYo", "HYP", "1_1", "modules", "sophon", "sophon_net", "prefs", "local_prefs.json")
	if !fileExists(p) {
		return false, nil
	}
	if err := os.Rename(p, p+".mhyacc.bak"); err != nil {
		os.Remove(p + ".mhyacc.bak")
		if err2 := os.Rename(p, p+".mhyacc.bak"); err2 != nil {
			return false, err2
		}
	}
	return true, nil
}
