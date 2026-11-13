// Package sophon reads the launcher's chunked-download (Sophon) manifests, so the
// speed test can download the very chunk files the launcher downloads.
package sophon

import (
	"encoding/binary"
	"errors"
)

// A minimal protobuf wire-format reader: enough to walk a message and pull out
// varint and length-delimited fields, without generated code or a schema.

var errWire = errors.New("malformed protobuf")

type field struct {
	num   int
	wire  int    // 0 varint, 1 fixed64, 2 bytes, 5 fixed32
	value uint64 // varint / fixed values
	bytes []byte // length-delimited payload
}

func readVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

// fields splits a message into its top-level fields.
func fields(b []byte) ([]field, error) {
	var out []field
	for len(b) > 0 {
		key, n := readVarint(b)
		if n == 0 {
			return nil, errWire
		}
		b = b[n:]
		f := field{num: int(key >> 3), wire: int(key & 7)}
		if f.num == 0 {
			return nil, errWire
		}
		switch f.wire {
		case 0:
			v, n := readVarint(b)
			if n == 0 {
				return nil, errWire
			}
			f.value, b = v, b[n:]
		case 1:
			if len(b) < 8 {
				return nil, errWire
			}
			f.value, b = binary.LittleEndian.Uint64(b), b[8:]
		case 2:
			l, n := readVarint(b)
			if n == 0 || uint64(len(b)-n) < l {
				return nil, errWire
			}
			f.bytes, b = b[n:n+int(l)], b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return nil, errWire
			}
			f.value, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		default:
			return nil, errWire
		}
		out = append(out, f)
	}
	return out, nil
}
