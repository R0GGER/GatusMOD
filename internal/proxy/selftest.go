package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"sync"
	"time"

	"gatusmod/internal/inject"
)

type Entry struct {
	Report inject.Report `json:"report"`
	Time   time.Time     `json:"time"`
}

// Tracker keeps the latest patch result per resource and logs when a patch starts or stops failing.
type Tracker struct {
	mu      sync.RWMutex
	entries map[string]Entry
}

func NewTracker() *Tracker { return &Tracker{entries: map[string]Entry{}} }

func (t *Tracker) Record(resource string, report inject.Report) {
	t.mu.Lock()
	previous, seen := t.entries[resource]
	t.entries[resource] = Entry{Report: report, Time: time.Now()}
	t.mu.Unlock()
	if !seen || !slices.Equal(previous.Report.Missing, report.Missing) {
		if len(report.Missing) > 0 {
			slog.Warn("GatusMOD patch not applied: the Gatus UI probably changed", "resource", resource, "missing", report.Missing)
		} else if seen {
			slog.Info("GatusMOD patches applied again", "resource", resource)
		}
	}
}

func (t *Tracker) Snapshot() map[string]Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]Entry, len(t.entries))
	for k, v := range t.entries {
		out[k] = v
	}
	return out
}

// SelfTest requests the patched resources through the proxy until Gatus answers, and logs the result.
func SelfTest(ctx context.Context, handler http.Handler, tracker *Tracker) {
	paths := []string{"/", appJSPath, manifestPath}
	for attempt := 0; attempt < 24; attempt++ {
		ok := true
		for _, p := range paths {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, p, nil)
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				ok = false
			}
		}
		if ok {
			logSummary(tracker)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	slog.Error("self-test: Gatus did not answer within 2 minutes")
}

func logSummary(tracker *Tracker) {
	snapshot := tracker.Snapshot()
	resources := make([]string, 0, len(snapshot))
	for r := range snapshot {
		resources = append(resources, r)
	}
	sort.Strings(resources)
	for _, r := range resources {
		e := snapshot[r]
		if len(e.Report.Missing) > 0 {
			slog.Warn("self-test", "resource", r, "applied", e.Report.Applied, "missing", e.Report.Missing)
		} else {
			slog.Info("self-test", "resource", r, "applied", e.Report.Applied)
		}
	}
}
