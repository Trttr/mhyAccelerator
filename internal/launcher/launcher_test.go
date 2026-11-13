package launcher

import "testing"

func TestParseLine(t *testing.T) {
	cases := []struct {
		line string
		kind string
		pkg  string
		dl   int64
	}{
		{`[sophon:15772:1112/175229.635:WARNING:application_manifest_fetcher.cc(142)] [chunk] normal_download app chunk task 1c60afba new fetch build, package_id: FfGDa3bsvp, branch: main, tag:`, "started", "FfGDa3bsvp", 0},
		{`[sophon:19900:1112/175259.716:WARNING:application_chunk_download_job.cc(85)] [chunk] request to cancel normal_download app chunk task 1c60afba`, "paused", "", 0},
		{`[sophon:444:1112/185006.787:WARNING:application_chunk_download_job.cc(1146)] [chunk] normal_download app chunk task 41048662 finished; cost 47986 milliseconds; info: {"action":"normal_download","current_download_byte":"60204710427","enable_pcdn":true,"package_id":"FfGDa3bsvp","status":"Success","total_download_byte":"68525316111"}`, "finished", "FfGDa3bsvp", 60204710427},
		{`[sophon:444:1112/185006.787:WARNING:application_chunk_download_job.cc(1146)] [chunk] fetch_size app chunk task 1 finished; cost 1 milliseconds; info: {"status":"Success"}`, "", "", 0},
	}
	for _, c := range cases {
		s, ok := parseLine(c.line)
		if c.kind == "" {
			if ok {
				t.Fatalf("unexpected state %+v for %q", s, c.line)
			}
			continue
		}
		if !ok || s.Kind != c.kind || s.PackageID != c.pkg || s.Downloaded != c.dl {
			t.Fatalf("got %+v (ok=%v), want kind=%s pkg=%s dl=%d", s, ok, c.kind, c.pkg, c.dl)
		}
	}
}
