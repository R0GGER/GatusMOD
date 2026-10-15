package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// probeSchemes are URL schemes Gatus cannot check. GatusMOD checks them itself and reports the
// results to Gatus as external endpoints.
var probeSchemes = map[string]string{
	"iperf3":      TypeIPerf3,
	"ntp":         TypeNTP,
	"snmp":        TypeSNMP,
	"smtp":        TypeSMTP,
	"smtps":       TypeSMTP,
	"postgres":    TypeDatabase,
	"postgresql":  TypeDatabase,
	"mysql":       TypeDatabase,
	"redis":       TypeDatabase,
	"rediss":      TypeDatabase,
	"mongodb":     TypeDatabase,
	"mongodb+srv": TypeDatabase,
	"push":        TypePush,
}

// Scheme returns the lower-case scheme of a URL like "ntp://host", or "" without one.
func Scheme(rawURL string) string {
	i := strings.Index(rawURL, "://")
	if i <= 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(rawURL[:i]))
}

// SchemeType returns the probe type (iperf3, ntp, snmp, smtp, database, push) of a URL, or "" when Gatus checks it itself.
func SchemeType(rawURL string) string { return probeSchemes[Scheme(rawURL)] }

// IsProbeURL reports whether the URL is checked by GatusMOD instead of Gatus.
func IsProbeURL(rawURL string) bool { return SchemeType(rawURL) != "" }

// DefaultPort is used as {port} for probe types when neither the row nor the import has a port.
func DefaultPort(typ string) string {
	switch typ {
	case TypeIPerf3:
		return "5201"
	case TypeNTP:
		return "123"
	case TypeSNMP:
		return "161"
	case TypeSMTP:
		return "25"
	}
	return ""
}

// GatusToken is the bearer token of every generated external endpoint.
func (cfg *Config) GatusToken() string { return cfg.derive("gatus-external-endpoint-token") }

// PushToken is the secret part of the public push URL of one endpoint (by Gatus key).
func (cfg *Config) PushToken(key string) string { return cfg.derive("push:" + key)[:32] }

func (cfg *Config) derive(label string) string {
	mac := hmac.New(sha256.New, []byte(cfg.ProbeSecret))
	mac.Write([]byte(label))
	return hex.EncodeToString(mac.Sum(nil))
}
