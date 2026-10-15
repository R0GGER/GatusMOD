package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gatusmod/internal/config"
)

type fakeGatus struct {
	mu     sync.Mutex
	pushes []url.Values
	auth   []string
}

func (g *fakeGatus) handler(known string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/endpoints/"+known+"/external" {
			http.NotFound(w, r)
			return
		}
		g.mu.Lock()
		g.pushes = append(g.pushes, r.URL.Query())
		g.auth = append(g.auth, r.Header.Get("Authorization"))
		g.mu.Unlock()
		_, _ = w.Write([]byte("OK"))
	}
}

func (g *fakeGatus) last() (url.Values, string, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.pushes) == 0 {
		return nil, "", 0
	}
	return g.pushes[len(g.pushes)-1], g.auth[len(g.auth)-1], len(g.pushes)
}

func startRunner(t *testing.T, gatus *httptest.Server, targets []Target) (*Runner, *config.Config) {
	t.Helper()
	cfg := &config.Config{Upstream: gatus.URL, ProbeSecret: "secret"}
	r := NewRunner(func() *config.Config { return cfg })
	r.Update(targets)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for len(r.Statuses()) < len(targets) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return r, cfg
}

func TestRunner_PushesResults(t *testing.T) {
	g := &fakeGatus{}
	srv := httptest.NewServer(g.handler("mail_relay"))
	defer srv.Close()
	port := fakeSMTP(t, "220 fake")
	_, cfg := startRunner(t, srv, []Target{{
		Key: "mail_relay", Group: "mail", Name: "relay", URL: "smtp://127.0.0.1:" + port,
		Interval: 50 * time.Millisecond, Conditions: []string{"[CODE] == 220"},
	}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		q, auth, n := g.last()
		if n > 0 {
			if q.Get("success") != "true" || q.Get("duration") == "" || auth != "Bearer "+cfg.GatusToken() {
				t.Fatalf("unexpected push %v %q", q, auth)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no result pushed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunner_UpdateKeepsUnchangedJobs(t *testing.T) {
	g := &fakeGatus{}
	srv := httptest.NewServer(g.handler("x"))
	defer srv.Close()
	target := Target{Key: "a_b", Group: "a", Name: "b", URL: "ntp://127.0.0.1:1", Interval: time.Hour, Conditions: []string{"[CONNECTED] == true"}}
	r, _ := startRunner(t, srv, []Target{target})
	r.mu.Lock()
	before := r.jobs["a_b"]
	r.mu.Unlock()
	r.Update([]Target{target})
	r.mu.Lock()
	same := r.jobs["a_b"] == before
	r.mu.Unlock()
	if !same {
		t.Error("an unchanged target must keep its job and schedule")
	}
	r.Update(nil)
	if len(r.Statuses()) != 0 {
		t.Error("removed targets must stop")
	}
}

func TestRunner_IPerf3Result(t *testing.T) {
	g := &fakeGatus{}
	srv := httptest.NewServer(g.handler("speed_a"))
	defer srv.Close()
	port := fakeIPerf3(t, false)
	r, _ := startRunner(t, srv, []Target{
		{Key: "speed_a", Group: "speed", Name: "a", URL: "iperf3://127.0.0.1:" + port + "?bandwidth=1s", Interval: 200 * time.Millisecond, Conditions: []string{"[CONNECTED] == true"}},
		{Key: "mail_b", Group: "mail", Name: "b", URL: "smtp://127.0.0.1:1", Interval: time.Hour},
	})
	deadline := time.Now().Add(10 * time.Second)
	var res IPerf3Result
	for {
		res, _ = r.IPerf3("speed_a")
		if res.Bandwidth != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no bandwidth result: %+v", res)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !res.Success || res.State != "ready" || res.Port != port || res.BandwidthInterval != "200ms" || *res.Bandwidth <= 0 {
		t.Errorf("unexpected result %+v", res)
	}
	if _, ok := r.IPerf3("mail_b"); ok {
		t.Error("only iperf3 targets have an iperf3 result")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/mod/api/endpoints/speed_a/iperf3", nil)
	req.SetPathValue("key", "speed_a")
	r.IPerf3Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "127.0.0.1") {
		t.Errorf("the API must answer without the URL: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	h, err := OpenHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	add := func(ago time.Duration, ok bool, mbit float64, mode string) {
		if err := h.Add("speed_a", HistoryEntry{Time: now.Add(-ago), OK: ok, Mbit: mbit, Mode: mode}); err != nil {
			t.Fatal(err)
		}
	}
	add(40*24*time.Hour, true, 1, "upload")
	add(3*24*time.Hour, true, 100, "upload")
	add(2*time.Hour, false, 0, "upload")
	add(30*time.Minute, true, 200, "upload")
	add(10*time.Minute, true, 1, "upload@1000000")

	h, err = OpenHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	p := h.Periods("speed_a", "upload", now)
	want := []struct{ tests, passed int }{{3, 2}, {3, 2}, {2, 1}, {1, 1}}
	for i, w := range want {
		if p[i].Tests != w.tests || p[i].Passed != w.passed {
			t.Errorf("%s: got %d/%d, want %d/%d", p[i].ID, p[i].Passed, p[i].Tests, w.passed, w.tests)
		}
	}
	if p[0].Bandwidth == nil || *p[0].Bandwidth != 150 {
		t.Errorf("30d average: %v", p[0].Bandwidth)
	}
	if last, ok := h.Last("speed_a", "upload"); !ok || last.Mbit != 200 {
		t.Errorf("last upload test: %+v", last)
	}
	if mode, ok := iperfModeOf("iperf3://h?bandwidth=5s&rate=1M&reverse=true"); !ok || mode != "download@1000000" {
		t.Errorf("mode: %q %v", mode, ok)
	}
	if _, ok := iperfModeOf("iperf3://h"); ok {
		t.Error("a handshake has no data test mode")
	}
}

func TestPushHandler(t *testing.T) {
	g := &fakeGatus{}
	srv := httptest.NewServer(g.handler("jobs_backup"))
	defer srv.Close()
	r, cfg := startRunner(t, srv, []Target{{Key: "jobs_backup", Group: "jobs", Name: "backup", URL: "push://", Interval: time.Hour}})
	handler := r.PushHandler()
	call := func(method, path string) int {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code
	}
	token := cfg.PushToken("jobs_backup")
	if code := call(http.MethodGet, PushPrefix+"wrong"); code != http.StatusNotFound {
		t.Errorf("unknown token: got %d", code)
	}
	if code := call(http.MethodGet, PushPrefix+token+"?status=down&msg=disk+full&ping=12.5"); code != http.StatusOK {
		t.Fatalf("push: got %d", code)
	}
	q, auth, _ := g.last()
	if q.Get("success") != "false" || q.Get("error") != "disk full" || q.Get("duration") != "13ms" || auth != "Bearer "+cfg.GatusToken() {
		t.Errorf("unexpected push to Gatus %v %q", q, auth)
	}
	if code := call(http.MethodPost, PushPrefix+token); code != http.StatusTooManyRequests {
		t.Errorf("a second push within a second must be rate limited, got %d", code)
	}
	if code := call(http.MethodDelete, PushPrefix+token); code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: got %d", code)
	}
	st := r.Statuses()[0]
	if st.Last.Success || st.Last.Error != "disk full" {
		t.Errorf("status not recorded: %+v", st.Last)
	}
	if got := PushURL(cfg, "https://status.example.org/", "jobs_backup"); got != "https://status.example.org/mod/push/"+token {
		t.Errorf("unexpected push URL %s", got)
	}
}
