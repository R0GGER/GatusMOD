package proxy

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gatusmod/internal/config"
)

func fakeGatus(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			t.Errorf("conditional headers should be removed for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("ETag", `"abc"`)
			_, _ = io.WriteString(w, `<html><head><link rel="shortcut icon" href="/favicon.ico" /></head><body><div id="app"></div></body></html>`)
		case appJSPath:
			w.Header().Set("Content-Type", "text/javascript")
			gz := gzip.NewWriter(w)
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = io.WriteString(gz, `x,Pa=96,Ka=50;var Va={__name:"Home"}`)
			_ = gz.Close()
		case manifestPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"icons":[]}`)
		case "/api/v1/endpoints/statuses":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestProxy(t *testing.T) {
	upstream := fakeGatus(t)
	defer upstream.Close()
	cfg := &config.Config{Upstream: upstream.URL, UI: config.UI{ItemsPerPage: 12, Footer: "<p>hi</p>", Favicon: config.Favicon{Default: "/images/x.ico"}}}
	tracker := NewTracker()
	p, err := New(cfg, tracker)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br")
	req.Header.Set("If-None-Match", `"abc"`)
	p.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `href="/images/x.ico"`) || !strings.Contains(body, "gatusmod-footer") || !strings.Contains(body, "/mod/static/mod.js") {
		t.Errorf("html not patched: %s", body)
	}
	if rec.Header().Get("ETag") != "" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("unexpected caching headers %v", rec.Header())
	}

	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, appJSPath, nil))
	if got := rec.Body.String(); got != `x,Pa=12,Ka=50;var Va={__name:"Home"}` {
		t.Errorf("app.js not patched: %q", got)
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("patched response must not claim to be compressed")
	}

	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/endpoints/statuses", nil))
	if rec.Body.String() != `[]` {
		t.Errorf("api should pass through unchanged, got %q", rec.Body.String())
	}

	snapshot := tracker.Snapshot()
	if len(snapshot["html"].Report.Missing) != 0 || len(snapshot["app.js"].Report.Applied) != 1 {
		t.Errorf("unexpected tracker state %+v", snapshot)
	}
}

func TestSelfTest(t *testing.T) {
	upstream := fakeGatus(t)
	defer upstream.Close()
	cfg := &config.Config{Upstream: upstream.URL, UI: config.UI{ItemsPerPage: 12, Favicon: config.Favicon{Size192x192: "/a.png"}}}
	tracker := NewTracker()
	p, _ := New(cfg, tracker)
	SelfTest(context.Background(), p, tracker)
	snapshot := tracker.Snapshot()
	for _, resource := range []string{"html", "app.js", "manifest"} {
		if e, ok := snapshot[resource]; !ok || len(e.Report.Missing) != 0 {
			t.Errorf("expected %s to be patched by the self-test, got %+v", resource, e)
		}
	}
}

func TestProxy_InvalidUpstream(t *testing.T) {
	if _, err := New(&config.Config{Upstream: "gatus:8080"}, NewTracker()); err == nil {
		t.Error("expected an error for an upstream without scheme")
	}
}
