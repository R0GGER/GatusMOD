// Package probe checks protocols Gatus does not support (iperf3, NTP, SNMP, SMTP, databases) and reports
// the results to Gatus as external endpoints. Push endpoints receive their status from outside instead.
package probe

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gatusmod/internal/config"
)

const (
	defaultTimeout = 10 * time.Second
	maxErrorLength = 500
	redacted       = "xxxxx"
)

// Target is one external endpoint: what to check, how often, and how Gatus identifies it.
type Target struct {
	Key        string
	Group      string
	Name       string
	URL        string
	Interval   time.Duration
	Conditions []string
}

func (t Target) Push() bool { return config.SchemeType(t.URL) == config.TypePush }

func (t Target) same(o Target) bool {
	return t.Key == o.Key && t.Group == o.Group && t.Name == o.Name && t.URL == o.URL && t.Interval == o.Interval &&
		strings.Join(t.Conditions, "\n") == strings.Join(o.Conditions, "\n")
}

// Values are the placeholders a probe fills, e.g. CONNECTED, RESPONSE_TIME (ms) or OFFSET (ms).
type Values map[string]any

// String lists the values sorted by name, e.g. "CONNECTED=true OFFSET=1.2 RESPONSE_TIME=12".
func (v Values) String() string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+formatValue(v[k]))
	}
	return strings.Join(parts, " ")
}

type Result struct {
	Time     time.Time
	Success  bool
	Error    string
	Duration time.Duration
	Values   Values
}

// Options change a single check.
type Options struct {
	// Bandwidth runs the iperf3 bandwidth test instead of only the handshake.
	Bandwidth bool
	// Previous values fill placeholders the probe did not measure this time, e.g. the last BANDWIDTH.
	Previous Values
}

type prober func(ctx context.Context, u *url.URL, opts Options) (Values, error)

var probers = map[string]prober{
	"iperf3":      probeIPerf3,
	"ntp":         probeNTP,
	"snmp":        probeSNMP,
	"smtp":        probeSMTP,
	"smtps":       probeSMTP,
	"postgres":    probeDatabase,
	"postgresql":  probeDatabase,
	"mysql":       probeDatabase,
	"redis":       probeDatabase,
	"rediss":      probeDatabase,
	"mongodb":     probeDatabase,
	"mongodb+srv": probeDatabase,
}

// placeholders a probe type can fill; conditions with other placeholders are rejected.
var placeholders = map[string][]string{
	config.TypeIPerf3:   {"CONNECTED", "RESPONSE_TIME", "STATE", "PORT", "BANDWIDTH"},
	config.TypeNTP:      {"CONNECTED", "RESPONSE_TIME", "OFFSET", "STRATUM"},
	config.TypeSNMP:     {"CONNECTED", "RESPONSE_TIME", "VALUE"},
	config.TypeSMTP:     {"CONNECTED", "RESPONSE_TIME", "CODE", "CERTIFICATE_EXPIRATION"},
	config.TypeDatabase: {"CONNECTED", "RESPONSE_TIME", "RESULT"},
}

// Placeholders lists the condition placeholders of the URL's probe type.
func Placeholders(rawURL string) []string { return placeholders[config.SchemeType(rawURL)] }

