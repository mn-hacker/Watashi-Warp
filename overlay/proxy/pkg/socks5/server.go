package socks5

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/bepass-org/warp-plus/proxy/pkg/statute"
)

// AssociateFirstPacketTimeout bounds how long a UDP association may wait
// for its first packet before it is given up.
// watashi v1.2.6-ws1: v1.2.6 waited forever, so every association a client
// opened and then abandoned kept a UDP socket, the TCP connection and two
// goroutines for the life of the process.
var AssociateFirstPacketTimeout = 30 * time.Second

// Server is accepting connections and handling the details of the SOCKS5 protocol
type Server struct {
	// bind is the address to listen on
	Bind string

	Listener net.Listener

	// ProxyDial specifies the optional proxyDial function for
	// establishing the transport connection.
	ProxyDial statute.ProxyDialFunc
	// ProxyListenPacket specifies the optional proxyListenPacket function for
	// establishing the transport connection.
	ProxyListenPacket statute.ProxyListenPacket
	// PacketForwardAddress specifies the packet forwarding address
	PacketForwardAddress statute.PacketForwardAddress
	// UserConnectHandle gives the user control to handle the TCP CONNECT requests
	UserConnectHandle statute.UserConnectHandler
	// UserAssociateHandle gives the user control to handle the UDP ASSOCIATE requests
	UserAssociateHandle statute.UserAssociateHandler
	// Logger error log
	Logger *slog.Logger
	// Context is default context
	Context context.Context
	// BytesPool getting and returning temporary bytes for use by io.CopyBuffer
	BytesPool statute.BytesPool
}

func NewServer(options ...ServerOption) *Server {
	s := &Server{
		Bind:                 statute.DefaultBindAddress,
		ProxyDial:            statute.DefaultProxyDial(),
		ProxyListenPacket:    statute.DefaultProxyListenPacket(),
		PacketForwardAddress: defaultReplyPacketForwardAddress,
		Logger:               slog.Default(),
		Context:              statute.DefaultContext(),
	}

	for _, option := range options {
		option(s)
	}

	return s
}

type ServerOption func(*Server)

func (s *Server) ListenAndServe() error {
	// Create a new listener
	if s.Listener == nil {
		ln, err := net.Listen("tcp", s.Bind)
		if err != nil {
			return err // Return error if binding was unsuccessful
		}
		s.Listener = ln
	}

	s.Bind = s.Listener.Addr().(*net.TCPAddr).String()

	// ensure listener will be closed
	defer func() {
		_ = s.Listener.Close()
	}()

	// Create a cancelable context based on s.Context
	ctx, cancel := context.WithCancel(s.Context)
	defer cancel() // Ensure resources are cleaned up

	// watashi v1.2.6-ws1: a cancel closes the listener so a blocked Accept
	// wakes up, and a failing Accept backs off instead of spinning.
	go func() {
		<-ctx.Done()
		_ = s.Listener.Close()
	}()

	var delay time.Duration
	for {
		conn, err := s.Listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return ctx.Err()
			}
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else if delay < time.Second {
				delay *= 2
			}
			s.Logger.Error(err.Error())
			time.Sleep(delay)
			continue
		}
		delay = 0

		// Start a new goroutine to handle each connection
		// This way, the server can handle multiple connections concurrently
		go func() {
			defer conn.Close()
			err := s.ServeConn(conn)
			if err != nil {
				s.Logger.Error(err.Error()) // Log errors from ServeConn
			}
		}()
	}
}

func WithLogger(logger *slog.Logger) ServerOption {
	return func(s *Server) {
		s.Logger = logger
	}
}

func WithBind(bindAddress string) ServerOption {
	return func(s *Server) {
		s.Bind = bindAddress
	}
}

func WithConnectHandle(handler statute.UserConnectHandler) ServerOption {
	return func(s *Server) {
		s.UserConnectHandle = handler
	}
}

func WithAssociateHandle(handler statute.UserAssociateHandler) ServerOption {
	return func(s *Server) {
		s.UserAssociateHandle = handler
	}
}

func WithProxyDial(proxyDial statute.ProxyDialFunc) ServerOption {
	return func(s *Server) {
		s.ProxyDial = proxyDial
	}
}

func WithProxyListenPacket(proxyListenPacket statute.ProxyListenPacket) ServerOption {
	return func(s *Server) {
		s.ProxyListenPacket = proxyListenPacket
	}
}

func WithPacketForwardAddress(packetForwardAddress statute.PacketForwardAddress) ServerOption {
	return func(s *Server) {
		s.PacketForwardAddress = packetForwardAddress
	}
}

func WithContext(ctx context.Context) ServerOption {
	return func(s *Server) {
		s.Context = ctx
	}
}

func WithBytesPool(bytesPool statute.BytesPool) ServerOption {
	return func(s *Server) {
		s.BytesPool = bytesPool
	}
}

