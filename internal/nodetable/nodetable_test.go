package nodetable

import "testing"

const host = "autopatchcn.bhsr.com"

func TestOneGoodNodeTakesAllConnections(t *testing.T) {
	tb := New()
	tb.Seed(host, "1.1.1.1", false, 25e6) // the only good node
	tb.Seed(host, "2.2.2.2", false, 1e6)
	tb.Seed(host, "3.3.3.3", false, 1e6)
	tb.SetMode(host, false)
	counts := map[string]int{}
	for i := 0; i < 12; i++ {
		n := tb.Pick(host, nil)
		counts[n.IP]++
	}
	if counts["1.1.1.1"] < 11 { // exploration may send the odd one elsewhere
		t.Fatalf("good node got %d of 12 connections: %v", counts["1.1.1.1"], counts)
	}
}

func TestConnectionsSpreadOverGoodNodes(t *testing.T) {
	tb := New()
	for _, ip := range []string{"1.1.1.1", "1.1.2.1", "1.1.3.1", "1.1.4.1"} {
		tb.Seed(host, ip, false, 10e6)
	}
	counts := map[string]int{}
	for i := 0; i < 12; i++ {
		counts[tb.Pick(host, nil).IP]++
	}
	for ip, c := range counts {
		if c > 6 {
			t.Fatalf("%s got %d connections; expected a spread: %v", ip, c, counts)
		}
	}
}

func TestFailRestsWholeSubnet(t *testing.T) {
	tb := New()
	tb.Seed(host, "117.187.26.224", true, 30e6)
	tb.Seed(host, "117.187.26.227", true, 29e6)
	tb.Seed(host, "111.48.108.100", true, 20e6)
	tb.SetMode(host, true)
	tb.FailIP(host, "117.187.26.224")
	for i := 0; i < 20; i++ {
		n := tb.Pick(host, nil)
		if n.IP != "111.48.108.100" {
			t.Fatalf("picked %s from a resting subnet", n.IP)
		}
		tb.Release(n)
	}
}

func TestModeOverride(t *testing.T) {
	tb := New()
	tb.Seed(host, "1.1.1.1", false, 5e6)
	tb.Seed(host, "2.2.2.2", false, 5e6)
	tb.Seed(host, "3.3.3.3", true, 40e6)
	if tb.Mode(host) != "http/1.1" {
		t.Fatal("default rule should prefer two healthy HTTP/1.1 nodes")
	}
	tb.SetMode(host, true)
	if tb.Mode(host) != "h2" {
		t.Fatal("override to h2 ignored")
	}
	if n := tb.Pick(host, nil); !n.H2 {
		t.Fatalf("picked HTTP/1.1 node %s in h2 mode", n.IP)
	}
}
