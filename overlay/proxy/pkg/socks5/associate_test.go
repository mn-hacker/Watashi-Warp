package socks5

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/bepass-org/warp-plus/proxy/pkg/statute"
)

func startOne(t *testing.T, s *Server) (net.Conn, chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() {
		defer ln.Close()
		c, err := ln.Accept()
		if err != nil {
			served <- err
			return
		}
		defer c.Close()
		served <- s.ServeConn(c)
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return c, served
}

// associate does the greeting and the ASSOCIATE request, and returns the
// UDP address the server relays on.
func associate(t *testing.T, c net.Conn) *net.UDPAddr {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	hello := make([]byte, 2)
	if _, err := io.ReadFull(c, hello); err != nil || hello[1] != 0 {
		t.Fatalf("greeting: %v %v", hello, err)
	}
	// DST.ADDR filled in with an address that is not ours, which v1.2.6
	// tried to bind and failed on
	if _, err := c.Write([]byte{5, 3, 0, 1, 1, 2, 3, 4, 0x30, 0x39}); err != nil {
		t.Fatal(err)
	}
	rep := make([]byte, 10)
	if _, err := io.ReadFull(c, rep); err != nil || rep[1] != 0 {
		t.Fatalf("associate reply: %v %v", rep, err)
	}
	_ = c.SetDeadline(time.Time{})
	return &net.UDPAddr{IP: net.IP(rep[4:8]), Port: int(binary.BigEndian.Uint16(rep[8:10]))}
}

func TestAssociateWithNoPacketIsGivenUp(t *testing.T) {
	old := AssociateFirstPacketTimeout
	AssociateFirstPacketTimeout = 300 * time.Millisecond
	defer func() { AssociateFirstPacketTimeout = old }()

	called := make(chan struct{}, 1)
	s := NewServer(WithAssociateHandle(func(*statute.ProxyRequest) error { called <- struct{}{}; return nil }))
	c, served := startOne(t, s)
	defer c.Close()
	associate(t, c)

	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("an association that never got a packet was never given up")
	}
	select {
	case <-called:
		t.Fatal("the handler ran without a packet")
	default:
	}
}

func TestAssociateEndsWhenTheClientHangsUp(t *testing.T) {
	s := NewServer(WithAssociateHandle(func(*statute.ProxyRequest) error { return nil }))
	c, served := startOne(t, s)
	associate(t, c)
	_ = c.Close()

	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("the association outlived the TCP connection that owns it")
	}
}

func TestAssociateKeepsADomainTarget(t *testing.T) {
	got := make(chan string, 1)
	s := NewServer(WithAssociateHandle(func(r *statute.ProxyRequest) error {
		b := make([]byte, 64)
		_ = r.Conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := r.Conn.Read(b)
		if err != nil {
			got <- "error: " + err.Error()
			return nil
		}
		got <- r.Destination + "|" + string(b[:n])
		_, _ = r.Conn.Write([]byte("pong"))
		return nil
	}))
	c, _ := startOne(t, s)
	defer c.Close()
	relayAddr := associate(t, c)

	u, err := net.DialUDP("udp", nil, relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	name := "example.com"
	pkt := []byte{0, 0, 0, 3, byte(len(name))}
	pkt = append(pkt, name...)
	pkt = append(pkt, 0x01, 0xbb)
	pkt = append(pkt, "ping"...)
	if _, err := u.Write(pkt); err != nil {
		t.Fatal(err)
	}

	select {
	case g := <-got:
		if g != "example.com:443|ping" {
			t.Fatalf("handler saw %q", g)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the packet never reached the handler")
	}

	_ = u.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 128)
	n, err := u.Read(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(b[:n], []byte("pong")) || b[3] != 3 {
		t.Fatalf("reply packet %v", b[:n])
	}
}

// Read must honour a deadline even though another goroutine owns the socket.
func TestUDPConnReadDeadline(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer b.Close()
	cc := newUDPCustomConn(pc, a)
	cc.asyncReadPackets()
	defer cc.Close()

	_ = cc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	start := time.Now()
	_, err = cc.Read(make([]byte, 8))
	if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("expected a timeout, got %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the deadline was ignored for %s", took)
	}
}
