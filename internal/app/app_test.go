package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gatusmod/internal/config"
)

func TestRouting(t *testing.T) {
	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "gatus:"+r.URL.Path)
	}))
	defer gatus.Close()
	dir := t.TempDir()
	images, favicon, public := filepath.Join(dir, "images"), filepath.Join(dir, "favicon"), filepath.Join(dir, "public")
	_ = os.MkdirAll(images, 0o755)
	_ = os.MkdirAll(favicon, 0o755)
	_ = os.MkdirAll(public, 0o755)
	_ = os.WriteFile(filepath.Join(images, "logo.png"), []byte("png"), 0o644)
	_ = os.WriteFile(filepath.Join(favicon, "icon_512.png"), []byte("icon"), 0o644)
	_ = os.WriteFile(filepath.Join(public, "favicon_256.png"), []byte("root-png"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
gatusmod:
  upstream: `+gatus.URL+`
  images-dir: `+filepath.ToSlash(images)+`
  favicon-dir: `+filepath.ToSlash(favicon)+`
  public-dir: `+filepath.ToSlash(public)+`
endpoints:
  - name: thing
    group: core
    url: https://thing.example.org
  - name: thing-ping
    group: core
    url: icmp://thing.example.org
`), 0o644)
	watcher, err := config.NewWatcher(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(watcher, filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		code int
		body string
	}{
		"/images/logo.png":                      {200, "png"},
		"/images/":                              {404, ""},
		"/favicon/icon_512.png":                 {200, "icon"},
		"/favicon/":                             {404, ""},
		"/favicon_256.png":                      {200, "root-png"},
		"/api/v1/config":                        {200, "gatus:/api/v1/config"},
		"/mod/health":                           {200, "ok"},
		"/mod/static/mod.css":                   {200, ""},
		"/mod/admin/":                           {404, ""},
		"/../config.yaml":                       {http.StatusTemporaryRedirect, ""},
		"/endpoints/core_thing":                 {200, "gatus:/endpoints/core_thing"},
		"/mod/api/endpoints/core_thing/related": {200, `{"type":"http","host":"thing.example.org","icmp":"core_thing-ping"}` + "\n"},
		"/mod/api/endpoints/core_nope/related":  {404, ""},
		"/mod/api/endpoints/core_thing/iperf3":  {404, ""},
		"/mod/badges/uptimes/2y/badge.svg":      {400, ""},
	}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want.code || (want.body != "" && rec.Body.String() != want.body) {
			t.Errorf("%s: got %d %q, want %d %q", path, rec.Code, rec.Body.String(), want.code, want.body)
		}
	}
}

func TestRouting_StatusPages(t *testing.T) {
	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "gatus:"+r.URL.Path)
	}))
	defer gatus.Close()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("gatusmod:\n  upstream: "+gatus.URL+"\n"), 0o644)
	data := filepath.Join(dir, "data")
	_ = os.MkdirAll(data, 0o700)
	_ = os.WriteFile(filepath.Join(data, "settings.json"), []byte(`{"statusPages":[{"id":"p","slug":"klant","groups":["core"],"domains":["status.klant.nl"]},`+
		`{"id":"s","slug":"geheim","groups":["secret"],"domains":["status.geheim.nl"],"passwordBcrypt":"$2a$04$invalidbutnonemptyhashvalue"}]}`), 0o600)
	watcher, err := config.NewWatcher(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(watcher, data)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		host, path string
		code       int
		body       string
	}{
		{"main.example.com", "/status/klant", 200, "gatus:/"},
		{"main.example.com", "/status/other", 404, ""},
		{"status.klant.nl", "/", 200, "gatus:/"},
		{"status.klant.nl", "/api/v1/endpoints/core_thing/statuses", 200, "gatus:/api/v1/endpoints/core_thing/statuses"},
		{"status.klant.nl", "/api/v1/endpoints/web_thing/statuses", 404, ""},
		{"status.klant.nl", "/mod/admin/", 404, ""},
		{"status.klant.nl", "/mod/health", 200, "ok"},
		{"status.geheim.nl", "/", 303, ""},
		{"status.geheim.nl", "/api/v1/endpoints/statuses", 401, ""},
		{"main.example.com", "/api/v1/endpoints/secret_x/statuses", 404, ""},
		{"main.example.com", "/api/v1/endpoints/core_thing/statuses", 200, "gatus:/api/v1/endpoints/core_thing/statuses"},
		{"main.example.com", "/status/geheim", 303, ""},
		{"main.example.com", "/mod/status-login?page=geheim", 200, ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.Host = c.host
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != c.code || (c.body != "" && rec.Body.String() != c.body) {
			t.Errorf("%s%s: got %d %q, want %d %q", c.host, c.path, rec.Code, rec.Body.String(), c.code, c.body)
		}
	}
}
