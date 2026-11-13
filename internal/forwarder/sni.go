package forwarder

import (
	"encoding/binary"
	"errors"
	"io"
)

const maxRecord = 16384 + 2048

var errNotTLS = errors.New("not a TLS ClientHello")

// readClientHello reads the first TLS record from r and returns its raw bytes
// together with the SNI host name. The bytes are replayed verbatim upstream.
func readClientHello(r io.Reader) ([]byte, string, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, "", err
	}
	if hdr[0] != 0x16 || hdr[1] != 0x03 {
		return hdr, "", errNotTLS
	}
	n := int(binary.BigEndian.Uint16(hdr[3:5]))
	if n == 0 || n > maxRecord {
		return hdr, "", errNotTLS
	}
	rec := make([]byte, 5+n)
	copy(rec, hdr)
	if _, err := io.ReadFull(r, rec[5:]); err != nil {
		return nil, "", err
	}
	sni, err := parseSNI(rec[5:])
	return rec, sni, err
}

// parseSNI extracts the server_name extension from a ClientHello handshake message.
func parseSNI(b []byte) (string, error) {
	p := parser{b: b}
	if p.u8() != 1 { // handshake type: client_hello
		return "", errNotTLS
	}
	p.skip(3) // handshake length
	p.skip(2) // legacy_version
	p.skip(32)
	p.skip(int(p.u8()))  // session id
	p.skip(int(p.u16())) // cipher suites
	p.skip(int(p.u8()))  // compression methods
	if p.err != nil {
		return "", p.err
	}
	if p.left() == 0 {
		return "", errors.New("ClientHello has no extensions")
	}
	ext := parser{b: p.bytes(int(p.u16()))}
	for ext.err == nil && ext.left() >= 4 {
		typ, data := ext.u16(), ext.bytes(int(ext.u16()))
		if typ != 0 { // server_name
			continue
		}
		list := parser{b: data}
		names := parser{b: list.bytes(int(list.u16()))}
		for names.err == nil && names.left() >= 3 {
			kind, name := names.u8(), names.bytes(int(names.u16()))
			if kind == 0 && names.err == nil {
				return string(name), nil
			}
		}
	}
	if ext.err != nil {
		return "", ext.err
	}
	return "", errors.New("ClientHello has no SNI")
}

type parser struct {
	b   []byte
	err error
}

var errShort = errors.New("truncated ClientHello")

func (p *parser) left() int { return len(p.b) }

func (p *parser) bytes(n int) []byte {
	if p.err != nil || n > len(p.b) {
		p.err = errShort
		return nil
	}
	out := p.b[:n]
	p.b = p.b[n:]
	return out
}

func (p *parser) skip(n int) { p.bytes(n) }

func (p *parser) u8() uint8 {
	b := p.bytes(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (p *parser) u16() uint16 {
	b := p.bytes(2)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint16(b)
}
