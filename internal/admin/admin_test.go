package admin

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"gatusmod/internal/config"
	"gatusmod/internal/inject"
	"gatusmod/internal/probe"
	"gatusmod/internal/proxy"
	"gatusmod/internal/settings"
	"gatusmod/internal/store"
	"gatusmod/internal/syncer"

	"golang.org/x/crypto/bcrypt"
)

type fixture struct {
	handler http.Handler
	mgr     *settings.Manager
	syncer  *syncer.Syncer
	probes  *probe.Runner
	cfgDir  string
	mu      sync.Mutex
	created []map[string]any
	cookie  *http.Cookie
	csrf    string
}

func newFixture(t *testing.T, gatusStatus int) *fixture {
	t.Helper()
	f := &fixture{}
	nocodb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v2/meta/tables/"):
			_, _ = io.WriteString(w, `{"columns":[{"title":"name","uidt":"SingleLineText"},{"title":"url","uidt":"SingleLineText"}]}`)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"list":[{"Id":1,"name":"Web Site","group":"Core","url":"https://example.org","conditions":"[STATUS] == 200"}],"pageInfo":{"isLastPage":true}}`)
		case r.Method == http.MethodPost:
			f.mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&f.created)
			f.mu.Unlock()
		}
	}))
	t.Cleanup(nocodb.Close)
	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gatusStatus != http.StatusOK {
			w.WriteHeader(gatusStatus)
			return
		}
		_, _ = io.WriteString(w, `[{"name":"api","group":"core","key":"core_api","results":[]}]`)
	}))
	t.Cleanup(gatus.Close)
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	f.cfgDir = t.TempDir()
	base := &config.Config{
		Path:     f.cfgDir,
		Upstream: gatus.URL,
		BaseURL:  "https://status.example.com",
		Admin:    &config.Admin{Username: "admin", PasswordBcrypt: string(hash)},
		Database: &config.Database{Provider: "nocodb", URL: nocodb.URL, TableID: "t"},
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	mgr, err := settings.Open(filepath.Join(t.TempDir(), "data"), base)
	if err != nil {
		t.Fatal(err)
	}
	f.mgr = mgr
	f.syncer = syncer.New(mgr.Get)
	tracker := proxy.NewTracker()
	tracker.Record("app.js", inject.Report{Missing: []string{"items-per-page"}})
	f.probes = probe.NewRunner(mgr.Get)
	f.handler = New(Deps{Config: mgr.Get, Settings: mgr, Syncer: f.syncer, Tracker: tracker, Probes: f.probes}).Handler()
	return f
}

func (f *fixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

var csrfInput = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

func (f *fixture) login(t *testing.T) {
	t.Helper()
	rec := f.post(t, "/login", url.Values{"username": {"admin"}, "password": {"secret"}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			f.cookie = c
		}
	}
	if f.cookie == nil || !f.cookie.HttpOnly || f.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected session cookie %+v", f.cookie)
	}
	m := csrfInput.FindStringSubmatch(f.get(t, "/").Body.String())
	if m == nil {
		t.Fatal("no CSRF token on the overview page")
	}
	f.csrf = m[1]
}

func (f *fixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, Prefix+path, nil)
	if f.cookie != nil {
		req.AddCookie(f.cookie)
	}
	return f.serve(req)
}

func (f *fixture) post(t *testing.T, path string, form url.Values, withCSRF bool) *httptest.ResponseRecorder {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	if withCSRF {
		form.Set("csrf", f.csrf)
	}
	req := httptest.NewRequest(http.MethodPost, Prefix+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if f.cookie != nil {
		req.AddCookie(f.cookie)
	}
	return f.serve(req)
}

func TestLoginRequired(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	rec := f.get(t, "/databases")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), Prefix+"/login?next=") {
		t.Errorf("expected a redirect to the login page, got %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := f.post(t, "/sync", nil, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for a post without session, got %d", rec.Code)
	}
	if rec := f.get(t, "/login"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="password"`) {
		t.Errorf("expected the login form, got %d", rec.Code)
	}
	if rec := f.post(t, "/login", url.Values{"username": {"admin"}, "password": {"wrong"}}, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for a wrong password, got %d", rec.Code)
	}
}

