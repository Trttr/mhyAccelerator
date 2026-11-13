// Package forwarder accepts the launcher's TLS connections on a loopback address,
// reads only the plaintext SNI, and relays the encrypted bytes unchanged to the
// CDN node chosen by the node table. It never terminates or inspects TLS.
package forwarder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"mhyacc/internal/events"
	"mhyacc/internal/nodetable"
)

// Stats are cumulative counters for one host.
type Stats struct {
	Active     int
	Total      int64
	Bytes      int64 // CDN -> launcher
	Retried    int64 // connections moved to another node before any data flowed
	Rotated    int64 // slow connections closed so the launcher reconnects elsewhere
	Refused    int64
	DialFailed int64
}

// Forwarder relays connections for the hosts that Allowed accepts.
type Forwarder struct {
	Table   *nodetable.Table
	Allowed func(host string) bool
	Emit    events.Sink

	// Slow-connection rotation. A connection is rotated when, after MinAge, its
	// throughput is below SlowRatio of the median for its host and a node at least
	// twice as fast as its own is available.
	MinAge    time.Duration
	SlowRatio float64

	// RecvBuf, if > 0, sets the socket receive buffer (bytes) of upstream connections.
	// Experimental: a larger buffer can help a single fast long-distance connection.
	RecvBuf int

	// paused is set while the launcher has paused its download (known from its log):
	// silence is then expected and must not be blamed on nodes.
	paused atomic.Bool

	// OnSuspect is called (in its own goroutine) when a node has been set aside, so
	// the engine can re-test it. May be nil.
	// repeat: the node was cleared by a re-test recently and misbehaved again.
	OnSuspect     func(host, ip string, h2, repeat bool)
	hold          map[string]time.Time // no switching until then, per host (guarded by mu)
	lastSuspect   map[string]time.Time // guarded by mu
	lastSuspectIP map[string]string    // guarded by mu

	nextID       atomic.Uint64
	lastSolo     map[string]time.Time // last decay rotation per host (monitor goroutine only)
	lastCollapse map[string]time.Time // last stall/collapse rotation per host (monitor goroutine only)
	mu           sync.Mutex
	sessions     map[uint64]*session
	stats        map[string]*Stats
}

type session struct {
	host     string
	node     *nodetable.Node
	client   net.Conn
	upstream net.Conn
	start    time.Time
	down     atomic.Int64 // CDN -> launcher
	up       atomic.Int64 // launcher -> CDN

	// owned by the monitor goroutine
	lastUp      int64
	lastData    time.Time // last second with real downstream traffic
	upSinceData int64     // launcher bytes sent since downstream traffic stopped
	last        int64
	rate        float64
	peak        float64
	busy        int
	slowSince   time.Time
	lowSince    time.Time // first second under 1 MB/s after having been fast
	markedAt    time.Time
	closing     bool
}

func New(table *nodetable.Table, allowed func(string) bool, emit events.Sink) *Forwarder {
	return &Forwarder{
		Table: table, Allowed: allowed, Emit: emit,
		MinAge: 10 * time.Second, SlowRatio: 0.3,
		sessions: map[uint64]*session{}, stats: map[string]*Stats{},
		lastSolo: map[string]time.Time{}, lastCollapse: map[string]time.Time{},
		hold: map[string]time.Time{}, lastSuspect: map[string]time.Time{}, lastSuspectIP: map[string]string{},
	}
}

// Serve accepts connections on ln until ctx is cancelled.
func (f *Forwarder) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go f.monitor(ctx)
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				f.closeAll()
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go f.handle(c)
	}
}

func (f *Forwarder) stat(host string) *Stats {
	s := f.stats[host]
	if s == nil {
		s = &Stats{}
		f.stats[host] = s
	}
	return s
}

