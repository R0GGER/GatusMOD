package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// probeSMTP reads the banner and sends EHLO. smtps:// uses implicit TLS (port 465), ?starttls=true requires STARTTLS.
// CODE is the banner code (220 when the server accepts mail). ?insecure=true skips certificate verification.
func probeSMTP(ctx context.Context, u *url.URL, _ Options) (Values, error) {
	q := u.Query()
	implicitTLS := u.Scheme == "smtps"
	startTLS, _ := strconv.ParseBool(q.Get("starttls"))
	insecure, _ := strconv.ParseBool(q.Get("insecure"))
	ehlo := q.Get("ehlo")
	if ehlo == "" {
		ehlo = "gatusmod"
	}
	port := u.Port()
	if port == "" {
		port = "25"
		if implicitTLS {
			port = "465"
		}
	}
	tlsConfig := &tls.Config{ServerName: u.Hostname(), InsecureSkipVerify: insecure}

	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return Values{"CONNECTED": false}, err
	}
	defer conn.Close()
	setDeadline(ctx, conn)
	values := Values{"CONNECTED": true}
	if implicitTLS {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			return values, fmt.Errorf("TLS: %w", err)
		}
		setCertificateExpiration(values, tc)
		conn = tc
	}
	tp := textproto.NewConn(conn)
	code, _, err := tp.ReadResponse(0)
	values["RESPONSE_TIME"] = ms(time.Since(start))
	if err != nil {
		return values, fmt.Errorf("reading the banner: %w", err)
	}
	values["CODE"] = code
	if code != 220 {
		_, _ = tp.Cmd("QUIT")
		return values, nil
	}
	extensions, err := smtpEHLO(tp, ehlo)
	if err != nil {
		return values, err
	}
	if startTLS {
		if !extensions["STARTTLS"] {
			return values, errors.New("the server does not offer STARTTLS")
		}
		if _, _, err := smtpCmd(tp, 220, "STARTTLS"); err != nil {
			return values, err
		}
		tc := tls.Client(conn, tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			return values, fmt.Errorf("STARTTLS: %w", err)
		}
		setCertificateExpiration(values, tc)
		tp = textproto.NewConn(tc)
		if _, err := smtpEHLO(tp, ehlo); err != nil {
			return values, err
		}
	}
	_, _ = tp.Cmd("QUIT")
	return values, nil
}

func smtpCmd(tp *textproto.Conn, expect int, format string, args ...any) (int, string, error) {
	id, err := tp.Cmd(format, args...)
	if err != nil {
		return 0, "", err
	}
	tp.StartResponse(id)
	defer tp.EndResponse(id)
	code, msg, err := tp.ReadResponse(expect)
	if err != nil {
		return code, msg, fmt.Errorf("%s: %w", strings.Fields(format)[0], err)
	}
	return code, msg, nil
}

func smtpEHLO(tp *textproto.Conn, name string) (map[string]bool, error) {
	_, msg, err := smtpCmd(tp, 250, "EHLO %s", name)
	if err != nil {
		return nil, err
	}
	ext := map[string]bool{}
	lines := strings.Split(msg, "\n")
	for _, line := range lines[1:] {
		if f := strings.Fields(line); len(f) > 0 {
			ext[strings.ToUpper(f[0])] = true
		}
	}
	return ext, nil
}

func setCertificateExpiration(values Values, tc *tls.Conn) {
	if certs := tc.ConnectionState().PeerCertificates; len(certs) > 0 {
		values["CERTIFICATE_EXPIRATION"] = ms(time.Until(certs[0].NotAfter))
	}
}
