// Command mhyacc-gui is the windowed version of mhyacc: pick games, press start,
// and watch speed, traffic, node switches and protocols live. Chinese and English.
//
// Build: go build -tags desktop,production -ldflags "-H windowsgui -s -w" ./cmd/mhyacc-gui
package main

import (
	"embed"
	"flag"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"mhyacc/internal/guard"
	"mhyacc/internal/sysconf"
	"mhyacc/internal/winutil"
)

//go:embed all:frontend
var assets embed.FS

func main() {
	// The safety nets re-run this executable: the watchdog as "watchdog -pid N",
	// the boot task as "off -boot". Handle those without opening a window.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "watchdog":
			fs := flag.NewFlagSet("watchdog", flag.ContinueOnError)
			pid := fs.Int("pid", 0, "")
			fs.Parse(os.Args[2:])
			if *pid > 0 {
				guard.Watch(*pid)
			}
			return
		case "off":
			sysconf.Remove()
			guard.RemoveBootTask()
			if !sysconf.LauncherRunning() {
				sysconf.FlushLauncherCache()
			}
			return
		}
	}

	// Changing the hosts file needs administrator rights.
	if !winutil.IsElevated() {
		if err := winutil.RelaunchElevated(os.Args[1:]); err != nil {
			msg := "需要管理员权限才能修改 hosts。\nAdministrator rights are needed to change the hosts file.\n\n" + err.Error()
			winutil.MessageBox("mhyAccelerator", msg)
		}
		return
	}

	// WebView2 throttles timers and stops painting when the window is unfocused or
	// covered (say, by a fullscreen game), which froze the dashboard. Turn that off.
	os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
		"--disable-background-timer-throttling --disable-renderer-backgrounding --disable-backgrounding-occluded-windows")

	// Tall enough to show everything without scrolling, but never beyond the screen.
	height := 1000
	if wa := winutil.WorkAreaHeight(); wa > 0 {
		height = min(height, wa-40)
	}
	height = max(height, 600)

	app := newApp()
	err := wails.Run(&options.App{
		Title:            "mhyAccelerator",
		Width:            1280,
		Height:           height,
		MinWidth:         720,
		MinHeight:        560,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 24, G: 24, B: 27, A: 255},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		Bind:             []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "mhyAccelerator-8c1f3b2e-gui",
		},
		Windows: &windows.Options{
			Theme: windows.SystemDefault,
		},
	})
	if err != nil {
		winutil.MessageBox("mhyAccelerator", err.Error())
	}
}
