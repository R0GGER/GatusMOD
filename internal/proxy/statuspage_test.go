package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"gatusmod/internal/config"
	"gatusmod/internal/statuspage"
)

func TestProxy_HidesDisabledEndpoints(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"key":"http_amazon-com"},{"key":"http_ebay-com"}]`)
	}))
	defer upstream.Close()
	p, err := New(&config.Config{Upstream: upstream.URL}, NewTracker())
	if err != nil {
		t.Fatal(err)
	}
	get := func() string {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, statuspage.StatusesPath, nil))
		return rec.Body.String()
	}
	if got := get(); !strings.Contains(got, "amazon") {
		t.Errorf("without disabled endpoints everything is shown, got %s", got)
	}
	p.HideDisabled(func() map[string]bool { return map[string]bool{config.EndpointKey("http", "amazon.com"): true} })
	if got := get(); got != `[{"key":"http_ebay-com"}]` {
		t.Errorf("a disabled endpoint must be left out, got %s", got)
	}
}

func TestProxy_StatusPage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<html><head><script>window.config = {};</script><title>Main</title></head><body></body></html>`)
		case statuspage.StatusesPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"key":"core_a","group":"core"},{"key":"other_b","group":"other"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	cfg := &config.Config{
		Upstream: upstream.URL, BaseURL: "https://main.example.com", UI: config.UI{Footer: `<a href="[baseurl]/x">x</a>`},
		StatusPages: []config.StatusPage{{Slug: "klant", Groups: []string{"core"}, Domains: []string{"status.klant.nl"}, Title: "Klant"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p, err := New(cfg, NewTracker())
	if err != nil {
		t.Fatal(err)
	}
	h := statuspage.New().Wrap(cfg, p)
	get := func(host, path string, header bool) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = host
		req.Header.Set("Accept-Encoding", "gzip")
		if header {
			req.Header.Set(statuspage.Header, "klant")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Body.String()
	}

	if got := get("main.example.com", statuspage.StatusesPath, false); !strings.Contains(got, "other_b") {
		t.Errorf("the main site should show every endpoint, got %s", got)
	}
	for _, c := range []struct {
		host   string
		header bool
	}{{"main.example.com", true}, {"status.klant.nl", false}} {
		if got := get(c.host, statuspage.StatusesPath, c.header); got != `[{"key":"core_a","group":"core"}]` {
			t.Errorf("%s: statuses not filtered, got %s", c.host, got)
		}
	}

	view := get("main.example.com", "/status/klant", false)
	if !strings.Contains(view, "<title>Klant</title>") || !strings.Contains(view, "statuspage.js") || !strings.Contains(view, "https://main.example.com/x") {
		t.Errorf("the status page on the main site is not branded:\n%s", view)
	}
	strict := get("status.klant.nl", "/", false)
	if !strings.Contains(strict, "<title>Klant</title>") || strings.Contains(strict, "statuspage.js") || !strings.Contains(strict, "http://status.klant.nl/x") {
		t.Errorf("the status page on its domain is not branded or [baseurl] is not its domain:\n%s", strict)
	}
	if main := get("main.example.com", "/", false); !strings.Contains(main, "<title>Main</title>") {
		t.Errorf("the main site should not be branded:\n%s", main)
	}
}

func TestProxy_PrivateStatusPage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"key":"core_a","group":"core"},{"key":"other_b","group":"other"},{"key":"other_c","group":"other"}]`)
	}))
	defer upstream.Close()
	hash, _ := bcrypt.GenerateFromPassword([]byte("geheim-wachtwoord"), bcrypt.MinCost)
	cfg := &config.Config{
		Upstream: upstream.URL,
		StatusPages: []config.StatusPage{
			{ID: "p", Slug: "geheim", Endpoints: []string{"other_b"}, Domains: []string{"status.geheim.nl"}, PasswordBcrypt: string(hash)},
			{ID: "q", Slug: "open", Groups: []string{"other"}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p, err := New(cfg, NewTracker())
	if err != nil {
		t.Fatal(err)
	}
	h := statuspage.New().Wrap(cfg, p)
	get := func(path, header string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "main.example.com"
		if header != "" {
			req.Header.Set(statuspage.Header, header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if got := get(statuspage.StatusesPath, ""); got != `[{"key":"core_a","group":"core"},{"key":"other_c","group":"other"}]` {
		t.Errorf("the main site should hide the private endpoint, got %s", got)
	}
	if got := get(statuspage.StatusesPath, "open"); got != `[{"key":"other_c","group":"other"}]` {
		t.Errorf("a public page should hide the endpoints of a private page too, got %s", got)
	}
}
