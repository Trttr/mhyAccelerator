// Package resolver discovers CDN node IPs for a host by asking DoH resolvers
// from several regional viewpoints. It never uses the system resolver, because
// while acceleration is on the hosts file points the download hosts at ourselves.
package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"mhyacc/internal/rules"
)

// Candidate is one discovered node IP and the viewpoints that returned it.
type Candidate struct {
	IP    string
	Views []string
}

type dohAnswer struct {
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

var client = &http.Client{Timeout: 8 * time.Second}

// Resolve returns the IPv4 candidates for host, most widely seen first.
func Resolve(ctx context.Context, host string) ([]Candidate, error) {
	var (
		mu    sync.Mutex
		seen  = map[string]map[string]bool{}
		wg    sync.WaitGroup
		fails int
	)
	for _, v := range rules.Views {
		for _, server := range rules.DoHServers {
			wg.Add(1)
			go func(v rules.View, server string) {
				defer wg.Done()
				ips, err := query(ctx, server, host, v.Subnet)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					fails++
					return
				}
				for _, ip := range ips {
					if seen[ip] == nil {
						seen[ip] = map[string]bool{}
					}
					seen[ip][v.Name] = true
				}
			}(v, server)
		}
	}
	wg.Wait()

	if len(seen) == 0 {
		return nil, fmt.Errorf("no node found for %s (%d of %d DoH queries failed)",
			host, fails, len(rules.Views)*len(rules.DoHServers))
	}
	out := make([]Candidate, 0, len(seen))
	for ip, views := range seen {
		c := Candidate{IP: ip}
		for v := range views {
			c.Views = append(c.Views, v)
		}
		sort.Strings(c.Views)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Views) != len(out[j].Views) {
			return len(out[i].Views) > len(out[j].Views)
		}
		return out[i].IP < out[j].IP
	})
	return out, nil
}

// Plain returns up to n IPv4 nodes a resolver hands this user without any hint:
// first AliDNS (the launcher's built-in HTTPDNS is Alibaba's, and both answer by
// the asker's own address, so this is the closest stand-in for what the launcher
// gets), then the system resolver. Loopback answers (our own hosts entries) are
// dropped.
func Plain(ctx context.Context, host string, n int) []string {
	var out []string
	add := func(ip string) {
		p := net.ParseIP(ip).To4()
		if p == nil || p.IsLoopback() || p.IsPrivate() || len(out) >= n {
			return
		}
		for _, x := range out {
			if x == p.String() {
				return
			}
		}
		out = append(out, p.String())
	}
	if ips, err := query(ctx, rules.DoHServers[0], host, ""); err == nil {
		for _, ip := range ips {
			add(ip)
		}
	}
	if len(out) < n {
		if addrs, err := net.DefaultResolver.LookupHost(ctx, host); err == nil {
			for _, a := range addrs {
				add(a)
			}
		}
	}
	return out
}

func query(ctx context.Context, server, host, subnet string) ([]string, error) {
	q := url.Values{"name": {host}, "type": {"A"}}
	if subnet != "" {
		q.Set("edns_client_subnet", subnet)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", server, resp.StatusCode)
	}
	var a dohAnswer
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return nil, err
	}
	var ips []string
	for _, ans := range a.Answer {
		if ans.Type != 1 {
			continue
		}
		ip := net.ParseIP(ans.Data).To4()
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
			continue
		}
		ips = append(ips, ip.String())
	}
	return ips, nil
}
