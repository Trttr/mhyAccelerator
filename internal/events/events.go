// Package events defines what the accelerator reports while it runs. Events are
// structured so that every front end (console, GUI) can present them in the
// user's language; Format renders one in Chinese or English.
package events

import (
	"fmt"
	"time"
)

// Kind identifies an event.
type Kind string

const (
	Phase        Kind = "phase"      // Text: one of the Phase* values
	Discovered   Kind = "discovered" // Host: N nodes found, H1/H2 counts, Remembered, Suspect
	Measured     Kind = "measured"   // Host, IP, H2, Rate, Peak, Collapsed, Err
	ModeChosen   Kind = "mode"       // Host, H2, Rate (expected), IP (preferred node)
	NodeSwitched Kind = "switch"     // Host, IP (from), Reason, Peak, Rate
	Warning      Kind = "warning"    // Text: message (already human readable)
	HostRefused  Kind = "refused"    // Text: SNI that is not on the allow list
	Launcher     Kind = "launcher"   // Text: started/paused/finished; Host; Downloaded, Total
	Baseline     Kind = "baseline"   // Host, Rate: estimated speed without us; IP: default nodes
	Summary      Kind = "summary"    // Host, Rate: average over the last minute; Downloaded: bytes in it
	Retest       Kind = "retest"     // Host, IP, Text: recovered/faulty, Rate: re-test result, Peak: usual speed
	Hold         Kind = "hold"       // Host, Reason: HoldFrequent / HoldLocal: switching paused
)

// Phases of a run.
const (
	PhaseDiscovering = "discovering"
	PhaseWaitLaunch  = "wait_launcher" // launcher must be closed before takeover
	PhaseRunning     = "running"       // hosts taken over, forwarding
	PhaseOptimizing  = "optimizing"    // background speed test in progress
	PhaseOptimized   = "optimized"
	PhaseStopping    = "stopping"
	PhaseStopped     = "stopped"
)

// Reasons for a node switch.
const (
	ReasonStall    = "stall"    // no data for 10 s
	ReasonCollapse = "collapse" // under 1 MB/s for 10 s after being fast
	ReasonDecay    = "decay"    // under 40% of its own peak for 20 s
	ReasonSlowPeer = "slow"     // far slower than the other connections (HTTP/1.1)
	ReasonDial     = "dial"     // could not connect or no handshake; another node was used

	HoldFrequent = "frequent" // two switches within a minute
	HoldLocal    = "local"    // the old node re-tested fine while the new one is slow too
)

