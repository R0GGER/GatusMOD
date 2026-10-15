package probe

import (
	"context"
	"math"
	"net"
	"net/url"

	"github.com/beevik/ntp"
)

// probeNTP queries the server once. OFFSET is the absolute clock offset in ms, so "[OFFSET] < 500ms" works both ways.
func probeNTP(ctx context.Context, u *url.URL, _ Options) (Values, error) {
	port := u.Port()
	if port == "" {
		port = "123"
	}
	resp, err := ntp.QueryWithOptions(net.JoinHostPort(u.Hostname(), port), ntp.QueryOptions{Timeout: deadline(ctx)})
	if err != nil {
		return Values{"CONNECTED": false}, err
	}
	values := Values{
		"CONNECTED":     true,
		"RESPONSE_TIME": ms(resp.RTT),
		"OFFSET":        math.Abs(ms(resp.ClockOffset)),
		"STRATUM":       int(resp.Stratum),
	}
	return values, resp.Validate()
}
