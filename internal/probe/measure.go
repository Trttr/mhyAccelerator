package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"mhyacc/internal/sophon"
)

// Measurement is the result of downloading real chunks from one node the way the
// launcher would.
type Measurement struct {
	IP        string
	H2        bool
	Rate      float64   // bytes/s after warm-up; equals Tail when the node collapsed
	Peak      float64   // best 1-second window after warm-up
	Tail      float64   // average over the last third of the test
	Collapsed bool      // fast at first, then fell below 40% of its peak
	Series    []float64 // bytes/s per second after warm-up, for display
	Err       error
}

// MeasureOpts controls one measurement.
type MeasureOpts struct {
	Parallel int           // h2: concurrent streams on one connection; HTTP/1.1: connections
	Warmup   time.Duration // discarded: TCP slow start would make fast nodes look slow
	Duration time.Duration // measured part
}

// Measure downloads chunks from ip, presenting host as SNI and Host and verifying
// the certificate. On an h2 node it uses a single connection carrying several
// streams, as the launcher does; on an HTTP/1.1 node, several connections.
func Measure(ctx context.Context, host, ip string, h2 bool, chunks []sophon.Chunk, o MeasureOpts) Measurement {
	m := Measurement{IP: ip, H2: h2}
	if len(chunks) == 0 {
		m.Err = errors.New("no chunks to test with")
		return m
	}
	if o.Parallel <= 0 {
		o.Parallel = 4
	}
	dialer := &net.Dialer{Timeout: 4 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip, "443"))
		},
		TLSClientConfig:     &tls.Config{ServerName: host},
		TLSHandshakeTimeout: 6 * time.Second,
		DisableCompression:  true,
		MaxIdleConnsPerHost: o.Parallel,
	}
	if h2 {
		tr.ForceAttemptHTTP2 = true
		tr.MaxConnsPerHost = 1
	} else {
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		tr.MaxConnsPerHost = o.Parallel
	}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr}

	total := o.Warmup + o.Duration
	ctx, cancel := context.WithTimeout(ctx, total+2*time.Second)
	defer cancel()

	var (
		bytes   atomic.Int64
		next    atomic.Int64
		errs    atomic.Int64
		lastErr atomic.Value
		wg      sync.WaitGroup
	)
	start := time.Now()
	deadline := start.Add(total)
	for w := 0; w < o.Parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 64<<10)
			for time.Now().Before(deadline) && ctx.Err() == nil {
				c := chunks[int(next.Add(1)-1)%len(chunks)]
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
				if err != nil {
					return
				}
				resp, err := hc.Do(req)
				if err != nil {
					if ctx.Err() == nil {
						errs.Add(1)
						lastErr.Store(err)
						time.Sleep(200 * time.Millisecond)
					}
					continue
				}
				if resp.StatusCode != http.StatusOK {
					resp.Body.Close()
					errs.Add(1)
					lastErr.Store(fmt.Errorf("HTTP %d", resp.StatusCode))
					time.Sleep(200 * time.Millisecond)
					continue
				}
				for {
					n, err := resp.Body.Read(buf)
					bytes.Add(int64(n))
					if err != nil || !time.Now().Before(deadline) {
						break
					}
				}
				resp.Body.Close()
			}
		}()
	}

	// Sample the byte counter every 250 ms.
	const step = 250 * time.Millisecond
	var samples []int64
	tick := time.NewTicker(step)
	for now := range tick.C {
		samples = append(samples, bytes.Load())
		if !now.Before(deadline) {
			break
		}
	}
	tick.Stop()
	cancel()
	wg.Wait()

	warm := int(o.Warmup / step)
	if warm >= len(samples) {
		warm = 0
	}
	base := int64(0)
	if warm > 0 {
		base = samples[warm-1]
	}
	measured := samples[len(samples)-1] - base
	secs := float64(len(samples)-warm) * step.Seconds()
	if measured <= 0 || secs <= 0 {
		if e, ok := lastErr.Load().(error); ok {
			m.Err = e
		} else {
			m.Err = errors.New("no data received")
		}
		return m
	}
	m.Rate = float64(measured) / secs

	// 1-second windows after warm-up.
	per := int(time.Second / step)
	for i := warm + per; i <= len(samples); i += per {
		prev := base
		if i-per > 0 {
			prev = samples[i-per-1]
		}
		m.Series = append(m.Series, float64(samples[i-1]-prev))
	}
	if len(m.Series) > 0 {
		for _, v := range m.Series {
			m.Peak = max(m.Peak, v)
		}
		third := max(1, len(m.Series)/3)
		var sum float64
		for _, v := range m.Series[len(m.Series)-third:] {
			sum += v
		}
		m.Tail = sum / float64(third)
		if m.Peak >= 5e6 && m.Tail < 0.4*m.Peak {
			m.Collapsed = true
			m.Rate = m.Tail // judge it by where it ended up, not where it started
		}
	}
	return m
}
