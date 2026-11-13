package main

import "testing"

func TestDisplayWidth(t *testing.T) {
	cases := map[string]int{
		"abc":          3,
		"加速中":          6,
		"速度 62.4 MB/s": 14,
		"│":            1,
	}
	for s, want := range cases {
		if got := displayWidth(s); got != want {
			t.Errorf("displayWidth(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestTrimWidthKeepsColoursAndNeverOverflows(t *testing.T) {
	s := cGreen + "● 加速中" + cReset + "  速度 62.4 MB/s  已加速 7.47 GB"
	for w := 1; w < 40; w++ {
		got := trimWidth(s, w)
		if dw := displayWidth(stripANSI(got)); dw > w {
			t.Fatalf("width %d: got %d columns: %q", w, dw, got)
		}
	}
	if got := trimWidth(s, 200); got != s {
		t.Fatalf("a line that fits was changed: %q", got)
	}
}

func TestEveryTextHasBothLanguages(t *testing.T) {
	for k := range texts["zh"] {
		if texts["en"][k] == "" {
			t.Errorf("missing English text for %q", k)
		}
	}
	for k := range texts["en"] {
		if texts["zh"][k] == "" {
			t.Errorf("missing Chinese text for %q", k)
		}
	}
}
