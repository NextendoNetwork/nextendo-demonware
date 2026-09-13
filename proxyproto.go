package main

// PROXY protocol v1 on the auth port, as nextendo-nex does for the NEX auth
// servers (nextendo-nex proxyproto.go): sni-router passes TLS through untouched,
// so without it the auth sees the router (127.0.0.1) instead of the player. With
// SNI_PROXY_PROTOCOL=1 the router prepends, before the ClientHello,
//
//	PROXY TCP4 <srcIP> <dstIP> <srcPort> <dstPort>\r\n
//
// and NEXTENDO_PROXY_PROTOCOL=1 here reads it. Connections without the header
// pass through unchanged. The header is read on a per-connection goroutine with
// a deadline, so a client that sends nothing cannot stall the accept loop.

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"time"
)

const proxyHeaderTimeout = 10 * time.Second

// proxyConn overrides RemoteAddr with the client address from the PROXY header.
type proxyConn struct {
	net.Conn
	reader     *bufio.Reader
	remoteAddr net.Addr
}

func (c *proxyConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

func (c *proxyConn) RemoteAddr() net.Addr {
	if c.remoteAddr != nil {
		return c.remoteAddr
	}
	return c.Conn.RemoteAddr()
}

type proxyListener struct {
	net.Listener
	conns chan net.Conn
	errc  chan error
}

func newProxyListener(ln net.Listener) *proxyListener {
	l := &proxyListener{Listener: ln, conns: make(chan net.Conn), errc: make(chan error, 1)}
	go l.acceptLoop()
	return l
}

func (l *proxyListener) acceptLoop() {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			l.errc <- err
			return
		}
		go func() {
			pc, err := readProxyHeader(c)
			if err != nil {
				c.Close()
				return
			}
			l.conns <- pc
		}()
	}
}

func (l *proxyListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case err := <-l.errc:
		return nil, err
	}
}

// readProxyHeader consumes a PROXY v1 line if the connection starts with one.
func readProxyHeader(c net.Conn) (*proxyConn, error) {
	pc := &proxyConn{Conn: c, reader: bufio.NewReader(c)}
	_ = c.SetReadDeadline(time.Now().Add(proxyHeaderTimeout))
	defer c.SetReadDeadline(time.Time{})

	head, err := pc.reader.Peek(6)
	if err != nil {
		return nil, err
	}
	if string(head) != "PROXY " {
		return pc, nil
	}
	// ReadSlice is bounded by the reader's buffer (a v1 line is at most 107 bytes).
	line, err := pc.reader.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	f := strings.Fields(strings.TrimSpace(string(line)))
	// f = [PROXY, TCP4|TCP6|UNKNOWN, srcIP, dstIP, srcPort, dstPort]
	if len(f) >= 6 && (f[1] == "TCP4" || f[1] == "TCP6") {
		port, _ := strconv.Atoi(f[4])
		if ip := net.ParseIP(f[2]); ip != nil {
			pc.remoteAddr = &net.TCPAddr{IP: ip, Port: port}
		}
	}
	return pc, nil
}
