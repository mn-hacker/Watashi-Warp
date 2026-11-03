package wiresocks

// watashi v1.2.6-ws1: the relay between the SOCKS client and the userspace
// network stack.
//
// v1.2.6 waited for both copy directions to end before it closed anything,
// and it never told the far end that the client had finished. So when the app
// behind the proxy hung up, the copy towards the app stayed blocked in a read
// on the WARP side until that server happened to talk or hang up. Many never
// do (idle keep-alive sockets, push channels, a peer that silently vanished
// when the WARP endpoint roamed), and every one of those left two goroutines,
// two 64 KiB buffers and a gVisor TCP endpoint behind for the life of the
// process. That is the RAM that kept climbing in warp and gool mode. cfon is
// spared because the app talks to Psiphon there, and only a handful of
// long-lived Psiphon tunnels ever cross this relay.
//
// Now: the finish is passed on with a half-close, the other direction gets a
// short grace period, an error ends both directions at once, and a flow with
// no traffic either way for the idle period is closed.

import (
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bepass-org/warp-plus/proxy/pkg/mixed"
)

var (
	// TCPIdleTimeout ends a TCP flow that moved nothing in either direction.
	TCPIdleTimeout = 5 * time.Minute
	// UDPIdleTimeout ends a UDP association that moved nothing either way.
	UDPIdleTimeout = 30 * time.Second
	// HalfCloseTimeout is how long one side may keep talking after the other
	// side said it is done.
	HalfCloseTimeout = 30 * time.Second
)

// flow is the state both directions of one relayed connection share.
type flow struct {
	last  atomic.Int64 // unix nanoseconds of the last byte moved either way
	grace atomic.Int64 // unix nanoseconds after which the flow is over, 0 for none
}

func newFlow() *flow {
	f := &flow{}
	f.touch()
	return f
}

func (f *flow) touch() { f.last.Store(time.Now().UnixNano()) }

// deadline is when a read that sees nothing should come back.
func (f *flow) deadline(idle time.Duration) time.Time {
	d := time.Now().Add(idle)
	if g := f.grace.Load(); g != 0 {
		if gt := time.Unix(0, g); gt.Before(d) {
			d = gt
		}
	}
	return d
}

// alive says whether a read that timed out should simply be tried again:
// the other direction is still moving bytes and no grace period ran out.
func (f *flow) alive(idle time.Duration) bool {
	now := time.Now()
	if g := f.grace.Load(); g != 0 && !now.Before(time.Unix(0, g)) {
		return false
	}
	return now.Sub(time.Unix(0, f.last.Load())) < idle
}

func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// quiet errors are the normal ways a connection ends and are not worth a log line.
func quiet(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || isTimeout(err)
}

// pump copies src to dst until src is finished (nil), or something fails.
func pump(dst, src net.Conn, b []byte, f *flow, idle time.Duration) error {
	for {
		if err := src.SetReadDeadline(f.deadline(idle)); err != nil {
			return err
		}
		nr, er := src.Read(b)
		if nr > 0 {
			f.touch()
			_ = dst.SetWriteDeadline(time.Now().Add(idle))
			nw, ew := dst.Write(b[:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errInvalidWrite
				}
			}
			if ew != nil {
				return ew
			}
			if nr != nw {
				return io.ErrShortWrite
			}
		}
		if er != nil {
			if isTimeout(er) && f.alive(idle) {
				continue
			}
			if errors.Is(er, io.EOF) {
				return nil
			}
			return er
		}
	}
}

type closeWriter interface{ CloseWrite() error }

// closeWrite sends a FIN on c if the connection underneath can do that.
func closeWrite(c net.Conn) bool {
	if sc, ok := c.(*mixed.SwitchConn); ok {
		c = sc.Conn
	}
	if cw, ok := c.(closeWriter); ok {
		return cw.CloseWrite() == nil
	}
	return false
}

// relay moves bytes between client and remote until the flow is over, then
// closes both. stream is true for TCP. The error is the first one that is not
// an ordinary way for a connection to end, or nil.
func relay(client, remote net.Conn, alloc func() []byte, free func([]byte), stream bool, idle, grace time.Duration) error {
	f := newFlow()
	type end struct {
		up  bool
		err error
	}
	done := make(chan end, 2)
	run := func(up bool, dst, src net.Conn) {
		b := alloc()
		defer free(b)
		done <- end{up, pump(dst, src, b, f, idle)}
	}
	go run(true, remote, client)
	go run(false, client, remote)

	first := <-done
	if first.err == nil && stream {
		// One side is done sending. Tell the other side, and give it a
		// short while to finish what it is saying.
		other := client
		if first.up {
			other = remote
		}
		closeWrite(other)
		until := time.Now().Add(grace)
		f.grace.Store(until.UnixNano())
		_ = other.SetReadDeadline(until)
	} else {
		_ = client.Close()
		_ = remote.Close()
	}
	second := <-done
	_ = client.Close()
	_ = remote.Close()

	if !quiet(first.err) {
		return first.err
	}
	if !quiet(second.err) {
		return second.err
	}
	return nil
}