func TestAdminDisabled(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	base := *f.mgr.Base()
	base.Admin = nil
	f.mgr.SetBase(&base)
	if rec := f.get(t, "/login"); rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 when admin is disabled, got %d", rec.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	for i := 0; i < maxFailures; i++ {
		f.post(t, "/login", url.Values{"username": {"admin"}, "password": {"wrong"}}, false)
	}
	if rec := f.post(t, "/login", url.Values{"username": {"admin"}, "password": {"secret"}}, false); rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 after too many failures, got %d", rec.Code)
	}
}

func TestLoginRedirectStaysOnAdmin(t *testing.T) {
	for in, want := range map[string]string{
		Prefix + "/imports":    Prefix + "/imports",
		"https://evil.example": Prefix + "/",
		"//evil.example":       Prefix + "/",
		Prefix + "/\\evil":     Prefix + "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCSRFAndCrossSite(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	if rec := f.post(t, "/sync", url.Values{"csrf": {"wrong"}}, false); rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for a wrong CSRF token, got %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, Prefix+"/sync", strings.NewReader("csrf="+f.csrf))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(f.cookie)
	if rec := f.serve(req); rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for a cross-site post, got %d", rec.Code)
	}
	if h := f.get(t, "/").Header().Get("Content-Security-Policy"); !strings.Contains(h, "frame-ancestors 'none'") {
		t.Errorf("missing CSP header: %q", h)
	}
}

func TestLogout(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	if rec := f.post(t, "/logout", nil, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("logout failed: %d", rec.Code)
	}
	if rec := f.get(t, "/"); rec.Code != http.StatusSeeOther {
		t.Errorf("the session should be gone after logout, got %d", rec.Code)
	}
}

func TestSyncAndOverview(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	rec := f.post(t, "/sync", nil, true)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), url.QueryEscape("Sync finished.")) {
		t.Fatalf("unexpected sync response %d %s", rec.Code, rec.Header().Get("Location"))
	}
	body := f.get(t, "/?msg=hello").Body.String()
	for _, want := range []string{"hello", "1 valid endpoints", "1 enabled", "items-per-page", "settings.json"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview should contain %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "Web Site") {
		t.Error("the overview should not list the generated endpoints")
	}
	if body := f.get(t, "/checks").Body.String(); !strings.Contains(body, "Web Site") {
		t.Errorf("the checks page should list the generated endpoints\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(f.cfgDir, config.DefaultOutputFile)); err != nil {
		t.Errorf("expected the generated file: %v", err)
	}
}

