package socks5

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	errStringTooLong        = errors.New("string too long")
	errNoSupportedAuth      = errors.New("no supported authentication mechanism")
	errUnrecognizedAddrType = errors.New("unrecognized address type")
)

const (
	maxUdpPacket = 2048
)

const (
	socks5Version = 0x05
)

const (
	ConnectCommand   Command = 0x01
	AssociateCommand Command = 0x03
)

// Command is a SOCKS Command.
type Command byte

func (cmd Command) String() string {
	switch cmd {
	case ConnectCommand:
		return "socks connect"
	case AssociateCommand:
		return "socks associate"
	default:
		return "socks " + strconv.Itoa(int(cmd))
	}
}

const (
	successReply         reply = 0x00
	serverFailure        reply = 0x01
	ruleFailure          reply = 0x02
	networkUnreachable   reply = 0x03
	hostUnreachable      reply = 0x04
	connectionRefused    reply = 0x05
	ttlExpired           reply = 0x06
	commandNotSupported  reply = 0x07
	addrTypeNotSupported reply = 0x08
)

func errToReply(err error) reply {
	if err == nil {
		return successReply
	}
	msg := err.Error()
	resp := hostUnreachable
	if strings.Contains(msg, "refused") {
		resp = connectionRefused
	} else if strings.Contains(msg, "network is unreachable") {
		resp = networkUnreachable
	}
	return resp
}

// reply is a SOCKS Command reply code.
type reply byte

func (code reply) String() string {
	switch code {
	case successReply:
		return "succeeded"
	case serverFailure:
		return "general SOCKS server failure"
	case ruleFailure:
		return "connection not allowed by ruleset"
	case networkUnreachable:
		return "network unreachable"
	case hostUnreachable:
		return "host unreachable"
	case connectionRefused:
		return "connection refused"
	case ttlExpired:
		return "TTL expired"
	case commandNotSupported:
		return "Command not supported"
	case addrTypeNotSupported:
		return "address type not supported"
	default:
		return "unknown code: " + strconv.Itoa(int(code))
	}
}

const (
	ipv4Address = 0x01
	fqdnAddress = 0x03
	ipv6Address = 0x04
)

// address is a SOCKS-specific address.
// Either Name or IP is used exclusively.
type address struct {
	Name string // fully-qualified domain name
	IP   net.IP
	Port int
}

func (a *address) Network() string { return "socks5" }

func (a *address) String() string {
	if a == nil {
		return " "
	}
	return a.Address()
}

// Address returns a string suitable to dial; prefer returning IP-based
// address, fallback to Name
func (a address) Address() string {
	port := strconv.Itoa(a.Port)
	if len(a.IP) != 0 {
		return net.JoinHostPort(a.IP.String(), port)
	}
	return net.JoinHostPort(a.Name, port)
}

// authMethod is a SOCKS authentication method.
type authMethod byte

const (
	noAuth       authMethod = 0x00 // no authentication required
	noAcceptable authMethod = 0xff // no acceptable authentication methods
)

