package nodetable

import (
	"testing"
	"time"
)

func TestSetAsideThenRecover(t *testing.T) {
	tb := New()
	tb.Seed(host, "1.1.1.1", true, 40e6)
	tb.Seed(host, "2.2.2.2", true, 20e6)
	tb.SetMode(host, true)
	n := tb.find(host, "1.1.1.1")
	tb.SetAside(n, time.Minute)
	for i := 0; i < 10; i++ {
		p := tb.Pick(host, nil)
		if p.IP == "1.1.1.1" {
			t.Fatal("picked a node that is set aside")
		}
		tb.Release(p)
	}
	tb.Resolve(host, "1.1.1.1", true, 30e6)
	v := tb.Snapshot(host)[0]
	if v.IP != "1.1.1.1" || v.Observing || v.Cooling || v.TotalFails != 0 || v.Score != 40e6 {
		t.Fatalf("after recovery: %+v", v)
	}
}

func TestSetAsideThenConfirm(t *testing.T) {
	tb := New()
	tb.Seed(host, "117.187.26.224", true, 40e6)
	tb.Seed(host, "117.187.26.227", true, 35e6)
	tb.Seed(host, "111.48.108.100", true, 20e6)
	tb.SetMode(host, true)
	tb.SetAside(tb.find(host, "117.187.26.224"), time.Minute)
	// Being set aside must not rest the neighbours: nobody is blamed yet.
	if v := viewOf(tb, "117.187.26.227"); v.Cooling {
		t.Fatal("neighbour rested before any verdict")
	}
	tb.Resolve(host, "117.187.26.224", false, 0.5e6)
	v := viewOf(tb, "117.187.26.224")
	if v.Observing || !v.Cooling || v.TotalFails != 1 || v.Score > 0.5e6 {
		t.Fatalf("after confirmation: %+v", v)
	}
	if !viewOf(tb, "117.187.26.227").Cooling {
		t.Fatal("confirmed failure should rest the /24")
	}
}

func viewOf(tb *Table, ip string) View {
	for _, v := range tb.Snapshot(host) {
		if v.IP == ip {
			return v
		}
	}
	return View{}
}
