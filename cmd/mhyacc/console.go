package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"mhyacc/internal/engine"
	"mhyacc/internal/events"
	"mhyacc/internal/rules"
	"mhyacc/internal/settings"
	"mhyacc/internal/sysconf"
	"mhyacc/internal/winutil"
)

// ---------- text ----------

var texts = map[string]map[string]string{
	"zh": {
		"tagline":     "米哈游国服启动器下载加速",
		"pickGames":   "选择要加速的游戏",
		"pickHint":    "输入序号切换选择（可输入多个，如 1 3），直接回车开始加速：",
		"pickNone":    "请至少选择一个游戏。",
		"discovering": "正在探测可用节点…",
		"quitTitle":   "检测到米哈游启动器正在运行，加速前需要先退出：",
		"quit1":       "1. 在启动器里暂停下载",
		"quit2":       "2. 完全退出启动器（包括屏幕右下角托盘里的图标）",
		"quit3":       "3. 等加速开启后，再重新打开启动器继续下载",
		"waiting":     "正在等待启动器退出…",
		"quitDone":    "启动器已退出",
		"on":          "加速已开启，现在可以打开启动器下载",
		"howStop":     "停止加速：关闭本窗口或按 Ctrl+C（hosts 会自动还原）",
		"logTitle":    "事件记录",
		"stopping":    "正在停止并还原…",
		"stopped":     "已停止，hosts 已还原",
		"stOn":        "加速中", "stPrep": "准备中", "stOff": "未加速",
		"speed": "速度", "faster": "比基准快", "traffic": "已加速", "uptime": "运行",
		"launcher": "启动器",
		"lStarted": "下载中", "lPaused": "已暂停", "lFinished": "已完成",
	},
	"en": {
		"tagline":     "Download accelerator for the miHoYo CN launcher",
		"pickGames":   "Games to accelerate",
		"pickHint":    "Type numbers to toggle (several at once, e.g. 1 3); press Enter to start: ",
		"pickNone":    "Select at least one game.",
		"discovering": "Probing available nodes…",
		"quitTitle":   "The miHoYo launcher is running and must be closed first:",
		"quit1":       "1. Pause the download in the launcher",
		"quit2":       "2. Quit the launcher completely (including its system tray icon)",
		"quit3":       "3. Once acceleration is on, reopen the launcher and resume",
		"waiting":     "Waiting for the launcher to quit…",
		"quitDone":    "The launcher has quit",
		"on":          "Acceleration is on. Open the launcher and download",
		"howStop":     "To stop: close this window or press Ctrl+C (hosts is restored automatically)",
		"logTitle":    "Event log",
		"stopping":    "Stopping and restoring…",
		"stopped":     "Stopped; hosts restored",
		"stOn":        "Accelerating", "stPrep": "Preparing", "stOff": "Off",
		"speed": "Speed", "faster": "vs baseline", "traffic": "Accelerated", "uptime": "Up",
		"launcher": "Launcher",
		"lStarted": "downloading", "lPaused": "paused", "lFinished": "finished",
	},
}

// ---------- console output ----------

// console prints event lines above a status line that is redrawn in place.
type console struct {
	lang  string
	color bool

	mu         sync.Mutex
	statusLen  int // display width of the status line currently on screen
	waitPrint  bool
	wasWaiting bool
}

func newConsole(lang string) *console {
	return &console{lang: lang, color: winutil.EnableVT()}
}

func (c *console) t(k string) string { return texts[c.lang][k] }

const (
	cReset  = "\x1b[0m"
	cBold   = "\x1b[1m"
	cDim    = "\x1b[90m"
	cGreen  = "\x1b[32m"
	cYellow = "\x1b[33m"
	cRed    = "\x1b[31m"
	cCyan   = "\x1b[36m"
)

func (c *console) paint(code, s string) string {
	if !c.color || code == "" {
		return s
	}
	return code + s + cReset
}

// clearStatus erases the status line (caller holds mu).
func (c *console) clearStatus() {
	if c.statusLen > 0 {
		fmt.Print("\r" + strings.Repeat(" ", c.statusLen) + "\r")
		c.statusLen = 0
	}
}

// line prints one line above the status line.
func (c *console) line(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearStatus()
	fmt.Println(s)
}

// status redraws the bottom line, trimmed to the window width so it never wraps.
func (c *console) status(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w := winutil.ConsoleWidth(); w > 0 {
		s = trimWidth(s, w-1)
	}
	c.clearStatus()
	fmt.Print(s)
	c.statusLen = displayWidth(stripANSI(s))
}

