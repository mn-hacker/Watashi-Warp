package mixed

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/bepass-org/warp-plus/proxy/pkg/http"
	"github.com/bepass-org/warp-plus/proxy/pkg/socks4"
	"github.com/bepass-org/warp-plus/proxy/pkg/socks5"
	"github.com/bepass-org/warp-plus/proxy/pkg/statute"
)

type userHandler func(request *statute.ProxyRequest) error

type Proxy struct {
	// bind is the address to listen on
	bind string

	listener net.Listener

	// socks5Proxy is a socks5 server with tcp and udp support
	socks5Proxy *socks5.Server
	// socks4Proxy is a socks4 server with tcp support
	socks4Proxy *socks4.Server
	// httpProxy is a http proxy server with http and http-connect support
	httpProxy *http.Server
	// userConnectHandle is a user handler for tcp and udp requests(its general handler)
	userHandler userHandler
	// if user doesnt set userHandler, it can specify userTCPHandler for manual handling of tcp requests
	userTCPHandler userHandler
	// if user doesnt set userHandler, it can specify userUDPHandler for manual handling of udp requests
	userUDPHandler userHandler
	// overwrite dial functions of http, socks4, socks5
	userDialFunc statute.ProxyDialFunc
	// logger error log
	logger *slog.Logger
	// ctx is default context
	ctx context.Context
}

func NewProxy(options ...Option) *Proxy {
	p := &Proxy{
		bind:         statute.DefaultBindAddress,
		socks5Proxy:  socks5.NewServer(),
		socks4Proxy:  socks4.NewServer(),
		httpProxy:    http.NewServer(),
		userDialFunc: statute.DefaultProxyDial(),
		logger:       slog.Default(),
		ctx:          statute.DefaultContext(),
	}

	for _, option := range options {
		option(p)
	}

	return p
}

type Option func(*Proxy)

// SwitchConn wraps a net.Conn and a bufio.Reader
type SwitchConn struct {
	net.Conn
	*bufio.Reader
}

// NewSwitchConn creates a new SwitchConn
func NewSwitchConn(conn net.Conn) *SwitchConn {
	return &SwitchConn{
		Conn:   conn,
		Reader: bufio.NewReaderSize(conn, 2048),
	}
}

// Read reads data into p, first from the bufio.Reader, then from the net.Conn
func (c *SwitchConn) Read(p []byte) (n int, err error) {
	return c.Reader.Read(p)
}

// CloseWrite half-closes the connection underneath when it can do that.
// watashi v1.2.6-ws1: the relay passes a finished direction on with this.
func (c *SwitchConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errors.New("half-close is not supported here")
}

func (p *Proxy) ListenAndServe() error {
	// Create a new listener
	if p.listener == nil {
		ln, err := net.Listen("tcp", p.bind)
		if err != nil {
			return err // Return error if binding was unsuccessful
		}
		p.listener = ln
	}

	p.bind = p.listener.Addr().(*net.TCPAddr).String()

	// ensure listener will be closed
	defer func() {
		_ = p.listener.Close()
	}()

	// Create a cancelable context based on p.Context
	ctx, cancel := context.WithCancel(p.ctx)
	defer cancel() // Ensure resources are cleaned up

	// watashi v1.2.6-ws1: v1.2.6 retried a failing Accept at once, so a
	// closed listener or a full file table spun a CPU core and flooded the
	// journal. A cancel now closes the listener, and errors back off.
	go func() {
		<-ctx.Done()
		_ = p.listener.Close()
	}()

	var delay time.Duration
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return ctx.Err()
			}
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else if delay < time.Second {
				delay *= 2
			}
			p.logger.Error(err.Error())
			time.Sleep(delay)
			continue
		}
		delay = 0

		// Start a new goroutine to handle each connection
		// This way, the server can handle multiple connections concurrently
		go func() {
			defer conn.Close()
			err := p.handleConnection(conn)
			if err != nil {
				p.logger.Debug(err.Error()) // watashi: one line per failed dial was most of the journal
			}
		}()
	}
}

func (p *Proxy) handleConnection(conn net.Conn) error {
	// Create a SwitchConn
	switchConn := NewSwitchConn(conn)

	// Peek one byte to determine the protocol
	buf, err := switchConn.Peek(1)
	if err != nil {
		return err
	}

	switch buf[0] {
	case 5:
		err = p.socks5Proxy.ServeConn(switchConn)
	case 4:
		err = p.socks4Proxy.ServeConn(switchConn)
	default:
		err = p.httpProxy.ServeConn(switchConn)
	}

	return err
}
