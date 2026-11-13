// Command mhyacc speeds up the official miHoYo CN launcher's downloads by steering
// its connections to the fastest CDN nodes. See docs/DESIGN.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"mhyacc/internal/engine"
	"mhyacc/internal/guard"
	"mhyacc/internal/rules"
	"mhyacc/internal/sysconf"
	"mhyacc/internal/winutil"
)

const usage = `mhyacc：米哈游国服启动器下载加速（非官方）

用法：
  直接双击 mhyacc.exe                                交互模式：选择语言和游戏后开始加速（会请求管理员权限）
  mhyacc run   [-game all] [-lang zh|en] [-no-hosts] 直接开始加速，Ctrl+C 或关闭窗口即还原
  mhyacc probe [-game all] [-lang zh|en]             只测速，不改动系统
  mhyacc off                                         手动还原 hosts（一般不需要，异常退出会自动还原）
  mhyacc status                                      查看 hosts 接管状态和保护措施

-game 可选：all，或用逗号分隔的 hk4e_cn,hkrpg_cn,nap_cn,bh3_cn
其他参数：-exclude IP,IP（不使用这些节点） -test-secs 6（每个候选节点的正式测速秒数）
`

func main() {
	var cmd string
	var args []string
	if len(os.Args) < 2 {
		cmd = "interactive" // double-clicked: ask for language and games
	} else {
		cmd, args = os.Args[1], os.Args[2:]
	}
	if cmd == "help" || cmd == "-h" || cmd == "--help" || cmd == "/?" {
		fmt.Print(usage)
		return
	}

	// Changing the hosts file needs administrator rights: ask through UAC and let the
	// elevated copy (in its own window) take over.
	needsAdmin := cmd == "off" || cmd == "interactive" || (cmd == "run" && !containsArg(args, "-no-hosts"))
	if needsAdmin && !winutil.IsElevated() {
		fmt.Println("需要管理员权限来修改 hosts，正在请求……")
		if err := winutil.RelaunchElevated(append([]string{cmd}, args...)); err != nil {
			fmt.Fprintln(os.Stderr, "错误：", err)
			winutil.WaitEnter()
			os.Exit(1)
		}
		if !winutil.OwnConsole() {
			fmt.Println("已在新的管理员窗口中运行。")
		}
		return
	}

	var err error
	switch cmd {
	case "interactive":
		err = interactive()
		winutil.WaitEnter() // keep a double-clicked window open to read the last lines
	case "run":
		err = cmdRun(args)
	case "probe":
		err = cmdProbe(args)
	case "off":
		// -boot is passed by the boot cleanup task; the behaviour is the same.
		err = cleanup()
		if err == nil {
			fmt.Println("hosts 已还原。")
		}
	case "watchdog": // internal: started by run, see internal/guard
		fs := flag.NewFlagSet("watchdog", flag.ExitOnError)
		pid := fs.Int("pid", 0, "process to watch")
		fs.Parse(args)
		if *pid > 0 {
			err = guard.Watch(*pid)
		}
	case "status":
		err = cmdStatus()
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		winutil.WaitEnter()
		os.Exit(1)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want || strings.HasPrefix(a, want+"=") {
			return true
		}
	}
	return false
}

// cleanup undoes everything run sets up: the hosts block, the boot task, and the
// launcher's DNS cache (which may still hold our loopback address).
func cleanup() error {
	err := sysconf.Remove()
	guard.RemoveBootTask()
	if !sysconf.LauncherRunning() {
		sysconf.FlushLauncherCache()
	}
	return err
}

func cmdStatus() error {
	pid, on, err := sysconf.Owner()
	if err != nil {
		return err
	}
	switch {
	case !on:
		fmt.Println("hosts 未被接管。")
	case processAlive(pid):
		fmt.Printf("hosts 处于接管状态（加速进程 %d 正在运行）。\n", pid)
	default:
		fmt.Printf("hosts 处于接管状态，但加速进程 %d 已不在运行。请执行 mhyacc off。\n", pid)
	}
	if guard.BootTaskInstalled() {
		fmt.Println("开机清理任务：已登记。")
	} else {
		fmt.Println("开机清理任务：未登记。")
	}
	return nil
}

func processAlive(pid int) bool { return winutil.PIDRunning(pid) }

// options parses the flags shared by run and probe.
func options(fs *flag.FlagSet, args []string) (engine.Options, error) {
	game := fs.String("game", "all", "games to accelerate")
	exclude := fs.String("exclude", "", "comma-separated node IPs never to use")
	testSecs := fs.Float64("test-secs", 6, "measured seconds per finalist node")
	fs.StringVar(&outLang, "lang", "zh", "output language: zh or en")
	if err := fs.Parse(args); err != nil {
		return engine.Options{}, err
	}
	if outLang != "en" {
		outLang = "zh"
	}
	games, ok := rules.Select(*game)
	if !ok {
		return engine.Options{}, fmt.Errorf("未知的游戏：%s", *game)
	}
	ex := map[string]bool{}
	for _, ip := range strings.Split(*exclude, ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			ex[ip] = true
		}
	}
	return engine.Options{Games: games, Exclude: ex, TestSecs: *testSecs}, nil
}

// outLang is the output language of the non-interactive commands (-lang).
var outLang = "zh"

func cmdProbe(args []string) error {
	opt, err := options(flag.NewFlagSet("probe", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	opt.Verbose = true
	c := newConsole(outLang)
	return engine.New(opt, c.event).Probe(context.Background())
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	noHosts := fs.Bool("no-hosts", false, "do not touch the hosts file (testing)")
	perNode := fs.Int("max-per-node", 6, "max simultaneous connections per node")
	recvBuf := fs.Int("rcvbuf-mb", 0, "experimental: upstream socket receive buffer in MB (0 = system default)")
	keepLauncher := fs.Bool("keep-launcher", false, "do not wait for the launcher to exit")
	opt, err := options(fs, args)
	if err != nil {
		return err
	}
	opt.TakeOver = !*noHosts
	opt.KeepLauncher = *keepLauncher
	opt.MaxPerNode = *perNode
	opt.RecvBuf = *recvBuf << 20
	return runWithConsole(opt, outLang)
}
