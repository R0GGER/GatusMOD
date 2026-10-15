package probe

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"gatusmod/internal/config"
)

// IPerf3Result is the public view of the latest iperf3 check, for the endpoint page of the dashboard.
// It holds no URL or error text, which may contain credentials or internal details.
type IPerf3Result struct {
	Type              string     `json:"type"`
	Checked           *time.Time `json:"checked,omitempty"`
	Success           bool       `json:"success"`
	State             string     `json:"state,omitempty"`
	Port              string     `json:"port,omitempty"`
	ResponseTime      *float64   `json:"responseTime,omitempty"`
	Bandwidth         *float64   `json:"bandwidth,omitempty"`
	BandwidthMeasured *time.Time `json:"bandwidthMeasured,omitempty"`
	BandwidthInterval string     `json:"bandwidthInterval,omitempty"`
	BandwidthFailed   bool       `json:"bandwidthFailed,omitempty"`
	Duration          string     `json:"duration,omitempty"`
	Rate              string     `json:"rate,omitempty"`
	IP                string     `json:"ip,omitempty"`
	Reverse           bool       `json:"reverse"`
	// Periods sum up the data tests of the last 30 days, 7 days, 24 hours and hour (with the same settings).
	Periods []IPerf3Period `json:"periods,omitempty"`
}

// IPerf3 returns the latest result of the iperf3 target with this key.
func (r *Runner) IPerf3(key string) (IPerf3Result, bool) {
	r.mu.Lock()
	j := r.jobs[key]
	r.mu.Unlock()
	if j == nil || config.SchemeType(j.target.URL) != config.TypeIPerf3 {
		return IPerf3Result{}, false
	}
	j.mu.Lock()
	last, measured, previous := j.status.Last, j.status.LastBandwidth, j.previous
	j.mu.Unlock()

	res := IPerf3Result{Type: config.TypeIPerf3}
	if u, err := Parse(j.target.URL); err == nil {
		if o, err := options(u); err == nil {
			res.Reverse = o.reverse
			if o.rate > 0 {
				res.Rate = FormatRate(o.rate)
			}
			res.IP = strings.TrimPrefix(o.network, "tcp")
			if o.bandwidth > 0 {
				res.Duration = shortDuration(o.bandwidth)
				every := o.bandwidthInterval
				if every == 0 {
					every = j.target.Interval
				}
				res.BandwidthInterval = shortDuration(every)
				if r.history != nil {
					res.Periods = r.history.Periods(key, iperfMode(o), time.Now())
				}
			}
		}
	}
	if !last.Time.IsZero() {
		t := last.Time
		res.Checked, res.Success = &t, last.Success
		res.State, _ = last.Values["STATE"].(string)
		res.Port, _ = last.Values["PORT"].(string)
		if rt, ok := last.Values["RESPONSE_TIME"].(float64); ok {
			res.ResponseTime = &rt
		}
	}
	bw, ok := last.Values["BANDWIDTH"].(float64)
	if !ok {
		bw, ok = previous["BANDWIDTH"].(float64)
	}
	if !measured.IsZero() {
		res.BandwidthMeasured = &measured
		if ok {
			res.Bandwidth = &bw
		} else {
			res.BandwidthFailed = true
		}
	}
	return res, true
}

// shortDuration drops zero minutes and seconds: 1h0m0s is 1h, 30m0s is 30m.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// IPerf3Handler serves GET /mod/api/endpoints/{key}/iperf3.
func (r *Runner) IPerf3Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		res, ok := r.IPerf3(req.PathValue("key"))
		if !ok {
			http.Error(w, "no iperf3 check with this key", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(res)
	})
}