func TestDatabasesCRUD(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	if body := f.get(t, "/databases").Body.String(); !strings.Contains(body, "NocoDB:database") || !strings.Contains(body, "Cron @every 1m0s") {
		t.Errorf("the migrated import should be listed under its database\n%s", body)
	}
	if rec := f.get(t, "/imports"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != Prefix+"/databases" {
		t.Errorf("the old page should redirect, got %d %s", rec.Code, rec.Header().Get("Location"))
	}
	migrated, _ := f.mgr.Settings().Database(f.mgr.Settings().Imports[0].DatabaseID)

	db := url.Values{"name": {"NocoDB-prod"}, "provider": {"nocodb"}, "url": {migrated.URL + "/api"}, "token": {"s3cr3t-value"}}
	rec := f.post(t, "/databases/save", db, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("saving the database failed: %d %s", rec.Code, rec.Body.String())
	}
	dbs := f.mgr.Settings().Databases
	if len(dbs) != 2 || dbs[1].Name != "NocoDB-prod" || dbs[1].Token != "s3cr3t-value" {
		t.Fatalf("unexpected databases %+v", dbs)
	}
	dbID := dbs[1].ID
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, Prefix+"/databases/"+dbID+"/views/new") {
		t.Errorf("a new database should continue with its first table, got %s", loc)
	}
	if body := f.get(t, "/databases/"+dbID).Body.String(); strings.Contains(body, "s3cr3t-value") || !strings.Contains(body, "Leave empty to keep the current token") {
		t.Error("the database form must not show the token")
	}
	db.Set("id", dbID)
	db.Set("token", "")
	f.post(t, "/databases/save", db, true)
	if c, _ := f.mgr.Settings().Database(dbID); c.Token != "s3cr3t-value" {
		t.Errorf("an empty token field must keep the token, got %q", c.Token)
	}
	dup := url.Values{"name": {"nocodb-PROD"}, "provider": {"nocodb"}, "url": {"http://x"}}
	if body := f.post(t, "/databases/save", dup, true).Body.String(); !strings.Contains(body, "already a database named") {
		t.Errorf("expected a duplicate name error\n%s", body)
	}

	form := url.Values{
		"name": {"servers"}, "group": {"iperf"}, "tableId": {"t"}, "viewId": {"v1"}, "type": {"tcp"}, "defaultPorts": {"5201-5210"},
		"interval": {"15m"}, "field_host": {"url"}, "field_name": {"name"}, "action": {"save"},
	}
	views := "/databases/" + dbID + "/views/"
	if rec := f.post(t, views+"save", form, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving the view failed: %d %s", rec.Code, rec.Body.String())
	}
	imps := f.mgr.Settings().Imports
	if len(imps) != 2 || imps[1].Name != "servers" || imps[1].DatabaseID != dbID || imps[1].Token != "" || imps[1].URL != "" || imps[1].ViewID != "v1" {
		t.Fatalf("unexpected imports %+v", imps)
	}
	if data, _ := os.ReadFile(f.mgr.Path()); strings.Count(string(data), "s3cr3t-value") != 1 {
		t.Errorf("the token must only be stored on the database\n%s", data)
	}
	id := imps[1].ID
	edit := f.get(t, views+id).Body.String()
	if strings.Contains(edit, "s3cr3t-value") || !strings.Contains(edit, "zz-gatusmod-nocodb-prod-servers.yaml") {
		t.Errorf("the view form should show its file and not the token\n%s", edit)
	}
	if rec := f.get(t, "/imports/"+id); rec.Header().Get("Location") != Prefix+views+id {
		t.Errorf("an old import link should redirect to the view, got %s", rec.Header().Get("Location"))
	}
	form.Set("id", id)
	form.Set("interval", "soon")
	if rec := f.post(t, views+"save", form, true); !strings.Contains(rec.Body.String(), "not a valid positive duration") {
		t.Errorf("expected a validation error\n%s", rec.Body.String())
	}
	form.Set("interval", "15m")
	form.Set("action", "test")
	body := f.post(t, views+"save", form, true).Body.String()
	for _, want := range []string{"Connection OK: 1 rows read, 1 valid endpoints", `id="test-result"`, "tcp://https://example.org:5201", "<code>name</code>"} {
		if !strings.Contains(body, want) {
			t.Errorf("test preview should contain %q", want)
		}
	}

	rec = f.post(t, views+id+"/run", nil, true)
	if loc, _ := url.QueryUnescape(rec.Header().Get("Location")); !strings.Contains(loc, `"NocoDB-prod:servers": 1 endpoints from 1 rows`) {
		t.Errorf("unexpected run result %s", loc)
	}
	file := filepath.Join(f.cfgDir, "zz-gatusmod-nocodb-prod-servers.yaml")
	if data, err := os.ReadFile(file); err != nil || !strings.Contains(string(data), "NocoDB-prod:servers (import "+id+")") {
		t.Fatalf("expected the file of the view: %v\n%s", err, data)
	}
	if body := f.get(t, "/databases").Body.String(); !strings.Contains(html.UnescapeString(body), "Delete NocoDB-prod:servers? zz-gatusmod-nocodb-prod-servers.yaml is deleted") {
		t.Error("the delete confirmation should name the file")
	}
	rec = f.post(t, views+id+"/delete", nil, true)
	if rec.Code != http.StatusSeeOther || len(f.mgr.Settings().Imports) != 1 {
		t.Errorf("delete failed: %d %+v", rec.Code, f.mgr.Settings().Imports)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("deleting a view must delete its file")
	}

	f.post(t, views+"save", url.Values{"name": {"sites"}, "tableId": {"t"}, "type": {"http"}, "field_host": {"url"}, "action": {"save"}}, true)
	f.post(t, views+f.mgr.Settings().Imports[1].ID+"/run", nil, true)
	sites := filepath.Join(f.cfgDir, "zz-gatusmod-nocodb-prod-sites.yaml")
	if _, err := os.Stat(sites); err != nil {
		t.Fatal(err)
	}
	if rec := f.post(t, "/databases/"+dbID+"/delete", nil, true); rec.Code != http.StatusSeeOther || len(f.mgr.Settings().Databases) != 1 || len(f.mgr.Settings().Imports) != 1 {
		t.Errorf("deleting the database failed: %d %+v", rec.Code, f.mgr.Settings())
	}
	if _, err := os.Stat(sites); !os.IsNotExist(err) {
		t.Error("deleting a database must delete the files of its views")
	}
}

