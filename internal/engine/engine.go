// Package engine runs the accelerator: it discovers nodes, takes over the
// launcher's download hosts, forwards connections, keeps optimizing nodes in the
// background, and restores everything on exit. Front ends (console, GUI) drive it
// through Run/Probe and observe it through events and Status.
package engine

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"mhyacc/internal/events"
	"mhyacc/internal/forwarder"
	"mhyacc/internal/guard"
	"mhyacc/internal/launcher"
	"mhyacc/internal/memory"
	"mhyacc/internal/nodetable"
	"mhyacc/internal/probe"
	"mhyacc/internal/resolver"
	"mhyacc/internal/rules"
	"mhyacc/internal/sophon"
	"mhyacc/internal/sysconf"
)

// Options configure a run.
type Options struct {
	Games        []rules.Game
	Exclude      map[string]bool
	TakeOver     bool // point the hosts file at us (false: forwarder only, for testing)
	KeepLauncher bool // do not wait for the launcher to exit before taking over
	MaxPerNode   int
	RecvBuf      int     // bytes; 0 = system default
	ScreenH1     int     // HTTP/1.1 nodes screened per game
	ScreenH2     int     // HTTP/2 nodes screened per game
	Finalists    int     // per protocol, measured at length
	TestSecs     float64 // measured seconds per finalist (after warm-up)
	Verbose      bool    // also report screening results
}

func (o *Options) defaults() {
	if o.MaxPerNode <= 0 {
		o.MaxPerNode = 6
	}
	if o.ScreenH1 <= 0 {
		o.ScreenH1 = 10
	}
	if o.ScreenH2 <= 0 {
		o.ScreenH2 = 6
	}
	if o.Finalists <= 0 {
		o.Finalists = 3
	}
	if o.TestSecs <= 0 {
		o.TestSecs = 6
	}
}

// HostStatus describes one game while running.
type HostStatus struct {
	Host      string           `json:"host"`
	Biz       string           `json:"biz"`
	Mode      string           `json:"mode"` // "http/1.1" or "h2"
	Rate      float64          `json:"rate"` // bytes/s, last few seconds
	Bytes     int64            `json:"bytes"`
	Active    int              `json:"active"`
	Switches  int64            `json:"switches"`
	Baseline  float64          `json:"baseline"`  // estimated speed without us (bytes/s), 0 until measured
	BaseState string           `json:"baseState"` // "" (pending), "done", "failed"
	Nodes     []nodetable.View `json:"nodes"`     // best first, at most 5
}

// Status is a point-in-time view for display.
type Status struct {
	Phase    string       `json:"phase"`
	Launcher string       `json:"launcher"` // started / paused / finished / "" (unknown)
	Started  time.Time    `json:"started"`
	Rate     float64      `json:"rate"`  // bytes/s through us, all games
	Bytes    int64        `json:"bytes"` // through us, all games
	History  []float64    `json:"history"`
	Hosts    []HostStatus `json:"hosts"`
}

// Engine is one accelerator run.
type Engine struct {
	opt   Options
	sink  events.Sink
	table *nodetable.Table
	mem   *memory.Store
	fwd   *forwarder.Forwarder

	mu       sync.Mutex
	phase    string
	started  time.Time
	prev     map[string]int64
	rates    map[string]float64
	history  []float64
	builds   map[string]*sophon.Build
	discover map[string]*found

	launcherState string             // last state from the launcher's log: started/paused/finished
	baseline      map[string]float64 // estimated speed without us, per host (bytes/s)
	baseState     map[string]string  // "" (pending), "done", "failed"

	runCtx  context.Context
	chunks  map[string][]sophon.Chunk // real chunk files per host, for (re-)tests
	retests map[string]*sync.Mutex    // one re-test at a time per host
}

// found is what discovery learned about one game.
type found struct {
	game      rules.Game
	defaults  []string // what the system resolver returns: the launcher's likely nodes without us
	order     []string // candidate IPs, best guess first
	h2        map[string]bool
	alive     map[string]bool
	handshake map[string]time.Duration
}

