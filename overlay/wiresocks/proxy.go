package wiresocks

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/sagernet/sing/common/buf"

	"github.com/bepass-org/warp-plus/proxy/pkg/mixed"
	"github.com/bepass-org/warp-plus/proxy/pkg/statute"
	"github.com/bepass-org/warp-plus/wireguard/device"
	"github.com/bepass-org/warp-plus/wireguard/tun/netstack"
)

// VirtualTun stores a reference to netstack network and DNS configuration
type VirtualTun struct {
	Tnet   *netstack.Net
	Logger *slog.Logger
	Dev    *device.Device
	Ctx    context.Context
	pool   buf.Allocator
}

// BuffSize is the copy buffer of one direction of one connection.
// watashi v1.2.6-ws1: 64 KiB per direction was twice what the stack hands
// over in one read on this path; 32 KiB halves the floor per connection.
var BuffSize = 32 * 1024

// DialTimeout bounds how long one connect through the tunnel may take.
var DialTimeout = 30 * time.Second

// StartProxy spawns a socks5 server.
func StartProxy(ctx context.Context, l *slog.Logger, tnet *netstack.Net, bindAddress netip.AddrPort) (netip.AddrPort, error) {
	ln, err := net.Listen("tcp", bindAddress.String())
	if err != nil {
		return netip.AddrPort{}, err // Return error if binding was unsuccessful
	}

	vt := VirtualTun{
		Tnet:   tnet,
		Logger: l.With("subsystem", "vtun"),
		Dev:    nil,
		Ctx:    ctx,
		pool:   buf.DefaultAllocator,
	}

	proxy := mixed.NewProxy(
		mixed.WithListener(ln),
		mixed.WithLogger(l),
		mixed.WithContext(ctx),
		mixed.WithUserHandler(func(request *statute.ProxyRequest) error {
			return vt.generalHandler(request)
		}),
	)
	go func() {
		_ = proxy.ListenAndServe()
	}()
	go func() {
		<-vt.Ctx.Done()
		vt.Stop()
	}()

	return ln.Addr().(*net.TCPAddr).AddrPort(), nil
}

func (vt *VirtualTun) generalHandler(req *statute.ProxyRequest) error {
	vt.Logger.Debug("handling connection", "protocol", req.Network, "destination", req.Destination)

	dctx, cancel := context.WithTimeout(vt.Ctx, DialTimeout)
	conn, err := vt.Tnet.DialContext(dctx, req.Network, req.Destination)
	cancel()
	if err != nil {
		// watashi v1.2.6-ws1: the client side used to be left open here.
		_ = req.Conn.Close()
		return err
	}

	stream, idle := true, TCPIdleTimeout
	switch req.Network {
	case "udp", "udp4", "udp6":
		stream, idle = false, UDPIdleTimeout
	}

	err = relay(req.Conn, conn,
		func() []byte { return vt.pool.Get(BuffSize) },
		func(b []byte) { _ = vt.pool.Put(b) },
		stream, idle, HalfCloseTimeout)
	if err != nil {
		vt.Logger.Debug("connection ended", "destination", req.Destination, "error", err)
	}
	return nil
}

func (vt *VirtualTun) Stop() {
	if vt.Dev != nil {
		if err := vt.Dev.Down(); err != nil {
			vt.Logger.Warn(err.Error())
		}
	}
}

var errInvalidWrite = errors.New("invalid write result")

// copyConnTimeout is the v1.2.6 copy loop, kept for anything outside this
// file that may still call it. generalHandler uses relay now.
func copyConnTimeout(dst net.Conn, src net.Conn, buf []byte, timeout time.Duration) (written int64, err error) {
	if buf != nil && len(buf) == 0 {
		panic("empty buffer in CopyBuffer")
	}

	for {
		deadline := time.Time{}
		if timeout != 0 {
			deadline = time.Now().Add(timeout)
		}
		if err := src.SetReadDeadline(deadline); err != nil {
			return 0, err
		}

		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errInvalidWrite
				}
			}
			written += int64(nw)
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
}
