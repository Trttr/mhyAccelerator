// Package nodetable keeps a live score for every CDN node and decides which node
// each new connection should use.
package nodetable

import (
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"
)

// Node is one CDN edge IP for one host.
type Node struct {
	IP string
	H2 bool // node negotiates HTTP/2 with the launcher

	host       string
	score      float64 // bytes per second, smoothed
	samples    int
	active     int
	fails      int // consecutive, reset by a good measurement
	totalFails int
	lastFail   time.Time
	cooldown   time.Time

	// Observation: a node whose connection misbehaved is set aside (no new
	// connections) without being blamed, until a re-test decides.
	observing bool
	testing   bool    // a re-test is running right now
	prevScore float64 // healthy speed before it was set aside, restored if it recovers

	// typical is the node's healthy speed: it follows good samples and ignores bad
	// ones, so a collapse does not drag down the yardstick it is later judged by.
	typical     float64
	lastRecover time.Time // last time a re-test cleared it
}

// View is a read-only copy of a node's state for display and persistence.
type View struct {
	IP         string
	H2         bool
	Score      float64
	Samples    int
	Active     int
	Cooling    bool
	Observing  bool // set aside pending a re-test
	Testing    bool // being re-tested now
	TotalFails int
	LastFail   time.Time
}

// Subnet returns the /24 of an IPv4 address ("a.b.c"), or the IP itself otherwise.
// Edge nodes in one /24 are usually one site and misbehave together.
func Subnet(ip string) string {
	if i := strings.LastIndexByte(ip, '.'); i > 0 && strings.Count(ip, ".") == 3 {
		return ip[:i]
	}
	return ip
}

// Table holds the nodes of every forwarded host.
type Table struct {
	MaxPerNode int     // simultaneous connections allowed on one node
	TopK       int     // picks are spread over this many best nodes
	Explore    float64 // share of picks sent to a random other node to keep scores fresh

	mu     sync.Mutex
	byHost map[string][]*Node
	mode   map[string]int
}

func New() *Table {
	return &Table{MaxPerNode: 6, TopK: 4, Explore: 0.1, byHost: map[string][]*Node{}, mode: map[string]int{}}
}

// Seed adds a node with an initial score (from an active probe; 0 if unmeasured).
func (t *Table) Seed(host, ip string, h2 bool, rate float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, n := range t.byHost[host] {
		if n.IP == ip {
			n.H2 = h2
			if rate > 0 {
				n.score = rate
				n.samples = 1
			}
			return
		}
	}
	n := &Node{IP: ip, H2: h2, host: host, score: rate}
	if rate > 0 {
		n.samples = 1
	}
	t.byHost[host] = append(t.byHost[host], n)
}

// Pick chooses a node for a new connection to host and counts it as active.
// Nodes in exclude (already tried for this connection) are skipped.
func (t *Table) Pick(host string, exclude map[string]bool) *Node {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()

	// If the launcher lands even one connection on an h2 node, it moves every download
	// onto that single connection. So while HTTP/1.1 nodes are available, use only those.
	h1Only := t.h1Available(host, now)
	limit := t.perNodeLimit(host, h1Only, now)
	var usable []*Node
	for _, n := range t.byHost[host] {
		if h1Only && n.H2 {
			continue
		}
		if !exclude[n.IP] && now.After(n.cooldown) && n.active < limit {
			usable = append(usable, n)
		}
	}
	if len(usable) == 0 {
		// Everything is busy or resting: take the least loaded node not yet tried,
		// preferring ones that are not under observation, then the higher score.
		var best *Node
		better := func(a, b *Node) bool {
			if a.observing != b.observing {
				return !a.observing
			}
			if a.active != b.active {
				return a.active < b.active
			}
			return a.score > b.score
		}
		for _, n := range t.byHost[host] {
			if h1Only && n.H2 {
				continue
			}
			if !exclude[n.IP] && (best == nil || better(n, best)) {
				best = n
			}
		}
		if best != nil {
			best.active++
		}
		return best
	}
	sort.Slice(usable, func(i, j int) bool { return usable[i].score > usable[j].score })

	// Single-connection (HTTP/2) mode: the whole download rides on this one pick, so
	// there is no load to spread and nothing to gain from chance. Take the best node;
	// only near ties (within 10%) are broken at random.
	if !h1Only {
		ties := 1
		for ties < len(usable) && usable[ties].score >= 0.9*usable[0].score && usable[0].score > 0 {
			ties++
		}
		chosen := usable[rand.IntN(ties)]
		chosen.active++
		return chosen
	}

	// Multi-connection mode. Exploration keeps scores fresh, but only tries nodes
	// that are unmeasured or plausibly good: a node known to be slow is not worth
	// one of the launcher's twelve connections.
	var chosen *Node
	var explore []*Node
	if len(usable) > t.TopK {
		for _, n := range usable[t.TopK:] {
			if n.samples <= 1 || n.score >= 0.4*usable[0].score {
				explore = append(explore, n)
			}
		}
	}
	if len(explore) > 0 && rand.Float64() < t.Explore {
		chosen = explore[rand.IntN(len(explore))]
	} else {
		top := usable[:min(t.TopK, len(usable))]
		// Drop nodes far slower than the best so one bad node in the top K gets little traffic.
		cut := top[0].score * 0.4
		var weights []float64
		var sum float64
		for _, n := range top {
			// The score is already a per-connection speed, so a node that saturates
			// shows it by itself. Damp by load only mildly (square root): dividing by
			// the full connection count left a 16 MB/s node with 3 of 12 connections
			// while 3 MB/s nodes took the rest.
			w := n.score / math.Sqrt(float64(1+n.active))
			if n.score < cut {
				w *= 0.1
			}
			if w <= 0 {
				w = 1 // unmeasured nodes still get a small chance
			}
			weights = append(weights, w)
			sum += w
		}
		x := rand.Float64() * sum
		chosen = top[len(top)-1]
		for i, w := range weights {
			if x < w {
				chosen = top[i]
				break
			}
			x -= w
		}
	}
	chosen.active++
	return chosen
}

