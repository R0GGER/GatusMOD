package probe

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

const defaultOID = "1.3.6.1.2.1.1.3.0" // sysUpTime.0

var (
	snmpAuth = map[string]gosnmp.SnmpV3AuthProtocol{
		"md5": gosnmp.MD5, "sha": gosnmp.SHA, "sha224": gosnmp.SHA224, "sha256": gosnmp.SHA256,
		"sha384": gosnmp.SHA384, "sha512": gosnmp.SHA512,
	}
	snmpPriv = map[string]gosnmp.SnmpV3PrivProtocol{
		"des": gosnmp.DES, "aes": gosnmp.AES, "aes192": gosnmp.AES192, "aes256": gosnmp.AES256,
		"aes192c": gosnmp.AES192C, "aes256c": gosnmp.AES256C,
	}
)

// probeSNMP gets one OID: snmp://community@host:161/1.3.6.1.2.1.1.3.0?version=2c.
// SNMPv3: snmp://user@host/oid?version=3&auth=sha&auth-pass=...&priv=aes&priv-pass=...
func probeSNMP(ctx context.Context, u *url.URL, _ Options) (Values, error) {
	g, oid, err := snmpClient(u)
	if err != nil {
		return nil, err
	}
	g.Context = ctx
	g.Timeout = deadline(ctx)
	if err := g.Connect(); err != nil {
		return Values{"CONNECTED": false}, err
	}
	defer g.Conn.Close()
	start := time.Now()
	pkt, err := g.Get([]string{oid})
	if err != nil {
		return Values{"CONNECTED": false}, err
	}
	values := Values{"CONNECTED": true, "RESPONSE_TIME": ms(time.Since(start))}
	if pkt.Error != gosnmp.NoError {
		return values, fmt.Errorf("SNMP error %s", pkt.Error)
	}
	if len(pkt.Variables) == 0 {
		return values, fmt.Errorf("no value for OID %s", oid)
	}
	v := pkt.Variables[0]
	switch v.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView:
		return values, fmt.Errorf("OID %s: %s", oid, v.Type)
	case gosnmp.OctetString:
		b, _ := v.Value.([]byte)
		values["VALUE"] = string(b)
	case gosnmp.ObjectIdentifier, gosnmp.IPAddress:
		values["VALUE"] = fmt.Sprint(v.Value)
	default:
		values["VALUE"] = gosnmp.ToBigInt(v.Value).String()
	}
	return values, nil
}

func snmpClient(u *url.URL) (*gosnmp.GoSNMP, string, error) {
	q := u.Query()
	port := uint16(161)
	if p := u.Port(); p != "" {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			return nil, "", fmt.Errorf("invalid port %q", p)
		}
		port = uint16(n)
	}
	oid := strings.Trim(u.Path, "/")
	if oid == "" {
		oid = q.Get("oid")
	}
	if oid == "" {
		oid = defaultOID
	}
	g := &gosnmp.GoSNMP{Target: u.Hostname(), Port: port, Retries: 1, MaxOids: gosnmp.MaxOids}
	switch v := strings.ToLower(q.Get("version")); v {
	case "", "2", "2c":
		g.Version = gosnmp.Version2c
	case "1":
		g.Version = gosnmp.Version1
	case "3":
		g.Version = gosnmp.Version3
	default:
		return nil, "", fmt.Errorf("unsupported SNMP version %q (1, 2c or 3)", v)
	}
	if g.Version != gosnmp.Version3 {
		g.Community = u.User.Username()
		if g.Community == "" {
			g.Community = "public"
		}
		return g, oid, nil
	}
	params := &gosnmp.UsmSecurityParameters{UserName: u.User.Username()}
	if params.UserName == "" {
		params.UserName = q.Get("user")
	}
	flags := gosnmp.NoAuthNoPriv
	if a := strings.ToLower(q.Get("auth")); a != "" {
		proto, ok := snmpAuth[a]
		if !ok {
			return nil, "", fmt.Errorf("unsupported SNMPv3 auth %q", a)
		}
		params.AuthenticationProtocol = proto
		params.AuthenticationPassphrase = q.Get("auth-pass")
		if p, ok := u.User.Password(); ok && params.AuthenticationPassphrase == "" {
			params.AuthenticationPassphrase = p
		}
		flags = gosnmp.AuthNoPriv
	}
	if p := strings.ToLower(q.Get("priv")); p != "" {
		proto, ok := snmpPriv[p]
		if !ok {
			return nil, "", fmt.Errorf("unsupported SNMPv3 priv %q", p)
		}
		if flags == gosnmp.NoAuthNoPriv {
			return nil, "", fmt.Errorf("SNMPv3 priv needs auth")
		}
		params.PrivacyProtocol = proto
		params.PrivacyPassphrase = q.Get("priv-pass")
		flags = gosnmp.AuthPriv
	}
	g.SecurityModel = gosnmp.UserSecurityModel
	g.MsgFlags = flags
	g.SecurityParameters = params
	return g, oid, nil
}