// New prepares an engine. sink receives every event; it may be nil.
func New(opt Options, sink events.Sink) *Engine {
	opt.defaults()
	if sink == nil {
		sink = func(events.Event) {}
	}
	t := nodetable.New()
	t.MaxPerNode = opt.MaxPerNode
	return &Engine{
		opt: opt, sink: sink, table: t, mem: memory.Load(memory.DefaultPath()),
		prev: map[string]int64{}, rates: map[string]float64{}, discover: map[string]*found{},
		baseline: map[string]float64{}, baseState: map[string]string{}, chunks: map[string][]sophon.Chunk{}, retests: map[string]*sync.Mutex{},
	}
}

func (e *Engine) emit(ev events.Event) {
	ev.Time = time.Now()
	e.sink(ev)
}

func (e *Engine) setPhase(p string) {
	e.mu.Lock()
	e.phase = p
	e.mu.Unlock()
	e.emit(events.Event{Kind: events.Phase, Text: p})
}

func (e *Engine) warn(format string, a ...any) {
	e.emit(events.Event{Kind: events.Warning, Text: fmt.Sprintf(format, a...)})
}

// Probe discovers and measures nodes without changing the system.
func (e *Engine) Probe(ctx context.Context) error {
	e.setPhase(events.PhaseDiscovering)
	if err := e.discoverAll(ctx); err != nil {
		return err
	}
	e.optimizeAll(ctx)
	return nil
}

// Run accelerates until ctx is cancelled, then restores the system.
func (e *Engine) Run(ctx context.Context) (err error) {
	e.mu.Lock()
	e.started = time.Now()
	e.mu.Unlock()
	e.setPhase(events.PhaseDiscovering)

	// Listen first: if another run is active this fails and we leave its hosts block alone.
	ln, err := net.Listen("tcp", net.JoinHostPort(sysconf.ListenIP, "443"))
	if err != nil {
		return fmt.Errorf("cannot listen on %s:443 (is another mhyacc running?): %w", sysconf.ListenIP, err)
	}
	defer ln.Close()
	if e.opt.TakeOver {
		if on, _ := sysconf.Present(); on {
			if err := sysconf.Remove(); err != nil {
				return err
			}
		}
	}

	if err := e.discoverAll(ctx); err != nil {
		return err
	}

	e.mu.Lock()
	e.runCtx = ctx
	e.mu.Unlock()
	e.fwd = forwarder.New(e.table, rules.Allowed, e.sink)
	e.fwd.RecvBuf = e.opt.RecvBuf
	e.fwd.OnSuspect = e.onSuspect
	serveCtx, stopServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- e.fwd.Serve(serveCtx, ln) }()
	defer func() {
		stopServe()
		<-serveDone
	}()

	if e.opt.TakeOver {
		restore, err := e.takeOver(ctx)
		if err != nil {
			return err
		}
		if restore == nil { // cancelled while waiting for the launcher
			return nil
		}
		defer restore()
	}
	e.setPhase(events.PhaseRunning)

	defer e.saveMemory()
	go e.sampler(ctx)
	go e.memorySaver(ctx)
	go launcher.Watch(ctx, e.onLauncher)
	go func() {
		e.setPhase(events.PhaseOptimizing)
		e.optimizeAll(ctx)
		if ctx.Err() == nil {
			e.setPhase(events.PhaseOptimized)
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-serveDone:
		serveDone <- err // let the deferred wait see it
		if err != nil {
			return err
		}
	}
	e.setPhase(events.PhaseStopping)
	return nil
}