func readBytes(r io.Reader) ([]byte, error) {
	var buf [1]byte
	_, err := r.Read(buf[:])
	if err != nil {
		return nil, err
	}
	bytes := make([]byte, buf[0])
	_, err = io.ReadFull(r, bytes)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func readByte(r io.Reader) (byte, error) {
	var buf [1]byte
	_, err := r.Read(buf[:])
	if err != nil {
		return 0, err
	}
	return buf[0], nil
}

func readAddr(r io.Reader) (*address, error) {
	address := &address{}

	var addrType [1]byte
	if _, err := r.Read(addrType[:]); err != nil {
		return nil, err
	}

	switch addrType[0] {
	case ipv4Address:
		addr := make(net.IP, net.IPv4len)
		if _, err := io.ReadFull(r, addr); err != nil {
			return nil, err
		}
		address.IP = addr
	case ipv6Address:
		addr := make(net.IP, net.IPv6len)
		if _, err := io.ReadFull(r, addr); err != nil {
			return nil, err
		}
		address.IP = addr
	case fqdnAddress:
		if _, err := r.Read(addrType[:]); err != nil {
			return nil, err
		}
		addrLen := int(addrType[0])
		fqdn := make([]byte, addrLen)
		if _, err := io.ReadFull(r, fqdn); err != nil {
			return nil, err
		}
		address.Name = string(fqdn)
	default:
		return nil, errUnrecognizedAddrType
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return nil, err
	}
	address.Port = int(binary.BigEndian.Uint16(port[:]))
	return address, nil
}

func writeAddr(w io.Writer, addr *address) error {
	if addr == nil {
		_, err := w.Write([]byte{ipv4Address, 0, 0, 0, 0, 0, 0})
		if err != nil {
			return err
		}
		return nil
	}
	if addr.IP != nil {
		if ip4 := addr.IP.To4(); ip4 != nil {
			_, err := w.Write([]byte{ipv4Address})
			if err != nil {
				return err
			}
			_, err = w.Write(ip4)
			if err != nil {
				return err
			}
		} else if ip6 := addr.IP.To16(); ip6 != nil {
			_, err := w.Write([]byte{ipv6Address})
			if err != nil {
				return err
			}
			_, err = w.Write(ip6)
			if err != nil {
				return err
			}
		} else {
			_, err := w.Write([]byte{ipv4Address, 0, 0, 0, 0})
			if err != nil {
				return err
			}
		}
	} else if addr.Name != "" {
		if len(addr.Name) > 255 {
			return errStringTooLong
		}
		_, err := w.Write([]byte{fqdnAddress, byte(len(addr.Name))})
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(addr.Name))
		if err != nil {
			return err
		}
	} else {
		_, err := w.Write([]byte{ipv4Address, 0, 0, 0, 0})
		if err != nil {
			return err
		}
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(addr.Port))
	_, err := w.Write(p[:])
	return err
}

func writeAddrWithStr(w io.Writer, addr string) error {
	host, port, err := splitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil {
		return writeAddr(w, &address{IP: ip, Port: port})
	}
	return writeAddr(w, &address{Name: host, Port: port})
}

func splitHostPort(address string) (string, int, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	portnum, err := strconv.Atoi(port)
	if err != nil {
		return "", 0, err
	}
	if 0 > portnum || portnum > 0xffff {
		return "", 0, errors.New("port number out of range " + port)
	}
	return host, portnum, nil
}

type readStruct struct {
	data []byte
	err  error
}

// udpCustomConn is one UDP association seen as a connection to one target.
//
// watashi v1.2.6-ws1, what changed and why:
//   - every packet is copied out of the read buffer. v1.2.6 handed out a
//     slice of the one buffer the next read overwrote, so under load a packet
//     could be corrupted before it was forwarded.
//   - Read honours SetReadDeadline. v1.2.6 forwarded the deadline to the
//     socket underneath, which the reader goroutine owns, so Read itself
//     could block forever on its channel.
//   - Close wakes everything. v1.2.6 left the reader goroutine blocked on an
//     unbuffered send when nobody read any more, one goroutine per
//     association for the life of the process.
//   - the target keeps its name. v1.2.6 turned a domain target into an
//     address with no IP, so "example.com:443" was dialled as ":443".
//   - a packet for another target, a fragment or a malformed header is
//     dropped instead of ending the whole association.
type udpCustomConn struct {
	net.PacketConn
	assocTCPConn net.Conn
	lock         sync.Mutex
	sourceAddr   net.Addr
	targetAddr   *address
	replyPrefix  []byte
	firstRead    sync.Once
	frc          chan struct{}
	packetQueue  chan *readStruct
	closed       chan struct{}
	closeOnce    sync.Once

	dlMu         sync.Mutex
	readDeadline time.Time
	dlChanged    chan struct{}
}

func newUDPCustomConn(pc net.PacketConn, assoc net.Conn) *udpCustomConn {
	return &udpCustomConn{
		PacketConn:   pc,
		assocTCPConn: assoc,
		frc:          make(chan struct{}),
		packetQueue:  make(chan *readStruct),
		closed:       make(chan struct{}),
		dlChanged:    make(chan struct{}),
	}
}

