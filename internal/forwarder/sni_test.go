package forwarder

import (
	"bytes"
	"crypto/tls"
	"net"
	"testing"
	"time"
)

// captureHello makes crypto/tls produce a real ClientHello for host and returns its bytes.
func captureHello(t *testing.T, host string) []byte {
	t.Helper()
	c1, c2 := net.Pipe()
	defer c2.Close()
	go func() {
		tls.Client(c1, &tls.Config{ServerName: host}).Handshake()
		c1.Close()
	}()
	c2.SetReadDeadline(time.Now().Add(3 * time.Second))
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	for {
		n, err := c2.Read(tmp)
		buf.Write(tmp[:n])
		if err != nil || buf.Len() >= 5 && buf.Len() >= 5+int(buf.Bytes()[3])<<8|int(buf.Bytes()[4]) {
			break
		}
	}
	return buf.Bytes()
}

func TestReadClientHello(t *testing.T) {
	raw := captureHello(t, "autopatchcn.bhsr.com")
	rec, sni, err := readClientHello(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if sni != "autopatchcn.bhsr.com" {
		t.Fatalf("sni = %q", sni)
	}
	if !bytes.Equal(rec, raw[:len(rec)]) {
		t.Fatal("record bytes must be returned verbatim for replay")
	}
}

func TestReadClientHelloRejectsGarbage(t *testing.T) {
	for _, in := range [][]byte{
		[]byte("GET / HTTP/1.1\r\n\r\n"),
		{0x16, 0x03, 0x01, 0x00, 0x05, 0x01, 0x00, 0x00, 0x01, 0x03},
	} {
		if _, sni, err := readClientHello(bytes.NewReader(in)); err == nil {
			t.Fatalf("accepted %q as SNI %q", in, sni)
		}
	}
}