// onSuspect re-tests a node the forwarder has set aside: 20 s after the switch,
// and again 40 s later if the first test is not convincing. The node is judged
// against its usual speed, or, while the launcher is busy on the line, against
// what the launcher is currently getting. A node that passes is back in rotation;
// one that fails both tests becomes a faulty node. If the old node is fine while
// the download stays slow on the new one, the cause is local and switching pauses.
//
// A repeat (cleared by a re-test minutes ago, misbehaving again) is not tested a
// third time: a node that starts every connection fast and then throttles it will
// always pass a few-second test. It is marked faulty at once.
func (e *Engine) onSuspect(host, ip string, h2, repeat bool) {
	suspectedAt := time.Now()
	e.mu.Lock()
	ctx := e.runCtx
	lock := e.retests[host]
	if lock == nil {
		lock = &sync.Mutex{}
		e.retests[host] = lock
	}
	e.mu.Unlock()
	if ctx == nil {
		e.table.Resolve(host, ip, true, 0)
		return
	}
	usual := e.table.PrevScore(host, ip)
	if repeat {
		e.table.Resolve(host, ip, false, 0)
		e.emit(events.Event{Kind: events.Retest, Host: host, IP: ip, H2: h2, Text: "repeat", Peak: usual})
		return
	}
	lock.Lock()
	defer lock.Unlock()
	chunks := e.chunksFor(ctx, host)
	if len(chunks) == 0 {
		e.table.Resolve(host, ip, true, 0) // nothing to test with: release it unjudged
		return
	}
	// Tests are due 20 s and 60 s after the suspicion itself. Time spent queued
	// behind another node's re-test counts, so a verdict is never pushed far out.
	due := []time.Duration{20 * time.Second, 60 * time.Second}
	for i, d := range due {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(suspectedAt.Add(d))):
		}
		par := 6
		if h2 {
			par = 8
		}
		e.table.SetTesting(host, ip, true)
		m := probe.Measure(ctx, host, ip, h2, chunks, probe.MeasureOpts{
			Parallel: par, Warmup: 1500 * time.Millisecond, Duration: 4 * time.Second})
		e.table.SetTesting(host, ip, false)
		if ctx.Err() != nil {
			return
		}
		e.mu.Lock()
		live := e.rates[host]
		e.mu.Unlock()
		ok := m.Err == nil && !m.Collapsed && (m.Rate >= 0.6*usual || (m.Rate >= 2e6 && m.Rate >= 0.6*live))
		if ok {
			e.table.Resolve(host, ip, true, m.Rate)
			e.emit(events.Event{Kind: events.Retest, Host: host, IP: ip, H2: h2, Text: "recovered", Rate: m.Rate, Peak: usual})
			if live > 1e6 && live < 0.5*usual && e.fwd != nil {
				e.fwd.HoldSwitching(host, 2*time.Minute, events.HoldLocal)
			}
			return
		}
		if i == len(due)-1 {
			e.table.Resolve(host, ip, false, m.Rate)
			e.emit(events.Event{Kind: events.Retest, Host: host, IP: ip, H2: h2, Text: "faulty", Rate: m.Rate, Peak: usual})
		}
	}
}

// chunksFor returns test chunks for host, fetching them if the background test
// has not done so yet.
func (e *Engine) chunksFor(ctx context.Context, host string) []sophon.Chunk {
	e.mu.Lock()
	c, builds := e.chunks[host], e.builds
	e.mu.Unlock()
	if len(c) > 0 {
		return c
	}
	if builds == nil {
		var err error
		if builds, err = sophon.FetchBuilds(ctx); err != nil {
			return nil
		}
	}
	b := builds[host]
	if b == nil {
		return nil
	}
	c, err := b.SampleChunks(ctx, 64)
	if err != nil {
		return nil
	}
	e.mu.Lock()
	e.chunks[host] = c
	e.mu.Unlock()
	return c
}

// onLauncher reacts to the launcher's own download state, read from its log.
func (e *Engine) onLauncher(s launcher.State) {
	if e.fwd != nil {
		e.fwd.SetPaused(s.Kind == "paused")
	}
	host := ""
	e.mu.Lock()
	for h, b := range e.builds {
		if b.PackageID != "" && b.PackageID == s.PackageID {
			host = h
		}
	}
	e.launcherState = s.Kind
	e.mu.Unlock()
	e.emit(events.Event{Kind: events.Launcher, Text: s.Kind, Host: host, Downloaded: s.Downloaded, Total: s.Total})
}

// Games returns what the launcher knows about each supported game (installed,
// pending download), for front ends to preselect games.
func Games() []launcher.Game { return launcher.Games() }

