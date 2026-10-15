package statuspage

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"gatusmod/internal/config"
	"gatusmod/internal/session"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{StatusPages: []config.StatusPage{{Slug: "klant-a", Groups: []string{"Core Services"}, Domains: []string{"status.klant.nl"}}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// echo answers with the path it got and the slug of the scope.
var echo = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	slug := "-"
	if s := FromContext(r.Context()); s != nil && s.Page != nil {
		slug = s.Page.Slug
		if s.Strict {
			slug += " strict " + s.Origin
		}
	}
	_, _ = w.Write([]byte(r.URL.Path + " " + slug))
})

func TestWrap(t *testing.T) {
	h := New().Wrap(testConfig(t), echo)
	cases := []struct {
		method, host, path, header string
		code                       int
		body                       string
	}{
		{"GET", "main.example.com", "/", "", 200, "/ -"},
		{"GET", "main.example.com", "/status/klant-a", "", 200, "/ klant-a"},
		{"GET", "main.example.com", "/status/klant-a/", "", 200, "/ klant-a"},
		{"GET", "main.example.com", "/status/nope", "", 404, ""},
		{"GET", "main.example.com", "/status/klant-a/x", "", 404, ""},
		{"GET", "main.example.com", "/?status=klant-a", "", 200, "/ klant-a"},
		{"GET", "main.example.com", "/endpoints/x?status=klant-a", "", 200, "/endpoints/x klant-a"},
		{"GET", "main.example.com", "/api/v1/endpoints/statuses?status=klant-a", "", 200, "/api/v1/endpoints/statuses -"},
		{"GET", "main.example.com", "/api/v1/endpoints/statuses", "klant-a", 200, "/api/v1/endpoints/statuses klant-a"},
		{"GET", "main.example.com", "/api/v1/endpoints/other_x/statuses", "klant-a", 200, "/api/v1/endpoints/other_x/statuses klant-a"},
		{"GET", "main.example.com", "/api/v1/suites/statuses", "klant-a", 200, "[]"},
		{"GET", "main.example.com", "/mod/admin/", "klant-a", 200, "/mod/admin/ klant-a"},
		{"GET", "status.klant.nl:8080", "/", "", 200, "/ klant-a strict http://status.klant.nl:8080"},
		{"GET", "status.klant.nl", "/endpoints/other_x", "", 200, "/endpoints/other_x klant-a strict http://status.klant.nl"},
		{"GET", "status.klant.nl", "/api/v1/endpoints/statuses", "", 200, "/api/v1/endpoints/statuses klant-a strict http://status.klant.nl"},
		{"GET", "status.klant.nl", "/api/v1/endpoints/core-services_web/uptimes/7d/badge.svg", "", 200, ""},
		{"GET", "status.klant.nl", "/mod/api/endpoints/core-services_web/related", "", 200, ""},
		{"GET", "status.klant.nl", "/api/v1/endpoints/other_web/statuses", "", 404, ""},
		{"GET", "status.klant.nl", "/mod/api/endpoints/other_web/related", "", 404, ""},
		{"POST", "status.klant.nl", "/api/v1/endpoints/core-services_web/external", "", 404, ""},
		{"GET", "status.klant.nl", "/api/v1/suites/statuses", "", 200, "[]"},
		{"GET", "status.klant.nl", "/api/v1/suites/x/statuses", "", 404, ""},
		{"GET", "status.klant.nl", "/api/v1/other", "", 404, ""},
		{"GET", "status.klant.nl", "/api/v1/config", "", 200, ""},
		{"GET", "status.klant.nl", "/mod/admin/", "", 404, ""},
		{"GET", "status.klant.nl", "/mod/admin", "", 404, ""},
		{"GET", "status.klant.nl", "/mod/badges/uptimes/7d/badge.svg", "", 404, ""},
		{"GET", "status.klant.nl", "/status/klant-a", "", 404, ""},
		{"GET", "status.klant.nl", "/metrics", "", 404, ""},
		{"GET", "status.klant.nl", "/mod/static/mod.js", "", 200, ""},
		{"GET", "status.klant.nl", "/mod/push/abc", "", 200, ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Host = c.host
		if c.header != "" {
			req.Header.Set(Header, c.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.code || (c.body != "" && rec.Body.String() != c.body) {
			t.Errorf("%s %s%s: got %d %q, want %d %q", c.method, c.host, c.path, rec.Code, rec.Body.String(), c.code, c.body)
		}
	}
}

func TestWrap_NoPages(t *testing.T) {
	rec := httptest.NewRecorder()
	New().Wrap(&config.Config{}, echo).ServeHTTP(rec, httptest.NewRequest("GET", "/status/x", nil))
	if rec.Body.String() != "/status/x -" {
		t.Errorf("without status pages nothing should change, got %q", rec.Body.String())
	}
}

func TestWrap_ForwardedProto(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "status.klant.nl"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	New().Wrap(testConfig(t), echo).ServeHTTP(rec, req)
	if !strings.HasSuffix(rec.Body.String(), "https://status.klant.nl") {
		t.Errorf("the origin should use X-Forwarded-Proto, got %q", rec.Body.String())
	}
}

func TestFilterStatuses(t *testing.T) {
	page := &testConfig(t).StatusPages[0]
	out, err := FilterStatuses([]byte(`[{"key":"core-services_a","results":[1]},{"key":"other_b"},{"key":"core-services_c"}]`), page.Shows)
	if err != nil || string(out) != `[{"key":"core-services_a","results":[1]},{"key":"core-services_c"}]` {
		t.Errorf("got %s %v", out, err)
	}
	if _, err := FilterStatuses([]byte(`{}`), page.Shows); err == nil {
		t.Error("expected an error for a response that is not a list")
	}
}

const testPassword = "klant-geheim-1"

func privateConfig(t *testing.T) *config.Config {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	cfg := &config.Config{StatusPages: []config.StatusPage{
		{ID: "p", Slug: "geheim", Groups: []string{"core"}, Endpoints: []string{"web_shop"}, Domains: []string{"status.geheim.nl"}, PasswordBcrypt: string(hash)},
		{ID: "q", Slug: "open", Groups: []string{"web"}},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

type client struct {
	t      *testing.T
	h      http.Handler
	host   string
	cookie *http.Cookie
}

func (c *client) do(method, target string, form url.Values, header string) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	req.Host = c.host
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if header != "" {
		req.Header.Set(Header, header)
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == cookieName {
			c.cookie = ck
			if ck.MaxAge < 0 {
				c.cookie = nil
			}
		}
	}
	return rec
}

func TestPrivate_Domain(t *testing.T) {
	c := &client{t: t, h: New().Wrap(privateConfig(t), echo), host: "status.geheim.nl"}
	for _, x := range []struct {
		path string
		code int
	}{
		{"/", http.StatusSeeOther},
		{"/endpoints/core_a", http.StatusSeeOther},
		{"/api/v1/endpoints/statuses", http.StatusUnauthorized},
		{"/api/v1/endpoints/core_a/statuses", http.StatusUnauthorized},
		{"/mod/api/endpoints/core_a/related", http.StatusUnauthorized},
		{"/index.html", http.StatusUnauthorized},
		{"/js/app.js", http.StatusOK},
		{"/mod/static/mod.css", http.StatusOK},
		{"/mod/push/abc", http.StatusOK},
	} {
		if rec := c.do("GET", x.path, nil, ""); rec.Code != x.code {
			t.Errorf("before login %s: got %d, want %d", x.path, rec.Code, x.code)
		}
	}
	if loc := c.do("GET", "/endpoints/core_a", nil, "").Header().Get("Location"); loc != LoginPath+"?next=%2Fendpoints%2Fcore_a" {
		t.Errorf("login redirect: got %q", loc)
	}
	if rec := c.do("GET", LoginPath, nil, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="password"`) || strings.Contains(rec.Body.String(), `name="page"`) {
		t.Errorf("login form: %d %s", rec.Code, rec.Body.String())
	}
	if rec := c.do("POST", LoginPath, url.Values{"password": {"wrong"}}, ""); rec.Code != http.StatusUnauthorized || c.cookie != nil {
		t.Errorf("a wrong password should fail, got %d", rec.Code)
	}
	rec := c.do("POST", LoginPath, url.Values{"password": {testPassword}, "next": {"/endpoints/core_a"}}, "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/endpoints/core_a" || c.cookie == nil {
		t.Fatalf("login failed: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for path, code := range map[string]int{
		"/": 200, "/api/v1/endpoints/core_a/statuses": 200, "/api/v1/endpoints/web_shop/statuses": 200,
		"/api/v1/endpoints/web_other/statuses": 404, "/mod/admin/": 404,
	} {
		if rec := c.do("GET", path, nil, ""); rec.Code != code {
			t.Errorf("after login %s: got %d, want %d", path, rec.Code, code)
		}
	}
	if rec := c.do("POST", LogoutPath, url.Values{}, ""); rec.Code != http.StatusSeeOther || c.cookie != nil {
		t.Errorf("logout failed: %d", rec.Code)
	}
	if rec := c.do("GET", "/", nil, ""); rec.Code != http.StatusSeeOther {
		t.Errorf("after logout / should redirect to the login, got %d", rec.Code)
	}
}

func TestPrivate_MainSite(t *testing.T) {
	cfg := privateConfig(t)
	pages := New()
	c := &client{t: t, h: pages.Wrap(cfg, echo), host: "main.example.com"}
	for _, x := range []struct {
		method, path, header string
		code                 int
		body                 string
	}{
		{"GET", "/", "", 200, "/ -"},
		{"GET", "/api/v1/endpoints/core_a/statuses", "", 404, ""},
		{"GET", "/api/v1/endpoints/web_shop/uptimes/7d/badge.svg", "", 404, ""},
		{"GET", "/api/v1/endpoints/web_other/statuses", "", 200, ""},
		{"GET", "/mod/api/endpoints/core_a/related", "", 404, ""},
		{"POST", "/api/v1/endpoints/core_a/external", "", 200, ""},
		{"GET", "/api/v1/endpoints/web_shop/statuses", "open", 404, ""},
		{"GET", "/api/v1/endpoints/web_other/statuses", "open", 200, ""},
		{"GET", "/status/geheim", "", 303, ""},
		{"GET", "/?status=geheim", "", 303, ""},
		{"GET", "/api/v1/endpoints/statuses", "geheim", 401, ""},
		{"GET", LoginPath, "", 404, ""},
		{"GET", LoginPath + "?page=open", "", 404, ""},
	} {
		if rec := c.do(x.method, x.path, nil, x.header); rec.Code != x.code || (x.body != "" && rec.Body.String() != x.body) {
			t.Errorf("%s %s (%s): got %d %q, want %d", x.method, x.path, x.header, rec.Code, rec.Body.String(), x.code)
		}
	}
	if loc := c.do("GET", "/status/geheim", nil, "").Header().Get("Location"); loc != LoginPath+"?next=%2Fstatus%2Fgeheim&page=geheim" {
		t.Errorf("login redirect: got %q", loc)
	}
	if rec := c.do("POST", LoginPath, url.Values{"page": {"geheim"}, "password": {testPassword}}, ""); rec.Header().Get("Location") != "/status/geheim" {
		t.Fatalf("login failed: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, x := range []struct{ path, header string }{
		{"/status/geheim", ""}, {"/api/v1/endpoints/statuses", "geheim"}, {"/api/v1/endpoints/core_a/statuses", "geheim"},
	} {
		if rec := c.do("GET", x.path, nil, x.header); rec.Code != 200 {
			t.Errorf("after login %s: got %d", x.path, rec.Code)
		}
	}
	if rec := c.do("GET", "/api/v1/endpoints/core_a/statuses", nil, ""); rec.Code != 404 {
		t.Errorf("the main site keeps hiding private endpoints after a login, got %d", rec.Code)
	}

	// A new password logs everybody out.
	hash, _ := bcrypt.GenerateFromPassword([]byte("another-password"), bcrypt.MinCost)
	cfg2 := privateConfig(t)
	cfg2.StatusPages[0].PasswordBcrypt = string(hash)
	c.h = pages.Wrap(cfg2, echo)
	if rec := c.do("GET", "/status/geheim", nil, ""); rec.Code != http.StatusSeeOther {
		t.Errorf("a changed password should end the session, got %d", rec.Code)
	}
}

func TestPrivate_LoginLockout(t *testing.T) {
	c := &client{t: t, h: New().Wrap(privateConfig(t), echo), host: "status.geheim.nl"}
	for i := 0; i < session.MaxFailures; i++ {
		c.do("POST", LoginPath, url.Values{"password": {"wrong"}}, "")
	}
	if rec := c.do("POST", LoginPath, url.Values{"password": {testPassword}}, ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 after too many failures, got %d", rec.Code)
	}
}

func TestPrivate_CrossSiteLogin(t *testing.T) {
	h := New().Wrap(privateConfig(t), echo)
	req := httptest.NewRequest("POST", LoginPath, strings.NewReader("password="+testPassword))
	req.Host = "status.geheim.nl"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a cross-site login should be blocked, got %d", rec.Code)
	}
}

func TestSafeNext(t *testing.T) {
	p := &config.StatusPage{Slug: "geheim"}
	for in, want := range map[string]string{"/endpoints/x": "/endpoints/x", "//evil": "/", "https://evil": "/", "/\\evil": "/", "/mod/admin/": "/"} {
		if got := safeNext(in, p, true); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
	if got := safeNext("", p, false); got != "/status/geheim" {
		t.Errorf("on the main site the default is the status page, got %q", got)
	}
}