func (s *Server) ServeConn(conn net.Conn) error {
	version, err := readByte(conn)
	if err != nil {
		return err
	}
	if version != socks5Version {
		return fmt.Errorf("unsupported SOCKS version: %d", version)
	}

	req := &request{
		Version: socks5Version,
		Conn:    conn,
	}

	methods, err := readBytes(conn)
	if err != nil {
		return err
	}

	if bytes.IndexByte(methods, byte(noAuth)) != -1 {
		_, err := conn.Write([]byte{socks5Version, byte(noAuth)})
		if err != nil {
			return err
		}
	} else {
		_, err := conn.Write([]byte{socks5Version, byte(noAcceptable)})
		if err != nil {
			return err
		}
		return errNoSupportedAuth
	}

	var header [3]byte
	_, err = io.ReadFull(conn, header[:])
	if err != nil {
		return err
	}

	if header[0] != socks5Version {
		return fmt.Errorf("unsupported Command version: %d", header[0])
	}

	req.Command = Command(header[1])

	dest, err := readAddr(conn)
	if err != nil {
		if err == errUnrecognizedAddrType {
			err := sendReply(conn, addrTypeNotSupported, nil)
			if err != nil {
				return err
			}
		}
		return err
	}
	req.DestinationAddr = dest
	err = s.handle(req)
	if err != nil {
		return err
	}

	return nil
}

func (s *Server) handle(req *request) error {
	switch req.Command {
	case ConnectCommand:
		return s.handleConnect(req)
	case AssociateCommand:
		return s.handleAssociate(req)
	default:
		if err := sendReply(req.Conn, commandNotSupported, nil); err != nil {
			return err
		}
		return fmt.Errorf("unsupported Command: %v", req.Command)
	}
}

func (s *Server) handleConnect(req *request) error {
	if s.UserConnectHandle == nil {
		return s.embedHandleConnect(req)
	}

	if err := sendReply(req.Conn, successReply, nil); err != nil {
		return fmt.Errorf("failed to send reply: %v", err)
	}
	host := req.DestinationAddr.IP.String()
	if req.DestinationAddr.Name != "" {
		host = req.DestinationAddr.Name
	}

	proxyReq := &statute.ProxyRequest{
		Conn:        req.Conn,
		Reader:      io.Reader(req.Conn),
		Writer:      io.Writer(req.Conn),
		Network:     "tcp",
		Destination: req.DestinationAddr.String(),
		DestHost:    host,
		DestPort:    int32(req.DestinationAddr.Port),
	}

	return s.UserConnectHandle(proxyReq)
}

func (s *Server) embedHandleConnect(req *request) error {
	defer func() {
		_ = req.Conn.Close()
	}()

	target, err := s.ProxyDial(s.Context, "tcp", req.DestinationAddr.Address())
	if err != nil {
		if err := sendReply(req.Conn, errToReply(err), nil); err != nil {
			return fmt.Errorf("failed to send reply: %v", err)
		}
		return fmt.Errorf("connect to %v failed: %w", req.DestinationAddr, err)
	}
	defer func() {
		_ = target.Close()
	}()

	localAddr := target.LocalAddr()
	local, ok := localAddr.(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("connect to %v failed: local address is %s://%s", req.DestinationAddr, localAddr.Network(), localAddr.String())
	}
	bind := address{IP: local.IP, Port: local.Port}
	if err := sendReply(req.Conn, successReply, &bind); err != nil {
		return fmt.Errorf("failed to send reply: %v", err)
	}

	var buf1, buf2 []byte
	if s.BytesPool != nil {
		buf1 = s.BytesPool.Get()
		buf2 = s.BytesPool.Get()
		defer func() {
			s.BytesPool.Put(buf1)
			s.BytesPool.Put(buf2)
		}()
	} else {
		buf1 = make([]byte, 32*1024)
		buf2 = make([]byte, 32*1024)
	}
	return statute.Tunnel(s.Context, target, req.Conn, buf1, buf2)
}

// associateBind is the local address the relay socket of one association
// listens on.
// watashi v1.2.6-ws1: v1.2.6 bound the socket to DST.ADDR from the request.
// RFC 1928 says that field is where the client will send from, not an
// address of this server, so any client that filled it in got "bind: cannot
// assign requested address" (upstream issues #95, #206 and #265). The relay
// listens on the address the client already reached us on, any port.
func associateBind(conn net.Conn) string {
	if tcp, ok := conn.LocalAddr().(*net.TCPAddr); ok && tcp.IP != nil {
		return net.JoinHostPort(tcp.IP.String(), "0")
	}
	return ":0"
}