func (f *Forwarder) handle(client net.Conn) {
	// A bug in one connection must not take down the process (and with it the cleanup).
	defer func() {
		if r := recover(); r != nil {
			f.warn(fmt.Sprintf("internal error (ignored): %v", r))
		}
	}()
	defer client.Close()
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	hello, host, err := readClientHello(client)
	client.SetReadDeadline(time.Time{})
	if err != nil || !f.Allowed(host) {
		f.mu.Lock()
		f.stat("(refused)").Refused++
		f.mu.Unlock()
		if err == nil {
			f.emit(events.Event{Kind: events.HostRefused, Text: host})
		}
		return
	}

	upstream, node, first, err := f.connect(host, hello)
	if err != nil {
		f.warn(fmt.Sprintf("%s: every node tried failed: %v", host, err))
		return
	}
	defer f.Table.Release(node)
	defer upstream.Close()

	s := &session{host: host, node: node, client: client, upstream: upstream, start: time.Now(), lastData: time.Now()}
	id := f.nextID.Add(1)
	f.mu.Lock()
	f.sessions[id] = s
	st := f.stat(host)
	st.Active++
	st.Total++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.sessions, id)
		st.Active--
		st.Bytes += s.down.Load()
		f.mu.Unlock()
	}()

	if _, err := client.Write(first); err != nil {
		return
	}
	s.down.Add(int64(len(first)))

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(&countingWriter{w: upstream, n: &s.up}, client)
		if tc, ok := upstream.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(&countingWriter{w: client, n: &s.down}, upstream)
		if tc, ok := client.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

// connect dials nodes until one answers the ClientHello. Because the launcher sends
// nothing more until the server replies, a node that fails before its first byte
// can be replaced transparently by replaying the same ClientHello elsewhere.
func (f *Forwarder) connect(host string, hello []byte) (net.Conn, *nodetable.Node, []byte, error) {
	tried := map[string]bool{}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		node := f.Table.Pick(host, tried)
		if node == nil {
			break
		}
		tried[node.IP] = true
		conn, first, err := dialAndHello(node.IP, hello, f.RecvBuf)
		if err == nil {
			if attempt > 0 {
				f.mu.Lock()
				f.stat(host).Retried++
				f.mu.Unlock()
			}
			return conn, node, first, nil
		}
		lastErr = err
		f.Table.Fail(node)
		f.Table.Release(node)
		f.emit(events.Event{Kind: events.NodeSwitched, Host: host, IP: node.IP, H2: node.H2, Reason: events.ReasonDial})
		f.mu.Lock()
		f.stat(host).DialFailed++
		f.mu.Unlock()
	}
	if lastErr == nil {
		lastErr = errors.New("no node available")
	}
	return nil, nil, nil, lastErr
}

