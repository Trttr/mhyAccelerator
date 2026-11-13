// Package rules holds the static knowledge about miHoYo's CN launcher and CDN:
// which games exist, which download hosts they use, and where to find test files.
package rules

import "strings"

// Game is one CN-server title and the CDN host its launcher downloads from.
type Game struct {
	Biz    string // launcher id, e.g. "hkrpg_cn"
	Name   string
	NameEn string
	Host   string // CDN host that serves chunks and manifests
}

var Games = []Game{
	{Biz: "hk4e_cn", Name: "原神", NameEn: "Genshin Impact", Host: "autopatchcn.yuanshen.com"},
	{Biz: "hkrpg_cn", Name: "崩坏：星穹铁道", NameEn: "Honkai: Star Rail", Host: "autopatchcn.bhsr.com"},
	{Biz: "nap_cn", Name: "绝区零", NameEn: "Zenless Zone Zero", Host: "autopatchcn.juequling.com"},
	{Biz: "bh3_cn", Name: "崩坏3", NameEn: "Honkai Impact 3rd", Host: "autopatchcn.bh3.com"},
}

const LauncherID = "jGHBHlcOq1"

// PackagesAPI lists full-package URLs. They are often outdated, but the files stay
// downloadable, which makes them good throughput test targets on the same CDN.
const PackagesAPI = "https://hyp-api.mihoyo.com/hyp/hyp-connect/api/getGamePackages?launcher_id=" + LauncherID

// BranchesAPI and BuildAPI describe the current Sophon (chunked) build of each game.
// Its manifest files live on the same CDN host and are always current.
const BranchesAPI = "https://hyp-api.mihoyo.com/hyp/hyp-connect/api/getGameBranches?launcher_id=" + LauncherID
const BuildAPI = "https://api-takumi.mihoyo.com/downloader/sophon_chunk/api/getBuild"

// View is a client subnet sent to DoH resolvers (EDNS Client Subnet) so that the CDN's
// scheduler answers as if we were located there. This is how we discover many nodes.
type View struct {
	Name   string
	Subnet string
}

var Views = []View{
	{"cn-telecom", "202.96.209.0/24"},
	{"cn-unicom", "202.106.0.0/24"},
	{"cn-mobile", "211.136.112.0/24"},
	{"hk", "203.80.96.0/24"},
	{"jp", "210.130.0.0/24"},
	{"sg", "165.21.83.0/24"},
	{"us", "129.250.35.0/24"},
	{"de", "194.25.0.0/24"},
}

// DoHServers must support the JSON API and the edns_client_subnet parameter.
var DoHServers = []string{
	"https://dns.alidns.com/resolve",
	"https://dns.google/resolve",
}

// Allowed reports whether host is a download host we are willing to forward.
// Everything else is refused, so the forwarder can never act as an open proxy.
func Allowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, g := range Games {
		if host == g.Host {
			return true
		}
	}
	return false
}

// Select returns the games matching a comma-separated list of biz ids, or all for "all" / "".
func Select(list string) ([]Game, bool) {
	if list == "" || list == "all" {
		return Games, true
	}
	var out []Game
	for _, want := range strings.Split(list, ",") {
		found := false
		for _, g := range Games {
			if g.Biz == strings.TrimSpace(want) {
				out = append(out, g)
				found = true
			}
		}
		if !found {
			return nil, false
		}
	}
	return out, true
}

// Hosts returns the CDN hosts of the given games.
func Hosts(games []Game) []string {
	out := make([]string, 0, len(games))
	for _, g := range games {
		out = append(out, g.Host)
	}
	return out
}