func TestOrphanedFiles(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	orphan := filepath.Join(f.cfgDir, "zz-gatusmod-old.yaml")
	_ = os.WriteFile(orphan, []byte("# Generated by GatusMOD for Old:x (import gone). Do not edit.\nendpoints: []\n"), 0o644)
	other := filepath.Join(f.cfgDir, "zz-gatusmod-mine.yaml")
	_ = os.WriteFile(other, []byte("endpoints: []\n"), 0o644)
	body := f.get(t, "/databases").Body.String()
	if !strings.Contains(body, "Orphaned files") || !strings.Contains(body, "zz-gatusmod-old.yaml") || strings.Contains(body, "zz-gatusmod-mine.yaml") {
		t.Errorf("only files written by GatusMOD should be listed\n%s", body)
	}
	f.post(t, "/databases/files/delete", url.Values{"file": {"zz-gatusmod-mine.yaml"}}, true)
	if _, err := os.Stat(other); err != nil {
		t.Error("a file not written by GatusMOD must never be deleted")
	}
	f.post(t, "/databases/files/delete", url.Values{"file": {"zz-gatusmod-old.yaml"}}, true)
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("the orphaned file should be deleted")
	}
}

func TestSettingsPage(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	form := url.Values{
		"baseUrl": {"https://up.example.com"}, "defaultInterval": {"2m"}, "itemsPerPage": {"24"}, "subtitleShow": {"on"},
		"footer": {"<p>hi</p>"}, "analyticsEnabled": {"on"}, "analyticsProvider": {"plausible"},
		"analyticsScript": {"https://p.example.com/js"}, "analyticsSite": {"example.com"},
	}
	if rec := f.post(t, "/settings", form, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving settings failed: %d %s", rec.Code, rec.Body.String())
	}
	cfg := f.mgr.Get()
	if cfg.BaseURL != "https://up.example.com" || cfg.UI.ItemsPerPage != 24 || cfg.UI.HeadingVisible() || cfg.Analytics == nil || cfg.Analytics.Provider != "plausible" {
		t.Errorf("settings not applied: %+v %+v", cfg.UI, cfg.Analytics)
	}
	form.Set("defaultInterval", "never")
	if body := f.post(t, "/settings", form, true).Body.String(); !strings.Contains(body, "defaults.interval") {
		t.Errorf("expected a validation error\n%s", body)
	}
	f.post(t, "/settings/reset", nil, true)
	if f.mgr.Get().BaseURL != "https://status.example.com" {
		t.Error("reset should use config.yaml again")
	}
}

func TestSettingsPage_ExampleEndpoint(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	file := filepath.Join(f.cfgDir, "config.yaml")
	_ = os.WriteFile(file, []byte("endpoints:\n  - name: GatusMOD\n    url: http://gatusmod:8080/mod/health\n    conditions: [\"[STATUS] == 200\"]\n"), 0o644)
	if body := f.get(t, "/settings").Body.String(); !strings.Contains(body, "Example endpoint") || !strings.Contains(body, "Wait until your tables have generated endpoints") {
		t.Errorf("removing should wait for generated endpoints\n%s", body)
	}
	f.post(t, "/sync", nil, true)
	body := f.get(t, "/settings").Body.String()
	if !strings.Contains(body, "/mod/admin/settings/example/delete") || !strings.Contains(body, "config.yaml") {
		t.Fatalf("expected the remove button\n%s", body)
	}
	rec := f.post(t, "/settings/example/delete", nil, true)
	if loc, _ := url.QueryUnescape(rec.Header().Get("Location")); !strings.Contains(loc, "Example endpoint GatusMOD removed from config.yaml") {
		t.Errorf("unexpected result %s", loc)
	}
	if data, _ := os.ReadFile(file); strings.Contains(string(data), "GatusMOD") {
		t.Errorf("the example endpoint is still there\n%s", data)
	}
	if body := f.get(t, "/settings").Body.String(); strings.Contains(body, "Example endpoint") {
		t.Error("the section should be gone after removing")
	}
}