func dialAndHello(ip string, hello []byte, recvBuf int) (net.Conn, []byte, error) {
	d := net.Dialer{Timeout: 4 * time.Second}
	if recvBuf > 0 {
		// Must be set before connecting so the TCP window scale is negotiated for it.
		d.Control = func(_, _ string, c syscall.RawConn) error {
			var serr error
			c.Control(func(fd uintptr) {
				serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, recvBuf)
			})
			return serr
		}
	}
	conn, err := d.Dial("tcp", net.JoinHostPort(ip, "443"))
	if err != nil {
		return nil, nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
	}
	conn.SetDeadline(time.Now().Add(6 * time.Second))
	if _, err := conn.Write(hello); err != nil {
		conn.Close()
		return nil, nil, err
	}
	buf := make([]byte, 32<<10)
	n, err := conn.Read(buf)
	if n == 0 {
		conn.Close()
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, nil, err
	}
	conn.SetDeadline(time.Time{})
	return conn, buf[:n], nil
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// monitor samples every connection once per second, feeds the node table, and
// rotates connections that are stuck on a slow node.
func (f *Forwarder) monitor(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			f.warn(fmt.Sprintf("monitor error, restarted: %v", r))
			if ctx.Err() == nil {
				go f.monitor(ctx)
			}
		}
	}()
	const busyBytes = 64 << 10 // a second with less traffic than this is treated as idle
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		f.mu.Lock()
		list := make([]*session, 0, len(f.sessions))
		for _, s := range f.sessions {
			list = append(list, s)
		}
		f.mu.Unlock()

		byHost := map[string][]*session{}
		now := time.Now()
		for _, s := range list {
			cur := s.down.Load()
			delta := cur - s.last
			s.last = cur
			idle := delta < 1024
			upCur := s.up.Load()
			upDelta := upCur - s.lastUp
			s.lastUp = upCur
			if !idle {
				s.lastData = now
				s.upSinceData = 0
			} else {
				s.upSinceData += upDelta
			}
			// While the launcher is paused, silence is expected: restart every timer and
			// judge nothing, so no node is blamed and nothing is rotated.
			if f.paused.Load() {
				s.lowSince, s.slowSince, s.lastData = time.Time{}, time.Time{}, now
				continue
			}
			// Collapse: a connection that was fast now crawls under 1 MB/s.
			if s.peak >= 5e6 && delta < 1e6 {
				if s.lowSince.IsZero() {
					s.lowSince = now
				}
			} else if delta >= 1e6 {
				s.lowSince = time.Time{}
			}
			// On an h2 node the launcher has exactly one connection, so a stalled (no data
			// for 10 s) or collapsed (under 1 MB/s for 10 s) connection stalls the whole
			// download. Close it so the launcher reconnects, and rest the node and its /24.
			// Not applied to HTTP/1.1 connections, which sit idle between requests normally.
			// If the launcher itself sent something while data had stopped (a user pause
			// cancels its requests), the node is not blamed.
			// Only a connection that has really been downloading (it has a peak, i.e. at
			// least five busy seconds) can stall. One that fetched a little and then went
			// quiet is the launcher pausing between steps, not a stuck node.
			stalled := s.peak > 0 && cur >= busyBytes && now.Sub(s.lastData) >= 10*time.Second
			collapsed := !s.lowSince.IsZero() && now.Sub(s.lowSince) >= 10*time.Second
			act := !s.closing && s.node.H2 && (stalled || collapsed) && now.Sub(f.lastCollapse[s.host]) >= 15*time.Second
			held := act && f.holding(s.host, now)
			// While switching is on hold, a slow connection is left alone, but a dead one
			// (no data at all) is still reconnected, without blaming anyone: nothing is
			// in flight on it, so reconnecting costs nothing.
			if act && (stalled || !held) {
				s.closing = true
				f.lastCollapse[s.host] = now
				if !held && s.upSinceData < 200 {
					reason, rate := events.ReasonCollapse, float64(delta)
					if stalled {
						reason, rate = events.ReasonStall, 0
					}
					f.suspect(s, reason, rate)
				}
				s.client.Close()
				s.upstream.Close()
				f.mu.Lock()
				f.stat(s.host).Rotated++
				f.mu.Unlock()
				continue
			}
			if delta >= busyBytes {
				r := float64(delta)
				if s.busy == 0 {
					s.rate = r
				} else {
					s.rate = s.rate*0.7 + r*0.3
				}
				s.busy++
				f.Table.Observe(s.node, r)
				if s.busy >= 5 && s.rate > s.peak {
					s.peak = s.rate
				}
				// Track how long a connection has been running far below its own peak.
				// Some CDN nodes give each connection a fast start and then throttle it.
				if s.peak > 0 && s.rate < s.peak*0.4 {
					if s.slowSince.IsZero() {
						s.slowSince = now
					}
				} else {
					s.slowSince = time.Time{}
				}
			}
			if !s.markedAt.IsZero() && !s.closing && (idle || now.Sub(s.markedAt) > 8*time.Second) {
				// Close between responses when possible, so no partial chunk is lost.
				s.closing = true
				s.client.Close()
				s.upstream.Close()
				f.mu.Lock()
				f.stat(s.host).Rotated++
				f.mu.Unlock()
				continue
			}
			byHost[s.host] = append(byHost[s.host], s)
		}

		for host, ss := range byHost {
			// Decay rotation, for the single HTTP/2 connection only (HTTP/1.1 connections
			// are compared with each other below): below 40% of its own peak for 20 s.
			// A peak can be a start-up burst, and 40% of a burst may still be a fine
			// speed, so switch only if another node is known to do clearly better than
			// what this one delivers now. At most once per 2 minutes per host, since
			// in-flight requests are lost.
			if now.Sub(f.lastSolo[host]) > 2*time.Minute && !f.holding(host, now) {
				for _, s := range ss {
					if !s.node.H2 || f.Table.BestOther(host, s.node) < 1.5*s.rate {
						continue
					}
					if s.markedAt.IsZero() && !s.slowSince.IsZero() && now.Sub(s.slowSince) > 20*time.Second {
						s.markedAt = now
						f.lastSolo[host] = now
						f.suspect(s, events.ReasonDecay, s.rate) // set aside so the reconnect goes elsewhere
						break
					}
				}
			}
			var rates []float64
			for _, s := range ss {
				if s.busy >= 3 {
					rates = append(rates, s.rate)
				}
			}
			if len(rates) < 3 {
				continue
			}
			sort.Float64s(rates)
			median := rates[len(rates)/2]
			best := f.Table.Best(host)
			marked := 0
			for _, s := range ss {
				if marked >= 2 {
					break // rotate gradually so the launcher is never starved of connections
				}
				if !s.markedAt.IsZero() || s.busy < 3 || now.Sub(s.start) < f.MinAge {
					continue
				}
				if s.rate < median*f.SlowRatio && best > 2*f.Table.Score(s.node) {
					s.markedAt = now
					marked++
					f.switched(s, events.ReasonSlowPeer, s.rate)
				}
			}
		}
	}
}

