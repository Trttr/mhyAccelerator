// Package launcher reads what the official launcher knows: which games are
// installed or have a download pending (from its state file), and when a download
// starts, pauses or finishes (from its download module's log). Read-only.
package launcher

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Game is the launcher's view of one game.
type Game struct {
	Biz         string `json:"biz"`
	Name        string `json:"name"`
	InstallPath string `json:"install_path"`
	Installed   bool   `json:"installed"`
	Pending     bool   `json:"pending"` // an unfinished download task exists
	PCDN        bool   `json:"pcdn"`
}

func dataDir() string {
	return filepath.Join(os.Getenv("APPDATA"), "miHoYo", "HYP", "1_1")
}

// readShared reads a file the launcher may hold open.
func readShared(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 8<<20))
}

var gameRecord = regexp.MustCompile(`\{"download_transaction_no".*?"gameBiz":"([a-z0-9_]+)"`)

// Games lists the games in the launcher's state file. Missing file: empty list.
func Games() []Game {
	raw, err := readShared(filepath.Join(dataDir(), "data", "gamedata.dat"))
	if err != nil {
		return nil
	}
	// The file is a binary container holding one JSON object per game; decode each
	// object starting at its opening brace and let the decoder find its end.
	var out []Game
	seen := map[string]bool{}
	for _, loc := range gameRecord.FindAllSubmatchIndex(raw, -1) {
		var rec struct {
			Downloading bool   `json:"downloading"`
			EnablePcdn  bool   `json:"enablePcdn"`
			GameBiz     string `json:"gameBiz"`
			Name        string `json:"gameShortcutName"`
			InstallPath string `json:"installPath"`
		}
		dec := json.NewDecoder(strings.NewReader(string(raw[loc[0]:])))
		if err := dec.Decode(&rec); err != nil || rec.GameBiz == "" || seen[rec.GameBiz] {
			continue
		}
		seen[rec.GameBiz] = true
		installed := false
		if rec.InstallPath != "" {
			if _, err := os.Stat(rec.InstallPath); err == nil {
				installed = true
			}
		}
		out = append(out, Game{Biz: rec.GameBiz, Name: rec.Name, InstallPath: rec.InstallPath,
			Installed: installed, Pending: rec.Downloading, PCDN: rec.EnablePcdn})
	}
	return out
}

// State is a download state change seen in the launcher's log.
type State struct {
	Time      time.Time
	Kind      string // "started", "paused", "finished"
	PackageID string
	// For "finished" and "paused" when the launcher reports totals:
	Downloaded int64 // bytes downloaded in this task (CDN + PCDN)
	Total      int64 // bytes the task needed
	PCDN       bool
}

var (
	reStart  = regexp.MustCompile(`normal_download app chunk task \S+ new fetch build, package_id: (\w+)`)
	reCancel = regexp.MustCompile(`request to cancel normal_download`)
	reFinish = regexp.MustCompile(`\[chunk\] normal_download app chunk task \S+ finished; .*?info: (\{.*)$`)
)

// Watch follows the download log from its current end and reports state changes
// until ctx ends. It copes with the log being recreated.
func Watch(ctx context.Context, report func(State)) {
	path := filepath.Join(dataDir(), "modules", "sophon", "sophon.log")
	var offset int64 = -1 // -1: start at the end of whatever exists now
	var partial string
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			continue
		}
		size := info.Size()
		if offset < 0 || size < offset {
			if offset < 0 {
				offset = size
			} else {
				offset = 0 // recreated
			}
			partial = ""
		}
		if size > offset {
			f.Seek(offset, io.SeekStart)
			data, _ := io.ReadAll(io.LimitReader(f, size-offset))
			offset += int64(len(data))
			text := partial + string(data)
			lines := strings.Split(text, "\n")
			partial = lines[len(lines)-1]
			for _, line := range lines[:len(lines)-1] {
				if s, ok := parseLine(line); ok {
					report(s)
				}
			}
		}
		f.Close()
	}
}

func parseLine(line string) (State, bool) {
	now := time.Now()
	if m := reStart.FindStringSubmatch(line); m != nil {
		return State{Time: now, Kind: "started", PackageID: m[1]}, true
	}
	if reCancel.MatchString(line) {
		return State{Time: now, Kind: "paused"}, true
	}
	if m := reFinish.FindStringSubmatch(line); m != nil {
		var info struct {
			Status     string `json:"status"`
			PackageID  string `json:"package_id"`
			Downloaded string `json:"current_download_byte"`
			Total      string `json:"total_download_byte"`
			PCDN       bool   `json:"enable_pcdn"`
		}
		if json.NewDecoder(bufio.NewReader(strings.NewReader(m[1]))).Decode(&info) != nil {
			return State{}, false
		}
		s := State{Time: now, PackageID: info.PackageID, PCDN: info.PCDN}
		s.Downloaded, _ = parseInt(info.Downloaded)
		s.Total, _ = parseInt(info.Total)
		switch info.Status {
		case "Success":
			s.Kind = "finished"
		case "Cancelled":
			s.Kind = "paused"
		default:
			return State{}, false
		}
		return s, true
	}
	return State{}, false
}

func parseInt(s string) (int64, error) {
	var v int64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		v = v*10 + int64(c-'0')
	}
	return v, nil
}