func (s *Server) handleAssociate(req *request) error {
	destinationAddr := req.DestinationAddr.String()
	udpConn, err := s.ProxyListenPacket(s.Context, "udp", associateBind(req.Conn))
	if err != nil {
		if err := sendReply(req.Conn, errToReply(err), nil); err != nil {
			return fmt.Errorf("failed to send reply: %v", err)
		}
		return fmt.Errorf("connect to %v failed: %w", req.DestinationAddr, err)
	}

	ip, port, err := s.PacketForwardAddress(s.Context, destinationAddr, udpConn, req.Conn)
	if err != nil {
		_ = udpConn.Close()
		return err
	}
	bind := address{IP: ip, Port: port}
	if err := sendReply(req.Conn, successReply, &bind); err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("failed to send reply: %v", err)
	}

	if s.UserAssociateHandle == nil {
		return s.embedHandleAssociate(req, udpConn)
	}

	cConn := newUDPCustomConn(udpConn, req.Conn)
	cConn.asyncReadPackets()

	// The TCP connection that asked for the association owns it (RFC 1928
	// section 7): when the client hangs up, the association is over.
	go func() {
		var b [1]byte
		for {
			if _, err := req.Conn.Read(b[:]); err != nil {
				_ = cConn.Close()
				return
			}
		}
	}()

	// wait for first packet so that target sender and receiver get known
	timer := time.NewTimer(AssociateFirstPacketTimeout)
	defer timer.Stop()
	select {
	case <-cConn.frc:
	case <-cConn.closed:
		return nil
	case <-timer.C:
		_ = cConn.Close()
		return nil
	case <-s.Context.Done():
		_ = cConn.Close()
		return s.Context.Err()
	}

	target := cConn.targetAddr
	host := target.Name
	if host == "" {
		host = target.IP.String()
	}
	proxyReq := &statute.ProxyRequest{
		Conn:        cConn,
		Reader:      cConn,
		Writer:      cConn,
		Network:     "udp",
		Destination: target.Address(),
		DestHost:    host,
		DestPort:    int32(target.Port),
	}

	return s.UserAssociateHandle(proxyReq)
}

func (s *Server) embedHandleAssociate(req *request, udpConn net.PacketConn) error {
	defer func() {
		_ = udpConn.Close()
	}()

	go func() {
		var buf [1]byte
		for {
			_, err := req.Conn.Read(buf[:])
			if err != nil {
				_ = udpConn.Close()
				break
			}
		}
	}()

	var (
		sourceAddr  net.Addr
		wantSource  string
		targetAddr  net.Addr
		wantTarget  string
		replyPrefix []byte
		buf         [maxUdpPacket]byte
	)

	for {
		n, addr, err := udpConn.ReadFrom(buf[:])
		if err != nil {
			return err
		}

		if sourceAddr == nil {
			sourceAddr = addr
			wantSource = sourceAddr.String()
		}

		gotAddr := addr.String()
		if wantSource == gotAddr {
			if n < 3 {
				continue
			}
			reader := bytes.NewBuffer(buf[3:n])
			addr, err := readAddr(reader)
			if err != nil {
				s.Logger.Debug(err.Error())
				continue
			}
			if targetAddr == nil {
				targetAddr = &net.UDPAddr{
					IP:   addr.IP,
					Port: addr.Port,
				}
				wantTarget = targetAddr.String()
			}
			if addr.String() != wantTarget {
				s.Logger.Debug("ignore non-target addresses", "address", addr)
				continue
			}
			_, err = udpConn.WriteTo(reader.Bytes(), targetAddr)
			if err != nil {
				return err
			}
		} else if targetAddr != nil && wantTarget == gotAddr {
			if replyPrefix == nil {
				b := bytes.NewBuffer(make([]byte, 3, 16))
				err = writeAddrWithStr(b, wantTarget)
				if err != nil {
					return err
				}
				replyPrefix = b.Bytes()
			}
			copy(buf[len(replyPrefix):len(replyPrefix)+n], buf[:n])
			copy(buf[:len(replyPrefix)], replyPrefix)
			_, err = udpConn.WriteTo(buf[:len(replyPrefix)+n], sourceAddr)
			if err != nil {
				return err
			}
		}
	}
}

func sendReply(w io.Writer, resp reply, addr *address) error {
	_, err := w.Write([]byte{socks5Version, byte(resp), 0})
	if err != nil {
		return err
	}
	err = writeAddr(w, addr)
	return err
}

type request struct {
	Version         uint8
	Command         Command
	DestinationAddr *address
	Username        string
	Password        string
	Conn            net.Conn
}

func defaultReplyPacketForwardAddress(_ context.Context, destinationAddr string, packet net.PacketConn, conn net.Conn) (net.IP, int, error) {
	udpLocal := packet.LocalAddr()
	udpLocalAddr, ok := udpLocal.(*net.UDPAddr)
	if !ok {
		return nil, 0, fmt.Errorf("connect to %v failed: local address is %s://%s", destinationAddr, udpLocal.Network(), udpLocal.String())
	}

	tcpLocal := conn.LocalAddr()
	tcpLocalAddr, ok := tcpLocal.(*net.TCPAddr)
	if !ok {
		return nil, 0, fmt.Errorf("connect to %v failed: local address is %s://%s", destinationAddr, tcpLocal.Network(), tcpLocal.String())
	}
	return tcpLocalAddr.IP, udpLocalAddr.Port, nil
}
