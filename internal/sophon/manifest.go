package sophon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"mhyacc/internal/rules"
)

// Chunk is one downloadable chunk file, exactly as the launcher fetches it.
type Chunk struct {
	URL  string
	Size int64 // compressed size, i.e. bytes on the wire
}

// Manifest points to one manifest file of the current build.
type Manifest struct {
	URL         string
	ChunkPrefix string
	Size        int64 // compressed manifest size
	ChunkCount  int64
}

// Build is the current Sophon build of one game, keyed by its CDN host.
type Build struct {
	Host      string
	PackageID string // how the launcher's log refers to this game
	Manifests []Manifest
}

var client = &http.Client{Timeout: 20 * time.Second}

func getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// FetchBuilds asks the launcher API for the current build of every game.
func FetchBuilds(ctx context.Context) (map[string]*Build, error) {
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
	out := map[string]*Build{}
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
					ChunkDownload struct {
						URLPrefix string `json:"url_prefix"`
						URLSuffix string `json:"url_suffix"`
					} `json:"chunk_download"`
					ManifestDownload struct {
						URLPrefix string `json:"url_prefix"`
						URLSuffix string `json:"url_suffix"`
					} `json:"manifest_download"`
					Stats struct {
						ChunkCount string `json:"chunk_count"`
					} `json:"stats"`
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
			host := strings.ToLower(u.Host)
			if out[host] == nil {
				out[host] = &Build{Host: host, PackageID: b.Main.PackageID}
			}
			size, _ := strconv.ParseInt(m.Manifest.CompressedSize, 10, 64)
			count, _ := strconv.ParseInt(m.Stats.ChunkCount, 10, 64)
			out[host].Manifests = append(out[host].Manifests, Manifest{
				URL: full, ChunkPrefix: m.ChunkDownload.URLPrefix, Size: size, ChunkCount: count,
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no Sophon build found")
	}
	return out, nil
}

// SampleChunks downloads one manifest of moderate size and returns up to n of its
// chunks of at least 512 KB, in random order. These are real launcher downloads,
// so a speed test built on them measures what the launcher will actually get.
func (b *Build) SampleChunks(ctx context.Context, n int) ([]Chunk, error) {
	// Prefer manifests with plenty of chunks but a manifest file that is cheap to fetch.
	cands := make([]Manifest, 0, len(b.Manifests))
	for _, m := range b.Manifests {
		if m.ChunkCount >= 32 && m.Size > 0 && m.Size < 4<<20 {
			cands = append(cands, m)
		}
	}
	if len(cands) == 0 {
		cands = b.Manifests
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].ChunkCount > cands[j].ChunkCount })
	var lastErr error
	for _, m := range cands[:min(3, len(cands))] {
		chunks, err := readChunks(ctx, m)
		if err != nil {
			lastErr = err
			continue
		}
		var big []Chunk
		for _, c := range chunks {
			if c.Size >= 512<<10 {
				big = append(big, c)
			}
		}
		if len(big) < 8 {
			big = chunks
		}
		rand.Shuffle(len(big), func(i, j int) { big[i], big[j] = big[j], big[i] })
		if len(big) > n {
			big = big[:n]
		}
		if len(big) > 0 {
			return big, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s: no usable manifest", b.Host)
	}
	return nil, lastErr
}

var decoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))

func readChunks(ctx context.Context, m Manifest) ([]Chunk, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	data, err := decoder.DecodeAll(raw, nil)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return parseManifest(data, m.ChunkPrefix)
}

// parseManifest walks the manifest message:
//
//	manifest { repeated asset = 1 }
//	asset    { name = 1; repeated chunk = 2; size = 4; md5 = 5 }
//	chunk    { name = 1; md5 = 2; offset = 3; size = 4; decompressed_size = 5; ... }
//
// Field meanings were inferred from real manifests; only name and size are used.
func parseManifest(data []byte, prefix string) ([]Chunk, error) {
	assets, err := fields(data)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Chunk
	for _, a := range assets {
		if a.num != 1 || a.wire != 2 {
			continue
		}
		af, err := fields(a.bytes)
		if err != nil {
			continue
		}
		for _, cf := range af {
			if cf.num != 2 || cf.wire != 2 {
				continue
			}
			chf, err := fields(cf.bytes)
			if err != nil {
				continue
			}
			var name string
			var size int64
			for _, f := range chf {
				switch {
				case f.num == 1 && f.wire == 2:
					name = string(f.bytes)
				case f.num == 4 && f.wire == 0:
					size = int64(f.value)
				}
			}
			if name == "" || seen[name] || strings.ContainsAny(name, "/\\?#") {
				continue
			}
			seen[name] = true
			out = append(out, Chunk{URL: prefix + "/" + name, Size: size})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("manifest has no chunks")
	}
	return out, nil
}
