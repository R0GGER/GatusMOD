package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gatusmod/internal/config"
)

const (
	maxConcurrent = 20
	maxJitter     = 30 * time.Second
	// retryAfterReload waits for Gatus to load a new external endpoint (it checks its configuration every 30 seconds).
	retryAfterReload = 35 * time.Second
)

var errNotLoaded = errors.New("Gatus does not know this external endpoint yet; it reloads its configuration within 30 seconds")

// Status is the latest state of one target, for the admin page.
type Status struct {
	Target        Target
	Last          Result
	Next          time.Time
	PushError     string
	LastBandwidth time.Time
}

// Runner checks every active target on its interval and reports the results to Gatus.
type Runner struct {
	cfg       func() *config.Config
	client    *http.Client
	sem       chan struct{}
	bandwidth sync.Mutex
	history   *History

	mu      sync.Mutex
	ctx     context.Context
	pending []Target
	jobs    map[string]*job
}

type job struct {
	target Target
	cancel context.CancelFunc

	mu       sync.Mutex
	status   Status
	previous Values
	lastPush time.Time
}

func NewRunner(cfg func() *config.Config) *Runner {
	return &Runner{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
		sem:    make(chan struct{}, maxConcurrent),
		jobs:   map[string]*job{},
	}
}

// UseHistory stores the iperf3 data tests in h; call it before Update and Run.
func (r *Runner) UseHistory(h *History) { r.history = h }

// Update replaces the targets. Unchanged targets keep their schedule; targets applied before Run start with it.
func (r *Runner) Update(targets []Target) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx == nil {
		r.pending = targets
		return
	}
	r.apply(targets)
}

// apply must be called with mu held.
func (r *Runner) apply(targets []Target) {
	next := make(map[string]*job, len(targets))
	for _, t := range targets {
		if _, dup := next[t.Key]; dup {
			continue
		}
		old, exists := r.jobs[t.Key]
		if exists && old.target.same(t) {
			next[t.Key] = old
			continue
		}
		if t.Interval <= 0 {
			t.Interval = config.FallbackInterval
		}
		j := &job{target: t, status: Status{Target: t}}
		if exists && old.target.URL == t.URL {
			old.mu.Lock()
			j.status.Last, j.status.LastBandwidth, j.previous = old.status.Last, old.status.LastBandwidth, old.previous
			old.mu.Unlock()
		} else if mode, ok := iperfModeOf(t.URL); ok && r.history != nil {
			if last, ok := r.history.Last(t.Key, mode); ok {
				j.status.LastBandwidth = last.Time
				if last.OK {
					j.previous = Values{"BANDWIDTH": last.Mbit}
				}
			}
		}
		if !t.Push() {
			ctx, cancel := context.WithCancel(r.ctx)
			j.cancel = cancel
			go r.loop(ctx, j)
		}
		next[t.Key] = j
	}
	for key, old := range r.jobs {
		if next[key] != old && old.cancel != nil {
			old.cancel()
		}
	}
	r.jobs = next
}

// Run starts the checks and stops them when ctx ends.
func (r *Runner) Run(ctx context.Context) {
	r.mu.Lock()
	r.ctx = ctx
	r.apply(r.pending)
	r.pending = nil
	r.mu.Unlock()
	<-ctx.Done()
}

// Statuses lists every target sorted by group and name.
func (r *Runner) Statuses() []Status {
	r.mu.Lock()
	jobs := make([]*job, 0, len(r.jobs))
	for _, j := range r.jobs {
		jobs = append(jobs, j)
	}
	r.mu.Unlock()
	out := make([]Status, 0, len(jobs))
	for _, j := range jobs {
		j.mu.Lock()
		out = append(out, j.status)
		j.mu.Unlock()
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].Target.Group != out[k].Target.Group {
			return out[i].Target.Group < out[k].Target.Group
		}
		return out[i].Target.Name < out[k].Target.Name
	})
	return out
}

func (r *Runner) loop(ctx context.Context, j *job) {
	jitter := min(j.target.Interval, maxJitter)
	delay := rand.N(jitter)
	for {
		j.mu.Lock()
		j.status.Next = time.Now().Add(delay)
		j.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		r.runOnce(ctx, j)
		delay = j.target.Interval
	}
}

func (r *Runner) runOnce(ctx context.Context, j *job) {
	t := j.target
	every, enabled := BandwidthInterval(t.URL)
	j.mu.Lock()
	due := enabled && (every == 0 || time.Since(j.status.LastBandwidth) >= every)
	previous := j.previous
	j.mu.Unlock()

	opts := Options{Bandwidth: due}
	if !due {
		opts.Previous = previous
	}
	if due {
		r.bandwidth.Lock()
	}
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		if due {
			r.bandwidth.Unlock()
		}
		return
	}
	res := Check(ctx, t.URL, t.Conditions, opts)
	<-r.sem
	if due {
		r.bandwidth.Unlock()
	}
	if ctx.Err() != nil {
		return
	}

	j.mu.Lock()
	// A busy server did not run the test, so it is tried again on the next check.
	tested := due && res.Values["STATE"] == "ready"
	bw, measured := res.Values["BANDWIDTH"].(float64)
	if tested {
		j.status.LastBandwidth = res.Time
		j.previous = nil
		if measured {
			j.previous = Values{"BANDWIDTH": bw}
		}
	}
	j.status.Last = res
	j.mu.Unlock()
	if mode, ok := iperfModeOf(t.URL); tested && ok && r.history != nil {
		if err := r.history.Add(t.Key, HistoryEntry{Time: res.Time, OK: measured, Mbit: bw, Mode: mode}); err != nil {
			slog.Warn("saving the iperf3 test failed", "endpoint", t.Key, "error", err)
		}
	}

	err := r.push(ctx, t.Key, res.Success, res.Error, res.Duration)
	if errors.Is(err, errNotLoaded) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryAfterReload):
		}
		err = r.push(ctx, t.Key, res.Success, res.Error, res.Duration)
	}
	j.setPushError(err)
}

func (j *job) setPushError(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	j.mu.Lock()
	changed := msg != j.status.PushError
	j.status.PushError = msg
	j.mu.Unlock()
	if changed && msg != "" {
		slog.Warn("reporting a probe result to Gatus failed", "endpoint", j.target.Key, "error", msg)
	}
}

// push reports one result to the external endpoint API of Gatus.
func (r *Runner) push(ctx context.Context, key string, success bool, msg string, d time.Duration) error {
	cfg := r.cfg()
	q := url.Values{}
	q.Set("success", strconv.FormatBool(success))
	if msg != "" {
		q.Set("error", msg)
	}
	if d >= 0 {
		q.Set("duration", d.Round(time.Millisecond).String())
	}
	endpoint := strings.TrimRight(cfg.Upstream, "/") + "/api/v1/endpoints/" + url.PathEscape(key) + "/external?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.GatusToken())
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotLoaded
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("Gatus answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