// launcherConns is how many connections the launcher opens on HTTP/1.1 nodes.
const launcherConns = 12

// perNodeLimit caps simultaneous connections per node. With MaxPerNode = 6 and a
// single good node, half of the launcher's twelve connections would be pushed onto
// slow nodes, so the cap grows until the good nodes alone can hold all twelve.
// "Good" means at least 40% of the best score. Callers hold t.mu.
func (t *Table) perNodeLimit(host string, h1Only bool, now time.Time) int {
	var best float64
	for _, n := range t.byHost[host] {
		if (!h1Only || !n.H2) && now.After(n.cooldown) && n.score > best {
			best = n.score
		}
	}
	good := 0
	for _, n := range t.byHost[host] {
		if (!h1Only || !n.H2) && now.After(n.cooldown) && best > 0 && n.score >= 0.4*best {
			good++
		}
	}
	if good == 0 {
		return t.MaxPerNode
	}
	return min(launcherConns, max(t.MaxPerNode, (launcherConns+good-1)/good))
}

// h1Available reports whether the launcher should be kept on HTTP/1.1 nodes.
// Before the background test has decided (SetMode), the rule is "at least two
// healthy HTTP/1.1 nodes". After it has, its choice holds as long as one healthy
// node of that kind is left. Callers hold t.mu.
func (t *Table) h1Available(host string, now time.Time) bool {
	count := 0
	for _, n := range t.byHost[host] {
		if !n.H2 && n.score > 0 && now.After(n.cooldown) {
			count++
		}
	}
	switch t.mode[host] {
	case modeH1:
		return count >= 1
	case modeH2:
		return false
	}
	return count >= 2
}

const (
	modeAuto = iota
	modeH1
	modeH2
)

// SetMode fixes which kind of node host is steered to, as decided by measuring
// both: preferH2 when one HTTP/2 connection beats the HTTP/1.1 nodes combined.
func (t *Table) SetMode(host string, preferH2 bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if preferH2 {
		t.mode[host] = modeH2
	} else {
		t.mode[host] = modeH1
	}
}

// Mode describes which kind of node host is currently steered to.
func (t *Table) Mode(host string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.h1Available(host, time.Now()) {
		return "http/1.1"
	}
	return "h2"
}

// SetProbe records a background speed test. Nodes that already carry real
// downloads keep mostly their live score; the test only nudges it.
func (t *Table) SetProbe(host, ip string, rate float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, n := range t.byHost[host] {
		if n.IP != ip {
			continue
		}
		if n.samples >= 5 {
			n.score = n.score*0.7 + rate*0.3
		} else {
			n.score = rate
			n.samples = 1
		}
		return
	}
}

// SetAside takes n out of rotation for up to hold without blaming it: no cooldown
// growth, no /24 penalty, nothing remembered. A re-test then calls Resolve. If no
// verdict comes within hold, the node simply becomes available again.
//
// It reports whether this is a repeat: the node was cleared by a re-test less than
// ten minutes ago and has misbehaved again. That is the signature of a node that
// gives every connection a fast start and then throttles it, which a short re-test
// cannot see; the caller should treat it as faulty without testing again.
func (t *Table) SetAside(n *Node, hold time.Duration) (repeat bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if !n.observing {
		// The live score has already been dragged down by the bad seconds that led
		// here; the healthy speed is the fair yardstick.
		n.prevScore = max(n.score, n.typical)
	}
	n.observing = true
	n.cooldown = now.Add(hold)
	return !n.lastRecover.IsZero() && now.Sub(n.lastRecover) < 10*time.Minute
}

// RestSubnet takes n's /24 neighbours out of rotation for d, without counting a
// failure against them. Used when two nodes of one site misbehave in a row.
func (t *Table) RestSubnet(n *Node, d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	until := time.Now().Add(d)
	sub := Subnet(n.IP)
	for _, m := range t.byHost[n.host] {
		if m != n && Subnet(m.IP) == sub && m.cooldown.Before(until) {
			m.cooldown = until
		}
	}
}

