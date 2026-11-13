// Package probe measures how fast a CDN node serves a real file, talking to it
// by IP while presenting the real host name (SNI and Host header). Certificates
// are always verified, so a probe can only succeed against a genuine node.
package probe

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mhyacc/internal/rules"
)

// Target is a file on the CDN used for throughput tests.
type Target struct {
	URL  string
	Size int64
}

// Result of probing one node.
type Result struct {
	IP        string
	Handshake time.Duration // TCP + TLS
	TTFB      time.Duration // request sent to first body byte
	Rate      float64       // bytes per second after the first byte; 0 if not measured
	Err       error
}

var apiClient = &http.Client{Timeout: 15 * time.Second}

func getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// Targets returns test files per CDN host, best candidates first: the largest
// manifest of the current Sophon build, then the largest legacy full package.
// Either may be missing or refused, so callers should try them in order.
func Targets(ctx context.Context) (map[string][]Target, error) {
	out := map[string][]Target{}
	sophon, errS := sophonTargets(ctx)
	for h, t := range sophon {
		out[h] = append(out[h], t)
	}
	legacy, errL := packageTargets(ctx)
	for h, t := range legacy {
		out[h] = append(out[h], t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sophon: %v; packages: %v", errS, errL)
	}
	return out, nil
}

func sophonTargets(ctx context.Context) (map[string]Target, error) {
	var branches struct {
		Retcode int    `json:"retcode"`
		Message string `json:"message"`
		Data    struct {
			GameBranches []struct {
				Main struct {
					PackageID string `json:"package_id"`
					Branch    string `json:"branch"`
					Password  string `json:"password"`
				} `json:"main"`
			} `json:"game_branches"`
		} `json:"data"`
	}
	if err := getJSON(ctx, rules.BranchesAPI, &branches); err != nil {
		return nil, err
	}
	if branches.Retcode != 0 {
		return nil, fmt.Errorf("getGameBranches: %s", branches.Message)
	}
	out := map[string]Target{}
	for _, b := range branches.Data.GameBranches {
		q := url.Values{"branch": {b.Main.Branch}, "package_id": {b.Main.PackageID}, "password": {b.Main.Password}}
		var build struct {
			Retcode int `json:"retcode"`
			Data    struct {
				Manifests []struct {
					Manifest struct {
						ID             string `json:"id"`
						CompressedSize string `json:"compressed_size"`
					} `json:"manifest"`
					ManifestDownload struct {
						URLPrefix string `json:"url_prefix"`
						URLSuffix string `json:"url_suffix"`
					} `json:"manifest_download"`
				} `json:"manifests"`
			} `json:"data"`
		}
		if err := getJSON(ctx, rules.BuildAPI+"?"+q.Encode(), &build); err != nil || build.Retcode != 0 {
			continue
		}
		for _, m := range build.Data.Manifests {
			full := m.ManifestDownload.URLPrefix + "/" + m.Manifest.ID + m.ManifestDownload.URLSuffix
			u, err := url.Parse(full)
			if err != nil {
				continue
			}
			size, _ := strconv.ParseInt(m.Manifest.CompressedSize, 10, 64)
			host := strings.ToLower(u.Host)
			if size > out[host].Size {
				out[host] = Target{URL: full, Size: size}
			}
		}
	}
	return out, nil
}

func packageTargets(ctx context.Context) (map[string]Target, error) {
	var body struct {
		Retcode int    `json:"retcode"`
		Message string `json:"message"`
		Data    struct {
			GamePackages []struct {
				Main struct {
					Major struct {
						GamePkgs []struct {
							URL  string `json:"url"`
							Size string `json:"size"`
						} `json:"game_pkgs"`
					} `json:"major"`
				} `json:"main"`
			} `json:"game_packages"`
		} `json:"data"`
	}
	if err := getJSON(ctx, rules.PackagesAPI, &body); err != nil {
		return nil, err
	}
	if body.Retcode != 0 {
		return nil, fmt.Errorf("launcher API: %s", body.Message)
	}
	out := map[string]Target{}
	for _, g := range body.Data.GamePackages {
		for _, p := range g.Main.Major.GamePkgs {
			u, err := url.Parse(p.URL)
			if err != nil {
				continue
			}
			size, _ := strconv.ParseInt(p.Size, 10, 64)
			host := strings.ToLower(u.Host)
			if size > out[host].Size {
				out[host] = Target{URL: p.URL, Size: size}
			}
		}
	}
	return out, nil
}

// ALPN reports which protocol the node picks when offered h2 and http/1.1, as the
// launcher does. This matters a lot: on an h2 node the launcher multiplexes all of
// its downloads onto a single TCP connection, which a lossy cross-border link caps
// far below what twelve parallel HTTP/1.1 connections achieve.
func ALPN(ctx context.Context, host, ip string) (string, time.Duration, error) {
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 3 * time.Second},
		Config:    &tls.Config{ServerName: host, NextProtos: []string{"h2", "http/1.1"}},
	}
	ctx, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, "443"))
	if err != nil {
		return "", 0, err
	}
	defer c.Close()
	p := c.(*tls.Conn).ConnectionState().NegotiatedProtocol
	if p == "" {
		p = "http/1.1"
	}
	return p, time.Since(start), nil
}

// Probe connects to ip as host and, if a target is given, downloads from it for
// up to dur. Without a target only the handshake is measured.
func Probe(ctx context.Context, host, ip string, target *Target, dur time.Duration) Result {
	r := Result{IP: ip}
	dialer := &net.Dialer{Timeout: 4 * time.Second}

	if target == nil {
		start := time.Now()
		conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(ip, "443"),
			&tls.Config{ServerName: host, NextProtos: []string{"http/1.1"}})
		if err != nil {
			r.Err = err
			return r
		}
		r.Handshake = time.Since(start)
		conn.Close()
		return r
	}

	var connStart, connDone time.Time
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			connStart = time.Now()
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip, "443"))
		},
		TLSClientConfig:     &tls.Config{ServerName: host},
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{}, // HTTP/1.1 only
		TLSHandshakeTimeout: 5 * time.Second,
		DisableKeepAlives:   true,
		DisableCompression:  true,
	}
	defer tr.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(ctx, dur+8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	if err != nil {
		r.Err = err
		return r
	}
	// Start mid-file on big packages; small manifests are read from the beginning.
	if target.Size > 64<<20 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", target.Size/2))
	}
	reqStart := time.Now()
	resp, err := tr.RoundTrip(req)
	if err != nil {
		r.Err = err
		return r
	}
	defer resp.Body.Close()
	connDone = time.Now()
	if !connStart.IsZero() {
		r.Handshake = connDone.Sub(connStart)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		r.Err = fmt.Errorf("HTTP %d", resp.StatusCode)
		return r
	}

	buf := make([]byte, 64<<10)
	n, err := resp.Body.Read(buf)
	if n == 0 && err != nil {
		r.Err = err
		return r
	}
	first := time.Now()
	r.TTFB = first.Sub(reqStart)
	var total int64
	deadline := first.Add(dur)
	for time.Now().Before(deadline) {
		n, err = resp.Body.Read(buf)
		total += int64(n)
		if err != nil {
			if err != io.EOF {
				r.Err = err
			}
			break
		}
	}
	if elapsed := time.Since(first).Seconds(); elapsed > 0.2 {
		r.Rate = float64(total) / elapsed
		r.Err = nil // a mid-transfer error still produced a usable measurement
	}
	return r
}
