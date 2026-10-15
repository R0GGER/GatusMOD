package badge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gatusmod/internal/config"
	"gatusmod/internal/store"
)

func TestUptimeBadge(t *testing.T) {
	var uptimeCalls atomic.Int32
	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/endpoints/statuses":
			_, _ = w.Write([]byte(`[{"name":"a","group":"web","key":"web_a"},{"name":"b","group":"web","key":"web_b"},{"name":"c","group":"dns","key":"dns_c"},{"name":"new","group":"dns","key":"dns_new"}]`))
		case strings.HasSuffix(r.URL.Path, "/uptimes/30d"):
			uptimeCalls.Add(1)
			switch {
			case strings.Contains(r.URL.Path, "web_a"):
				_, _ = w.Write([]byte("1.000000"))
			case strings.Contains(r.URL.Path, "web_b"):
				_, _ = w.Write([]byte("0.990000"))
			case strings.Contains(r.URL.Path, "dns_c"):
				_, _ = w.Write([]byte("0.500000"))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gatus.Close()
	cfg := &config.Config{Upstream: gatus.URL}
	records := []store.Record{{Name: "a", Group: "web", Source: "imp1"}}
	h := New(Deps{Config: func() *config.Config { return cfg }, Records: func() []store.Record { return records }}).Routes()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	cases := map[string]struct {
		code int
		want []string
	}{
		"/mod/badges/uptimes/30d/badge.svg":                                                {200, []string{"uptime 30d", "83%", "#e05d44", `rx="3"`}},
		"/mod/badges/uptimes/30d/badge.svg?check=group:web&style=flat-square":              {200, []string{"99.50%", "#97ca00", `rx="0"`}},
		"/mod/badges/uptimes/30d/badge.svg?check=import:imp1&content=percentage-endpoints": {200, []string{"100% · 1 endpoints", "#44cc11"}},
		"/mod/badges/uptimes/30d/badge.svg?check=group:nope":                               {404, []string{"unknown check"}},
		"/mod/badges/uptimes/2y/badge.svg":                                                 {400, []string{"invalid duration"}},
		"/mod/badges/uptimes/30d/badge.svg?label=<script>":                                 {200, []string{"&lt;script&gt;"}},
	}
	for path, tc := range cases {
		rec := get(path)
		body := rec.Body.String()
		if rec.Code != tc.code || rec.Header().Get("Content-Type") != "image/svg+xml" {
			t.Errorf("%s: got %d %s", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s: badge should contain %q\n%s", path, want, body)
			}
		}
	}
	before := uptimeCalls.Load()
	get("/mod/badges/uptimes/30d/badge.svg?style=plastic")
	if uptimeCalls.Load() != before {
		t.Error("a second request within the cache time must not query Gatus again")
	}
}

func TestUptimeBadge_HidesPrivateEndpoints(t *testing.T) {
	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/endpoints/statuses":
			_, _ = w.Write([]byte(`[{"name":"a","group":"web","key":"web_a"},{"name":"b","group":"web","key":"web_b"},{"name":"c","group":"dns","key":"dns_c"}]`))
		case strings.Contains(r.URL.Path, "web_a"):
			_, _ = w.Write([]byte("1.000000"))
		default:
			_, _ = w.Write([]byte("0.500000"))
		}
	}))
	defer gatus.Close()
	cfg := &config.Config{Upstream: gatus.URL}
	h := New(Deps{Config: func() *config.Config { return cfg }, Records: func() []store.Record { return nil }}).Routes()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	if body := get("/mod/badges/uptimes/30d/badge.svg?content=percentage-endpoints").Body.String(); !strings.Contains(body, "3 endpoints") {
		t.Fatalf("without private pages every endpoint counts\n%s", body)
	}
	cfg = &config.Config{Upstream: gatus.URL, StatusPages: []config.StatusPage{
		{Slug: "p", Groups: []string{"dns"}, Endpoints: []string{"web_b"}, PasswordBcrypt: "x"},
	}}
	if body := get("/mod/badges/uptimes/30d/badge.svg?content=percentage-endpoints").Body.String(); !strings.Contains(body, "100% · 1 endpoints") {
		t.Errorf("private endpoints must not count, also not from the cache\n%s", body)
	}
	if rec := get("/mod/badges/uptimes/30d/badge.svg?check=group:dns"); rec.Code != http.StatusNotFound {
		t.Errorf("a group with only private endpoints is unknown, got %d", rec.Code)
	}
}

const gatusHealthSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="76" height="20">
  <linearGradient id="b" x2="0" y2="100%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>
  <mask id="a"><rect width="76" height="20" rx="3" fill="#fff"/></mask>
  <g mask="url(#a)">
    <path fill="#555" d="M0 0h48v20H0z"/>
    <path fill="#40cc11" d="M48 0h28v20H48z"/>
    <path fill="url(#b)" d="M0 0h76v20H0z"/>
  </g>
  <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
    <text x="24" y="15" fill="#010101" fill-opacity=".3">
      health
    </text>
    <text x="24" y="14">
      health
    </text>
    <text x="62" y="15" fill="#010101" fill-opacity=".3">
      up
    </text>
    <text x="62" y="14">
      up
    </text>
  </g>
</svg>`

func TestEndpointBadge(t *testing.T) {
	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/endpoints/web_a/health/badge.svg":
			_, _ = w.Write([]byte(gatusHealthSVG))
		case "/api/v1/endpoints/web_a/response-times/7d/badge.svg":
			_, _ = w.Write([]byte(strings.NewReplacer("#40cc11", "#cc8111", ">\n      health", ">\n      response time 7d", ">\n      up", ">\n      612ms").Replace(gatusHealthSVG)))
		case "/api/v1/endpoints/web_broken/health/badge.svg":
			_, _ = w.Write([]byte("<svg/>"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gatus.Close()
	cfg := &config.Config{Upstream: gatus.URL, StatusPages: []config.StatusPage{{Slug: "p", Endpoints: []string{"web_secret"}, PasswordBcrypt: "x"}}}
	h := New(Deps{Config: func() *config.Config { return cfg }, Records: func() []store.Record { return nil }}).Routes()
	cases := map[string]struct {
		code int
		want []string
	}{
		"/mod/badges/endpoints/web_a/health/badge.svg?style=flat-square":        {200, []string{">health<", ">up<", "#40cc11", `rx="0"`}},
		"/mod/badges/endpoints/web_a/response-times/7d/badge.svg?style=plastic": {200, []string{">response time 7d<", ">612ms<", "#cc8111", `height="18"`}},
		"/mod/badges/endpoints/web_a/uptimes/2y/badge.svg":                      {400, []string{"invalid badge"}},
		"/mod/badges/endpoints/web_a/charts/7d/badge.svg":                       {400, []string{"invalid badge"}},
		"/mod/badges/endpoints/web_missing/health/badge.svg":                    {404, []string{"not found"}},
		"/mod/badges/endpoints/web_secret/health/badge.svg":                     {404, []string{"not found"}},
		"/mod/badges/endpoints/web_broken/health/badge.svg":                     {502, []string{"no data"}},
	}
	for path, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := rec.Body.String()
		if rec.Code != tc.code || rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(rec.Header().Get("Cache-Control"), "no-cache") {
			t.Errorf("%s: got %d %s %s", path, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
		}
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s: badge should contain %q\n%s", path, want, body)
			}
		}
	}
}

func TestEndpointURL(t *testing.T) {
	if got := EndpointURL("https://x/", "web_a", KindHealth, "", "flat"); got != "https://x/api/v1/endpoints/web_a/health/badge.svg" {
		t.Errorf("the flat style uses the badge of Gatus, got %s", got)
	}
	if got := EndpointURL("https://x", "web_a", KindUptime, "7d", "plastic"); got != "https://x/mod/badges/endpoints/web_a/uptimes/7d/badge.svg?style=plastic" {
		t.Errorf("unexpected url %s", got)
	}
}

func TestURL(t *testing.T) {
	if got := URL("https://up.example.com/", "30d", "flat", ContentPercentage, CheckOverall); got != "https://up.example.com/mod/badges/uptimes/30d/badge.svg" {
		t.Errorf("unexpected default url %s", got)
	}
	if got := URL("https://x", "7d", "plastic", ContentEndpoints, "group:a b"); got != "https://x/mod/badges/uptimes/7d/badge.svg?check=group%3Aa+b&content=percentage-endpoints&style=plastic" {
		t.Errorf("unexpected url %s", got)
	}
}

func TestKey(t *testing.T) {
	if got := Key("My Group", "a.b/c_d #1"); got != "my-group_a-b-c-d--1" {
		t.Errorf("unexpected key %s", got)
	}
}
