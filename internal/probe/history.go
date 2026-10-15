package probe

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	historyRetention = 30 * 24 * time.Hour
	historyMaxPerKey = 10000
)

// HistoryEntry is one iperf3 data test. Mode tells tests with other settings apart (direction and rate limit),
// so the statistics do not mix a full-speed test with a 1 Mbit/s one.
type HistoryEntry struct {
	Time time.Time `json:"t"`
	OK   bool      `json:"ok"`
	Mbit float64   `json:"mbit,omitempty"`
	Mode string    `json:"mode"`
}

// History keeps the iperf3 data tests of the last 30 days in a JSON file, for the statistics and to keep
// the test schedule across restarts.
type History struct {
	path string

	mu      sync.Mutex
	entries map[string][]HistoryEntry
}

// OpenHistory reads the file; a missing file is an empty history.
func OpenHistory(path string) (*History, error) {
	h := &History{path: path, entries: map[string][]HistoryEntry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(data, &h.entries); err != nil {
		return h, err
	}
	if h.entries == nil {
		h.entries = map[string][]HistoryEntry{}
	}
	return h, nil
}

// Add records a test and writes the file.
func (h *History) Add(key string, e HistoryEntry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries[key] = append(h.entries[key], e)
	cutoff := e.Time.Add(-historyRetention)
	for k, list := range h.entries {
		i := sort.Search(len(list), func(i int) bool { return list[i].Time.After(cutoff) })
		if len(list)-i > historyMaxPerKey {
			i = len(list) - historyMaxPerKey
		}
		if i == len(list) {
			delete(h.entries, k)
		} else if i > 0 {
			h.entries[k] = append([]HistoryEntry(nil), list[i:]...)
		}
	}
	data, err := json.Marshal(h.entries)
	if err != nil {
		return err
	}
	tmp := h.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, h.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Last returns the latest test of key with this mode.
func (h *History) Last(key, mode string) (HistoryEntry, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.entries[key]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Mode == mode {
			return list[i], true
		}
	}
	return HistoryEntry{}, false
}

// IPerf3Period sums up the data tests of one period: how many ran, how many passed, and the average result.
type IPerf3Period struct {
	ID        string   `json:"id"`
	Tests     int      `json:"tests"`
	Passed    int      `json:"passed"`
	Bandwidth *float64 `json:"bandwidth,omitempty"`
}

var historyPeriods = []struct {
	id string
	d  time.Duration
}{{"30d", 30 * 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}, {"24h", 24 * time.Hour}, {"1h", time.Hour}}

// Periods sums up the tests of key with this mode for the last 30 days, 7 days, 24 hours and hour.
func (h *History) Periods(key, mode string, now time.Time) []IPerf3Period {
	h.mu.Lock()
	list := h.entries[key]
	h.mu.Unlock()
	out := make([]IPerf3Period, 0, len(historyPeriods))
	for _, p := range historyPeriods {
		from := now.Add(-p.d)
		period := IPerf3Period{ID: p.id}
		var sum float64
		for _, e := range list {
			if e.Mode != mode || e.Time.Before(from) {
				continue
			}
			period.Tests++
			if e.OK {
				period.Passed++
				sum += e.Mbit
			}
		}
		if period.Passed > 0 {
			avg := roundTo(sum/float64(period.Passed), 2)
			period.Bandwidth = &avg
		}
		out = append(out, period)
	}
	return out
}

// iperfMode is the direction plus the rate limit, e.g. "upload" or "download@1000000".
func iperfMode(o common) string {
	mode := "upload"
	if o.reverse {
		mode = "download"
	}
	if o.rate > 0 {
		mode += "@" + strconv.FormatFloat(o.rate, 'f', -1, 64)
	}
	return mode
}

// iperfModeOf returns the mode of an iperf3 URL with a data test, or false.
func iperfModeOf(rawURL string) (string, bool) {
	u, err := Parse(rawURL)
	if err != nil || u.Scheme != "iperf3" {
		return "", false
	}
	o, err := options(u)
	if err != nil || o.bandwidth == 0 {
		return "", false
	}
	return iperfMode(o), true
}
