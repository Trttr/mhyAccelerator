package nodetable

import (
	"testing"
	"time"
)

// In single-connection mode the best node must win every time: the old weighted
// draw gave a 52 MB/s node only ~38% of picks against three 25-30 MB/s ones.
func TestH2ModePicksBestNode(t *testing.T) {
	tb := New()
	tb.Seed(host, "1.1.1.1", true, 52e6)
	tb.Seed(host, "2.2.2.2", true, 30e6)
	tb.Seed(host, "3.3.3.3", true, 28e6)
	tb.Seed(host, "4.4.4.4", true, 25e6)
	tb.Seed(host, "5.5.5.5", true, 1e6)
	tb.SetMode(host, true)
	for i := 0; i < 50; i++ {
		n := tb.Pick(host, nil)
		if n.IP != "1.1.1.1" {
			t.Fatalf("pick %d went to %s", i, n.IP)
		}
		tb.Release(n)
	}
}

// The yardstick a node is re-tested against must be its healthy speed, not the
// score after the collapse that got it set aside has dragged it down.
func TestYardstickSurvivesCollapse(t *testing.T) {
	tb := New()
	tb.Seed(host, "1.1.1.1", true, 40e6)
	n := tb.find(host, "1.1.1.1")
	for i := 0; i < 30; i++ {
		tb.Observe(n, 40e6)
	}
	for i := 0; i < 10; i++ { // ten seconds crawling at 0.2 MB/s
		tb.Observe(n, 0.2e6)
	}
	tb.SetAside(n, time.Minute)
	if got := tb.PrevScore(host, "1.1.1.1"); got < 35e6 {
		t.Fatalf("yardstick fell to %.1f MB/s", got/1e6)
	}
}

// A few slow seconds (the launcher pausing between files) must not sink a good node.
func TestScoreFallsSlowly(t *testing.T) {
	tb := New()
	tb.Seed(host, "1.1.1.1", true, 40e6)
	n := tb.find(host, "1.1.1.1")
	for i := 0; i < 3; i++ {
		tb.Observe(n, 1e6)
	}
	if got := tb.Score(n); got < 30e6 {
		t.Fatalf("three slow seconds cut the score to %.1f MB/s", got/1e6)
	}
}

// Cleared by a re-test, then misbehaving again soon: report it as a repeat.
func TestRepeatAfterRecovery(t *testing.T) {
	tb := New()
	tb.Seed(host, "1.1.1.1", true, 40e6)
	n := tb.find(host, "1.1.1.1")
	if tb.SetAside(n, time.Minute) {
		t.Fatal("first suspicion reported as a repeat")
	}
	tb.Resolve(host, "1.1.1.1", true, 38e6)
	if !tb.SetAside(n, time.Minute) {
		t.Fatal("second suspicion after a recovery not reported as a repeat")
	}
	// A release without a test (rate 0) is not a recovery and must not arm the rule.
	tb2 := New()
	tb2.Seed(host, "2.2.2.2", true, 40e6)
	m := tb2.find(host, "2.2.2.2")
	tb2.SetAside(m, time.Minute)
	tb2.Resolve(host, "2.2.2.2", true, 0)
	if tb2.SetAside(m, time.Minute) {
		t.Fatal("an untested release armed the repeat rule")
	}
}

func TestRestSubnetDoesNotCountFailures(t *testing.T) {
	tb := New()
	tb.Seed(host, "117.187.26.224", true, 40e6)
	tb.Seed(host, "117.187.26.227", true, 35e6)
	tb.Seed(host, "111.48.108.100", true, 20e6)
	tb.SetMode(host, true)
	tb.RestSubnet(tb.find(host, "117.187.26.224"), time.Minute)
	v := viewOf(tb, "117.187.26.227")
	if !v.Cooling || v.TotalFails != 0 {
		t.Fatalf("neighbour: %+v", v)
	}
	if viewOf(tb, "111.48.108.100").Cooling {
		t.Fatal("a node at another site was rested")
	}
}
