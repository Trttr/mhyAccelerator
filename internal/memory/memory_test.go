package memory

import (
	"path/filepath"
	"testing"
	"time"

	"mhyacc/internal/nodetable"
)

const host = "autopatchcn.juequling.com"

func TestRoundTripAndRanking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	s := Load(path)
	now := time.Now()
	s.Update(host, []nodetable.View{
		{IP: "111.48.108.100", H2: true, Score: 30e6, Samples: 100},
		{IP: "111.48.162.242", H2: true, Score: 20e6, Samples: 100},
		{IP: "117.187.26.227", H2: true, Score: 25e6, Samples: 50, TotalFails: 3, LastFail: now},
		{IP: "1.2.3.4", H2: true, Score: 9e6, Samples: 1}, // not enough traffic: ignored
	})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	s = Load(path)
	good := s.Good(host)
	if len(good) != 2 || good[0] != "111.48.108.100" || good[1] != "111.48.162.242" {
		t.Fatalf("good = %v", good)
	}
	if !s.Suspect(host, "117.187.26.227") {
		t.Fatal("failing node not suspect")
	}
	if !s.Suspect(host, "117.187.26.230") {
		t.Fatal("/24 neighbour of a failing node not suspect")
	}
	if s.Suspect(host, "111.48.108.100") {
		t.Fatal("good node marked suspect")
	}
	if s.Score(host, "1.2.3.4") != 0 {
		t.Fatal("barely used node was remembered")
	}
}

func TestCorruptFileIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	s := Load(path)
	s.Hosts = nil
	if got := Load(filepath.Join(t.TempDir(), "missing.json")); len(got.Hosts) != 0 {
		t.Fatal("expected empty store")
	}
}
