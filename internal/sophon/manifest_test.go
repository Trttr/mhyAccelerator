package sophon

import "testing"

// pb helpers to build a manifest in wire format.
func pbBytes(num int, b []byte) []byte {
	out := pbVarint(uint64(num<<3 | 2))
	out = append(out, pbVarint(uint64(len(b)))...)
	return append(out, b...)
}

func pbUint(num int, v uint64) []byte {
	return append(pbVarint(uint64(num<<3)), pbVarint(v)...)
}

func pbVarint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func chunk(name string, size uint64) []byte {
	var c []byte
	c = append(c, pbBytes(1, []byte(name))...)
	c = append(c, pbBytes(2, []byte("md5"))...)
	c = append(c, pbUint(3, 0)...)
	c = append(c, pbUint(4, size)...)
	c = append(c, pbUint(5, size+10)...)
	return c
}

func TestParseManifest(t *testing.T) {
	var asset []byte
	asset = append(asset, pbBytes(1, []byte("Game_Data/a.bin"))...)
	asset = append(asset, pbBytes(2, chunk("aaaa_1111", 1_400_000))...)
	asset = append(asset, pbBytes(2, chunk("bbbb_2222", 900_000))...)
	asset = append(asset, pbUint(4, 2_300_000)...)
	var asset2 []byte
	asset2 = append(asset2, pbBytes(1, []byte("Game_Data/b.bin"))...)
	asset2 = append(asset2, pbBytes(2, chunk("aaaa_1111", 1_400_000))...) // shared chunk, listed once
	manifest := append(pbBytes(1, asset), pbBytes(1, asset2)...)

	got, err := parseManifest(manifest, "https://cdn.example/chunks")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2: %+v", len(got), got)
	}
	if got[0].URL != "https://cdn.example/chunks/aaaa_1111" || got[0].Size != 1_400_000 {
		t.Fatalf("first chunk = %+v", got[0])
	}
}

func TestParseManifestRejectsGarbage(t *testing.T) {
	if _, err := parseManifest([]byte{0xff, 0xff, 0xff}, "x"); err == nil {
		t.Fatal("garbage accepted")
	}
}
