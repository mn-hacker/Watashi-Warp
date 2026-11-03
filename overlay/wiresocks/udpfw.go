package wiresocks

// watashi v1.2.6-ws1: the gool forwarder between the inner and the outer
// WARP tunnel.
//
// v1.2.6 checked ctx only between reads, so a cancel never woke a read that
// was already blocked, and the sockets were closed only after both readers
// had returned - which a blocked reader never did. A read error was retried
// at once with `continue`, so a closed socket spun one CPU core forever, and
// the address of the local peer was shared by two goroutines with no
// synchronisation. Now closing the sockets is what stops both readers, an
// error backs off or ends the loop, and the peer address is atomic.

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bepass-org/warp-plus/wireguard/tun/netstack"
)

func NewVtunUDPForwarder(ctx context.Context, localBind netip.AddrPort, dest string, tnet *netstack.Net, mtu int) (netip.AddrPort, error) {
	destAddr, err := net.ResolveUDPAddr("udp", dest)
	if err != nil {
		return netip.AddrPort{}, err
	}

	listener, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(localBind))
	if err != nil {
		return netip.AddrPort{}, err
	}

	rconn, err := tnet.DialUDP(nil, destAddr)
	if err != nil {
		_ = listener.Close()
		return netip.AddrPort{}, err
	}

	var (
		client   atomic.Pointer[net.UDPAddr]
		closed   atomic.Bool
		stopOnce sync.Once
	)
	stop := func() {
		stopOnce.Do(func() {
			closed.Store(true)
			_ = listener.Close()
			_ = rconn.Close()
		})
	}
	over := func(err error) bool {
		return closed.Load() || ctx.Err() != nil || errors.Is(err, net.ErrClosed)
	}
	// a read that keeps failing waits a little before it tries again
	backoff := func(n *int) {
		if *n < 10 {
			*n++
		}
		time.Sleep(time.Duration(*n) * 10 * time.Millisecond)
	}

	if mtu < 1500 {
		mtu = 1500
	}

	go func() {
		<-ctx.Done()
		stop()
	}()

	// local wireguard-go -> outer tunnel
	go func() {
		defer stop()
		buffer := make([]byte, mtu+128)
		fails := 0
		for {
			n, cAddr, err := listener.ReadFromUDP(buffer)
			if err != nil {
				if over(err) {
					return
				}
				backoff(&fails)
				continue
			}
			fails = 0
			client.Store(cAddr)
			if _, err := rconn.WriteTo(buffer[:n], destAddr); err != nil && over(err) {
				return
			}
		}
	}()

	// outer tunnel -> local wireguard-go
	go func() {
		defer stop()
		buffer := make([]byte, mtu+128)
		fails := 0
		for {
			n, _, err := rconn.ReadFrom(buffer)
			if err != nil {
				if over(err) {
					return
				}
				backoff(&fails)
				continue
			}
			fails = 0
			if c := client.Load(); c != nil {
				if _, err := listener.WriteToUDP(buffer[:n], c); err != nil && over(err) {
					return
				}
			}
		}
	}()

	return listener.LocalAddr().(*net.UDPAddr).AddrPort(), nil
}