// displayWidth counts CJK and full-width characters as two columns.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r >= 0x1100 && (r <= 0x115f || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe4f) || (r >= 0xff00 && r <= 0xff60) || (r >= 0xffe0 && r <= 0xffe6)):
			w += 2
		default:
			w++
		}
	}
	return w
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// trimWidth cuts s (which may contain colour codes) to at most w columns.
func trimWidth(s string, w int) string {
	if displayWidth(stripANSI(s)) <= w {
		return s
	}
	var b strings.Builder
	col := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := strings.IndexByte(s[i:], 'm')
			if j < 0 {
				break
			}
			b.WriteString(s[i : i+j+1])
			i += j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := displayWidth(string(r))
		if col+rw > w {
			break
		}
		b.WriteRune(r)
		col += rw
		i += size
	}
	return b.String() + cResetIf(s)
}

func cResetIf(s string) string {
	if strings.Contains(s, "\x1b[") {
		return cReset
	}
	return ""
}

// event renders one engine event.
func (c *console) event(e events.Event) {
	if e.Kind == events.Phase {
		c.phase(e.Text)
		return
	}
	text := events.Format(e, c.lang)
	if e.Host != "" {
		text = strings.ReplaceAll(text, e.Host, gameName(e.Host, c.lang))
	}
	code := ""
	switch e.Kind {
	case events.NodeSwitched, events.Hold:
		code = cYellow
	case events.Warning:
		code = cRed
	case events.ModeChosen, events.Baseline, events.Summary:
		code = cGreen
	case events.Retest:
		code = cCyan
	case events.Measured:
		code = ""
	}
	if e.Kind == events.Baseline && e.Err != "" {
		code = cDim
	}
	c.line(c.paint(cDim, e.Time.Format("15:04:05")) + "  " + c.paint(code, text))
}

// phase turns phase changes into the prominent messages of the flow.
func (c *console) phase(p string) {
	switch p {
	case events.PhaseDiscovering:
		c.line(c.paint(cCyan, "▶ ") + c.t("discovering"))
	case events.PhaseWaitLaunch:
		c.launcherWarning()
	case events.PhaseRunning:
		c.mu.Lock()
		waited := c.wasWaiting
		c.mu.Unlock()
		if waited {
			c.line(c.paint(cGreen, "✓ "+c.t("quitDone")))
		}
		c.line("")
		c.line(c.paint(cGreen+cBold, "✓ "+c.t("on")))
		c.line("  " + c.t("howStop"))
		c.line("")
		c.line(c.paint(cBold, "── "+c.t("logTitle")+" ──"))
		winutil.SetTitle("mhyAccelerator - " + c.t("stOn"))
	case events.PhaseOptimizing, events.PhaseOptimized:
		code := cDim
		if p == events.PhaseOptimized {
			code = cGreen
		}
		c.line(c.paint(cDim, time.Now().Format("15:04:05")) + "  " + c.paint(code, events.Format(events.Event{Kind: events.Phase, Text: p}, c.lang)))
	case events.PhaseStopping:
		c.line("")
		c.line(c.paint(cYellow, "■ "+c.t("stopping")))
	case events.PhaseStopped:
		c.line(c.paint(cGreen, "✓ "+c.t("stopped")))
		winutil.SetTitle("mhyAccelerator")
	}
}

// launcherWarning prints the "quit the launcher" block once per wait.
func (c *console) launcherWarning() {
	c.mu.Lock()
	if c.waitPrint {
		c.mu.Unlock()
		return
	}
	c.waitPrint, c.wasWaiting = true, true
	c.mu.Unlock()
	c.line("")
	c.line(c.paint(cYellow+cBold, "⚠ "+c.t("quitTitle")))
	for _, k := range []string{"quit1", "quit2", "quit3"} {
		c.line(c.paint(cYellow, "    "+c.t(k)))
	}
	c.line("")
}

func gameName(host, lang string) string {
	for _, g := range rules.Games {
		if g.Host == host {
			if lang == "en" {
				return g.NameEn
			}
			return g.Name
		}
	}
	return host
}

// statusLoop redraws the status line every second until ctx ends.
func (c *console) statusLoop(ctx context.Context, eng *engine.Engine) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.mu.Lock()
			c.clearStatus()
			c.mu.Unlock()
			return
		case <-t.C:
		}
		st := eng.Status()
		if st.Phase == events.PhaseWaitLaunch {
			c.status(c.paint(cYellow, "  … "+c.t("waiting")))
			continue
		}
		if st.Phase == "" || st.Phase == events.PhaseDiscovering || st.Phase == events.PhaseStopping || st.Phase == events.PhaseStopped {
			continue
		}
		c.status(c.statusText(st))
	}
}