// takeOver points the launcher at us and returns the function that undoes it.
func (e *Engine) takeOver(ctx context.Context) (func(), error) {
	// The launcher's DNS cache can only be cleared while it is closed, and it only
	// picks up the hosts change after a restart, so wait for it to exit.
	if sysconf.LauncherRunning() && !e.opt.KeepLauncher {
		e.setPhase(events.PhaseWaitLaunch)
		for sysconf.LauncherRunning() {
			select {
			case <-ctx.Done():
				return nil, nil
			case <-time.After(time.Second):
			}
		}
	}
	if !sysconf.LauncherRunning() {
		if _, err := sysconf.FlushLauncherCache(); err != nil {
			e.warn("could not clear the launcher's DNS cache: %v", err)
		}
	}
	// Safety nets for exits this process cannot handle itself (see internal/guard).
	if err := guard.InstallBootTask(); err != nil {
		e.warn("could not register the boot cleanup task: %v", err)
	}
	if _, err := guard.StartWatchdog(); err != nil {
		e.warn("could not start the watchdog: %v", err)
	}
	if err := sysconf.Apply(rules.Hosts(e.opt.Games), os.Getpid()); err != nil {
		guard.RemoveBootTask()
		return nil, err
	}
	return func() {
		if err := sysconf.Remove(); err != nil {
			e.warn("restoring hosts failed, run \"mhyacc off\": %v", err)
			return // keep the boot task as a last resort
		}
		guard.RemoveBootTask()
		if !sysconf.LauncherRunning() {
			sysconf.FlushLauncherCache()
		}
		e.setPhase(events.PhaseStopped)
	}, nil
}

