package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// iperf3 control protocol states (iperf_api.h). The state byte is signed: -1 and -2 arrive as 0xff and 0xfe.
const (
	iperfTestStart       = 1
	iperfTestRunning     = 2
	iperfTestEnd         = 4
	iperfParamExchange   = 9
	iperfCreateStreams   = 10
	iperfServerTerminate = 11
	iperfClientTerminate = 12
	iperfExchangeResults = 13
	iperfDisplayResults  = 14
	iperfDone            = 16
	iperfAccessDenied    = -1
	iperfServerError     = -2

	iperfCookieSize  = 37
	iperfBlockSize   = 128 * 1024
	iperfMaxJSONSize = 1 << 20
	iperfDefaultPort = "5201"
)

const cookieAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// iperfOptions reads reverse=true (download, iperf3 -R), rate=1M (target bitrate, iperf3 -b) and ip=4|6 (iperf3 -4/-6).
func iperfOptions(q url.Values, o *common) error {
	if s := q.Get("reverse"); s != "" {
		v, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("reverse %q must be true or false", s)
		}
		o.reverse = v
	}
	if s := q.Get("rate"); s != "" {
		v, err := parseRate(s)
		if err != nil {
			return err
		}
		o.rate = v
	}
	switch s := q.Get("ip"); s {
	case "":
	case "4":
		o.network = "tcp4"
	case "6":
		o.network = "tcp6"
	default:
		return fmt.Errorf("ip %q must be 4 or 6", s)
	}
	return nil
}

// parseRate reads a bitrate like iperf3 -b: a number of bit/s with an optional K, M or G (powers of 1000).
func parseRate(s string) (float64, error) {
	t := strings.TrimSpace(s)
	mult := 1.0
	if n := len(t); n > 0 {
		switch t[n-1] {
		case 'k', 'K':
			mult, t = 1e3, t[:n-1]
		case 'm', 'M':
			mult, t = 1e6, t[:n-1]
		case 'g', 'G':
			mult, t = 1e9, t[:n-1]
		}
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v <= 0 || v*mult < 1e3 {
		return 0, fmt.Errorf("rate %q is not a bitrate of at least 1K, e.g. 500K, 1M or 1G", s)
	}
	return v * mult, nil
}

// FormatRate shows a bitrate in the largest unit, e.g. 1 Mbit/s.
func FormatRate(bps float64) string {
	for _, u := range []struct {
		div  float64
		name string
	}{{1e9, "Gbit/s"}, {1e6, "Mbit/s"}, {1e3, "Kbit/s"}} {
		if bps >= u.div {
			return strconv.FormatFloat(roundTo(bps/u.div, 2), 'f', -1, 64) + " " + u.name
		}
	}
	return strconv.FormatFloat(bps, 'f', -1, 64) + " bit/s"
}

// probeIPerf3 tries the ports in order until a server accepts the handshake. A busy server (another test
// is running) counts as reachable with STATE=busy. With opts.Bandwidth a short TCP test follows the handshake.
func probeIPerf3(ctx context.Context, u *url.URL, opts Options) (Values, error) {
	ports, err := iperfPorts(u)
	if err != nil {
		return nil, err
	}
	o, err := options(u)
	if err != nil {
		return nil, err
	}
	var lastErr error
	var busy Values
	for _, port := range ports {
		addr := net.JoinHostPort(u.Hostname(), port)
		start := time.Now()
		var d net.Dialer
		conn, err := d.DialContext(ctx, o.network, addr)
		if err != nil {
			lastErr = err
			continue
		}
		values := Values{"CONNECTED": true, "RESPONSE_TIME": ms(time.Since(start)), "PORT": port}
		state, cookie, err := iperfHello(ctx, conn)
		if err != nil {
			conn.Close()
			if busy == nil {
				busy = values
				busy["STATE"] = "error"
			}
			lastErr = err
			continue
		}
		switch state {
		case iperfParamExchange:
			values["STATE"] = "ready"
			if !opts.Bandwidth || o.bandwidth == 0 {
				_, _ = conn.Write([]byte{iperfClientTerminate})
				conn.Close()
				return values, nil
			}
			mbit, err := iperfTest(ctx, conn, addr, cookie, o)
			conn.Close()
			if err != nil {
				return values, fmt.Errorf("bandwidth test on port %s: %w", port, err)
			}
			values["BANDWIDTH"] = mbit
			return values, nil
		case iperfAccessDenied:
			values["STATE"] = "busy"
			busy, lastErr = values, nil
		case iperfServerError:
			values["STATE"] = "error"
			if busy == nil {
				busy = values
			}
			lastErr = errors.New("the server reported an error")
		default:
			values["STATE"] = "error"
			if busy == nil {
				busy = values
			}
			lastErr = fmt.Errorf("unexpected iperf3 state %d", state)
		}
		conn.Close()
	}
	if busy != nil {
		if busy["STATE"] == "busy" {
			return busy, nil
		}
		return busy, lastErr
	}
	return Values{"CONNECTED": false}, lastErr
}

// iperfPorts returns the port of the URL (default 5201) followed by the other ports of ?ports=5201-5210.
func iperfPorts(u *url.URL) ([]string, error) {
	first := u.Port()
	if first == "" {
		first = iperfDefaultPort
	}
	ports := []string{first}
	seen := map[string]bool{first: true}
	list := u.Query().Get("ports")
	for _, part := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := parsePort(lo)
		if err != nil {
			return nil, err
		}
		b := a
		if isRange {
			if b, err = parsePort(hi); err != nil {
				return nil, err
			}
		}
		if b < a || b-a > 100 {
			return nil, fmt.Errorf("port range %q is invalid or longer than 100 ports", part)
		}
		for p := a; p <= b; p++ {
			s := strconv.Itoa(p)
			if !seen[s] {
				seen[s] = true
				ports = append(ports, s)
			}
		}
	}
	return ports, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return p, nil
}