// suspect handles a misbehaving connection: switch first, judge later. The node is
// set aside (no new connections, no blame) and OnSuspect lets the engine re-test
// it in the background; only a failed re-test turns it into a faulty node.
//
// Two suspicions on one game within a minute are read by where they happened:
// same /24 means that site is misbehaving, so its other nodes are rested too
// (otherwise the launcher is walked through them one by one); different sites
// suggest the cause is local (busy line, launcher writing files), so switching
// pauses for a minute.
func (f *Forwarder) suspect(s *session, reason string, rate float64) {
	f.switched(s, reason, rate)
	// Long enough to cover queued re-tests; the engine always ends it with a verdict.
	repeat := f.Table.SetAside(s.node, 3*time.Minute)
	now := time.Now()
	f.mu.Lock()
	prev, prevIP := f.lastSuspect[s.host], f.lastSuspectIP[s.host]
	f.lastSuspect[s.host], f.lastSuspectIP[s.host] = now, s.node.IP
	f.mu.Unlock()
	if !prev.IsZero() && now.Sub(prev) < time.Minute && prevIP != s.node.IP {
		if nodetable.Subnet(prevIP) == nodetable.Subnet(s.node.IP) {
			f.Table.RestSubnet(s.node, 3*time.Minute)
		} else {
			f.HoldSwitching(s.host, time.Minute, events.HoldFrequent)
		}
	}
	if f.OnSuspect != nil {
		go f.OnSuspect(s.host, s.node.IP, s.node.H2, repeat)
	}
}

// HoldSwitching stops stall/collapse/decay switching for host for d.
func (f *Forwarder) HoldSwitching(host string, d time.Duration, why string) {
	f.mu.Lock()
	f.hold[host] = time.Now().Add(d)
	f.mu.Unlock()
	f.emit(events.Event{Kind: events.Hold, Host: host, Reason: why})
}

func (f *Forwarder) holding(host string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return now.Before(f.hold[host])
}

// SetPaused tells the forwarder whether the launcher has paused its download.
func (f *Forwarder) SetPaused(p bool) { f.paused.Store(p) }

func (f *Forwarder) emit(e events.Event) {
	if f.Emit != nil {
		e.Time = time.Now()
		f.Emit(e)
	}
}

func (f *Forwarder) warn(text string) { f.emit(events.Event{Kind: events.Warning, Text: text}) }

func (f *Forwarder) switched(s *session, reason string, rate float64) {
	f.emit(events.Event{Kind: events.NodeSwitched, Host: s.host, IP: s.node.IP, H2: s.node.H2,
		Reason: reason, Peak: s.peak, Rate: rate})
}

func (f *Forwarder) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		s.client.Close()
		s.upstream.Close()
	}
}

// Snapshot returns a copy of the per-host counters.
func (f *Forwarder) Snapshot() map[string]Stats {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]Stats, len(f.stats))
	for h, s := range f.stats {
		cp := *s
		// include bytes of connections still open
		for _, sess := range f.sessions {
			if sess.host == h {
				cp.Bytes += sess.down.Load()
			}
		}
		out[h] = cp
	}
	return out
}
