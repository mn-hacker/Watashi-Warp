package wiresocks

import (
	"io"
	"net"
	"testing"
	"time"
)

func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ch := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			ch <- nil
			return
		}
		ch <- c
	}()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b := <-ch
	if b == nil {
		t.Fatal("accept failed")
	}
	return a, b
}

func testAlloc() []byte { return make([]byte, 4096) }
func testFree([]byte)   {}

// The v1.2.6 leak: the app hangs up, the far end stays quiet, and the relay
// waited for the far end forever.
func TestRelayEndsWhenTheAppLeavesAndTheServerIsQuiet(t *testing.T) {
	app, appSide := tcpPair(t)
	srvSide, server := tcpPair(t)
	defer server.Close()

	ended := make(chan error, 1)
	go func() { ended <- relay(appSide, srvSide, testAlloc, testFree, true, time.Minute, 300*time.Millisecond) }()

	_ = app.Close()

	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := server.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("the server was not told the app left: %v", err)
	}
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the relay is still holding the flow after the app left")
	}
}

func TestRelayEndsAFlowThatIsIdleBothWays(t *testing.T) {
	app, appSide := tcpPair(t)
	srvSide, server := tcpPair(t)
	defer app.Close()
	defer server.Close()

	start := time.Now()
	_ = relay(appSide, srvSide, testAlloc, testFree, true, 300*time.Millisecond, time.Minute)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("an idle flow lived %s", took)
	}
}

// A download where the app never sends a byte must not be cut by the idle
// timer of the silent direction.
func TestRelayKeepsAOneWayStreamAlive(t *testing.T) {
	app, appSide := tcpPair(t)
	srvSide, server := tcpPair(t)
	defer app.Close()

	ended := make(chan error, 1)
	go func() { ended <- relay(appSide, srvSide, testAlloc, testFree, true, 300*time.Millisecond, time.Minute) }()

	go func() {
		defer server.Close()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		stop := time.After(1200 * time.Millisecond)
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				if _, err := server.Write([]byte("x")); err != nil {
					return
				}
			}
		}
	}()

	got := 0
	b := make([]byte, 64)
	_ = app.SetReadDeadline(time.Now().Add(4 * time.Second))
	for {
		n, err := app.Read(b)
		got += n
		if err != nil {
			break
		}
	}
	if got < 15 {
		t.Fatalf("the stream was cut after %d bytes", got)
	}
	_ = app.Close()
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the relay did not end after both sides left")
	}
}

// Bytes still arrive in both directions and in order.
func TestRelayCarriesBothDirections(t *testing.T) {
	app, appSide := tcpPair(t)
	srvSide, server := tcpPair(t)
	defer app.Close()
	defer server.Close()
	go func() { _ = relay(appSide, srvSide, testAlloc, testFree, true, time.Minute, time.Minute) }()

	go func() {
		b := make([]byte, 5)
		if _, err := io.ReadFull(server, b); err == nil {
			_, _ = server.Write(append([]byte("re:"), b...))
		}
	}()
	if _, err := app.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 8)
	_ = app.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(app, b); err != nil || string(b) != "re:hello" {
		t.Fatalf("got %q, %v", b, err)
	}
}