// BestOther returns the highest score among available nodes of host other than n,
// of the kind currently in use.
func (t *Table) BestOther(host string, n *Node) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	h1Only := t.h1Available(host, now)
	var best float64
	for _, m := range t.byHost[host] {
		if m == n || (h1Only && m.H2) {
			continue
		}
		if now.After(m.cooldown) && m.score > best {
			best = m.score
		}
	}
	return best
}

// SetTesting marks a node as being re-tested (for display).
func (t *Table) SetTesting(host, ip string, on bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := t.find(host, ip); n != nil {
		n.testing = on
	}
}

// PrevScore returns the score a node had before it was set aside (or its score).
func (t *Table) PrevScore(host, ip string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := t.find(host, ip); n != nil {
		if n.observing && n.prevScore > 0 {
			return n.prevScore
		}
		return n.score
	}
	return 0
}

// Resolve ends an observation. A node that recovered gets its old score back and
// returns to rotation. One that did not is failed now (cooldown, /24, memory), and
// its score drops to what the re-test measured.
func (t *Table) Resolve(host, ip string, recovered bool, rate float64) {
	t.mu.Lock()
	n := t.find(host, ip)
	if n == nil {
		t.mu.Unlock()
		return
	}
	wasObserving := n.observing
	n.observing, n.testing = false, false
	if recovered {
		if wasObserving {
			n.score = max(n.prevScore, rate)
			n.cooldown = time.Time{}
		}
		n.fails = 0
		if rate > 0 { // cleared by an actual test, not merely released
			n.lastRecover = time.Now()
		}
		t.mu.Unlock()
		return
	}
	if rate > 0 {
		n.score = min(n.score, rate)
	} else {
		n.score *= 0.3
	}
	t.mu.Unlock()
	t.Fail(n)
}

func (t *Table) find(host, ip string) *Node {
	for _, n := range t.byHost[host] {
		if n.IP == ip {
			return n
		}
	}
	return nil
}

// FailIP is Fail for a node known by address.
func (t *Table) FailIP(host, ip string) {
	t.mu.Lock()
	var target *Node
	for _, n := range t.byHost[host] {
		if n.IP == ip {
			target = n
		}
	}
	t.mu.Unlock()
	if target != nil {
		t.Fail(target)
	}
}

// Release marks a connection on n as finished.
func (t *Table) Release(n *Node) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n.active > 0 {
		n.active--
	}
}

// Observe feeds one throughput measurement (bytes per second) from a live connection.
func (t *Table) Observe(n *Node, rate float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// The score rises quickly and falls slowly. A slow second is often the launcher
	// pausing between files rather than the node, so a few of them must not sink a
	// good node; a real collapse is caught by the forwarder's own rules instead.
	alpha := 0.08
	if n.samples == 0 {
		alpha = 1
	} else if rate > n.score {
		alpha = 0.2
	}
	n.score = n.score*(1-alpha) + rate*alpha
	n.samples++
	n.fails = 0

	// Healthy speed: follow samples at or above it, drift slowly on mildly lower
	// ones, ignore anything under half of it.
	switch {
	case n.typical == 0 || rate >= n.typical:
		n.typical = n.typical*0.8 + rate*0.2
		if n.typical < n.score {
			n.typical = n.score
		}
	case rate >= 0.5*n.typical:
		n.typical = n.typical*0.95 + rate*0.05
	}
}

// Fail records a failure (refused connection, stall, collapse) and rests the node,
// longer each time. Its /24 neighbours are rested too, at least as long, because
// they tend to share the problem; otherwise the next pick lands right next door.
func (t *Table) Fail(n *Node) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	n.fails++
	n.totalFails++
	n.lastFail = now
	until := now.Add(time.Duration(min(n.fails, 6)) * 30 * time.Second)
	n.cooldown = until
	sub := Subnet(n.IP)
	for _, m := range t.byHost[n.host] {
		if m != n && Subnet(m.IP) == sub && m.cooldown.Before(until) {
			m.cooldown = until
		}
	}
}

// Best returns the highest score among nodes of host that are not cooling down.
func (t *Table) Best(host string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var best float64
	now := time.Now()
	h1Only := t.h1Available(host, now)
	for _, n := range t.byHost[host] {
		if h1Only && n.H2 {
			continue
		}
		if now.After(n.cooldown) && n.score > best {
			best = n.score
		}
	}
	return best
}

// Score returns n's current score.
func (t *Table) Score(n *Node) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return n.score
}

// Snapshot lists the nodes of host, best first.
func (t *Table) Snapshot(host string) []View {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	out := make([]View, 0, len(t.byHost[host]))
	for _, n := range t.byHost[host] {
		out = append(out, View{IP: n.IP, H2: n.H2, Score: n.score, Samples: n.samples, Active: n.active,
			Cooling: now.Before(n.cooldown) && !n.observing, Observing: n.observing, Testing: n.testing,
			TotalFails: n.totalFails, LastFail: n.lastFail})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}