var envPlaceholder = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Parse expands ${VAR} from the environment (only that form, so passwords may contain $) and parses the URL.
func Parse(rawURL string) (*url.URL, error) {
	expanded := envPlaceholder.ReplaceAllStringFunc(strings.TrimSpace(rawURL), func(m string) string {
		return os.Getenv(m[2 : len(m)-1])
	})
	u, err := url.Parse(expanded)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("invalid URL %s: %v", Redact(rawURL), err)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if probers[u.Scheme] == nil && u.Scheme != "push" {
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Scheme != "push" && u.Hostname() == "" {
		return nil, errors.New("the URL has no host")
	}
	return u, nil
}

// Validate checks the URL and the conditions before the endpoint is written for Gatus.
func Validate(rawURL string, conditions []string) error {
	u, err := Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme == "push" {
		return nil
	}
	if _, err := options(u); err != nil {
		return err
	}
	if len(conditions) == 0 {
		return errors.New("at least one condition is required")
	}
	known := knownSet(rawURL)
	for _, c := range conditions {
		if _, err := parseCondition(c, known); err != nil {
			return fmt.Errorf("condition %q: %v", c, err)
		}
	}
	return nil
}

// common are the GatusMOD options in the query string; drivers never see them.
type common struct {
	timeout           time.Duration
	query             string
	maxOffset         string
	bandwidth         time.Duration
	bandwidthInterval time.Duration
	// iperf3 only: the target bitrate in bit/s (0 = unlimited), the dial network (tcp, tcp4, tcp6) and the direction.
	rate    float64
	network string
	reverse bool
}

var ownParams = []string{"timeout", "query", "max-offset", "bandwidth", "bandwidth-interval"}

func options(u *url.URL) (common, error) {
	q := u.Query()
	o := common{timeout: defaultTimeout, query: q.Get("query"), maxOffset: q.Get("max-offset"), network: "tcp"}
	if u.Scheme == "iperf3" {
		if err := iperfOptions(q, &o); err != nil {
			return o, err
		}
	}
	durations := []struct {
		key string
		dst *time.Duration
	}{{"timeout", &o.timeout}, {"bandwidth", &o.bandwidth}, {"bandwidth-interval", &o.bandwidthInterval}}
	for _, d := range durations {
		if s := q.Get(d.key); s != "" {
			v, err := time.ParseDuration(s)
			if err != nil || v <= 0 {
				return o, fmt.Errorf("%s %q is not a valid duration", d.key, s)
			}
			*d.dst = v
		}
	}
	if o.maxOffset != "" {
		if _, err := time.ParseDuration(o.maxOffset); err != nil {
			return o, fmt.Errorf("max-offset %q is not a valid duration", o.maxOffset)
		}
	}
	if o.bandwidth > 60*time.Second {
		return o, errors.New("bandwidth tests are limited to 60s")
	}
	return o, nil
}

// BandwidthInterval tells whether the URL runs an iperf3 data test, and how often: 0 means on every check
// (the interval of the endpoint), a bandwidth-interval in the URL tests less often.
func BandwidthInterval(rawURL string) (time.Duration, bool) {
	u, err := Parse(rawURL)
	if err != nil || u.Scheme != "iperf3" {
		return 0, false
	}
	o, err := options(u)
	if err != nil || o.bandwidth == 0 {
		return 0, false
	}
	return o.bandwidthInterval, true
}

// Check runs one probe and evaluates the conditions. Passwords in the URL never appear in the error.
func Check(ctx context.Context, rawURL string, conditions []string, opts Options) Result {
	res := Result{Time: time.Now(), Values: Values{}}
	u, err := Parse(rawURL)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	o, err := options(u)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	p := probers[u.Scheme]
	if p == nil {
		res.Error = "nothing to check for " + u.Scheme + " endpoints"
		return res
	}
	timeout := o.timeout
	if opts.Bandwidth {
		timeout += 2 * o.bandwidth
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	values, probeErr := p(pctx, u, opts)
	cancel()
	elapsed := time.Since(res.Time)
	if values == nil {
		values = Values{}
	}
	if _, ok := values["CONNECTED"]; !ok {
		values["CONNECTED"] = probeErr == nil
	}
	if _, ok := values["RESPONSE_TIME"]; !ok {
		values["RESPONSE_TIME"] = ms(elapsed)
	}
	res.Duration = elapsed
	if v, ok := values["RESPONSE_TIME"].(float64); ok {
		res.Duration = time.Duration(v * float64(time.Millisecond))
	}
	for k, v := range opts.Previous {
		if _, ok := values[k]; !ok {
			values[k] = v
		}
	}
	res.Values = values

	if o.maxOffset != "" && u.Scheme == "ntp" {
		conditions = append(append([]string(nil), conditions...), "[OFFSET] <= "+o.maxOffset)
	}
	var messages []string
	if probeErr != nil {
		messages = append(messages, redactError(probeErr.Error(), u))
	}
	messages = append(messages, evaluate(conditions, values, knownSet(rawURL))...)
	res.Success = len(messages) == 0
	res.Error = truncate(strings.Join(messages, "; "), maxErrorLength)
	return res
}

func knownSet(rawURL string) map[string]bool {
	known := map[string]bool{}
	for _, p := range Placeholders(rawURL) {
		known[p] = true
	}
	return known
}

// Redact hides the password and every query parameter whose name contains "pass" or "secret".
func Redact(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return rawURL
	}
	changed := false
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword(u.User.Username(), redacted)
		changed = true
	}
	q := u.Query()
	query := false
	for k := range q {
		if sensitive(k) {
			q.Set(k, redacted)
			query = true
		}
	}
	if query {
		u.RawQuery = q.Encode()
	}
	if !changed && !query {
		return rawURL
	}
	return u.String()
}

func sensitive(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "pass") || strings.Contains(k, "secret") || strings.Contains(k, "token")
}

// redactError removes the secrets of u from a driver error message.
func redactError(msg string, u *url.URL) string {
	var secrets []string
	if p, ok := u.User.Password(); ok && p != "" {
		secrets = append(secrets, p, url.QueryEscape(p), url.PathEscape(p))
	}
	for k, vs := range u.Query() {
		if sensitive(k) {
			secrets = append(secrets, vs...)
		}
	}
	for _, s := range secrets {
		if len(s) >= 3 {
			msg = strings.ReplaceAll(msg, s, redacted)
		}
	}
	return msg
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func ms(d time.Duration) float64 { return math.Round(float64(d)/float64(time.Millisecond)*100) / 100 }

func formatValue(v any) string {
	switch t := v.(type) {
	case float64:
		return strconv.FormatFloat(math.Round(t*100)/100, 'f', -1, 64)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func deadline(ctx context.Context) time.Duration {
	if d, ok := ctx.Deadline(); ok {
		return time.Until(d)
	}
	return defaultTimeout
}