func (cc *udpCustomConn) RemoteAddr() net.Addr {
	if cc.targetAddr == nil {
		return nil
	}
	return cc.targetAddr
}

// push hands one packet to Read, or gives up when the association is closed.
func (cc *udpCustomConn) push(r *readStruct) bool {
	select {
	case cc.packetQueue <- r:
		return true
	case <-cc.closed:
		return false
	}
}

func (cc *udpCustomConn) asyncReadPackets() {
	go func() {
		tempBuf := make([]byte, maxUdpPacket)
		for {
			n, addr, err := cc.PacketConn.ReadFrom(tempBuf)
			if err != nil {
				cc.push(&readStruct{err: err})
				return
			}
			// RSV(2) FRAG(1) ATYP DST.ADDR DST.PORT DATA - fragments are not supported
			if n < 3 || tempBuf[2] != 0 {
				continue
			}
			if cc.sourceAddr == nil {
				cc.sourceAddr = addr
			} else if addr.String() != cc.sourceAddr.String() {
				// only the client that opened the association may use it
				continue
			}

			reader := bytes.NewBuffer(tempBuf[3:n])
			target, err := readAddr(reader)
			if err != nil {
				continue
			}
			if cc.targetAddr == nil {
				cc.targetAddr = target
			} else if target.String() != cc.targetAddr.String() {
				continue
			}

			data := append([]byte(nil), reader.Bytes()...)
			cc.firstRead.Do(func() {
				// source and destination are known now, the user handler may run
				close(cc.frc)
			})
			if !cc.push(&readStruct{data: data}) {
				return
			}
		}
	}()
}

func (cc *udpCustomConn) Read(b []byte) (int, error) {
	for {
		cc.dlMu.Lock()
		d, changed := cc.readDeadline, cc.dlChanged
		cc.dlMu.Unlock()

		var expire <-chan time.Time
		var timer *time.Timer
		if !d.IsZero() {
			wait := time.Until(d)
			if wait <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(wait)
			expire = timer.C
		}
		stop := func() {
			if timer != nil {
				timer.Stop()
			}
		}

		select {
		case r := <-cc.packetQueue:
			stop()
			if r.err != nil {
				return 0, r.err
			}
			return copy(b, r.data), nil
		case <-expire:
			return 0, os.ErrDeadlineExceeded
		case <-changed:
			stop()
			continue
		case <-cc.closed:
			stop()
			return 0, net.ErrClosed
		}
	}
}

func (cc *udpCustomConn) SetReadDeadline(t time.Time) error {
	cc.dlMu.Lock()
	cc.readDeadline = t
	close(cc.dlChanged)
	cc.dlChanged = make(chan struct{})
	cc.dlMu.Unlock()
	return nil
}

func (cc *udpCustomConn) SetDeadline(t time.Time) error {
	_ = cc.SetReadDeadline(t)
	return cc.PacketConn.SetWriteDeadline(t)
}

func (cc *udpCustomConn) Write(b []byte) (int, error) {
	cc.lock.Lock()
	defer cc.lock.Unlock()
	if cc.replyPrefix == nil {
		prefix := bytes.NewBuffer(make([]byte, 3, 16))
		err := writeAddrWithStr(prefix, cc.targetAddr.String())
		if err != nil {
			return 0, err
		}
		cc.replyPrefix = prefix.Bytes()
	}
	buff := make([]byte, 0, len(cc.replyPrefix)+len(b))
	buff = append(buff, cc.replyPrefix...)
	buff = append(buff, b...)
	_, err := cc.WriteTo(buff, cc.sourceAddr)
	return len(b), err
}

func (cc *udpCustomConn) Close() error {
	var udpErr, tcpErr error
	cc.closeOnce.Do(func() {
		close(cc.closed)
		udpErr = cc.PacketConn.Close()
		tcpErr = cc.assocTCPConn.Close()
	})
	if udpErr != nil {
		return udpErr
	}
	return tcpErr
}