func (c *console) statusText(st engine.Status) string {
	var parts []string
	parts = append(parts, c.paint(cGreen+cBold, "● "+c.t("stOn")))
	parts = append(parts, fmt.Sprintf("%s %s", c.t("speed"), c.paint(cBold, fmt.Sprintf("%.1f MB/s", st.Rate/1e6))))
	var base, live float64
	for _, h := range st.Hosts {
		if h.Rate > 1e6 && h.Baseline > 0 {
			base += h.Baseline
			live += h.Rate
		}
	}
	if base > 0 && live > base {
		parts = append(parts, c.paint(cGreen, fmt.Sprintf("%s +%.1f MB/s", c.t("faster"), (live-base)/1e6)))
	}
	parts = append(parts, fmt.Sprintf("%s %s", c.t("traffic"), fmtBytes(st.Bytes)))
	if !st.Started.IsZero() {
		parts = append(parts, fmt.Sprintf("%s %s", c.t("uptime"), fmtDur(time.Since(st.Started))))
	}
	if l := map[string]string{"started": c.t("lStarted"), "paused": c.t("lPaused"), "finished": c.t("lFinished")}[st.Launcher]; l != "" {
		parts = append(parts, fmt.Sprintf("%s %s", c.t("launcher"), l))
	}
	return " " + strings.Join(parts, c.paint(cDim, "  │  "))
}

func fmtBytes(b int64) string {
	if b >= 1e9 {
		return fmt.Sprintf("%.2f GB", float64(b)/1e9)
	}
	return fmt.Sprintf("%.0f MB", float64(b)/1e6)
}

func fmtDur(d time.Duration) string {
	s := int(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// runWithConsole runs the engine with the console front end until Ctrl+C or the
// window closes.
func runWithConsole(opt engine.Options, lang string) error {
	c := newConsole(lang)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	winutil.SetTitle("mhyAccelerator - " + c.t("stPrep"))

	// Warn right away if the launcher is open; the engine will wait for it after
	// probing nodes, and the block is printed only once.
	if opt.TakeOver && !opt.KeepLauncher && sysconf.LauncherRunning() {
		c.launcherWarning()
	}
	eng := engine.New(opt, c.event)
	go c.statusLoop(ctx, eng)
	err := eng.Run(ctx)
	stop()
	time.Sleep(50 * time.Millisecond) // let the status loop clear its line
	return err
}

// ---------- interactive start ----------

// interactive asks for the language and games, then runs.
func interactive() error {
	s := settings.Load()
	in := bufio.NewReader(os.Stdin)
	color := winutil.EnableVT()
	paint := func(code, x string) string {
		if !color {
			return x
		}
		return code + x + cReset
	}

	def := s.Lang
	if def != "zh" && def != "en" {
		def = "en"
		if winutil.ChineseUI() {
			def = "zh"
		}
	}
	winutil.SetTitle("mhyAccelerator")
	fmt.Println()
	fmt.Println(paint(cBold, "  mhyAccelerator"))
	fmt.Println(paint(cDim, "  "+texts["zh"]["tagline"]+" · "+texts["en"]["tagline"]))
	fmt.Println()
	fmt.Println("  请选择语言 / Select language")
	mark := func(l string) string {
		if l == def {
			return paint(cGreen, " ◀")
		}
		return ""
	}
	fmt.Println("    1  中文" + mark("zh"))
	fmt.Println("    2  English" + mark("en"))
	fmt.Print("  > ")
	lang := def
	switch strings.TrimSpace(readLine(in)) {
	case "1":
		lang = "zh"
	case "2":
		lang = "en"
	}
	T := texts[lang]

	// Preselect: last choice, else games the launcher knows as installed or pending.
	selected := map[string]bool{}
	for _, b := range s.Games {
		selected[b] = true
	}
	if len(selected) == 0 {
		for _, g := range engine.Games() {
			if g.Installed || g.Pending {
				selected[g.Biz] = true
			}
		}
	}
	for {
		fmt.Println()
		fmt.Println(paint(cBold, "  "+T["pickGames"]))
		for i, g := range rules.Games {
			box := "[ ]"
			if selected[g.Biz] {
				box = paint(cGreen, "[x]")
			}
			name := g.Name
			if lang == "en" {
				name = g.NameEn
			}
			fmt.Printf("    %s %d  %s\n", box, i+1, name)
		}
		fmt.Print("  " + T["pickHint"])
		line := strings.TrimSpace(readLine(in))
		if line == "" {
			if len(selected) > 0 {
				break
			}
			fmt.Println(paint(cRed, "  "+T["pickNone"]))
			continue
		}
		for _, f := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == ',' || r == '，' }) {
			if n, err := strconv.Atoi(f); err == nil && n >= 1 && n <= len(rules.Games) {
				b := rules.Games[n-1].Biz
				if selected[b] {
					delete(selected, b)
				} else {
					selected[b] = true
				}
			}
		}
	}

	var games []rules.Game
	var biz []string
	for _, g := range rules.Games {
		if selected[g.Biz] {
			games = append(games, g)
			biz = append(biz, g.Biz)
		}
	}
	s.Lang, s.Games = lang, biz
	settings.Save(s)
	fmt.Println()
	return runWithConsole(engine.Options{Games: games, TakeOver: true}, lang)
}

func readLine(in *bufio.Reader) string {
	s, _ := in.ReadString('\n')
	return strings.TrimRight(s, "\r\n")
}