func iperfHello(ctx context.Context, conn net.Conn) (int8, []byte, error) {
	setDeadline(ctx, conn)
	cookie := make([]byte, iperfCookieSize)
	random := make([]byte, iperfCookieSize-1)
	if _, err := rand.Read(random); err != nil {
		return 0, nil, err
	}
	for i, b := range random {
		cookie[i] = cookieAlphabet[int(b)%len(cookieAlphabet)]
	}
	if _, err := conn.Write(cookie); err != nil {
		return 0, nil, fmt.Errorf("sending the cookie: %w", err)
	}
	state, err := readState(conn)
	if err != nil {
		return 0, nil, fmt.Errorf("reading the server state: %w", err)
	}
	return state, cookie, nil
}

func setDeadline(ctx context.Context, conn net.Conn) {
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
}

func readState(r io.Reader) (int8, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return int8(b[0]), nil
}

func writeJSON(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf, uint32(len(data)))
	copy(buf[4:], data)
	_, err = w.Write(buf)
	return err
}

func readJSON(r io.Reader, v any) error {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n > iperfMaxJSONSize {
		return fmt.Errorf("JSON message of %d bytes is too large", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

type iperfStream struct {
	ID          int     `json:"id"`
	Bytes       int64   `json:"bytes"`
	Retransmits int     `json:"retransmits"`
	Jitter      float64 `json:"jitter"`
	Errors      int     `json:"errors"`
	Packets     int     `json:"packets"`
	StartTime   float64 `json:"start_time"`
	EndTime     float64 `json:"end_time"`
}

type iperfResults struct {
	CPUUtilTotal         float64       `json:"cpu_util_total"`
	CPUUtilUser          float64       `json:"cpu_util_user"`
	CPUUtilSystem        float64       `json:"cpu_util_system"`
	SenderHasRetransmits int           `json:"sender_has_retransmits"`
	Streams              []iperfStream `json:"streams"`
}

// iperfBlock is the block length: 128 KiB, or about 10ms of data when the rate is limited so the pacing stays smooth.
func iperfBlock(rate float64) int {
	if rate <= 0 {
		return iperfBlockSize
	}
	return int(min(max(rate/8/100, 1024), iperfBlockSize))
}

// iperfTest runs a single-stream TCP test after the handshake and returns the throughput in Mbit/s.
// The receiver's byte count is used: the server's when sending, our own in reverse mode.
func iperfTest(ctx context.Context, ctrl net.Conn, addr string, cookie []byte, o common) (float64, error) {
	seconds := int(o.bandwidth.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	block := iperfBlock(o.rate)
	params := map[string]any{
		"tcp": true, "omit": 0, "time": seconds, "num": 0, "blockcount": 0, "parallel": 1,
		"len": block, "pacing_timer": 1000, "client_version": "3.17",
	}
	if o.reverse {
		params["reverse"] = true
	}
	if o.rate > 0 {
		// The sender paces to this rate: the server in reverse mode, transfer otherwise.
		params["bandwidth"] = int64(o.rate)
	}
	reverse := o.reverse
	if err := writeJSON(ctrl, params); err != nil {
		return 0, fmt.Errorf("sending the parameters: %w", err)
	}
	var stream net.Conn
	defer func() {
		if stream != nil {
			stream.Close()
		}
	}()
	var local int64
	var elapsed time.Duration
	var server iperfResults
	for {
		state, err := readState(ctrl)
		if err != nil {
			return 0, fmt.Errorf("reading the server state: %w", err)
		}
		switch state {
		case iperfCreateStreams:
			var d net.Dialer
			if stream, err = d.DialContext(ctx, o.network, addr); err != nil {
				return 0, fmt.Errorf("opening the data stream: %w", err)
			}
			setDeadline(ctx, stream)
			if _, err := stream.Write(cookie); err != nil {
				return 0, fmt.Errorf("opening the data stream: %w", err)
			}
		case iperfTestStart:
		case iperfTestRunning:
			if stream == nil {
				return 0, errors.New("the server started the test without a data stream")
			}
			local, elapsed, err = transfer(ctx, stream, time.Duration(seconds)*time.Second, reverse, o.rate, block)
			if err != nil {
				return 0, err
			}
			if _, err := ctrl.Write([]byte{iperfTestEnd}); err != nil {
				return 0, err
			}
		case iperfExchangeResults:
			ours := iperfResults{SenderHasRetransmits: 0, Streams: []iperfStream{{
				ID: 1, Bytes: local, Retransmits: -1, EndTime: elapsed.Seconds(),
			}}}
			if err := writeJSON(ctrl, ours); err != nil {
				return 0, fmt.Errorf("sending the results: %w", err)
			}
			if err := readJSON(ctrl, &server); err != nil {
				return 0, fmt.Errorf("reading the server results: %w", err)
			}
		case iperfDisplayResults:
			_, _ = ctrl.Write([]byte{iperfDone})
			bytes, secs := local, elapsed.Seconds()
			if !reverse && len(server.Streams) > 0 && server.Streams[0].Bytes > 0 {
				bytes = server.Streams[0].Bytes
				if s := server.Streams[0].EndTime - server.Streams[0].StartTime; s > 0 {
					secs = s
				}
			}
			if secs <= 0 {
				return 0, errors.New("the test did not run")
			}
			return roundTo(float64(bytes)*8/secs/1e6, 2), nil
		case iperfAccessDenied:
			return 0, errors.New("the server is busy")
		case iperfServerError:
			return 0, errors.New("the server reported an error")
		case iperfServerTerminate:
			return 0, errors.New("the server ended the test")
		default:
			return 0, fmt.Errorf("unexpected iperf3 state %d", state)
		}
	}
}

// transfer sends (or in reverse mode receives) data for the test duration. When sending, rate (bit/s, 0 = unlimited)
// caps the average throughput.
func transfer(ctx context.Context, stream net.Conn, d time.Duration, reverse bool, rate float64, block int) (int64, time.Duration, error) {
	start := time.Now()
	end := start.Add(d)
	if dl, ok := ctx.Deadline(); ok && dl.Before(end) {
		end = dl
	}
	_ = stream.SetDeadline(end)
	buf := make([]byte, block)
	var total int64
	for {
		if !reverse && rate > 0 && total > 0 {
			due := start.Add(time.Duration(float64(total) * 8 / rate * float64(time.Second)))
			if !due.Before(end) {
				break
			}
			if wait := time.Until(due); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return total, time.Since(start), ctx.Err()
				case <-timer.C:
				}
			}
		}
		var n int
		var err error
		if reverse {
			n, err = stream.Read(buf)
		} else {
			n, err = stream.Write(buf)
		}
		total += int64(n)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, io.EOF) {
				break
			}
			return total, time.Since(start), fmt.Errorf("data stream: %w", err)
		}
	}
	elapsed := time.Since(start)
	if dl, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(dl)
	}
	return total, elapsed, nil
}

func roundTo(f float64, decimals int) float64 {
	p := 1.0
	for i := 0; i < decimals; i++ {
		p *= 10
	}
	return float64(int64(f*p+0.5)) / p
}
