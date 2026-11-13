// Package memory remembers how CDN nodes performed in earlier runs, so a new run
// starts from nodes known to be good and avoids ones (and their /24) that recently
// stalled. DNS answers rotate, so without this a good node may simply not show up.
package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"mhyacc/internal/nodetable"
)

// Entry is what we know about one node.
type Entry struct {
	Score    float64   `json:"score"`     // bytes per second observed on real downloads, smoothed
	H2       bool      `json:"h2"`        // negotiated HTTP/2 when last seen
	LastGood time.Time `json:"last_good"` // last run in which it carried traffic well
	LastBad  time.Time `json:"last_bad"`  // last stall, collapse or refused connection
	Bad      int       `json:"bad"`       // failures counted over all runs
}

const (
	goodFor = 7 * 24 * time.Hour // remembered good nodes are retried for a week
	badFor  = 12 * time.Hour     // a recent failure pushes the node (and its /24) to the back
)

// Store is the on-disk node memory.
type Store struct {
	path  string
	mu    sync.Mutex
	Hosts map[string]map[string]*Entry `json:"hosts"`
}

// DefaultPath is %LOCALAPPDATA%\mhyAccelerator\nodes.json.
func DefaultPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "mhyAccelerator", "nodes.json")
}

// Load reads the store; a missing or corrupt file yields an empty store.
func Load(path string) *Store {
	s := &Store{path: path, Hosts: map[string]map[string]*Entry{}}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, s)
		if s.Hosts == nil {
			s.Hosts = map[string]map[string]*Entry{}
		}
	}
	return s
}

// Save writes the store atomically.
func (s *Store) Save() error {
	s.mu.Lock()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Good returns remembered nodes of host that performed well within the last week
// and have not failed since, best first.
func (s *Store) Good(host string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var ips []string
	for ip, e := range s.Hosts[host] {
		if e.Score > 0 && now.Sub(e.LastGood) < goodFor && !e.LastBad.After(e.LastGood) {
			ips = append(ips, ip)
		}
	}
	sort.Slice(ips, func(i, j int) bool { return s.Hosts[host][ips[i]].Score > s.Hosts[host][ips[j]].Score })
	return ips
}

// Score returns the remembered score of a node, or 0.
func (s *Store) Score(host, ip string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.Hosts[host][ip]; e != nil {
		return e.Score
	}
	return 0
}

// Suspect reports whether ip, or another node in its /24, failed within the last 12 hours.
func (s *Store) Suspect(host, ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	sub := nodetable.Subnet(ip)
	for other, e := range s.Hosts[host] {
		if nodetable.Subnet(other) == sub && now.Sub(e.LastBad) < badFor && e.LastBad.After(e.LastGood) {
			return true
		}
	}
	return false
}

// Update merges what the node table learned during this run.
func (s *Store) Update(host string, views []nodetable.View) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Hosts[host] == nil {
		s.Hosts[host] = map[string]*Entry{}
	}
	now := time.Now()
	for _, v := range views {
		measured := v.Samples >= 5 && v.Score > 0 // enough live traffic to mean something
		if !measured && v.TotalFails == 0 {
			continue
		}
		e := s.Hosts[host][v.IP]
		if e == nil {
			e = &Entry{}
			s.Hosts[host][v.IP] = e
		}
		e.H2 = v.H2
		if v.TotalFails > 0 {
			e.Bad += v.TotalFails
			if v.LastFail.After(e.LastBad) {
				e.LastBad = v.LastFail
			}
		}
		// Only call it good if it was not failing at the end of the run.
		if measured && (v.TotalFails == 0 || now.Sub(v.LastFail) > 5*time.Minute) {
			if e.Score == 0 {
				e.Score = v.Score
			} else {
				e.Score = e.Score*0.5 + v.Score*0.5
			}
			e.LastGood = now
		}
	}
	// Forget nodes not seen for a month.
	for ip, e := range s.Hosts[host] {
		if now.Sub(e.LastGood) > 30*24*time.Hour && now.Sub(e.LastBad) > 30*24*time.Hour {
			delete(s.Hosts[host], ip)
		}
	}
}