// discoverAll resolves, orders and handshakes the nodes of every game in parallel,
// and seeds the node table so forwarding can start right away.
func (e *Engine) discoverAll(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make([]error, len(e.opt.Games))
	for i, g := range e.opt.Games {
		wg.Add(1)
		go func(i int, g rules.Game) {
			defer wg.Done()
			errs[i] = e.discoverGame(ctx, g)
		}(i, g)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (e *Engine) discoverGame(ctx context.Context, g rules.Game) error {
	all, err := resolver.Resolve(ctx, g.Host)
	if err != nil {
		return err
	}
	// Order: nodes remembered as good first (even if DNS did not return them this
	// time), then fresh DNS answers, and nodes that recently failed (or whose /24
	// did) last.
	var order, suspects []string
	seen := map[string]bool{}
	remembered := e.mem.Good(g.Host)
	for _, ip := range remembered {
		if !e.opt.Exclude[ip] {
			order = append(order, ip)
			seen[ip] = true
		}
	}
	for _, c := range all {
		switch {
		case e.opt.Exclude[c.IP] || seen[c.IP]:
		case e.mem.Suspect(g.Host, c.IP):
			suspects = append(suspects, c.IP)
		default:
			order = append(order, c.IP)
		}
		seen[c.IP] = true
	}
	order = append(order, suspects...)

	// The nodes the launcher would use without us. Used only to estimate the original
	// speed, and asked before the hosts file is taken over.
	defaults := resolver.Plain(ctx, g.Host, 3)

	// Handshake the candidates (cheap, in parallel): alive or not, and which protocol.
	// The first 48 in order are plenty and keep start-up short on a busy line.
	if len(order) > 48 {
		order = order[:48]
	}
	f := &found{game: g, defaults: defaults, order: order, h2: map[string]bool{}, alive: map[string]bool{}, handshake: map[string]time.Duration{}}
	var mu sync.Mutex
	sem := make(chan struct{}, 24)
	var wg sync.WaitGroup
	toCheck := append([]string(nil), order...)
	inCheck := map[string]bool{}
	for _, ip := range order {
		inCheck[ip] = true
	}
	for _, ip := range defaults {
		// Handshake default nodes too, to learn their protocol. Checked against the
		// (possibly truncated) candidate list, not everything DNS returned: a default
		// node cut from the list must still be tested, or the baseline is silently lost.
		if !inCheck[ip] {
			toCheck = append(toCheck, ip)
			inCheck[ip] = true
		}
	}
	for _, ip := range toCheck {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p, d, err := probe.ALPN(ctx, g.Host, ip)
			if err != nil {
				return
			}
			mu.Lock()
			f.alive[ip], f.h2[ip], f.handshake[ip] = true, p == "h2", d
			mu.Unlock()
		}(ip)
	}
	wg.Wait()

	h1n, h2n := 0, 0
	inOrder := map[string]bool{}
	for _, ip := range order {
		inOrder[ip] = true
	}
	for ip := range f.alive {
		if !inOrder[ip] {
			continue // a default node outside our candidates: measured for the baseline only
		}
		if f.h2[ip] {
			h2n++
		} else {
			h1n++
		}
		// Placeholder score until measured: remembered live speed, else latency-based.
		e.table.Seed(g.Host, ip, f.h2[ip], e.initialScore(g.Host, ip, 1e5/max(f.handshake[ip].Seconds(), 0.01)))
	}
	e.emit(events.Event{Kind: events.Discovered, Host: g.Host, N: h1n + h2n, H1Count: h1n, H2Count: h2n,
		Remember: len(remembered), Suspect: len(suspects)})
	if h1n+h2n == 0 {
		return fmt.Errorf("%s: no node reachable", g.Host)
	}
	e.mu.Lock()
	e.discover[g.Host] = f
	e.mu.Unlock()
	return nil
}

// initialScore combines a quick estimate with what earlier runs observed. Live
// speeds from earlier runs are far more telling, so the larger wins; recently
// failing nodes start heavily discounted.
func (e *Engine) initialScore(host, ip string, estimate float64) float64 {
	score := max(estimate, e.mem.Score(host, ip))
	if e.mem.Suspect(host, ip) {
		score *= 0.2
	}
	return score
}

// optimizeAll measures nodes game by game (one at a time, so tests do not compete).
func (e *Engine) optimizeAll(ctx context.Context) {
	builds, err := sophon.FetchBuilds(ctx)
	if err != nil {
		e.warn("cannot read the current builds, keeping estimated node order: %v", err)
		for _, g := range e.opt.Games {
			e.baselineFailed(g.Host, "cannot read the current builds")
		}
		return
	}
	e.mu.Lock()
	e.builds = builds
	e.mu.Unlock()
	for _, g := range e.opt.Games {
		if ctx.Err() != nil {
			return
		}
		e.mu.Lock()
		f := e.discover[g.Host]
		e.mu.Unlock()
		if f != nil {
			e.optimizeGame(ctx, f, builds[g.Host])
		}
	}
}

// optimizeGame runs a two-stage test on real chunk files:
//
//  1. screening: short tests on many nodes, four at a time, to drop the bad ones;
//  2. finals: the best few of each protocol, one at a time, measured at length the
//     way the launcher downloads (one HTTP/2 connection with eight streams, or four
//     HTTP/1.1 connections), after a warm-up, watching for "fast then slow" nodes.
//
// Then it decides whether the launcher is better off on HTTP/1.1 or HTTP/2 nodes.
func (e *Engine) optimizeGame(ctx context.Context, f *found, b *sophon.Build) {
	host := f.game.Host
	if b == nil {
		e.warn("%s: no current build found, keeping estimated node order", host)
		e.baselineFailed(host, "no current build found")
		return
	}
	chunks, err := b.SampleChunks(ctx, 64)
	if err != nil {
		e.warn("%s: cannot read chunk list, keeping estimated node order: %v", host, err)
		e.baselineFailed(host, "cannot read chunk list")
		return
	}
	e.mu.Lock()
	e.chunks[host] = chunks
	e.mu.Unlock()

	// Baseline first, while the launcher is most likely still idle: the default
	// nodes, downloaded the way the launcher would (one HTTP/2 connection with
	// eight streams, or HTTP/1.1 connections spread over the nodes).
	var base, h2Sum float64
	var h2N int
	var baseIPs []string
	why := "no default node found" // reported if no default node can be measured
	if len(f.defaults) > 0 {
		why = "default nodes unreachable: " + strings.Join(f.defaults, ", ")
	}
	for _, ip := range f.defaults {
		if ctx.Err() != nil || len(baseIPs) >= 2 {
			break
		}
		if !f.alive[ip] {
			continue
		}
		par := 6
		if f.h2[ip] {
			par = 8
		}
		m := probe.Measure(ctx, host, ip, f.h2[ip], chunks, probe.MeasureOpts{
			Parallel: par, Warmup: 2 * time.Second, Duration: 5 * time.Second})
		if m.Err != nil {
			why = fmt.Sprintf("test of %s failed: %v", ip, m.Err)
			continue
		}
		baseIPs = append(baseIPs, ip)
		if f.h2[ip] {
			h2Sum += m.Rate // the launcher uses one of them: expect the average
			h2N++
		} else {
			base += m.Rate // twelve connections spread over all of them
		}
	}
	if h2N > 0 {
		base += h2Sum / float64(h2N)
	}
	if base > 0 {
		e.mu.Lock()
		e.baseline[host] = base
		e.baseState[host] = "done"
		e.mu.Unlock()
		e.emit(events.Event{Kind: events.Baseline, Host: host, Rate: base, IP: strings.Join(baseIPs, ", ")})
	} else if ctx.Err() == nil {
		e.baselineFailed(host, why)
	}

	var h1, h2 []string
	for _, ip := range f.order {
		if !f.alive[ip] {
			continue
		}
		if f.h2[ip] {
			if len(h2) < e.opt.ScreenH2 {
				h2 = append(h2, ip)
			}
		} else if len(h1) < e.opt.ScreenH1 {
			h1 = append(h1, ip)
		}
	}

	// Stage 1: screening.
	screen := func(ips []string, isH2 bool) []probe.Measurement {
		out := make([]probe.Measurement, len(ips))
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for i, ip := range ips {
			wg.Add(1)
			go func(i int, ip string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				par := 2
				if isH2 {
					par = 4
				}
				out[i] = probe.Measure(ctx, host, ip, isH2, chunks, probe.MeasureOpts{
					Parallel: par, Warmup: 700 * time.Millisecond, Duration: 1300 * time.Millisecond})
			}(i, ip)
		}
		wg.Wait()
		sort.SliceStable(out, func(i, j int) bool { return out[i].Rate > out[j].Rate })
		return out
	}
	finalists := func(ms []probe.Measurement) []string {
		var ips []string
		for _, m := range ms {
			if m.Err == nil && len(ips) < e.opt.Finalists {
				ips = append(ips, m.IP)
			}
			if m.Err == nil {
				e.table.SetProbe(host, m.IP, m.Rate)
			}
			if e.opt.Verbose {
				e.emitMeasured(host, m)
			}
		}
		return ips
	}
	f1 := finalists(screen(h1, false))
	if ctx.Err() != nil {
		return
	}
	f2 := finalists(screen(h2, true))
	if ctx.Err() != nil {
		return
	}

	// Stage 2: finals, one at a time.
	final := func(ips []string, isH2 bool) []probe.Measurement {
		var out []probe.Measurement
		for _, ip := range ips {
			if ctx.Err() != nil {
				break
			}
			par := 4
			if isH2 {
				par = 8
			}
			m := probe.Measure(ctx, host, ip, isH2, chunks, probe.MeasureOpts{
				Parallel: par, Warmup: 2 * time.Second, Duration: time.Duration(e.opt.TestSecs * float64(time.Second))})
			e.emitMeasured(host, m)
			if m.Err == nil {
				e.table.SetProbe(host, ip, m.Rate)
				if m.Collapsed {
					e.table.FailIP(host, ip)
				}
				out = append(out, m)
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Rate > out[j].Rate })
		return out
	}
	r1 := final(f1, false)
	r2 := final(f2, true)
	if ctx.Err() != nil {
		return
	}

	// Mode: the launcher spreads twelve HTTP/1.1 connections over the best nodes, so
	// their rates roughly add up; on HTTP/2 it has a single connection. HTTP/1.1 is
	// preferred unless one HTTP/2 connection is clearly faster, since many independent
	// connections ride out packet loss far better.
	var h1Sum float64
	for i, m := range r1 {
		if i < 3 && !m.Collapsed {
			h1Sum += m.Rate
		}
	}
	var h2Best float64
	var h2IP string
	for _, m := range r2 {
		if !m.Collapsed && m.Rate > h2Best {
			h2Best, h2IP = m.Rate, m.IP
		}
	}
	if h1Sum == 0 && h2Best == 0 {
		return // nothing usable measured; keep the table's own rule
	}
	// While the launcher is downloading, our tests share the line with it and read
	// low. Its live speed is then the better measure of what HTTP/1.1 delivers, so
	// switching to HTTP/2 must beat that too.
	e.mu.Lock()
	live := e.rates[host]
	e.mu.Unlock()
	if e.table.Mode(host) == "http/1.1" {
		h1Sum = max(h1Sum, live)
	}
	preferH2 := h1Sum == 0 || h2Best > 1.2*h1Sum
	e.table.SetMode(host, preferH2)
	ev := events.Event{Kind: events.ModeChosen, Host: host, H2: preferH2}
	if preferH2 {
		ev.IP, ev.Rate = h2IP, h2Best
	} else {
		ev.IP, ev.Rate = r1[0].IP, h1Sum
	}
	e.emit(ev)
}

// baselineFailed records that the original speed cannot be estimated, so front
// ends stop saying "estimating" and the log says why.
func (e *Engine) baselineFailed(host, why string) {
	e.mu.Lock()
	e.baseState[host] = "failed"
	e.mu.Unlock()
	e.emit(events.Event{Kind: events.Baseline, Host: host, Err: why})
}

func (e *Engine) emitMeasured(host string, m probe.Measurement) {
	ev := events.Event{Kind: events.Measured, Host: host, IP: m.IP, H2: m.H2, Rate: m.Rate, Peak: m.Peak, Collapsed: m.Collapsed}
	if m.Err != nil {
		ev.Err = shortErr(m.Err)
	}
	e.emit(ev)
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// sampler computes per-game rates once a second, keeps a 5-minute history, and
// once a minute reports a short summary for each game that downloaded something,
// so the log shows the accelerator is alive even when nothing goes wrong.
func (e *Engine) sampler(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	minuteStart := map[string]int64{}
	lastSummary := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if e.fwd == nil {
			continue
		}
		snap := e.fwd.Snapshot()
		if since := time.Since(lastSummary); since >= time.Minute {
			for _, g := range e.opt.Games {
				b := snap[g.Host].Bytes
				if d := b - minuteStart[g.Host]; d > 1<<20 {
					e.emit(events.Event{Kind: events.Summary, Host: g.Host, Rate: float64(d) / since.Seconds(), Downloaded: d})
				}
				minuteStart[g.Host] = b
			}
			lastSummary = time.Now()
		}
		e.mu.Lock()
		var total float64
		for _, g := range e.opt.Games {
			b := snap[g.Host].Bytes
			r := float64(b - e.prev[g.Host])
			e.prev[g.Host] = b
			e.rates[g.Host] = e.rates[g.Host]*0.6 + r*0.4
			total += r
		}
		e.history = append(e.history, total)
		if len(e.history) > 300 {
			e.history = e.history[len(e.history)-300:]
		}
		e.mu.Unlock()
	}
}

// memorySaver saves node memory every minute and, while the hosts file is taken
// over, makes sure the boot cleanup task still exists: if it has vanished (for any
// reason), a power cut would leave the launcher pointed at a dead address.
func (e *Engine) memorySaver(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.saveMemory()
			if e.opt.TakeOver && ctx.Err() == nil && !guard.BootTaskInstalled() {
				if err := guard.InstallBootTask(); err != nil {
					e.warn("开机清理任务丢失，重新登记失败 / boot cleanup task missing, re-registering failed: %v", err)
				} else {
					e.warn("开机清理任务丢失，已重新登记 / boot cleanup task was missing and has been re-registered")
				}
			}
		}
	}
}

