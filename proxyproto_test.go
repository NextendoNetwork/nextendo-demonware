package main

import (
	"io"
	"net"
	"testing"
	"time"
)

func acceptOne(t *testing.T, send string) (net.Conn, []byte) {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	ln := newProxyListener(inner)

	go func() {
		c, err := net.Dial("tcp", inner.Addr().String())
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte(send))
		time.Sleep(200 * time.Millisecond)
	}()

	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	return c, buf
}

func TestProxyHeaderSetsRemoteAddr(t *testing.T) {
	c, rest := acceptOne(t, "PROXY TCP4 192.0.2.10 192.0.2.1 51000 443\r\nhello")
	defer c.Close()
	addr, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok || addr.IP.String() != "192.0.2.10" || addr.Port != 51000 {
		t.Fatalf("RemoteAddr = %v", c.RemoteAddr())
	}
	if string(rest) != "hello" {
		t.Fatalf("payload after header = %q", rest)
	}
}

func TestNoProxyHeaderPassesThrough(t *testing.T) {
	c, rest := acceptOne(t, "\x16\x03\x01hello")
	defer c.Close()
	if addr, ok := c.RemoteAddr().(*net.TCPAddr); !ok || !addr.IP.IsLoopback() {
		t.Fatalf("RemoteAddr = %v", c.RemoteAddr())
	}
	if string(rest) != "\x16\x03\x01he" {
		t.Fatalf("payload = %q", rest)
	}
}