func TestChangePassword(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	other := f.cookie
	f.login(t)
	if body := f.post(t, "/settings/password", url.Values{"current": {"secret"}, "new": {"short"}, "confirm": {"short"}}, true).Body.String(); !strings.Contains(body, "at least 10") {
		t.Error("expected a length error")
	}
	rec := f.post(t, "/settings/password", url.Values{"current": {"secret"}, "new": {"new-password-1"}, "confirm": {"new-password-1"}}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("changing the password failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.get(t, "/"); rec.Code != http.StatusOK {
		t.Errorf("the current session should stay valid, got %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, Prefix+"/", nil)
	req.AddCookie(other)
	if rec := f.serve(req); rec.Code != http.StatusSeeOther {
		t.Errorf("other sessions should be logged out, got %d", rec.Code)
	}
	f.cookie = nil
	if rec := f.post(t, "/login", url.Values{"username": {"admin"}, "password": {"new-password-1"}}, false); rec.Code != http.StatusSeeOther {
		t.Errorf("login with the new password failed: %d", rec.Code)
	}
}

func TestYAMLImport(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	target := f.mgr.Settings().Imports[0].ID
	form := url.Values{"target": {target}, "yaml": {"endpoints:\n  - name: new\n    url: https://new.example.org\n    conditions: ['[STATUS] == 200']\n"}}
	rec := f.post(t, "/import", form, true)
	if !strings.Contains(rec.Body.String(), "Created: 1") {
		t.Errorf("expected import result in page\n%s", rec.Body.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) != 1 || f.created[0]["name"] != "new" {
		t.Errorf("unexpected created rows %+v", f.created)
	}
}

func TestBackupExportRestore(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	if body := f.get(t, "/import").Body.String(); !strings.Contains(body, "1 external databases, 1 tables/views, 0 local tables") {
		t.Errorf("expected the counts on the page\n%s", body)
	}
	rec := f.post(t, "/import/export", nil, true)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), `attachment; filename="gatusmod-backup-`) {
		t.Fatalf("expected a download, got %d %v", rec.Code, rec.Header())
	}
	backup := rec.Body.String()
	if !strings.Contains(backup, "gatusmodBackup: 1") || !strings.Contains(backup, "tokensIncluded: false") {
		t.Fatalf("unexpected backup\n%s", backup)
	}
	err := f.mgr.Update(func(s *settings.Settings) error {
		s.Imports = append(s.Imports, config.Import{ID: "local1", Name: "mine", Provider: config.ProviderLocal, Type: "custom"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	restore := func(mode, content string) string {
		var body strings.Builder
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("csrf", f.csrf)
		_ = mw.WriteField("mode", mode)
		fw, _ := mw.CreateFormFile("file", "backup.yaml")
		_, _ = io.WriteString(fw, content)
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost, Prefix+"/import/restore", strings.NewReader(body.String()))
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(f.cookie)
		return f.serve(req).Body.String()
	}
	if page := restore("merge", "endpoints: []\n"); !strings.Contains(page, "not a GatusMOD backup") {
		t.Errorf("expected an error for a Gatus file\n%s", page)
	}
	if page := restore("merge", backup); !strings.Contains(page, "Backup restored (merged)") || len(f.mgr.Settings().Imports) != 2 {
		t.Errorf("merge must keep the local table\n%s", page)
	}
	page := restore("replace", backup)
	if !strings.Contains(page, "Backup restored (replaced)") || !strings.Contains(page, "Removed: 1") {
		t.Errorf("expected a replace result\n%s", page)
	}
	if imps := f.mgr.Settings().Imports; len(imps) != 1 || imps[0].Local() {
		t.Errorf("replace must drop the local table: %+v", imps)
	}
}

func TestBadges(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	body := f.get(t, "/badges?duration=7d&style=flat-square&content=percentage-endpoints&snippet=markdown").Body.String()
	for _, want := range []string{
		"https://status.example.com/api/v1/endpoints/core_api/health/badge.svg",
		"https://status.example.com/api/v1/endpoints/core_api/uptimes/7d/badge.svg",
		"https://status.example.com/api/v1/endpoints/core_api/response-times/7d/chart.svg",
		"https://status.example.com/mod/badges/uptimes/30d/badge.svg?content=percentage-endpoints&amp;style=flat-square",
		"Group: core",
		"Gatus API",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("badges page should contain %q", want)
		}
	}
}

func TestBadges_FallbackToImports(t *testing.T) {
	f := newFixture(t, http.StatusUnauthorized)
	f.login(t)
	f.post(t, "/sync", nil, true)
	body := f.get(t, "/badges").Body.String()
	if !strings.Contains(body, "/api/v1/endpoints/core_web-site/health/badge.svg") || !strings.Contains(body, "HTTP 401") {
		t.Errorf("expected the imports fallback with computed key\n%s", body)
	}
}

func TestOverview_Probes(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.probes.Update([]probe.Target{
		{Key: "jobs_backup", Group: "jobs", Name: "backup", URL: "push://", Interval: time.Hour},
		{Key: "core_db", Group: "core", Name: "db", URL: "postgres://app:s3cret@127.0.0.1:1/app", Interval: time.Hour, Conditions: []string{"[CONNECTED] == true"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.probes.Run(ctx)
	checked := func() bool {
		for _, s := range f.probes.Statuses() {
			if s.Target.Key == "core_db" && !s.Last.Time.IsZero() {
				return true
			}
		}
		return false
	}
	for i := 0; i < 1000 && !checked(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	f.login(t)
	body := f.get(t, "/checks").Body.String()
	token := f.mgr.Get().PushToken("jobs_backup")
	pushURL := "https://status.example.com/mod/push/" + token
	if !strings.Contains(body, pushURL) {
		t.Error("the push URL is missing on the checks page")
	}
	if strings.Contains(body, "s3cret") || !strings.Contains(body, "postgres://app:xxxxx@127.0.0.1:1/app") {
		t.Error("probe URLs must be shown without password")
	}

	if !checked() {
		t.Skip("the database probe did not finish in time")
	}
	overview := f.get(t, "/").Body.String()
	for _, want := range []string{"1 down", "1 not checked yet", "Probes that need attention", "postgres://app:xxxxx@127.0.0.1:1/app"} {
		if !strings.Contains(overview, want) {
			t.Errorf("overview should contain %q\n%s", want, overview)
		}
	}
	if strings.Contains(overview, pushURL) {
		t.Error("the overview should only list probes that are down")
	}
}

func TestImportForm_URLExamples(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	db := f.mgr.Settings().Databases[0].ID
	body := html.UnescapeString(f.get(t, "/databases/"+db+"/views/new").Body.String())
	if strings.Contains(body, "ZgotmplZ") {
		t.Error("an example was filtered by html/template")
	}
	for _, typ := range config.ImportTypes {
		for _, ex := range config.URLTemplateExamples(typ) {
			if !strings.Contains(body, `data-example-template="`+ex.Template+`"`) {
				t.Errorf("example %s of %s is missing", ex.Template, typ)
			}
		}
	}
}

func TestUnknownColumns(t *testing.T) {
	columns := []store.Field{{Name: "host"}, {Name: "port"}}
	got := unknownColumns(config.Fields{Host: "host", Port: "port", Interval: "1m"}, columns)
	if len(got) != 1 || !strings.Contains(got[0], `"1m"`) {
		t.Errorf("unexpected warnings %q", got)
	}
	if got := unknownColumns(config.Fields{Interval: "1m"}, nil); got != nil {
		t.Errorf("no warnings without a column list, got %q", got)
	}
}