// Event is one thing that happened.
type Event struct {
	Time       time.Time `json:"time"`
	Kind       Kind      `json:"kind"`
	Host       string    `json:"host,omitempty"`
	IP         string    `json:"ip,omitempty"`
	H2         bool      `json:"h2,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Rate       float64   `json:"rate,omitempty"` // bytes/s
	Peak       float64   `json:"peak,omitempty"` // bytes/s
	Collapsed  bool      `json:"collapsed,omitempty"`
	N          int       `json:"n,omitempty"`
	H1Count    int       `json:"h1,omitempty"`
	H2Count    int       `json:"h2count,omitempty"`
	Remember   int       `json:"remembered,omitempty"`
	Suspect    int       `json:"suspect,omitempty"`
	Text       string    `json:"text,omitempty"`
	Err        string    `json:"err,omitempty"`
	Downloaded int64     `json:"downloaded,omitempty"`
	Total      int64     `json:"total,omitempty"`
}

// Sink receives events. It must not block for long.
type Sink func(Event)

func mb(v float64) string { return fmt.Sprintf("%.1f MB/s", v/1e6) }

func proto(h2 bool) string {
	if h2 {
		return "HTTP/2"
	}
	return "HTTP/1.1"
}

var phaseText = map[string][2]string{
	PhaseDiscovering: {"正在探测可用节点…", "Probing available nodes…"},
	PhaseWaitLaunch:  {"等待米哈游启动器退出…", "Waiting for the miHoYo launcher to quit…"},
	PhaseRunning:     {"加速已开启，可以打开启动器下载", "Acceleration is on; open the launcher to download"},
	PhaseOptimizing:  {"开始后台测速，持续优化节点", "Background speed test started"},
	PhaseOptimized:   {"节点优化完成", "Node optimization finished"},
	PhaseStopping:    {"正在停止并还原…", "Stopping and restoring…"},
	PhaseStopped:     {"已停止，hosts 已还原", "Stopped; hosts restored"},
}

// Format renders e for lang "zh" or "en". Host names are left as is; front ends
// may replace them with the game's name.
func Format(e Event, lang string) string {
	en := lang == "en"
	pick := func(zh, enText string) string {
		if en {
			return enText
		}
		return zh
	}
	switch e.Kind {
	case Phase:
		if t, ok := phaseText[e.Text]; ok {
			return pick(t[0], t[1])
		}
		return e.Text
	case Discovered:
		return pick(
			fmt.Sprintf("%s：探测到可用节点 %d 个（HTTP/1.1 %d · HTTP/2 %d），历史优选节点 %d 个，近期异常节点 %d 个",
				e.Host, e.N, e.H1Count, e.H2Count, e.Remember, e.Suspect),
			fmt.Sprintf("%s: %d nodes available (HTTP/1.1 %d · HTTP/2 %d); %d preferred from history, %d recently faulty",
				e.Host, e.N, e.H1Count, e.H2Count, e.Remember, e.Suspect))
	case Measured:
		if e.Err != "" {
			return pick(fmt.Sprintf("%s：测速 %s 失败（%s）", e.Host, e.IP, e.Err),
				fmt.Sprintf("%s: speed test of %s failed (%s)", e.Host, e.IP, e.Err))
		}
		if e.Collapsed {
			return pick(fmt.Sprintf("%s：测速 %s（%s）%s，峰值 %s 后明显回落，已降低优先级", e.Host, e.IP, proto(e.H2), mb(e.Rate), mb(e.Peak)),
				fmt.Sprintf("%s: speed test %s (%s) %s; dropped after a %s peak, deprioritized", e.Host, e.IP, proto(e.H2), mb(e.Rate), mb(e.Peak)))
		}
		return pick(fmt.Sprintf("%s：测速 %s（%s）%s", e.Host, e.IP, proto(e.H2), mb(e.Rate)),
			fmt.Sprintf("%s: speed test %s (%s) %s", e.Host, e.IP, proto(e.H2), mb(e.Rate)))
	case ModeChosen:
		return pick(fmt.Sprintf("%s：采用 %s 节点，首选 %s（预计 %s）", e.Host, proto(e.H2), e.IP, mb(e.Rate)),
			fmt.Sprintf("%s: using %s nodes, preferred %s (expected %s)", e.Host, proto(e.H2), e.IP, mb(e.Rate)))
	case NodeSwitched:
		switch e.Reason {
		case ReasonStall:
			return pick(fmt.Sprintf("%s：节点 %s 连续 10 秒无数据返回，已切换节点，原节点进入观察", e.Host, e.IP),
				fmt.Sprintf("%s: node %s returned no data for 10 s; switched node, the old one is under observation", e.Host, e.IP))
		case ReasonCollapse:
			return pick(fmt.Sprintf("%s：节点 %s 速度由 %s 骤降至 %s，已切换节点，原节点进入观察", e.Host, e.IP, mb(e.Peak), mb(e.Rate)),
				fmt.Sprintf("%s: node %s fell from %s to %s; switched node, the old one is under observation", e.Host, e.IP, mb(e.Peak), mb(e.Rate)))
		case ReasonDecay:
			return pick(fmt.Sprintf("%s：节点 %s 速度持续下降（峰值 %s，当前 %s），已切换节点，原节点进入观察", e.Host, e.IP, mb(e.Peak), mb(e.Rate)),
				fmt.Sprintf("%s: node %s kept slowing (peak %s, now %s); switched node, the old one is under observation", e.Host, e.IP, mb(e.Peak), mb(e.Rate)))
		case ReasonSlowPeer:
			return pick(fmt.Sprintf("%s：节点 %s 的连接明显慢于其他连接（%s），已切换节点", e.Host, e.IP, mb(e.Rate)),
				fmt.Sprintf("%s: a connection on node %s was much slower than the rest (%s); switched node", e.Host, e.IP, mb(e.Rate)))
		case ReasonDial:
			return pick(fmt.Sprintf("%s：节点 %s 连接失败，已自动改用其他节点", e.Host, e.IP),
				fmt.Sprintf("%s: could not connect to node %s; used another node", e.Host, e.IP))
		}
		return pick(fmt.Sprintf("%s：已切换节点（%s）", e.Host, e.IP), fmt.Sprintf("%s: switched node (%s)", e.Host, e.IP))
	case Retest:
		if e.Text == "repeat" {
			return pick(fmt.Sprintf("%s：节点 %s 复测通过后再次异常，判定为限速节点（起速快、随后限速），已列为异常节点", e.Host, e.IP),
				fmt.Sprintf("%s: node %s misbehaved again after passing a re-test; it throttles after a fast start. Marked faulty", e.Host, e.IP))
		}
		if e.Text == "recovered" {
			return pick(fmt.Sprintf("%s：节点 %s 复测恢复正常（%s），已重新列入候选", e.Host, e.IP, mb(e.Rate)),
				fmt.Sprintf("%s: node %s re-tested fine (%s); back in rotation", e.Host, e.IP, mb(e.Rate)))
		}
		return pick(fmt.Sprintf("%s：节点 %s 复测仍异常（%s，平时约 %s），已列为异常节点", e.Host, e.IP, mb(e.Rate), mb(e.Peak)),
			fmt.Sprintf("%s: node %s still faulty on re-test (%s, usually about %s); marked faulty", e.Host, e.IP, mb(e.Rate), mb(e.Peak)))
	case Hold:
		if e.Reason == HoldLocal {
			return pick(e.Host+"：原节点复测正常而当前下载也偏慢，可能是本机网络繁忙或启动器正在写入文件，暂停切换节点 2 分钟",
				e.Host+": the old node re-tested fine while the download is still slow; likely a busy local line or the launcher writing files. Switching paused for 2 minutes")
		}
		return pick(e.Host+"：不同机房的节点接连异常，速度可能是整体下降（本机网络繁忙或启动器正在写入文件），暂停切换节点 1 分钟",
			e.Host+": nodes at different sites misbehaved in a row; the slowdown is probably local (busy line or the launcher writing files). Switching paused for 1 minute")
	case Summary:
		return pick(fmt.Sprintf("%s：近 1 分钟平均 %s，加速流量 %.2f GB", e.Host, mb(e.Rate), float64(e.Downloaded)/1e9),
			fmt.Sprintf("%s: last minute averaged %s, %.2f GB accelerated", e.Host, mb(e.Rate), float64(e.Downloaded)/1e9))
	case HostRefused:
		return pick("已拒绝对 "+e.Text+" 的连接（不在加速范围内）", "refused a connection for "+e.Text+" (not covered)")
	case Baseline:
		if e.Err != "" {
			return pick(fmt.Sprintf("%s：无法估算基准速度（%s）", e.Host, e.Err),
				fmt.Sprintf("%s: cannot estimate the baseline speed (%s)", e.Host, e.Err))
		}
		return pick(fmt.Sprintf("%s：基准速度（估算）%s，默认分配节点 %s", e.Host, mb(e.Rate), e.IP),
			fmt.Sprintf("%s: baseline speed (estimated) %s, default nodes %s", e.Host, mb(e.Rate), e.IP))
	case Launcher:
		game := e.Host
		if game == "" {
			game = pick("启动器", "launcher")
		}
		switch e.Text {
		case "started":
			return pick(game+"：启动器开始下载", game+": the launcher started downloading")
		case "paused":
			return pick(game+"：启动器已暂停下载", game+": the launcher paused the download")
		case "finished":
			return pick(fmt.Sprintf("%s：下载完成（本次任务 %.1f GB）", game, float64(e.Downloaded)/1e9),
				fmt.Sprintf("%s: download finished (%.1f GB in this task)", game, float64(e.Downloaded)/1e9))
		}
	case Warning:
		return e.Text
	}
	return string(e.Kind)
}
