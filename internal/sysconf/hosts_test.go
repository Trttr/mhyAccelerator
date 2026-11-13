package sysconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const original = "# Copyright (c) Microsoft Corp.\r\n127.0.0.1 localhost\r\n10.0.0.5 nas.local\r\n"

func scratchHosts(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	hostsOverride = p
	t.Cleanup(func() { hostsOverride = "" })
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApplyAndRemoveRoundTrip(t *testing.T) {
	p := scratchHosts(t)
	if err := Apply([]string{"autopatchcn.bhsr.com", "autopatchcn.bh3.com"}, 4242); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	if !strings.Contains(got, ListenIP+" autopatchcn.bhsr.com\r\n") || !strings.HasPrefix(got, original) {
		t.Fatalf("unexpected hosts after apply:\n%s", got)
	}
	if pid, on, _ := Owner(); !on || pid != 4242 {
		t.Fatalf("owner = %d, present = %v", pid, on)
	}
	// Re-applying replaces the block instead of stacking a second one.
	if err := Apply([]string{"autopatchcn.bhsr.com"}, 5151); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(read(t, p), beginPrefix); n != 1 {
		t.Fatalf("%d blocks after re-apply", n)
	}
	if err := Remove(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != original {
		t.Fatalf("hosts not restored byte for byte:\n%q", got)
	}
}

func TestRemoveIfOwner(t *testing.T) {
	scratchHosts(t)
	if err := Apply([]string{"autopatchcn.bhsr.com"}, 100); err != nil {
		t.Fatal(err)
	}
	if removed, _ := RemoveIfOwner(200); removed {
		t.Fatal("removed a block owned by another run")
	}
	if on, _ := Present(); !on {
		t.Fatal("block disappeared")
	}
	if removed, err := RemoveIfOwner(100); !removed || err != nil {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
	if on, _ := Present(); on {
		t.Fatal("block still present")
	}
}

func TestRemovesLegacyBlock(t *testing.T) {
	p := scratchHosts(t)
	legacy := original + "\r\n# >>> mhyAccelerator (managed block, removed by: mhyacc off)\r\n127.66.0.1 autopatchcn.bhsr.com\r\n# <<< mhyAccelerator\r\n"
	os.WriteFile(p, []byte(legacy), 0o644)
	if pid, on, _ := Owner(); !on || pid != 0 {
		t.Fatalf("legacy block: owner = %d, present = %v", pid, on)
	}
	if err := Remove(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != original {
		t.Fatalf("legacy block not removed:\n%q", got)
	}
}
