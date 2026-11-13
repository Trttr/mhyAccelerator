// Package winutil wraps the few Windows calls the front ends need: elevation,
// console handling and the UI language.
package winutil

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

var (
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procShellExecuteW         = shell32.NewProc("ShellExecuteW")
	procSetConsoleTitleW      = kernel32.NewProc("SetConsoleTitleW")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procGetUserDefaultUILang  = kernel32.NewProc("GetUserDefaultUILanguage")
)

var (
	user32                    = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW           = user32.NewProc("MessageBoxW")
	procSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
	procGetDpiForSystem       = user32.NewProc("GetDpiForSystem")
)

// Command is exec.Command with no console window. A windowed program that runs
// console tools (tasklist, ipconfig, schtasks) would otherwise flash a black
// window each time.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

// ProcessRunning reports whether a process with this executable name (for
// example "HYP.exe") is running. It asks Windows directly instead of running
// tasklist.
func ProcessRunning(exe string) bool {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(snap)
	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = syscall.Process32First(snap, &pe); err == nil; err = syscall.Process32Next(snap, &pe) {
		if strings.EqualFold(syscall.UTF16ToString(pe.ExeFile[:]), exe) {
			return true
		}
	}
	return false
}

// PIDRunning reports whether a process with this id exists.
func PIDRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(snap)
	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = syscall.Process32First(snap, &pe); err == nil; err = syscall.Process32Next(snap, &pe) {
		if int(pe.ProcessID) == pid {
			return true
		}
	}
	return false
}

// WorkAreaHeight returns the usable screen height (without the taskbar) in
// device-independent pixels, or 0 if unknown.
func WorkAreaHeight() int {
	var r struct{ Left, Top, Right, Bottom int32 }
	const spiGetWorkArea = 0x0030
	ok, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	if ok == 0 {
		return 0
	}
	h := int(r.Bottom - r.Top)
	if procGetDpiForSystem.Find() == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi > 0 {
			h = h * 96 / int(dpi)
		}
	}
	return h
}

// MessageBox shows a simple error dialog (for windowed programs with no console).
func MessageBox(title, text string) {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	const mbIconError = 0x10
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbIconError)
}

var (
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

// EnableVT turns on ANSI colour and cursor sequences for the console (Windows 10+).
// It reports whether they are available.
func EnableVT() bool {
	h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return false
	}
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false // not a console (redirected)
	}
	const enableVirtualTerminalProcessing = 0x0004
	r, _, _ := procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}

// ConsoleWidth returns the console window width in columns, or 0 if unknown.
func ConsoleWidth() int {
	h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return 0
	}
	var info struct {
		Size, Cursor             struct{ X, Y int16 }
		Attr                     uint16
		Left, Top, Right, Bottom int16
		MaxX, MaxY               int16
	}
	if r, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0
	}
	return int(info.Right-info.Left) + 1
}

// ChineseUI reports whether the Windows display language is Chinese.
func ChineseUI() bool {
	lang, _, _ := procGetUserDefaultUILang.Call()
	return lang&0x3ff == 0x04 // LANG_CHINESE (primary language id)
}

// IsElevated reports whether this process runs with administrator rights.
func IsElevated() bool {
	tok, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer tok.Close()
	const tokenElevation = 20
	var elevated uint32
	var n uint32
	err = syscall.GetTokenInformation(tok, tokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &n)
	return err == nil && elevated != 0
}

// RelaunchElevated starts this program again with the same arguments through the
// UAC prompt ("runas"). It returns once the prompt was accepted or refused.
func RelaunchElevated(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(strings.Join(quoted, " "))
	const swShowNormal = 1
	r, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)), 0, swShowNormal)
	if r <= 32 { // ShellExecute reports errors as values <= 32; 5 means the user said no
		if r == 5 {
			return fmt.Errorf("没有获得管理员权限（在弹窗中选择了“否”）")
		}
		return fmt.Errorf("无法以管理员身份启动（错误码 %d）", r)
	}
	return nil
}

// SetTitle sets the console window title.
func SetTitle(title string) {
	p, err := syscall.UTF16PtrFromString(title)
	if err == nil {
		procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(p)))
	}
}

// OwnConsole reports whether this process is alone in its console window, which is
// the case when it was started by double-clicking (or through the UAC prompt)
// rather than from a terminal. The window then closes the moment we exit.
func OwnConsole() bool {
	var pids [4]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), 4)
	return n == 1
}

// WaitEnter keeps a double-clicked window open so the user can read the last message.
func WaitEnter() {
	if !OwnConsole() {
		return
	}
	fmt.Print("\n按回车键关闭窗口……")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