func (e *Engine) saveMemory() {
	for _, g := range e.opt.Games {
		e.mem.Update(g.Host, e.table.Snapshot(g.Host))
	}
	if err := e.mem.Save(); err != nil {
		e.warn("could not save node memory: %v", err)
	}
}

// Status returns the current state for display.
func (e *Engine) Status() Status {
	var snap map[string]forwarder.Stats
	if e.fwd != nil {
		snap = e.fwd.Snapshot()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	st := Status{Phase: e.phase, Launcher: e.launcherState, Started: e.started, History: append([]float64(nil), e.history...)}
	for _, g := range e.opt.Games {
		s := snap[g.Host]
		nodes := e.table.Snapshot(g.Host)
		if len(nodes) > 5 {
			nodes = nodes[:5]
		}
		hs := HostStatus{Host: g.Host, Biz: g.Biz, Mode: e.table.Mode(g.Host), Rate: e.rates[g.Host],
			Bytes: s.Bytes, Active: s.Active, Switches: s.Rotated + s.Retried, Baseline: e.baseline[g.Host], BaseState: e.baseState[g.Host], Nodes: nodes}
		st.Rate += hs.Rate
		st.Bytes += hs.Bytes
		st.Hosts = append(st.Hosts, hs)
	}
	return st
}
