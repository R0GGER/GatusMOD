package probe

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeIPerf3 is a minimal iperf3 server: it answers the handshake, or ACCESS_DENIED when busy, and runs a
// forward TCP test. It returns the port.
func fakeIPerf3(t *testing.T, busy bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			serveIPerf3(ln, conn, busy)
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

func serveIPerf3(ln net.Listener, conn net.Conn, busy bool) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	cookie := make([]byte, iperfCookieSize)
	if _, err := io.ReadFull(conn, cookie); err != nil {
		return
	}
	if busy {
		_, _ = conn.Write([]byte{0xff})
		return
	}
	_, _ = conn.Write([]byte{iperfParamExchange})
	r := bufio.NewReader(conn)
	if b, err := r.Peek(1); err != nil || b[0] == iperfClientTerminate {
		return
	}
	var params map[string]any
	if err := readJSON(r, &params); err != nil {
		return
	}
	_, _ = conn.Write([]byte{iperfCreateStreams})
	stream, err := ln.Accept()
	if err != nil {
		return
	}
	defer stream.Close()
	if _, err := io.ReadFull(stream, make([]byte, iperfCookieSize)); err != nil {
		return
	}
	var received atomic.Int64
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := stream.Read(buf)
			received.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	start := time.Now()
	_, _ = conn.Write([]byte{iperfTestStart, iperfTestRunning})
	if s, err := readState(r); err != nil || s != iperfTestEnd {
		return
	}
	_, _ = conn.Write([]byte{iperfExchangeResults})
	var client iperfResults
	if err := readJSON(r, &client); err != nil {
		return
	}
	_ = writeJSON(conn, iperfResults{Streams: []iperfStream{{ID: 1, Bytes: received.Load(), EndTime: time.Since(start).Seconds()}}})
	_, _ = conn.Write([]byte{iperfDisplayResults})
	_, _ = readState(r)
}

// fakeNTP answers every request as a stratum 2 server with the current time.
func fakeNTP(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 128)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 48 {
				continue
			}
			resp := make([]byte, 48)
			resp[0] = 4<<3 | 4 // version 4, server mode
			resp[1] = 2
			resp[2] = 6
			resp[3] = 0xec
			copy(resp[12:16], "TEST")
			now := ntpTime(time.Now())
			binary.BigEndian.PutUint64(resp[16:], now)
			copy(resp[24:32], buf[40:48])
			binary.BigEndian.PutUint64(resp[32:], now)
			binary.BigEndian.PutUint64(resp[40:], now)
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	_, port, _ := net.SplitHostPort(pc.LocalAddr().String())
	return port
}

func ntpTime(t time.Time) uint64 {
	secs := uint64(t.Unix() + 2208988800)
	frac := uint64(t.Nanosecond()) << 32 / 1e9
	return secs<<32 | frac
}

// fakeSMTP greets with banner (e.g. "220 fake ESMTP") and answers EHLO and QUIT.
func fakeSMTP(t *testing.T, banner string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.WriteString(conn, banner+"\r\n")
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch cmd := strings.ToUpper(strings.Fields(line + " x")[0]); cmd {
					case "EHLO":
						_, _ = io.WriteString(conn, "250-fake.example.org\r\n250-SIZE 1000\r\n250 8BITMIME\r\n")
					case "QUIT":
						_, _ = io.WriteString(conn, "221 bye\r\n")
						return
					default:
						_, _ = io.WriteString(conn, "502 not implemented\r\n")
					}
				}
			}(conn)
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	return port
}
