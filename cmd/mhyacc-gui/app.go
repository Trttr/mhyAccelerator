package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"mhyacc/internal/engine"
	"mhyacc/internal/events"
	"mhyacc/internal/guard"
	"mhyacc/internal/rules"
	"mhyacc/internal/sysconf"
	"mhyacc/internal/winutil"
)

// App is bound to the web front end: its exported methods are callable from JS.
type App struct {
	ctx context.Context

	mu       sync.Mutex
	eng      *engine.Engine
	cancel   context.CancelFunc
	done     chan struct{}
	lastErr  string
	events   []UIEvent
	settings Settings
}

// Settings persist between runs.
type Settings struct {
	Lang  string   `json:"lang"`  // "zh" or "en"
	Games []string `json:"games"` // last selection (biz ids)
}

// UIEvent is an event rendered in both languages, so switching language
// re-renders history without asking the back end.
type UIEvent struct {
	Time string `json:"time"`
	Kind string `json:"kind"`
	Host string `json:"host"`
	Zh   string `json:"zh"`
	En   string `json:"en"`
}

// GameItem describes one supported game for the selection list.
type GameItem struct {
	Biz       string `json:"biz"`
	Host      string `json:"host"`
	NameZh    string `json:"nameZh"`
	NameEn    string `json:"nameEn"`
	Installed bool   `json:"installed"`
	Pending   bool   `json:"pending"` // the launcher has an unfinished download for it
}

// UIStatus is engine.Status plus whether a run is active.
type UIStatus struct {
	Running         bool   `json:"running"`
	Error           string `json:"error"`
	LauncherRunning bool   `json:"launcherRunning"` // the official launcher process is open
	engine.Status
}

// LauncherRunning reports whether the official launcher is open right now.
func (a *App) LauncherRunning() bool { return sysconf.LauncherRunning() }

// Confirm asks a yes/no question in a native Windows dialog.
func (a *App) Confirm(title, message string) bool {
	if a.ctx == nil {
		return false
	}
	res, err := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type: runtime.QuestionDialog, Title: title, Message: message,
	})
	return err == nil && (res == "Yes" || res == "yes" || res == "Ok" || res == "OK")
}

// ClearData deletes node memory, settings and logs. Not while accelerating.
func (a *App) ClearData() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.eng != nil {
		return errors.New("busy")
	}
	dir := filepath.Dir(settingsPath())
	for _, name := range []string{"nodes.json", "nodes.json.tmp", "settings.json"} {
		os.Remove(filepath.Join(dir, name))
	}
	os.RemoveAll(filepath.Join(dir, "logs"))
	lang := a.settings.Lang
	a.settings = Settings{Lang: lang}
	a.events = nil
	return nil
}

// RestoreHosts removes our hosts entries and the boot task right now, in case
// anything was left behind. Not while accelerating.
func (a *App) RestoreHosts() error {
	a.mu.Lock()
	busy := a.eng != nil
	a.mu.Unlock()
	if busy {
		return errors.New("busy")
	}
	err := sysconf.Remove()
	guard.RemoveBootTask()
	if !sysconf.LauncherRunning() {
		sysconf.FlushLauncherCache()
	}
	return err
}

// appendLog adds a line to today's event log under %LOCALAPPDATA%\mhyAccelerator\logs,
// so what happened can be checked afterwards. Caller holds a.mu.
func (a *App) appendLog(line string) {
	dir := filepath.Join(filepath.Dir(settingsPath()), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	name := filepath.Join(dir, "events-"+time.Now().Format("20060102")+".log")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	f.WriteString(line + "\r\n")
	f.Close()
}

func settingsPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "mhyAccelerator", "settings.json")
}

func newApp() *App {
	a := &App{}
	if data, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(data, &a.settings)
	}
	if a.settings.Lang != "zh" && a.settings.Lang != "en" {
		a.settings.Lang = "en"
		if winutil.ChineseUI() {
			a.settings.Lang = "zh"
		}
	}
	return a
}

// startup also starts pushing the status to the page every second, so the page
// does not depend on its own timers (which the browser may throttle).
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		lastTitle := ""
		for range t.C {
			st := a.Status()
			runtime.EventsEmit(ctx, "status", st)
			title := a.title(st)
			if title != lastTitle {
				runtime.WindowSetTitle(ctx, title)
				lastTitle = title
			}
		}
	}()
}

// title shows the state in the window title (and so in the taskbar).
func (a *App) title(st UIStatus) string {
	a.mu.Lock()
	en := a.settings.Lang == "en"
	a.mu.Unlock()
	switch {
	case !st.Running:
		return "mhyAccelerator"
	case st.Phase == events.PhaseRunning || st.Phase == events.PhaseOptimizing || st.Phase == events.PhaseOptimized:
		if en {
			return "mhyAccelerator - Accelerating"
		}
		return "mhyAccelerator - 加速中"
	default:
		if en {
			return "mhyAccelerator - Preparing…"
		}
		return "mhyAccelerator - 准备中…"
	}
}

// beforeClose stops a running acceleration (restoring the system) before the
// window closes. Returning false lets the window close.
func (a *App) beforeClose(ctx context.Context) bool {
	a.Stop()
	return false
}

func (a *App) saveSettings() {
	data, _ := json.MarshalIndent(a.settings, "", "  ")
	os.MkdirAll(filepath.Dir(settingsPath()), 0o755)
	os.WriteFile(settingsPath(), data, 0o644)
}

// GetSettings returns the saved language and game selection.
func (a *App) GetSettings() Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

// SetLang switches the UI language and remembers it.
func (a *App) SetLang(lang string) {
	if lang != "zh" && lang != "en" {
		return
	}
	a.mu.Lock()
	a.settings.Lang = lang
	a.saveSettings()
	a.mu.Unlock()
}

// Games lists the supported games with what the launcher knows about them.
func (a *App) Games() []GameItem {
	known := map[string]GameItem{}
	for _, g := range engine.Games() {
		known[g.Biz] = GameItem{Installed: g.Installed, Pending: g.Pending}
	}
	var out []GameItem
	for _, g := range rules.Games {
		k := known[g.Biz]
		out = append(out, GameItem{Biz: g.Biz, Host: g.Host, NameZh: g.Name, NameEn: g.NameEn,
			Installed: k.Installed, Pending: k.Pending})
	}
	return out
}

// Start begins accelerating the given games.
func (a *App) Start(biz []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.eng != nil {
		return errors.New("already running")
	}
	var games []rules.Game
	for _, g := range rules.Games {
		for _, b := range biz {
			if g.Biz == b {
				games = append(games, g)
			}
		}
	}
	if len(games) == 0 {
		return errors.New("no game selected")
	}
	a.settings.Games = biz
	a.saveSettings()
	a.lastErr = ""
	a.events = nil

	ctx, cancel := context.WithCancel(context.Background())
	eng := engine.New(engine.Options{Games: games, TakeOver: true}, a.onEvent)
	done := make(chan struct{})
	a.eng, a.cancel, a.done = eng, cancel, done
	go func() {
		defer close(done)
		err := eng.Run(ctx)
		a.mu.Lock()
		if err != nil {
			a.lastErr = err.Error()
		}
		a.eng, a.cancel = nil, nil
		a.mu.Unlock()
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "stopped", a.lastErr)
		}
	}()
	return nil
}

// Stop ends the acceleration and waits (briefly) for the system to be restored.
func (a *App) Stop() {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
	}
}

// Status returns the live state for the dashboard.
func (a *App) Status() UIStatus {
	a.mu.Lock()
	eng, errText := a.eng, a.lastErr
	a.mu.Unlock()
	st := UIStatus{Running: eng != nil, Error: errText, LauncherRunning: sysconf.LauncherRunning()}
	if eng != nil {
		st.Status = eng.Status()
	}
	return st
}

// Events returns the events of the current (or last) run, oldest first.
func (a *App) Events() []UIEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]UIEvent(nil), a.events...)
}

func (a *App) onEvent(e events.Event) {
	ui := UIEvent{Time: e.Time.Format("15:04:05"), Kind: string(e.Kind), Host: e.Host,
		Zh: events.Format(e, "zh"), En: events.Format(e, "en")}
	a.mu.Lock()
	a.events = append(a.events, ui)
	if len(a.events) > 500 {
		a.events = a.events[len(a.events)-500:]
	}
	a.appendLog(e.Time.Format("2006-01-02 15:04:05") + "  " + ui.Zh)
	a.mu.Unlock()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "event", ui)
	}
}
